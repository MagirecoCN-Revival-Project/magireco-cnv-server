package scenemanifest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const okHash = "3c0a9b8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a09"

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "scenes.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFile_OK(t *testing.T) {
	p := writeManifest(t, `{
	  "version": "2026-08-01",
	  "scenes": {
	    "story/11011": [
	      {"path": "resource/b.hca", "sha256": "`+okHash+`", "size": 200},
	      {"path": "resource/a.png", "sha256": "`+okHash+`", "size": 100}
	    ]
	  }
	}`)
	pr, err := LoadFile(p)
	if err != nil {
		t.Fatalf("应当加载成功: %v", err)
	}
	if pr.Version() != "2026-08-01" || pr.Len() != 1 {
		t.Fatalf("version=%q len=%d", pr.Version(), pr.Len())
	}

	got, err := pr.Lookup(context.Background(), "story/11011")
	if err != nil || len(got) != 2 {
		t.Fatalf("Lookup: %v %v", got, err)
	}
	// 预排序:响应要求按 path 升序,加载时就该做掉。
	if got[0].Path != "resource/a.png" {
		t.Errorf("加载后应已按 path 排序,得到 %v", got[0].Path)
	}

	// 未知场景返回 nil 而不是空切片——handler 靠这个区分 404 与"无资产"。
	miss, err := pr.Lookup(context.Background(), "story/nope")
	if err != nil || miss != nil {
		t.Errorf("未知场景应当返回 nil,得到 %v %v", miss, err)
	}
}

// 全量校验在**启动时**做完。这类错误的症状离原因极远:一个 sha256 少两位,
// 表现是某个玩家在某个场景卡住,而日志里一切正常。
func TestLoadFile_RejectsBadManifests(t *testing.T) {
	cases := map[string]string{
		"scene_id 畸形": `{"scenes":{"quest_101101":[{"path":"a","sha256":"` + okHash + `","size":1}]}}`,
		"scene_id 穿越": `{"scenes":{"story/..":[{"path":"a","sha256":"` + okHash + `","size":1}]}}`,
		"缺 scenes":    `{"version":"x"}`,
		"空资产列表":       `{"scenes":{"story/1":[]}}`,
		"缺 path":      `{"scenes":{"story/1":[{"sha256":"` + okHash + `","size":1}]}}`,
		"哈希截断":        `{"scenes":{"story/1":[{"path":"a","sha256":"abc","size":1}]}}`,
		"哈希大写":        `{"scenes":{"story/1":[{"path":"a","sha256":"` + strings.ToUpper(okHash) + `","size":1}]}}`,
		"size 为负":     `{"scenes":{"story/1":[{"path":"a","sha256":"` + okHash + `","size":-1}]}}`,
		"path 重复": `{"scenes":{"story/1":[
			{"path":"a","sha256":"` + okHash + `","size":1},
			{"path":"a","sha256":"` + okHash + `","size":2}]}}`,
		"不是 JSON": `not json at all`,
	}
	for name, body := range cases {
		if _, err := LoadFile(writeManifest(t, body)); err == nil {
			t.Errorf("%s: 应当拒绝加载", name)
		}
	}
}

func TestLoadFile_MissingFile(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("文件不存在时应当报错")
	}
}

// 清单由构建管线产出;将来加字段不该让旧服务端拒绝启动。
func TestLoadFile_IgnoresUnknownFields(t *testing.T) {
	p := writeManifest(t, `{
	  "version": "v1",
	  "generated_by": "future-pipeline",
	  "scenes": {"story/1": [{"path":"a","sha256":"`+okHash+`","size":1,"codec":"astc"}]}
	}`)
	if _, err := LoadFile(p); err != nil {
		t.Errorf("未知字段应当被忽略而不是拒绝: %v", err)
	}
}

// 同一份清单必须算出同一个 manifest_hash——这是客户端增量的地基。
func TestProviderHashStableAcrossLoads(t *testing.T) {
	body := `{"scenes":{"story/1":[
		{"path":"b","sha256":"` + okHash + `","size":2},
		{"path":"a","sha256":"` + okHash + `","size":1}]}}`

	first, err := LoadFile(writeManifest(t, body))
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadFile(writeManifest(t, body))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first.Lookup(context.Background(), "story/1")
	b, _ := second.Lookup(context.Background(), "story/1")
	if Hash(a) != Hash(b) {
		t.Fatal("同一份清单两次加载必须算出同一个 manifest_hash")
	}
}
