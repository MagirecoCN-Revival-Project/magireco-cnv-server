package masterdata

import (
	"encoding/json"
	"fmt"
	"os"
)

// File 是 master data 文件的线格式。
//
//	{
//	  "version": "2026-08-01",
//	  "charas":  [ { "id": 1002, "attribute": "WATER", ... } ],
//	  "memoria": [ { "number": 1, "rarity": 3, ... } ]
//	}
//
// 与 scenemanifest.File 同款做法:构建管线的产物,部署期挂载,不入版本库;
// 服务端侧只有加载器。理由见契约登记表 R5a/R5b。
type File struct {
	Version string    `json:"version"`
	Charas  []Chara   `json:"charas"`
	Memoria []Memoria `json:"memoria"`
}

// Set 是加载好的 master data,可安全并发读取。
type Set struct {
	version string
	charas  map[int]*Chara
	memoria map[int]*Memoria
}

// Version 返回数据版本标识。
func (s *Set) Version() string { return s.version }

// Counts 返回各类记录数,供启动日志对账。
func (s *Set) Counts() (charas, memoria int) { return len(s.charas), len(s.memoria) }

// Chara 按 charaId 取角色;未知返回 nil。
func (s *Set) Chara(id int) *Chara { return s.charas[id] }

// Memoria 按图鉴编号取记忆结晶;未知返回 nil。
func (s *Set) Memoria(number int) *Memoria { return s.memoria[number] }

// Load 读取并**全量校验**一份 master data。
//
// 校验在启动时一次做完,而不是等战斗开打才发现。这类错误的症状离原因极远:
// 一个读反了的数值区间表现为"某个角色越练越弱",玩家会当成平衡性问题反馈;
// 一个拼错的 code 表现为"某个技能什么都不做",而日志一切正常。
// 启动时拒绝加载,运维立刻知道是数据的问题。
func Load(path string) (*Set, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 master data 失败: %w", err)
	}
	var f File
	// 不用 DisallowUnknownFields:数据由构建管线产出,将来加字段不该让旧服务端
	// 拒绝启动。
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("master data 不是合法 JSON: %w", err)
	}
	if len(f.Charas) == 0 && len(f.Memoria) == 0 {
		// 空数据集几乎总是提取管线出了错。放行的话服务端会带着一份空 master data
		// 正常启动,直到第一场战斗才发现什么都生成不出来。
		return nil, fmt.Errorf("master data 里 charas 与 memoria 都为空")
	}

	set := &Set{
		version: f.Version,
		charas:  make(map[int]*Chara, len(f.Charas)),
		memoria: make(map[int]*Memoria, len(f.Memoria)),
	}

	for i := range f.Charas {
		c := &f.Charas[i]
		if c.ID <= 0 {
			return nil, fmt.Errorf("第 %d 个角色缺少合法 id", i)
		}
		if _, dup := set.charas[c.ID]; dup {
			// 重复 ID 会让"取到哪一份"取决于遍历顺序——一个非确定性的 bug。
			return nil, fmt.Errorf("角色 id 重复: %d", c.ID)
		}
		if _, ok := Attributes[string(c.Attribute)]; !ok {
			return nil, fmt.Errorf("角色 %d(%s): 未知属性 %q,合法取值: %v",
				c.ID, c.NameZh, c.Attribute, SortedKeys(Attributes))
		}
		if len(c.ByRarity) == 0 {
			return nil, fmt.Errorf("角色 %d(%s): 没有任何稀有度档位的数值", c.ID, c.NameZh)
		}
		for rarity, st := range c.ByRarity {
			if rarity < 1 || rarity > 5 {
				return nil, fmt.Errorf("角色 %d(%s): 稀有度 %d 越界", c.ID, c.NameZh, rarity)
			}
			if err := st.Validate(); err != nil {
				return nil, fmt.Errorf("角色 %d(%s) 稀有度 %d: %w", c.ID, c.NameZh, rarity, err)
			}
		}
		for rarity, arts := range c.Magia {
			if _, ok := c.ByRarity[rarity]; !ok {
				// magia 挂在一个没有数值的稀有度上,说明提取时档位对错了。
				return nil, fmt.Errorf("角色 %d(%s): magia 有稀有度 %d,但该档没有数值",
					c.ID, c.NameZh, rarity)
			}
			if err := validateArts(arts); err != nil {
				return nil, fmt.Errorf("角色 %d(%s) 稀有度 %d 的 magia: %w", c.ID, c.NameZh, rarity, err)
			}
		}
		if err := validateArts(c.Doppel); err != nil {
			return nil, fmt.Errorf("角色 %d(%s) 的 doppel: %w", c.ID, c.NameZh, err)
		}
		set.charas[c.ID] = c
	}

	for i := range f.Memoria {
		m := &f.Memoria[i]
		if m.Number <= 0 {
			return nil, fmt.Errorf("第 %d 枚记忆结晶缺少合法 number", i)
		}
		if _, dup := set.memoria[m.Number]; dup {
			return nil, fmt.Errorf("记忆结晶 number 重复: %d", m.Number)
		}
		if err := m.Stats.Validate(); err != nil {
			return nil, fmt.Errorf("记忆结晶 %d(%s): %w", m.Number, m.NameZh, err)
		}
		// 满级冷却不该长于初始冷却(强化只会缩短它)。读反了的话表现是"练满反而
		// 更慢",同样是玩家当平衡性问题反馈的那类缺陷。
		if m.CooldownMax > m.Cooldown {
			return nil, fmt.Errorf("记忆结晶 %d(%s): 满级冷却 %d 长于初始冷却 %d",
				m.Number, m.NameZh, m.CooldownMax, m.Cooldown)
		}
		if err := validateArts(m.Effects); err != nil {
			return nil, fmt.Errorf("记忆结晶 %d(%s): %w", m.Number, m.NameZh, err)
		}
		set.memoria[m.Number] = m
	}

	return set, nil
}

func validateArts(arts []Art) error {
	for i, a := range arts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("第 %d 条效果: %w", i, err)
		}
	}
	return nil
}
