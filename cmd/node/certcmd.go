package main

// 管控通道上的证书指令。
//
// 方向说明:管控通道是**上级拨号连节点**、指令 上级→节点。所以换证由上级驱动:
// 上级先 cert_csr 问节点要一份 CSR,签完再 cert_install 送回来。
//
// 这个方向比"节点主动去要"更合适:上级本来就知道自己什么时候能签(离线根有没有
// 拿出来、是否处于维护窗口),而节点只知道自己快到期了。让知情的一方驱动,
// 少一层"节点反复重试却永远等不到"的失败模式。
//
// # 本服务端没有 cert_sign
//
// 信任树里它是 role=api 的子 CA,与资源分发服务端**平级**,allowedChildRoles[api]
// 是空集——签不出任何下级。所以这里只实现"给自己换证"和"接受吊销",不实现签发。
// 边缘节点的自动续签由资源分发服务端那侧的子 CA 承担。
//
// 另外它自己那张 90 天的子 CA 证书是**离线根手工签**的,不参与面板的自动续期
// 巡检(那条路径只自动续 role=edge)。cert_csr / cert_install 在这里的用途是让
// 手工换证也走同一条可审计的通道,而不必登机器拷文件。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"magirecocn-revival/api-server/internal/control"
	"magirecocn-revival/api-server/internal/pki"
)

// certCommands 把证书相关指令注册进管控指令表。
//
// renewer 为 nil(未配置 PKI)时,这些指令一律返回明确的错误而不是静默成功——
// 静默成功会让上级以为吊销已经生效,而实际上这台节点根本没在验证任何证书。
func certCommands(
	renewer *pki.Renewer,
	revocations *pki.Revocations,
	log *slog.Logger,
) map[string]control.CommandFunc {
	notConfigured := errors.New("本节点未配置 PKI(CNV_PKI_CERT 等),证书指令不可用")

	return map[string]control.CommandFunc{
		control.ActionCertCSR: func(_ context.Context, _ json.RawMessage) (any, error) {
			if renewer == nil {
				return nil, notConfigured
			}
			csr, err := renewer.CSR()
			if err != nil {
				return nil, err
			}
			leaf := renewer.Identity().Leaf()
			return map[string]any{
				"csr":        csr,
				"subject":    leaf.Sub,
				"role":       leaf.Role,
				"expires_at": leaf.Exp,
				// 上级据此决定是否值得现在就签:没到续期时点的请求可以先放着。
				"needs_renewal": leaf.NeedsRenewal(time.Now()),
			}, nil
		},

		control.ActionCertInstall: func(_ context.Context, payload json.RawMessage) (any, error) {
			if renewer == nil {
				return nil, notConfigured
			}
			var req struct {
				Chain []string `json:"chain"`
			}
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, fmt.Errorf("解析入参: %w", err)
			}
			// Install 内部会完整校验后才落盘;失败时磁盘与内存都不动。
			if err := renewer.Install(req.Chain); err != nil {
				log.Warn("拒绝安装上级送来的证书", "err", err)
				return nil, err
			}
			leaf := renewer.Identity().Leaf()
			return map[string]any{
				"subject":    leaf.Sub,
				"expires_at": leaf.Exp,
				"renew_at":   leaf.RenewAt().UnixMilli(),
			}, nil
		},

		control.ActionCertRevoke: func(_ context.Context, payload json.RawMessage) (any, error) {
			if revocations == nil {
				return nil, notConfigured
			}
			var rev pki.Revocation
			if err := json.Unmarshal(payload, &rev); err != nil {
				return nil, fmt.Errorf("解析入参: %w", err)
			}
			now := time.Now()
			if err := revocations.Add(rev, now); err != nil {
				return nil, err
			}
			// 吊销是紧急操作,必须在日志里留下完整痕迹:谁、为什么、什么时候。
			// 出事之后复盘时,这条日志往往是唯一能对上时间线的东西。
			log.Warn("已接受证书吊销",
				"serial", rev.Serial, "subject", rev.Subject,
				"reason", rev.Reason,
				"原过期时刻", time.UnixMilli(rev.ExpiresAt).Format(time.RFC3339))
			return map[string]any{
				"accepted": true,
				"active":   len(revocations.List(now)),
			}, nil
		},

		control.ActionCertStatus: func(_ context.Context, _ json.RawMessage) (any, error) {
			if renewer == nil || revocations == nil {
				return nil, notConfigured
			}
			now := time.Now()
			leaf := renewer.Identity().Leaf()
			return map[string]any{
				"subject":       leaf.Sub,
				"role":          leaf.Role,
				"caps":          leaf.Caps,
				"serial":        leaf.Serial,
				"not_before":    leaf.NBF,
				"expires_at":    leaf.Exp,
				"renew_at":      leaf.RenewAt().UnixMilli(),
				"needs_renewal": leaf.NeedsRenewal(now),
				"revocations":   revocations.List(now),
			}, nil
		},
	}
}
