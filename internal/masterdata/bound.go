package masterdata

// 战斗结算的伤害上界(契约登记表 R4)。
//
// # 这不是伤害公式
//
// 真实伤害要过属性克制、防御减伤、暴击、buff 叠加、连携加成——那套公式我们并不
// 掌握,也不需要掌握。这里算的是一个**可证明的过估值**:把所有会**降低**伤害的
// 因素一概忽略,只留会提高的。
//
// 忽略减伤只会让结果偏大,所以算出来的数一定 ≥ 任何真实伤害。这正是裁定需要的
// 方向——R4 的默认取向是「宁可漏判,不可误判」:
//
//   - 上界偏大 → 个别作弊者卡在界内没被抓到(可接受);
//   - 上界偏小 → **正常玩家的通关被判为作弊**(不可接受)。
//
// 误封的代价由无辜的人承担,而作弊者大不了换个号。所以这里刻意只往宽了算。

// TeamMember 是队伍里的一个出战位,用于算上界。
type TeamMember struct {
	// CharaID 角色 ID。
	CharaID int
	// Rarity 出战时的稀有度档位。
	Rarity int
	// MemoriaNumbers 该位装备的记忆结晶图鉴编号。
	MemoriaNumbers []int
}

// PeakATK 返回某稀有度档位下的满级攻击力;该档不存在时返回 0。
func (c *Chara) PeakATK(rarity int) int {
	st, ok := c.ByRarity[rarity]
	if !ok {
		return 0
	}
	return st.ATKMax
}

// peakMultiplier 返回该角色在某档位下最强的效果倍率(百分比)。
//
// magia 与 doppel 都算进来并取最大值:一次出手不可能同时放两者,取大的那个即可
// 保证不低估。
func (c *Chara) peakMultiplier(rarity int) int {
	best := 100 // 普通攻击按 100% 计,保证至少有个下限
	for _, a := range c.Magia[rarity] {
		if m := a.Effect + a.GrowPoint*maxGrowLevels; m > best {
			best = m
		}
	}
	for _, a := range c.Doppel {
		if m := a.Effect + a.GrowPoint*maxGrowLevels; m > best {
			best = m
		}
	}
	return best
}

// maxGrowLevels 是效果成长的档数上限。
//
// 成长档数本身属于 quest / 养成 master data,现在还没有(见 R4「还缺什么」)。
// 在拿到之前取一个**明确偏大**的常数:上界偏大只会漏判,而偏小会误判。
// 接入真实档数后把这个常数换掉,上界会收紧,但方向不变。
const maxGrowLevels = 10

// MaxSingleTurnDamage 返回一支队伍单回合理论最大输出的过估值。
//
// 未知的角色/稀有度档位按 0 计入——**不是**报错:队伍构成的合法性由归属校验那一步
// 负责(那一步会拒绝不属于该账号的成员),这里只管算上界。把两件事混在一起,会让
// 一次数据缺失表现成一次作弊指控。
func (s *Set) MaxSingleTurnDamage(team []TeamMember) int64 {
	var total int64
	for _, m := range team {
		c := s.Chara(m.CharaID)
		if c == nil {
			continue
		}
		atk := int64(c.PeakATK(m.Rarity))
		// 记忆结晶的攻击力加成直接累加。同样只加不减。
		for _, num := range m.MemoriaNumbers {
			if mem := s.Memoria(num); mem != nil {
				atk += int64(mem.Stats.ATKMax)
			}
		}
		total += atk * int64(c.peakMultiplier(m.Rarity)) / 100
	}
	return total
}

// DefaultDamageTolerance 是伤害上界的宽容系数(百分比)。
//
// 理论最大输出的计算必然比实际偏保守——buff 叠加、暴击、连携的组合爆炸算不全。
// 卡太死就会误判,所以再乘一道余量。
//
// 150 = 1.5 倍。宁可让一个作弊者卡在 1.5 倍上界之内,也不要让一个欧皇的暴击连携
// 被判作弊。
const DefaultDamageTolerance = 150

// DamageWithinBound 判断上报的总伤害是否在允许范围内。
//
// turns 是回合数,tolerancePct 是宽容系数(百分比,<=0 时用 DefaultDamageTolerance)。
// 上界为 0(队伍全员未知)时一律放行——**不能因为数据缺失就指控作弊**。
func (s *Set) DamageWithinBound(team []TeamMember, turns int, reported int64, tolerancePct int) bool {
	if turns <= 0 {
		return false
	}
	if tolerancePct <= 0 {
		tolerancePct = DefaultDamageTolerance
	}
	per := s.MaxSingleTurnDamage(team)
	if per == 0 {
		return true
	}
	bound := per * int64(turns) * int64(tolerancePct) / 100
	return reported <= bound
}
