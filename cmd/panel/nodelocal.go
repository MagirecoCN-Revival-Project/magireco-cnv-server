// nodelocal.go 是面板安装向导的"本机也装业务节点"分支。
//
// 责任范围:
//
//  1. 找到 magireco-node 二进制(默认与面板二进制平级;CNV_NODE_BIN 覆盖)
//  2. 跑 node --version 对照面板版本(硬拒绝不一致)
//  3. 通过 TCP dial 探测 ControlAddr,判断节点进程是否在跑
//  4. setsid fork 启动节点(脱离面板生命周期),把 stdout/stderr 重定向到日志文件,
//     pid 写文件
//  5. 启动后轮询管控端口,在合理时限内确认起来
//
// 不在本文件做的事:数据库迁移、超管创建、setup_state 写入 —— 那些仍走
// install.go 的 initBusinessNode(直接 Open 业务节点 DB 操作,不经节点 RPC)。
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 这一组 var 是给单测开的"测试缝":生产代码默认指向下面的真实实现,
// 单测里 saveAndStub 替换成假的,跑完再恢复。这样 installLocalBusinessNode
// 的业务逻辑(顺序、错误分支、失败原子性)能脱离真实二进制和真实 fork 验证。
var (
	nodeFindBin      = findNodeBinary
	nodeProbeVersion = probeNodeVersion
	nodeIsRunning    = isNodeRunning
	nodeSpawn        = spawnNodeDetached
	nodeWaitReady    = waitNodeReady
)

// defaultNodeControlAddr 节点管控通道默认监听地址。优先用 CNV_NODE_CONTROL_ADDR
// (运维想换端口时);否则回到 internal/config 里 CNV_CONTROL_ADDR 的默认值
// 127.0.0.1:9090。面板与节点必须看到同一份地址才能 dial 上。
func defaultNodeControlAddr() string {
	if v := strings.TrimSpace(os.Getenv("CNV_CONTROL_ADDR")); v != "" {
		return v
	}
	return "127.0.0.1:9090"
}

// nodeBinaryName 平台对应的节点二进制文件名。
func nodeBinaryName() string {
	if runtime.GOOS == "windows" {
		return "magireco-node.exe"
	}
	return "magireco-node"
}

// findNodeBinary 按"面板平级默认 + CNV_NODE_BIN 覆盖"约定定位节点二进制。
//
// 解析顺序:
//  1. CNV_NODE_BIN 非空 → 视为绝对/相对路径,Stat 验证
//  2. 面板二进制所在目录 + magireco-node[.exe]
//  3. 都找不到 → 报错
//
// 返回的路径保证已 Stat 过(存在且为常规文件)。
func findNodeBinary() (string, error) {
	if env := strings.TrimSpace(os.Getenv("CNV_NODE_BIN")); env != "" {
		if err := statRegular(env); err != nil {
			return "", fmt.Errorf("CNV_NODE_BIN 指向的文件无法使用: %w", err)
		}
		abs, err := filepath.Abs(env)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	// 面板自身的可执行路径
	panelExe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位面板二进制: %w", err)
	}
	panelDir := filepath.Dir(panelExe)
	candidate := filepath.Join(panelDir, nodeBinaryName())
	if err := statRegular(candidate); err != nil {
		return "", fmt.Errorf("未在面板平级目录找到节点二进制 %s(可设 CNV_NODE_BIN 覆盖): %w",
			candidate, err)
	}
	return candidate, nil
}

func statRegular(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是常规文件", p)
	}
	return nil
}

// probeNodeVersion 跑 node --version,返回第一行 trim 后的字符串。
// 用 context 控制 5s 超时,防止坏二进制阻塞向导。
func probeNodeVersion(ctx context.Context, binPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("跑 %s --version 失败: %w", binPath, err)
	}
	line := strings.TrimSpace(out.String())
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if line == "" {
		return "", errors.New("--version 输出为空")
	}
	return line, nil
}

// versionsMatch 硬比对:两边字符串完全相等才认。"dev" 与 "dev" 算匹配
// (开发期常用),正式版本号一字一句对。
func versionsMatch(panelVer, nodeVer string) bool {
	return strings.TrimSpace(panelVer) == strings.TrimSpace(nodeVer)
}

// isNodeRunning 通过 TCP dial 探测节点是否已经在指定的 ControlAddr 上监听。
// 1s 超时;dial 成功即认为在跑。
func isNodeRunning(addr string) bool {
	if addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// spawnNodeOpts 启节点用的全部参数。
type spawnNodeOpts struct {
	BinPath  string            // 节点二进制
	WorkDir  string            // 工作目录(放 node.key / SQLite / data/);默认面板进程的 cwd
	LogDir   string            // 日志目录;默认 WorkDir/logs
	PidFile  string            // pid 文件路径;默认 WorkDir/data/node.pid
	EnvExtra map[string]string // 追加/覆盖给子进程的环境变量(CNV_DB_URL 等)
}

// spawnNodeDetached 启动节点并立即返回。子进程:
//   - setsid 脱离面板进程组(Linux/Darwin),独立 SIGTERM 生命周期
//   - stdout/stderr 追加到 logs/node.log
//   - pid 写到 PidFile(供 UI 后续状态展示)
//
// 不等节点起完;由调用方再用 isNodeRunning 轮询确认。
func spawnNodeDetached(opts spawnNodeOpts) (pid int, err error) {
	if opts.WorkDir == "" {
		opts.WorkDir, _ = os.Getwd()
	}
	if opts.LogDir == "" {
		opts.LogDir = filepath.Join(opts.WorkDir, "logs")
	}
	if opts.PidFile == "" {
		opts.PidFile = filepath.Join(opts.WorkDir, "data", "node.pid")
	}
	if err := os.MkdirAll(opts.LogDir, 0o755); err != nil {
		return 0, fmt.Errorf("创建日志目录失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.PidFile), 0o755); err != nil {
		return 0, fmt.Errorf("创建 pid 目录失败: %w", err)
	}

	logFile := filepath.Join(opts.LogDir, "node.log")
	out, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("打开节点日志失败: %w", err)
	}
	// 由 cmd.Wait() 之后再关;detached 不 Wait,所以这里关掉父进程持有的句柄,
	// 子进程 fork 时已经把 fd dup 过去。
	defer out.Close()

	cmd := exec.Command(opts.BinPath)
	cmd.Dir = opts.WorkDir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Stdin = nil

	// 子进程环境:继承父 env + 追加 EnvExtra(后者覆盖)
	env := append([]string(nil), os.Environ()...)
	for k, v := range opts.EnvExtra {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	// 平台特定的 detach 处理在 nodelocal_unix.go / nodelocal_windows.go
	setDetachedAttrs(cmd)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("启动节点进程失败: %w", err)
	}
	pid = cmd.Process.Pid

	// 立刻 Release —— 让子进程脱离 Wait,父进程退出时不会带走它
	if err := cmd.Process.Release(); err != nil {
		// 非致命:已经启起来了,只是不能 Release;记录但不报错
		_ = err
	}

	_ = os.WriteFile(opts.PidFile, []byte(fmt.Sprintf("%d\n", pid)), 0o644)
	return pid, nil
}

// waitNodeReady 在 timeout 内轮询 ControlAddr,看到节点起来就返回 nil。
// 没起来返回 deadline exceeded。
func waitNodeReady(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if isNodeRunning(addr) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("节点在 %s 内未在 %s 上响应,请查 logs/node.log",
				timeout.Round(time.Second), addr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
