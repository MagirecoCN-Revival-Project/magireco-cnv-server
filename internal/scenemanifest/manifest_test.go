package scenemanifest

import "testing"

func TestValidateSceneID(t *testing.T) {
	good := []string{
		"story/11011", "battle/quest_200400401", "chara/1001",
		"ui/event_2026_fes", "movie/op-2026.mp4", "audio/bgm_battle_extra",
		"my_ns/a",
	}
	for _, id := range good {
		if err := ValidateSceneID(id); err != nil {
			t.Errorf("%q 应当合法: %v", id, err)
		}
	}

	bad := []string{
		"", "story", "/11011", "story/", "Story/11011", "1story/x",
		"story//11011", "story/a/b", "story/../../etc/passwd",
		"story/" + string(make([]byte, 65)), "story/带中文",
		"story/a b", "story/a?b=1", "story/a%2e%2e",
	}
	for _, id := range bad {
		if err := ValidateSceneID(id); err == nil {
			t.Errorf("%q 应当被拒", id)
		}
	}
}

// 路径穿越形态单列一条:即便当前实现只拿它查表,格式闸也必须挡住这些。
func TestSceneIDRejectsTraversalShapes(t *testing.T) {
	for _, id := range []string{
		"story/..", "story/../secret", "../story/x", "story/%2e%2e%2f",
	} {
		if err := ValidateSceneID(id); err != ErrMalformedSceneID {
			t.Errorf("%q 必须以 ErrMalformedSceneID 被拒,得到 %v", id, err)
		}
	}
}

var sample = []Asset{
	{Path: "resource/chara/1001/base.moc3", SHA256: "aaa", Size: 412849},
	{Path: "resource/bgm/battle.acb", SHA256: "bbb", Size: 1048576},
}

// 提供方的返回顺序不该影响哈希,否则一次 ORDER BY 改动就让所有客户端白重下一遍。
func TestHashIsOrderIndependent(t *testing.T) {
	reversed := []Asset{sample[1], sample[0]}
	if Hash(sample) != Hash(reversed) {
		t.Fatal("清单哈希不应随输入顺序变化")
	}
}

func TestHashChangesWithContent(t *testing.T) {
	base := Hash(sample)

	for name, mutate := range map[string]func([]Asset){
		"改哈希": func(a []Asset) { a[0].SHA256 = "ccc" },
		"改大小": func(a []Asset) { a[0].Size = 1 },
		"改路径": func(a []Asset) { a[0].Path = "resource/other" },
	} {
		m := make([]Asset, len(sample))
		copy(m, sample)
		mutate(m)
		if Hash(m) == base {
			t.Errorf("%s 之后清单哈希必须变化", name)
		}
	}

	// 少一项也必须变——否则客户端会以为自己手里那份是全的。
	if Hash(sample[:1]) == base {
		t.Error("清单少一项时哈希必须变化")
	}
}

// 字段用 \x00 分隔,防止跨字段的拼接歧义:
// 若用普通分隔符,{"a","b"} 与 {"a\tb",""} 这类组合会撞成同一个哈希。
func TestHashNoFieldConfusion(t *testing.T) {
	a := []Asset{{Path: "a", SHA256: "b", Size: 1}}
	b := []Asset{{Path: "a\x00b", SHA256: "", Size: 1}}
	if Hash(a) == Hash(b) {
		t.Fatal("跨字段拼接不得产生相同哈希")
	}
}

func TestHashEmptyIsStable(t *testing.T) {
	if Hash(nil) != Hash([]Asset{}) {
		t.Error("nil 与空切片应当算出同一个哈希")
	}
	if Hash(nil) == Hash(sample) {
		t.Error("空清单与非空清单的哈希不得相同")
	}
}

func TestSortedDoesNotMutateInput(t *testing.T) {
	in := []Asset{sample[0], sample[1]}
	orig := in[0].Path
	out := Sorted(in)
	if in[0].Path != orig {
		t.Error("Sorted 不得改动入参")
	}
	if out[0].Path >= out[1].Path {
		t.Errorf("输出应按 path 升序: %v", out)
	}
}
