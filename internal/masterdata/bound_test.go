package masterdata

import "testing"

func boundSet(t *testing.T) *Set {
	t.Helper()
	s, err := Load(write(t, `{
	  "charas": [{
	    "id": 1, "attribute": "FIRE",
	    "by_rarity": {"5": {"atk_min": 100, "atk_max": 1000,
	                        "hp_min": 1, "hp_max": 2, "def_min": 1, "def_max": 2}},
	    "magia": {"5": [{"code": "ATTACK", "target": "ALL", "effect": 300, "grow_point": 10}]}
	  }],
	  "memoria": [{"number": 7, "rarity": 5,
	    "stats": {"atk_min": 10, "atk_max": 500, "hp_min": 1, "hp_max": 2,
	              "def_min": 1, "def_max": 2},
	    "cooldown": 5, "cooldown_max": 4}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMaxSingleTurnDamage(t *testing.T) {
	s := boundSet(t)
	// ATK 1000 + 结晶 500 = 1500;倍率 300 + 10*10 = 400% → 6000
	got := s.MaxSingleTurnDamage([]TeamMember{
		{CharaID: 1, Rarity: 5, MemoriaNumbers: []int{7}},
	})
	if got != 6000 {
		t.Fatalf("上界 = %d, want 6000", got)
	}
}

// 上界忽略一切减伤,所以必须 ≥ 任何真实伤害。这条是整个方案的地基:
// 上界偏大只会漏判,偏小会把正常玩家判成作弊。
func TestBoundIsOverEstimate(t *testing.T) {
	s := boundSet(t)
	team := []TeamMember{{CharaID: 1, Rarity: 5, MemoriaNumbers: []int{7}}}
	per := s.MaxSingleTurnDamage(team)

	// 满攻击、满倍率、无减伤的一击不可能超过单回合上界。
	if per < 1000 {
		t.Fatalf("上界 %d 低于裸攻击力,必然会误判", per)
	}
	// 宽容系数只会让它更宽。
	if !s.DamageWithinBound(team, 1, per, 0) {
		t.Error("恰好等于上界的伤害应当放行")
	}
	if !s.DamageWithinBound(team, 1, per*3/2, 0) {
		t.Error("1.5 倍宽容内的伤害应当放行")
	}
}

func TestDamageWithinBound_RejectsAbsurd(t *testing.T) {
	s := boundSet(t)
	team := []TeamMember{{CharaID: 1, Rarity: 5}}
	if s.DamageWithinBound(team, 1, 999_999_999, 0) {
		t.Error("填爆的伤害必须被拒")
	}
	if s.DamageWithinBound(team, 0, 1, 0) {
		t.Error("回合数为 0 应当被拒")
	}
}

// 数据缺失不得表现成作弊指控——那会让一次提取遗漏变成一批玩家被封。
func TestDamageWithinBound_UnknownTeamPasses(t *testing.T) {
	s := boundSet(t)
	if !s.DamageWithinBound([]TeamMember{{CharaID: 9999, Rarity: 5}}, 3, 1_000_000, 0) {
		t.Error("队伍全员未知时应当放行,而不是判为作弊")
	}
	// 未知稀有度档位同理:算 0,不报错。
	if s.MaxSingleTurnDamage([]TeamMember{{CharaID: 1, Rarity: 3}}) != 0 {
		t.Error("未知稀有度档位应当计 0")
	}
}

func TestDamageBound_ScalesWithTurns(t *testing.T) {
	s := boundSet(t)
	team := []TeamMember{{CharaID: 1, Rarity: 5}}
	per := s.MaxSingleTurnDamage(team)
	if !s.DamageWithinBound(team, 10, per*10, 0) {
		t.Error("10 回合的累计伤害应当按 10 倍上界判")
	}
	if s.DamageWithinBound(team, 1, per*10, 0) {
		t.Error("1 回合打出 10 回合的量应当被拒")
	}
}

func TestPeakATK(t *testing.T) {
	s := boundSet(t)
	c := s.Chara(1)
	if c.PeakATK(5) != 1000 {
		t.Errorf("PeakATK(5) = %d", c.PeakATK(5))
	}
	if c.PeakATK(3) != 0 {
		t.Errorf("不存在的档位应当返回 0,得到 %d", c.PeakATK(3))
	}
}
