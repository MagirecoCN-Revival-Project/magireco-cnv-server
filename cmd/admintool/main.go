// Command admintool 是 CLI 工具,用于:
//   * 引导第一个 super_admin 账号(避免上线时仍需手动算 scrypt 哈希)
//   * 重置任意管理员/玩家密码
//   * 离线生成口令哈希,便于一次性外部导入
//
// 用法:
//   admintool create-admin -dsn=$CNV_DB_URL -email=a@b -username=admin
//   admintool reset-admin  -dsn=$CNV_DB_URL -email=a@b
//   admintool reset-account -dsn=$CNV_DB_URL -id=acc_xxxxxxxx
//   admintool hash-password
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"magirecocn-revival/api-server/internal/auth"
	"magirecocn-revival/api-server/internal/panelstore"
	"magirecocn-revival/api-server/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "create-admin":
		os.Exit(runCreateAdmin(args))
	case "reset-admin":
		os.Exit(runResetAdmin(args))
	case "reset-account":
		os.Exit(runResetAccount(args))
	case "hash-password":
		os.Exit(runHashPassword(args))
	case "create-panel-admin":
		os.Exit(runCreatePanelAdmin(args))
	case "gen-directory-key":
		os.Exit(runGenDirectoryKey(args))
	case "sign-directory":
		os.Exit(runSignDirectory(args))
	case "verify-directory":
		os.Exit(runVerifyDirectory(args))
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `admintool — 魔法纪录服务端运维工具

子命令（游戏 DB）:
  create-admin   -dsn=DSN -email=EMAIL -username=NAME [-role=super_admin]
                 引导第一个或追加新建游戏管理员;密码从 stdin 读
  reset-admin    -dsn=DSN -email=EMAIL
                 重置某游戏管理员密码;密码从 stdin 读
  reset-account  -dsn=DSN -id=acc_xxx | -email=EMAIL
                 重置玩家账号密码
  hash-password  仅生成 scrypt 哈希,不连数据库

子命令（面板 DB）:
  create-panel-admin -db=PATH -email=EMAIL -username=NAME [-role=super_admin]
                 在面板 SQLite 里创建面板管理员（首次使用时用于离线引导）

子命令（节点目录）:
  gen-directory-key
                 生成节点目录的 Ed25519 信任根密钥对(公钥钉客户端,私钥离线)
  sign-directory   -in=DIR.json [-out=OUT.json] [-seq=N] [-ttl=48h]
                 用离线私钥签名节点目录(私钥来自 -privkey/-privkey-file/CNV_DIRECTORY_PRIVKEY)
  verify-directory -in=SIGNED.json [-pubkey=HEX]
                 用公钥校验已签名目录(CI 自检/排障)

DSN 形式与服务端 CNV_DB_URL 完全一致:postgres:// / mysql:// / sqlite:// / 文件路径。
面板 DB 始终是 SQLite 文件路径(如 ./data/panel.db)。`)
}

