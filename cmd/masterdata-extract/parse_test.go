package main

import "testing"

// 固件全部自制(铁律三:不得使用提取自游戏的数据),只复刻 Wiki 的**写法**。
var testEffects = effectMap{
	"属性强化伤害": {Code: "ATTACK"},
	"攻击力UP":  {Code: "BUFF", Sub: "ATTACK"},
	"魅惑":     {Code: "CONDITION_BAD"},
}

func TestParseParams(t *testing.T) {
	got := parseParams(`<includeonly>{{某模板}}</includeonly>
| 属性 = 火
| 成长类型 = ATKDEF
| 盘 = 11124
| HP最小3 = 3645
| HP最大3 = 12305
`)
	for k, want := range map[string]string{
		"属性": "火", "成长类型": "ATKDEF", "盘": "11124",
		"HP最小3": "3645", "HP最大3": "12305",
	} {
		if got[k] != want {
			t.Errorf("params[%q] = %q, want %q", k, got[k], want)
		}
	}
}

func TestParseArts(t *testing.T) {
	gaps := gapSet{}
	arts := parseArts("（全体）属性强化伤害，效果值：305%，成长：10%", testEffects, gaps)
	if len(arts) != 1 {
		t.Fatalf("应当解出 1 条,得到 %+v", arts)
	}
	a := arts[0]
	if a.Code != "ATTACK" || a.Target != "ALL" || a.Effect != 305 || a.GrowPoint != 10 {
		t.Errorf("解析错误: %+v", a)
	}
	if len(gaps) != 0 {
		t.Errorf("不该有缺口: %v", gaps)
	}
}

func TestParseArts_MultipleSeparatedByBr(t *testing.T) {
	gaps := gapSet{}
	arts := parseArts(
		"（全体）属性强化伤害，效果值：335%，成长：10%<br>（全体）魅惑，发动率：50%，持续回合：1",
		testEffects, gaps)
	if len(arts) != 2 {
		t.Fatalf("应当解出 2 条,得到 %+v", arts)
	}
	if arts[1].Code != "CONDITION_BAD" || arts[1].Rate != 50 || arts[1].Turns != 1 {
		t.Errorf("第二条解析错误: %+v", arts[1])
	}
	// 发动率/持续回合只属于所在的那一段,不得串到前一条上。
	if arts[0].Rate != 0 || arts[0].Turns != 0 {
		t.Errorf("字段串段了: %+v", arts[0])
	}
}

// 空格差异是 Wiki 的转录噪声,不该让映射表为它多存一条。
func TestParseArts_NormalizesSpacing(t *testing.T) {
	eff := effectMap{"AcceleMPUP": {Code: "BUFF"}}
	gaps := gapSet{}
	if arts := parseArts("（自身）Accele MPUP，效果值：20%", eff, gaps); len(arts) != 1 {
		t.Errorf("带空格的写法应当命中同一条映射,gaps=%v", gaps)
	}
}

// 未映射的效果名必须进 gaps 并跳过——静默放过会产出一份看起来正常、
// 实则缺了效果的 master data。
func TestParseArts_RecordsGaps(t *testing.T) {
	gaps := gapSet{}
	arts := parseArts("（全体）某个没见过的效果，效果值：100%", testEffects, gaps)
	if len(arts) != 0 {
		t.Errorf("未映射的效果不得产出: %+v", arts)
	}
	e := gaps["某个没见过的效果"]
	if e == nil || e.Count != 1 {
		t.Fatalf("应当记进 gaps: %+v", e)
	}
	// 例句要一并留下——光有名字判断不出该映射到哪个 code。
	if e.Sample == "" {
		t.Error("缺口应当带一条真实例句")
	}
}

func TestTargetsCoverWikiVocabulary(t *testing.T) {
	// 抓包的 target 是 7 值封闭集合;映射表的值必须都落在里面。
	legal := map[string]bool{
		"ALL": true, "CONNECT": true, "LIMITED": true,
		"ONE": true, "RANDOM5": true, "SELF": true, "TARGET": true,
	}
	for zh, en := range targets {
		if !legal[en] {
			t.Errorf("targets[%q] = %q 不在上游封闭集合内", zh, en)
		}
	}
}
