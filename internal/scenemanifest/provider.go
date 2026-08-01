package scenemanifest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// File 是场景清单文件的线格式(契约登记表 R5a)。
//
//	{
//	  "version": "2026-08-01",
//	  "scenes": {
//	    "story/11011": [
//	      { "path": "resource/chara/1001/base.moc3", "sha256": "…", "size": 412849 }
//	    ]
//	  }
//	}
//
// # 为什么是「部署期加载一个文件」而不是查库
//
// 清单是**构建管线的产物**,不是运行时可变状态:同一份资产集合对所有玩家一样,
// 也不会因为谁做了什么而改变。放数据库意味着每次场景加载都查一次库、还要额外维护
// 一套写入路径与一致性问题,换不来任何东西。
//
// 也不入版本库(铁律三):清单描述的是游戏资产,随部署挂载或同步,仓库里只有加载器。
//
// # 为什么整份加载进内存
//
// 清单是纯元数据。以已知规模估算(3 万个文件级条目、每条约 120 字节),整份不过
// 几 MB——比一个连接池还小。换来的是场景加载路径上**零 IO、零锁**。
// 真到了内存装不下的那天,再谈分片也不迟,而那一天很可能永远不来。
type File struct {
	// Version 这份清单的版本标识,原样写进日志便于对账。
	// 建议用构建产出的日期或内容哈希,不做格式约束。
	Version string `json:"version"`
	// Scenes 场景到资产列表的映射。
	Scenes map[string][]Asset `json:"scenes"`
}

// sha256Re 校验内容哈希的形状:64 位小写十六进制。
var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Provider 是加载好的清单,可安全并发读取。
type Provider struct {
	version string
	scenes  map[string][]Asset
}

// Version 返回清单版本标识。
func (p *Provider) Version() string { return p.version }

// Len 返回场景数。
func (p *Provider) Len() int { return len(p.scenes) }

// Lookup 实现 Handler.SceneAssets 所需的签名。
// 未知场景返回 nil(客户端得到 404),而不是空切片。
func (p *Provider) Lookup(_ context.Context, sceneID string) ([]Asset, error) {
	assets, ok := p.scenes[sceneID]
	if !ok {
		return nil, nil
	}
	return assets, nil
}

// LoadFile 读取并**全量校验**一份场景清单。
//
// 校验在启动时一次做完,而不是等请求进来才发现问题。理由是这类错误的症状离原因
// 极远:一个 sha256 少了两位,表现是某个玩家在某个场景卡住,而日志里一切正常。
// 启动时拒绝加载,运维立刻就知道是清单的问题。
func LoadFile(path string) (*Provider, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取场景清单失败: %w", err)
	}
	var f File
	// 不用 DisallowUnknownFields:清单由构建管线产出,将来加字段不该让旧服务端
	// 拒绝启动。未知字段忽略即可。
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("场景清单不是合法 JSON: %w", err)
	}
	if f.Scenes == nil {
		return nil, fmt.Errorf("场景清单缺少 scenes 字段")
	}

	seen := make(map[string]struct{})
	for sceneID, assets := range f.Scenes {
		if err := ValidateSceneID(sceneID); err != nil {
			return nil, fmt.Errorf("场景 %q: %w", sceneID, err)
		}
		if len(assets) == 0 {
			// 空清单的语义是"该场景不需要任何资产",这几乎总是构建管线出了错
			// 而不是真的不需要。放行的话客户端会静默进入一个残缺场景——
			// 那正是整条链路上最难排查的一类故障。
			return nil, fmt.Errorf("场景 %q 的资产列表为空;若确实无资产,应当从清单里删掉该场景", sceneID)
		}
		clear(seen)
		for i, a := range assets {
			switch {
			case a.Path == "":
				return nil, fmt.Errorf("场景 %q 第 %d 项缺少 path", sceneID, i)
			case !sha256Re.MatchString(a.SHA256):
				// 形状不对就拒绝。哈希是**完整性校验**的依据,一个大小写混用或
				// 截断的值会让客户端每次都校验失败,而服务端毫不知情。
				return nil, fmt.Errorf("场景 %q 的 %s: sha256 须为 64 位小写十六进制,得到 %q",
					sceneID, a.Path, a.SHA256)
			case a.Size < 0:
				return nil, fmt.Errorf("场景 %q 的 %s: size 不得为负", sceneID, a.Path)
			}
			if _, dup := seen[a.Path]; dup {
				// 同一场景里重复的 path 会让客户端重复下载同一个文件,也会让
				// manifest_hash 依赖于重复项的个数——两者都不是想要的。
				return nil, fmt.Errorf("场景 %q 里 path 重复: %s", sceneID, a.Path)
			}
			seen[a.Path] = struct{}{}
		}
	}

	// 预排序:响应要求按 path 升序,提前做掉,每次请求就不必再排。
	scenes := make(map[string][]Asset, len(f.Scenes))
	for id, assets := range f.Scenes {
		scenes[id] = Sorted(assets)
	}
	return &Provider{version: f.Version, scenes: scenes}, nil
}
