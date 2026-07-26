// Package packer 把指定目录(资源根)打成离线整包 zip,生成 cnv_pack_meta.json
// 放在 zip 根目录,并产出 sha256/版本号供 /admin/offline-package 直接落库。
//
// 设计目标:
//   * 不依赖外部命令(unzip/zip 工具),只用 archive/zip
//   * 单遍流式:不把整个 zip 缓冲到内存,边写边算 sha256
//   * 幂等:并发调用同一份 dest 会有惊扰但不会损坏旧包
//     (用临时文件 + os.Rename 保证 atomic)
//   * 自带 retention:按文件名前缀里的版本号倒序保留 N 份
//
// 对应客户端文档 server-offline-pack-validation.md §6:
//   cnv_pack_meta.json 是客户端解包时寻找的元数据文件,落在 zip 根目录,
//   字段含 pack_version(必填) / build_date / description / min_client_version。
package packer

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Config 一次打包的全部输入。
type Config struct {
	// SourceDir 资源根目录;walker 从这里开始递归。空则报错。
	SourceDir string
	// DestDir 产物目录;不存在会自动 MkdirAll。
	DestDir string
	// FilenamePrefix zip 文件名前缀(不含扩展名)。默认 "cn_offline_pack"。
	FilenamePrefix string
	// MinClientVersion 写入 cnv_pack_meta.json 的 min_client_version(可选)。
	MinClientVersion string
	// Description 写入 cnv_pack_meta.json 的 description(可选)。
	Description string
	// RetainN 产物目录里保留最近 N 份 zip;<=0 不清理。
	RetainN int
	// Now 注入当前时间,便于测试。零值时取 time.Now()。
	Now time.Time
}

// Result 打包结果。
type Result struct {
	Path     string    // 产物的绝对路径
	Filename string    // 产物文件名(basename)
	Version  string    // pack_version,格式 YYYYMMDDhhmm
	SHA256   string    // zip 的 SHA-256(64 字符小写 hex)
	Size     int64     // zip 大小(字节)
	BuiltAt  time.Time // 打包完成时间
}

var (
	ErrNoSource     = errors.New("packer: SourceDir 为空")
	ErrSourceMissing = errors.New("packer: SourceDir 不存在或不可读")
)

