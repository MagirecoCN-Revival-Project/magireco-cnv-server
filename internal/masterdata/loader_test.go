package masterdata

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "md.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const okFile = `{
  "version": "2026-08-01",
  "charas": [{
    "id": 1002, "name_zh": "七海八千代", "attribute": "WATER",
    "growth_type": "ATKDEF", "disks": "11124",
    "by_rarity": {"5": {"hp_min": 5127, "hp_max": 20354,
                        "atk_min": 2141, "atk_max": 8692,
                        "def_min": 1730, "def_max": 6971}},
    "magia": {"5": [{"code": "ATTACK", "target": "ALL", "effect": 335, "grow_point": 10}]},
    "doppel": [{"code": "ATTACK", "target": "ALL", "effect": 781, "grow_point": 22}]
  }],
  "memoria": [{
    "number": 1, "name_zh": "测试结晶", "rarity": 3,
    "stats": {"hp_min": 496, "hp_max": 1240, "atk_min": 0, "atk_max": 0,
              "def_min": 456, "def_max": 1140},
    "cooldown": 5, "cooldown_max": 4,
    "effects": [{"code": "OTHER", "target": "SELF", "sub": "ACCEL", "effect": 100, "rate": 100}]
  }]
}`

func TestLoad_OK(t *testing.T) {
	s, err := Load(write(t, okFile))
	if err != nil {
		t.Fatalf("应当加载成功: %v", err)
	}
	if s.Version() != "2026-08-01" {
		t.Errorf("version = %q", s.Version())
	}
	c, m := s.Counts()
	if c != 1 || m != 1 {
		t.Errorf("counts = %d/%d", c, m)
	}
	if ch := s.Chara(1002); ch == nil || ch.ByRarity[5].HPMax != 20354 {
		t.Errorf("Chara(1002) = %+v", ch)
	}
	if s.Chara(9999) != nil {
		t.Error("未知 charaId 应当返回 nil")
	}
	if mem := s.Memoria(1); mem == nil || mem.CooldownMax != 4 {
		t.Errorf("Memoria(1) = %+v", mem)
	}
}

// 枚举是封闭集合。放行未知 code 的后果不是报错,而是客户端拿到一条它不认识的
// 战斗指令——表现是某个技能什么都不做,而日志一切正常。
func TestLoad_RejectsUnknownEnums(t *testing.T) {
	cases := map[string]string{
		"未知 code": `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"5":{}},
			"magia":{"5":[{"code":"NOPE","target":"ALL"}]}}]}`,
		"未知 target": `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"5":{}},
			"magia":{"5":[{"code":"ATTACK","target":"NOPE"}]}}]}`,
		"未知属性": `{"charas":[{"id":1,"attribute":"PLASMA","by_rarity":{"5":{}}}]}`,
	}
	for name, body := range cases {
		_, err := Load(write(t, body))
		if err == nil {
			t.Errorf("%s: 应当被拒", name)
		}
	}
}

// 错误信息里要列出合法取值。只说"取值非法"会让排查从翻代码开始。
func TestLoad_ErrorNamesLegalValues(t *testing.T) {
	_, err := Load(write(t, `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"5":{}},
		"magia":{"5":[{"code":"NOPE","target":"ALL"}]}}]}`))
	if err == nil {
		t.Fatal("应当报错")
	}
	if got := err.Error(); !contains(got, "ATTACK") || !contains(got, "HEAL") {
		t.Errorf("错误信息应当列出合法 code: %s", got)
	}
}

// 数值区间读反了的症状是"某个角色越练越弱",玩家会当平衡性问题反馈。
func TestLoad_RejectsInvertedRange(t *testing.T) {
	_, err := Load(write(t, `{"charas":[{"id":1,"attribute":"FIRE",
		"by_rarity":{"5":{"hp_min":9999,"hp_max":100}}}]}`))
	if err == nil {
		t.Fatal("满级值低于 1 级值应当被拒")
	}
}

// 满级冷却长于初始冷却:强化只会缩短它,读反了表现是"练满反而更慢"。
func TestLoad_RejectsInvertedCooldown(t *testing.T) {
	_, err := Load(write(t, `{"memoria":[{"number":1,"rarity":3,
		"stats":{},"cooldown":4,"cooldown_max":9}]}`))
	if err == nil {
		t.Fatal("满级冷却长于初始冷却应当被拒")
	}
}

func TestLoad_RejectsStructuralProblems(t *testing.T) {
	cases := map[string]string{
		"两个集合都空": `{"version":"x","charas":[],"memoria":[]}`,
		"charaId 重复": `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"5":{}}},
			{"id":1,"attribute":"FIRE","by_rarity":{"5":{}}}]}`,
		"number 重复": `{"memoria":[{"number":1,"rarity":3,"stats":{}},
			{"number":1,"rarity":3,"stats":{}}]}`,
		"角色无数值档位": `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{}}]}`,
		"稀有度越界":   `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"9":{}}}]}`,
		"magia 档位无数值": `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"5":{}},
			"magia":{"3":[{"code":"ATTACK","target":"ALL"}]}}]}`,
		"发动率越界": `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"5":{}},
			"magia":{"5":[{"code":"ATTACK","target":"ALL","rate":150}]}}]}`,
		"不是 JSON": `nope`,
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: 应当被拒", name)
		}
	}
}

// sub 刻意不做封闭校验:抓包实测 60 种,但那是两个月的样本,几乎肯定不全。
// 把它做成封闭集合等于让样本外的合法子类型在加载期炸掉。
func TestLoad_AcceptsUnseenSub(t *testing.T) {
	_, err := Load(write(t, `{"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"5":{}},
		"magia":{"5":[{"code":"BUFF","target":"SELF","sub":"SOME_NEW_SUB"}]}}]}`))
	if err != nil {
		t.Errorf("未见过的 sub 应当放行: %v", err)
	}
}

func TestLoad_IgnoresUnknownFields(t *testing.T) {
	_, err := Load(write(t, `{"version":"v1","generated_by":"pipeline",
		"charas":[{"id":1,"attribute":"FIRE","by_rarity":{"5":{}},"future_field":42}]}`))
	if err != nil {
		t.Errorf("未知字段应当被忽略: %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("文件不存在时应当报错")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
