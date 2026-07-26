package packer

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// 准备一份 source 目录,里面有几个文件 + 一个嵌套目录。
func makeSourceTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(p, content string) {
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("data/master.bin", "MASTER_BLOB")
	mustWrite("data/scenario_001.zip", "FAKE_SCENARIO_PAYLOAD_1234567890")
	mustWrite("images/cards/100.png", "PNG_BYTES")
	mustWrite("README.txt", "demo")
	return dir
}

func TestRunOnce_BuildsValidZipWithMeta(t *testing.T) {
	src := makeSourceTree(t)
	dest := t.TempDir()
	res, err := RunOnce(context.Background(), Config{
		SourceDir:        src,
		DestDir:          dest,
		MinClientVersion: "2.4.0",
		Description:      "测试包",
		Now:              time.Date(2026, 5, 31, 22, 55, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "202605312255" {
		t.Errorf("版本号格式异常: %q", res.Version)
	}
	if res.Filename != "cn_offline_pack_202605312255.zip" {
		t.Errorf("文件名: %q", res.Filename)
	}
	if got := len(res.SHA256); got != 64 {
		t.Errorf("SHA-256 必须 64 hex 字符,得到 %d", got)
	}

	// SHA-256 必须与磁盘上 zip 真实哈希一致(防止 multiWriter 接错)
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(data)
	if hex.EncodeToString(wantHash[:]) != res.SHA256 {
		t.Errorf("SHA-256 与磁盘文件不一致")
	}
	if res.Size != int64(len(data)) {
		t.Errorf("Size 与磁盘大小不一致: %d vs %d", res.Size, len(data))
	}

	// 打开 zip 校验内容
	zr, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		got[f.Name] = string(b)
	}
	wantFiles := []string{
		"cnv_pack_meta.json",
		"data/master.bin",
		"data/scenario_001.zip",
		"images/cards/100.png",
		"README.txt",
	}
	for _, w := range wantFiles {
		if _, has := got[w]; !has {
			t.Errorf("zip 缺少 %q", w)
		}
	}
	// 元数据内容
	var meta metaJSON
	if err := json.Unmarshal([]byte(got["cnv_pack_meta.json"]), &meta); err != nil {
		t.Fatalf("元数据不是合法 JSON: %v", err)
	}
	if meta.PackVersion != "202605312255" {
		t.Errorf("pack_version: %q", meta.PackVersion)
	}
	if meta.MinClientVersion != "2.4.0" {
		t.Errorf("min_client_version: %q", meta.MinClientVersion)
	}
	if meta.Description != "测试包" {
		t.Errorf("description: %q", meta.Description)
	}
	if meta.BuildDate == "" {
		t.Errorf("build_date 不应为空")
	}

	// 校验内嵌的资源文件内容
	if got["data/master.bin"] != "MASTER_BLOB" {
		t.Errorf("master.bin 内容错: %q", got["data/master.bin"])
	}
}

func TestRunOnce_Retention(t *testing.T) {
	src := makeSourceTree(t)
	dest := t.TempDir()
	// 跑 3 次,RetainN=2 → 最早那次会被删
	for i, ts := range []time.Time{
		time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 30, 14, 0, 0, 0, time.UTC),
	} {
		if _, err := RunOnce(context.Background(), Config{
			SourceDir: src, DestDir: dest, RetainN: 2, Now: ts,
		}); err != nil {
			t.Fatalf("第 %d 次打包: %v", i+1, err)
		}
	}
	ent, _ := os.ReadDir(dest)
	var zips []string
	for _, e := range ent {
		if strings.HasSuffix(e.Name(), ".zip") {
			zips = append(zips, e.Name())
		}
	}
	sort.Strings(zips)
	if len(zips) != 2 {
		t.Fatalf("应保留 2 份,得到 %d: %v", len(zips), zips)
	}
	// 留下的应当是最新的两个
	if zips[0] != "cn_offline_pack_202605301200.zip" ||
		zips[1] != "cn_offline_pack_202605301400.zip" {
		t.Errorf("留下的不是最新两份: %v", zips)
	}
}

func TestRunOnce_RefusesMetaCollision(t *testing.T) {
	src := t.TempDir()
	// 攻击者在 SourceDir 里塞了一个同名文件想覆盖元数据
	_ = os.WriteFile(filepath.Join(src, "cnv_pack_meta.json"),
		[]byte(`{"pack_version":"forged"}`), 0o644)
	dest := t.TempDir()
	_, err := RunOnce(context.Background(), Config{
		SourceDir: src, DestDir: dest,
	})
	if err == nil || !strings.Contains(err.Error(), "cnv_pack_meta.json") {
		t.Errorf("应当拒绝并报名字冲突,得到 err=%v", err)
	}
}

func TestRunOnce_MissingSource(t *testing.T) {
	if _, err := RunOnce(context.Background(), Config{
		SourceDir: "/nonexistent/path/xyz", DestDir: t.TempDir(),
	}); err == nil {
		t.Errorf("不存在的 SourceDir 应当报错")
	}
	if _, err := RunOnce(context.Background(), Config{
		SourceDir: "", DestDir: t.TempDir(),
	}); err == nil {
		t.Errorf("空 SourceDir 应当报错")
	}
}
