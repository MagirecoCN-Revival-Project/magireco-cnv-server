// Package scenemanifest 实现场景资产清单的领域逻辑(契约登记表 R2)。
//
// # 场景包是清单与调度单位,文件是传输与缓存单位
//
// 客户端拿到清单后与本地缓存做差集,只拉缺失的文件;边缘节点下发的始终是单个文件,
// 因此保持为纯对象存储 + 鉴权,不理解游戏结构。
//
// 场景包**不做传输单位**:共享资产的复用率极高(主要角色出现在绝大多数场景),
// 包若自包含,同一份角色资产会被复制进几百个包——存储翻数倍、玩家重复下载,
// 且缓存淘汰会为了 3 个需要的文件保留 47 个不需要的。
//
// # 为什么清单里带 sha256
//
// 边缘节点持小时级证书、可能是第三方镜像,是信任树的**最外层**。客户端从它取来的
// 字节必须能对着一个由业务节点这条路径下发的哈希核对——这里抗碰撞有真实的安全意义,
// 所以是 sha256 而不是更便宜的 CRC32/MD5。
//
// 另一半原因是缓存:边缘节点刻意不发 ETag(算全树摘要的代价与收益不成比例),
// 缓存失效的判据因此必须由清单这一侧给出。
//
// # 清单里不放 URL
//
// 只给 path。把完整 URL 钉进一份可能被缓存很久的清单,等于把线路选择固化掉,
// 多节点、故障转移、就近接入全部作废——线路该由签名节点目录在取用那一刻决定。
package scenemanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Asset 是清单里的一项。
type Asset struct {
	// Path 边缘节点上的 key,相对资产根。客户端把它拼到按 caps:["resource"]
	// 选出的节点 base 上。
	Path string `json:"path"`
	// SHA256 内容哈希,64 位小写十六进制。缓存失效判据兼完整性校验。
	SHA256 string `json:"sha256"`
	// Size 字节数。调度用(并发与进度),也是下载前一道廉价 sanity check——
	// 长度对不上就不必算哈希了。
	Size int64 `json:"size"`
}

var (
	// ErrMalformedSceneID scene_id 不符合命名空间格式。
	ErrMalformedSceneID = errors.New("scenemanifest: scene_id 格式非法")
)

// 命名空间格式:<namespace>/<id>
//
// **在它还不危险的时候就把闸设上。** 现在 scene_id 只用于查表、不拼进文件路径,
// 所以畸形值不危险。但一旦将来有人改成按约定拼路径(很自然的优化),没有格式校验的
// scene_id 就是一个现成的路径穿越入口——那时候补要贵得多,而且很可能没人想起来补。
var (
	nsRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	idRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// ReservedNamespaces 是已登记的一级命名空间。
//
// 刻意**不**用它做白名单校验:新增一类资产时不该被迫改服务端代码。这里只是登记,
// 让读代码的人知道现有约定;真正的强制项是上面那两条正则。
var ReservedNamespaces = []string{"story", "battle", "chara", "ui", "movie", "audio"}

// ValidateSceneID 校验 scene_id 的命名空间格式。
func ValidateSceneID(id string) error {
	ns, rest, ok := strings.Cut(id, "/")
	if !ok || !nsRe.MatchString(ns) || !idRe.MatchString(rest) {
		return ErrMalformedSceneID
	}
	// 纯点号的 id 单独拦一道。
	//
	// idRe 允许 '.'(文件名式的 id 需要它,如 op-2026.mp4),而 "." 与 ".." 恰好
	// 全由允许字符组成——正则挡不住它们。这两个正是路径穿越的核心记号,漏过去的话
	// 上面那条"在它还不危险的时候把闸设上"就白写了。
	if strings.Trim(rest, ".") == "" {
		return ErrMalformedSceneID
	}
	return nil
}

// Hash 计算整张清单的哈希,供客户端跳过重复传输。
//
// 先按 path 排序再算:提供方(构建管线 / 数据库查询)的返回顺序不该影响结果,
// 否则同一份清单会因为一次 ORDER BY 的改动而"变了",让所有客户端白白重下一遍。
//
// 序列化用 \x00 分隔而不是拼字符串:path 里理论上可以出现任何字符,用普通分隔符的话
// "a\tb" + "c" 与 "a" + "b\tc" 会算出同一个哈希。\x00 在路径里不可能出现。
func Hash(assets []Asset) string {
	sorted := make([]Asset, len(assets))
	copy(sorted, assets)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	h := sha256.New()
	for _, a := range sorted {
		h.Write([]byte(a.Path))
		h.Write([]byte{0})
		h.Write([]byte(a.SHA256))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(a.Size, 10)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Sorted 返回按 path 排序的副本,供响应使用。
//
// 清单顺序稳定不只是为了好看:客户端可能按顺序调度下载,顺序抖动会让重试与断点
// 续传的行为变得不可复现。
func Sorted(assets []Asset) []Asset {
	out := make([]Asset, len(assets))
	copy(out, assets)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