func runHashPassword(args []string) int {
	fs := flag.NewFlagSet("hash-password", flag.ContinueOnError)
	_ = fs.Parse(args)
	pwd, err := readPasswordTwice()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	h, err := auth.HashPassword(pwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(h)
	return 0
}

func runCreateAdmin(args []string) int {
	fs := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("CNV_DB_URL"), "数据库 DSN")
	email := fs.String("email", "", "管理员邮箱")
	username := fs.String("username", "", "管理员用户名")
	role := fs.String("role", "super_admin", "角色 (super_admin / admin / readonly)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dsn == "" || *email == "" || *username == "" {
		fmt.Fprintln(os.Stderr, "必填: -dsn -email -username")
		return 2
	}
	st, err := openStore(*dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()

	pwd, err := readPasswordTwice()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hash, err := auth.HashPassword(pwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	id := "adm_" + randHex(12)
	if err := st.AdminInsert(ctx, store.Admin{
		ID: id, Username: *username, Email: *email,
		PasswordHash: hash, Role: *role, CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		fmt.Fprintln(os.Stderr, "创建失败:", err)
		return 1
	}
	fmt.Printf("已创建管理员 id=%s role=%s\n", id, *role)
	return 0
}

func runResetAdmin(args []string) int {
	fs := flag.NewFlagSet("reset-admin", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("CNV_DB_URL"), "数据库 DSN")
	email := fs.String("email", "", "管理员邮箱")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dsn == "" || *email == "" {
		fmt.Fprintln(os.Stderr, "必填: -dsn -email")
		return 2
	}
	st, err := openStore(*dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := st.AdminByEmail(ctx, *email)
	if err != nil {
		fmt.Fprintln(os.Stderr, "找不到管理员:", err)
		return 1
	}
	pwd, err := readPasswordTwice()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hash, err := auth.HashPassword(pwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := st.AdminUpdatePassword(ctx, a.ID, hash); err != nil {
		fmt.Fprintln(os.Stderr, "更新失败:", err)
		return 1
	}
	if err := st.AdminSessionDeleteByAdmin(ctx, a.ID); err != nil {
		fmt.Fprintln(os.Stderr, "清理会话失败(已重置但旧会话仍可用):", err)
	}
	fmt.Println("已重置管理员密码,所有现有会话已失效")
	return 0
}

func runResetAccount(args []string) int {
	fs := flag.NewFlagSet("reset-account", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("CNV_DB_URL"), "数据库 DSN")
	id := fs.String("id", "", "玩家 ID(与 -email 二选一)")
	email := fs.String("email", "", "玩家邮箱(与 -id 二选一)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dsn == "" || (*id == "" && *email == "") {
		fmt.Fprintln(os.Stderr, "必填: -dsn,且 -id 或 -email 选一")
		return 2
	}
	st, err := openStore(*dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var acc *store.Account
	if *id != "" {
		acc, err = st.AccountByID(ctx, *id)
	} else {
		acc, err = st.AccountByEmail(ctx, *email)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "找不到账号:", err)
		return 1
	}
	pwd, err := readPasswordTwice()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hash, err := auth.HashPassword(pwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := st.AccountUpdatePassword(ctx, acc.ID, hash); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = st.AccountSessionDeleteByAccount(ctx, acc.ID)
	fmt.Printf("已重置玩家 %s 的密码,所有现有会话已失效\n", acc.ID)
	return 0
}

func runCreatePanelAdmin(args []string) int {
	fs := flag.NewFlagSet("create-panel-admin", flag.ContinueOnError)
	db := fs.String("db", "./data/panel.db", "面板 SQLite 文件路径")
	email := fs.String("email", "", "面板管理员邮箱")
	username := fs.String("username", "", "面板管理员用户名")
	role := fs.String("role", "super_admin", "角色 (super_admin / admin)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *email == "" || *username == "" {
		fmt.Fprintln(os.Stderr, "必填: -email -username")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ps, err := panelstore.Open(ctx, *db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开面板数据库失败:", err)
		return 1
	}
	defer ps.Close()

	pwd, err := readPasswordTwice()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hash, err := auth.HashPassword(pwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	a := panelstore.Admin{
		ID:           "padm_" + randHex(8),
		Username:     *username,
		Email:        *email,
		PasswordHash: hash,
		Role:         *role,
		CreatedAt:    time.Now().UnixMilli(),
	}
	if err := ps.AdminInsert(ctx, a); err != nil {
		fmt.Fprintln(os.Stderr, "创建失败:", err)
		return 1
	}
	fmt.Printf("已创建面板管理员 id=%s role=%s\n", a.ID, *role)
	return 0
}

func openStore(dsn string) (*store.Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	// 顺便跑一次迁移,避免空库报错
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, fmt.Errorf("迁移失败: %w", err)
	}
	return st, nil
}

// readPasswordTwice 从终端读取并要求确认密码;最少 8 位。
func readPasswordTwice() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// 非 TTY:把整行 stdin 当作密码,只读一次(便于 CI/管道)
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			return "", errors.New("stdin 没有数据")
		}
		pwd := strings.TrimSpace(sc.Text())
		if len(pwd) < 8 {
			return "", errors.New("密码至少 8 位")
		}
		return pwd, nil
	}
	fmt.Fprint(os.Stderr, "新密码: ")
	a, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(a) < 8 {
		return "", errors.New("密码至少 8 位")
	}
	fmt.Fprint(os.Stderr, "再次输入: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("两次输入不一致")
	}
	return string(a), nil
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
