// Package masterdata 是战斗数值 master data 的领域类型与加载器(契约登记表 R5b)。
//
// # 它承担什么
//
// 服务端要能**生成** quest/native/get 那份战斗定义——本质上是 cards / pieces /
// enemies 的一次 join,数值已按等级解析完推给客户端。客户端只跑状态机,所有数值
// 由服务端算。这条路径的输入就是本包加载的东西。
//
// R4(战斗结算裁定边界)也直接读它:裁定伤害/回合的上界,要的正是这些数值。
//
// # 数据从哪来
//
// 两个来源互补,都不入版本库(铁律三),由构建管线提取成本包的格式后随部署挂载:
//
//   - **Wiki 标定数值**(magireco-wiki-data):253 个角色数据表 + 1,404 条记忆结晶,
//     人类校对过的 HP/ATK/DEF 区间、效果值与成长率。数值对,但形态是中文散文。
//   - **历史流量归档**(puella-historia):24,116 次 quest/native/get 真实调用,
//     把 schema 与枚举词汇表钉死。形态对,但里面是解析后的结果,反推不出成长曲线。
//
// 合起来才完整:抓包钉住枚举,Wiki 提供数值。
//
// # 为什么枚举必须是封闭集合
//
// 抓包在全部 293k 行上聚合出的词汇表是**有界**的:code 15 种、target 7 种、
// sub 60 种。本包据此拒绝任何未登记的取值。
//
// 放行未知值的后果不是报错,而是**客户端拿到一条它不认识的战斗指令**——表现是
// 战斗里某个技能什么都不做,而服务端日志一切正常。那种缺陷要靠玩家察觉"这个技能
// 好像没生效"才会被发现,且极难复现。封闭集合把它变成一次加载期的启动失败。
package masterdata

import (
	"fmt"
	"sort"
)

// Attribute 属性(火/水/木/光/暗/无)。
type Attribute string

// Stats 某个稀有度下的数值区间(Lv1 → 满级)。
type Stats struct {
	HPMin  int `json:"hp_min"`
	HPMax  int `json:"hp_max"`
	ATKMin int `json:"atk_min"`
	ATKMax int `json:"atk_max"`
	DEFMin int `json:"def_min"`
	DEFMax int `json:"def_max"`
}

// Art 是一条已结构化的效果,字段名与上游 quest/native/get 的 artList 对齐。
//
// 这是 Wiki 那句中文散文("（全体）属性强化伤害，效果值：305%，成长：10%")被映射
// 之后的形态。映射是**受限**的:三个枚举都是封闭集合,所以这不是开放式自然语言
// 解析,而是一次查表。
type Art struct {
	// Code 效果大类,如 ATTACK / HEAL / BUFF / DEBUFF。
	Code string `json:"code"`
	// Target 作用目标,如 ALL / ONE / SELF / CONNECT。
	Target string `json:"target"`
	// Sub 子类型,如 HP / ACCEL / POISON。可空(部分 code 无子类型)。
	Sub string `json:"sub,omitempty"`
	// Effect 效果值(上游是整数百分比,305 表示 305%)。
	Effect int `json:"effect"`
	// GrowPoint 每级成长值。
	GrowPoint int `json:"grow_point"`
	// Rate 发动率百分比;0 表示必定发动。
	Rate int `json:"rate,omitempty"`
	// Turns 持续回合;0 表示瞬时。
	Turns int `json:"turns,omitempty"`
}

// Chara 一名魔法少女的战斗数值。
type Chara struct {
	// ID 上游 charaId,与 Wiki 的 charaId、抓包里的 ID 对齐。
	ID int `json:"id"`
	// NameZh 中文名,仅供人读(日志、后台),不参与任何逻辑。
	NameZh string `json:"name_zh"`
	// Attribute 属性。
	Attribute Attribute `json:"attribute"`
	// GrowthType 成长类型,如 ATKDEF。
	GrowthType string `json:"growth_type,omitempty"`
	// Disks 行动盘配置,5 位数字(如 11124),每位对应一个盘位。
	Disks string `json:"disks"`
	// ByRarity 按稀有度分档的数值。键是稀有度(3/4/5)。
	ByRarity map[int]Stats `json:"by_rarity"`
	// Magia / Doppel 各稀有度下的效果。键同 ByRarity。
	Magia  map[int][]Art `json:"magia,omitempty"`
	Doppel []Art         `json:"doppel,omitempty"`
}