// RunOnce 执行一次打包。
//
// 过程:
//  1. 校验 SourceDir 存在且可读
//  2. 计算版本号(time.Now().Format(\"200601021504\"))
//  3. 在 DestDir 里建临时文件 <prefix>-<version>.zip.tmp,边写 zip 边算 sha256
//  4. 先把 cnv_pack_meta.json 写进 zip(根目录第一项,便于调试解包看到元数据)
//  5. 递归把 SourceDir 的所有文件加进 zip(路径用正斜杠相对 SourceDir)
//  6. 关闭 zip writer + flush,fsync,rename 到正式文件名
//  7. 按 RetainN 清理旧产物
//
// ctx 在大目录里能用来提前取消(每 256 个文件检查一次)。
func RunOnce(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.SourceDir == "" {
		return nil, ErrNoSource
	}
	if cfg.FilenamePrefix == "" {
		cfg.FilenamePrefix = "cn_offline_pack"
	}
	srcInfo, err := os.Stat(cfg.SourceDir)
	if err != nil || !srcInfo.IsDir() {
		return nil, ErrSourceMissing
	}
	if err := os.MkdirAll(cfg.DestDir, 0o755); err != nil {
		return nil, fmt.Errorf("packer: MkdirAll(%s): %w", cfg.DestDir, err)
	}

	now := cfg.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	version := now.Format("200601021504") // YYYYMMDDHHMM
	finalName := fmt.Sprintf("%s_%s.zip", cfg.FilenamePrefix, version)
	tmpPath := filepath.Join(cfg.DestDir, finalName+".tmp")
	finalPath := filepath.Join(cfg.DestDir, finalName)

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	// 失败路径上清理 tmp
	cleanupTmp := func() { _ = os.Remove(tmpPath) }

	// 同时写文件 + 算 sha256
	hash := sha256.New()
	mw := io.MultiWriter(f, hash)
	zw := zip.NewWriter(mw)

	// 1) cnv_pack_meta.json 放在 zip 根目录第一位
	meta := metaJSON{
		PackVersion:      version,
		MinClientVersion: cfg.MinClientVersion,
		BuildDate:        now.Format(time.RFC3339),
		Description:      cfg.Description,
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	if err := writeZipEntry(zw, "cnv_pack_meta.json", metaBytes, now); err != nil {
		_ = zw.Close()
		_ = f.Close()
		cleanupTmp()
		return nil, fmt.Errorf("packer: 写元数据失败: %w", err)
	}

	// 2) 递归打包 SourceDir
	count := 0
	walkErr := filepath.Walk(cfg.SourceDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		count++
		if count%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if info.IsDir() {
			return nil
		}
		// 软链接默认跟随;若为奇怪文件类型(socket/device)跳过
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(cfg.SourceDir, p)
		if err != nil {
			return err
		}
		// zip 用正斜杠
		rel = filepath.ToSlash(rel)
		// 防呆:不允许 .. 跳出
		if strings.HasPrefix(rel, "..") {
			return nil
		}
		// 与 cnv_pack_meta.json 同名时覆盖元数据是 bug,直接拒绝
		if rel == "cnv_pack_meta.json" {
			return fmt.Errorf("packer: SourceDir 含名为 cnv_pack_meta.json 的文件,会覆盖元数据;请移除")
		}
		fh, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		fh.Name = rel
		fh.Method = zip.Deflate
		fh.Modified = info.ModTime()
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, src)
		_ = src.Close()
		return copyErr
	})
	if walkErr != nil {
		_ = zw.Close()
		_ = f.Close()
		cleanupTmp()
		return nil, fmt.Errorf("packer: 打包失败: %w", walkErr)
	}

	if err := zw.Close(); err != nil {
		_ = f.Close()
		cleanupTmp()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanupTmp()
		return nil, err
	}
	if err := f.Close(); err != nil {
		cleanupTmp()
		return nil, err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		cleanupTmp()
		return nil, err
	}

	stat, err := os.Stat(finalPath)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Path:     finalPath,
		Filename: finalName,
		Version:  version,
		SHA256:   hex.EncodeToString(hash.Sum(nil)),
		Size:     stat.Size(),
		BuiltAt:  now,
	}

	if cfg.RetainN > 0 {
		_ = retainRecent(cfg.DestDir, cfg.FilenamePrefix, cfg.RetainN)
	}
	return res, nil
}

// metaJSON 与客户端 server-offline-pack-validation.md §6.2 字段一一对应。
type metaJSON struct {
	PackVersion      string `json:"pack_version"`
	MinClientVersion string `json:"min_client_version,omitempty"`
	BuildDate        string `json:"build_date,omitempty"`
	Description      string `json:"description,omitempty"`
}

// writeZipEntry 把一段内存数据写成 zip 条目(deflate)。
func writeZipEntry(zw *zip.Writer, name string, content []byte, modTime time.Time) error {
	fh := &zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: modTime,
	}
	w, err := zw.CreateHeader(fh)
	if err != nil {
		return err
	}
	_, err = w.Write(content)
	return err
}

// retainRecent 把目录里同前缀的 zip 按文件名降序排,保留前 keep 份,
// 其余删除。文件名形如 <prefix>_YYYYMMDDhhmm.zip,字典序 = 时间序。
func retainRecent(dir, prefix string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var matched []string
	pfx := prefix + "_"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, pfx) && strings.HasSuffix(n, ".zip") {
			matched = append(matched, n)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(matched)))
	for i, n := range matched {
		if i < keep {
			continue
		}
		_ = os.Remove(filepath.Join(dir, n))
	}
	return nil
}
