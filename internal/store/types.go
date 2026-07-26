package store

import "encoding/json"

// Admin 后台管理员。
type Admin struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	Role         string // super_admin | admin | readonly
	CreatedAt    int64
	LastLoginAt  *int64
}

// Account 游戏账号。
type Account struct {
	ID            string
	Username      string
	Email         string
	PasswordHash  string
	Status        string // active | disabled
	EmailVerified bool   // false = 邮箱未验证,限制云存档与客户端登录
	CreatedAt     int64
	LastLoginAt   *int64
	LoginCount    int
}

// AccountSession 玩家会话(token = account_token)。
type AccountSession struct {
	Token       string
	AccountID   string
	DeviceName  string
	OS          string
	IP          string
	Region      string
	CreatedAt   int64
	LastSeenAt  int64
	ExpiresAt   int64
}

// AdminSession 管理员会话。
type AdminSession struct {
	Token     string
	AdminID   string
	CreatedAt int64
	ExpiresAt int64
}

// ClientSession 握手期 access_token,客户端 authTriple 用。
// 不绑定 account_id —— 玩家登录是 /account/login 独立流程。
type ClientSession struct {
	AccessToken   string
	DeviceID      string
	Signature     string
	ClientVersion string
	Channel       string
	CreatedAt     int64
	ExpiresAt     int64
	LastSeenAt    int64
}

// Ban 封禁记录,active=true 表示活跃。
type Ban struct {
	ID         string
	DeviceID   string
	Reason     string
	IssuedAt   int64
	ExpireTime *int64 // nil = 永久
	IssuedBy   string
	Auto       bool
	LiftedAt   *int64
	LiftedBy   *string
	Active     bool
}

// Device 设备指纹记录。
type Device struct {
	DeviceID      string
	Signature     *string
	FirstSeen     int64
	LastSeen      int64
	ClientVersion *string
}

// Mirror 单个镜像。Files 可能是 nil(让客户端走 S3 XML 自发现),
// 也可能是内联的文件清单([{key,size}])。
type Mirror struct {
	ID        int64
	GroupID   int64
	Kind      string  // "http"(默认)| "s3"
	URL       string
	Bucket    *string // 仅 kind="s3" 用
	Region    *string // 仅 kind="s3" 用
	Files     json.RawMessage // 可为 nil;非 nil 时是 [{key, size?}] 或 [key] 数组
	SortOrder int
}

// MirrorGroup 镜像线路组。
type MirrorGroup struct {
	ID        int64
	Name      string
	SortOrder int
}

// MirrorWithGroup 镜像 + 所在组名,给管理后台展示与回写。
type MirrorWithGroup struct {
	Mirror
	GroupName string
}

// MirrorGroupWithMirrors 一组镜像 + 其组元数据,给 /client/online-download
// 直接拼成 groups[{name, mirrors}] 返回。
type MirrorGroupWithMirrors struct {
	Group   MirrorGroup
	Mirrors []Mirror
}

// HotBundle 热更新包。
type HotBundle struct {
	Kind         string // js | scenario
	Version      int
	SHA256       string
	DownloadURL  string
	Size         int64
	PublishedAt  *int64
}

// OfflinePackage 离线包单例。
type OfflinePackage struct {
	DownloadURL    string
	PackageVersion string
	SHA256         string
	Size           int64
	UploadedAt     *int64
}

// AuditEntry 单条审计日志。
type AuditEntry struct {
	ID      string
	Ts      int64
	Actor   string
	Type    string
	Target  *string
	Details json.RawMessage
}

// CapChallenge cap-worker PoW 挑战。
type CapChallenge struct {
	Token     string
	C         int
	D         int
	S         string
	ExpiresAt int64
	Solved    bool
}

// MirrorTrafficRow 镜像流量统计行（mirror_traffic 表）。
type MirrorTrafficRow struct {
	URL             string
	TodayBytes      int64
	MonthBytes      int64
	CumBytes        int64
	TodayDate       string // YYYY-MM-DD
	MonthDate       string // YYYY-MM
	DailyLimitBytes int64  // 0 = 不限
	SpeedLimitBps   int64  // 0 = 不限
}

// EmailCode 邮箱验证码。
type EmailCode struct {
	ID        int64
	Email     string
	Code      string
	Purpose   string // register | change_email | reset
	ExpiresAt int64
	Consumed  bool
}