// Memoria 一枚记忆结晶的战斗数值。
type Memoria struct {
	// Number 图鉴编号,与 Wiki 的 number 对齐。
	Number int `json:"number"`
	// NameZh 中文名,仅供人读。
	NameZh string `json:"name_zh"`
	Rarity int    `json:"rarity"`
	Stats  Stats  `json:"stats"`
	// Cooldown / CooldownMax 冷却回合(满级时缩短)。
	Cooldown    int   `json:"cooldown"`
	CooldownMax int   `json:"cooldown_max"`
	Effects     []Art `json:"effects,omitempty"`
}

// ── 封闭枚举(来自抓包在 293k 行上的聚合)────────────────────────────────

// Codes 是 art 的效果大类,15 种。
var Codes = closedSet(
	"ATTACK", "BUFF", "BUFF_DIE", "BUFF_DYING", "BUFF_HPMAX", "BUFF_PARTY_DIE",
	"CONDITION_BAD", "CONDITION_GOOD", "DEBUFF", "ENCHANT", "HEAL", "IGNORE",
	"OTHER", "RESURRECT", "REVOKE",
)

// Targets 是 art 的作用目标,7 种。
var Targets = closedSet(
	"ALL", "CONNECT", "LIMITED", "ONE", "RANDOM5", "SELF", "TARGET",
)

// Attributes 是角色属性的封闭集合。
var Attributes = closedSet("FIRE", "WATER", "TIMBER", "LIGHT", "DARK", "VOID")

func closedSet(vs ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(vs))
	for _, v := range vs {
		m[v] = struct{}{}
	}
	return m
}

// SortedKeys 返回集合里的取值,升序。用于把"允许什么"写进错误信息——
// 报错只说"取值非法"而不说合法的是哪些,会让排查从查代码开始。
func SortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Validate 校验一条 art。
//
// Sub 刻意**不做封闭校验**:抓包实测 60 种,但那是 2024-06~07 两个月的样本,
// 几乎肯定不全——把它做成封闭集合,等于让任何一个样本外的合法子类型在加载期
// 炸掉。code 与 target 不同:它们是效果系统的骨架,15 与 7 这两个数字在两个月、
// 29 万次调用里都没变过,可以当封闭集合看。
//
// 这个不对称是有意的:封闭集合的价值在于挡住拼写错误,而代价是挡住没见过的合法值。
// 只在"证据足够说明它确实封闭"的地方付这个代价。
func (a Art) Validate() error {
	if _, ok := Codes[a.Code]; !ok {
		return fmt.Errorf("未知的 code %q,合法取值: %v", a.Code, SortedKeys(Codes))
	}
	if _, ok := Targets[a.Target]; !ok {
		return fmt.Errorf("未知的 target %q,合法取值: %v", a.Target, SortedKeys(Targets))
	}
	if a.Effect < 0 || a.GrowPoint < 0 {
		return fmt.Errorf("effect/grow_point 不得为负: effect=%d grow_point=%d", a.Effect, a.GrowPoint)
	}
	if a.Rate < 0 || a.Rate > 100 {
		return fmt.Errorf("rate 须在 0..100: %d", a.Rate)
	}
	if a.Turns < 0 {
		return fmt.Errorf("turns 不得为负: %d", a.Turns)
	}
	return nil
}

// Validate 校验一档数值区间。
func (s Stats) Validate() error {
	for _, p := range []struct {
		name     string
		min, max int
	}{
		{"hp", s.HPMin, s.HPMax},
		{"atk", s.ATKMin, s.ATKMax},
		{"def", s.DEFMin, s.DEFMax},
	} {
		if p.min < 0 || p.max < 0 {
			return fmt.Errorf("%s 不得为负: %d..%d", p.name, p.min, p.max)
		}
		// 满级不该低于 1 级。这类错误通常是提取时把两列读反了,而症状是某个角色
		// 越练越弱——玩家会当成平衡性问题反馈,没人会想到是数据提取的问题。
		if p.max < p.min {
			return fmt.Errorf("%s 满级值低于 1 级值: %d < %d", p.name, p.max, p.min)
		}
	}
	return nil
}
