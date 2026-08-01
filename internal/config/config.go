// Package config 从环境变量加载运行时配置。
// 所有敏感值禁止硬编码,统一走 CNV_* 前缀的环境变量。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"magirecocn-revival/api-server/internal/resourceauth"
)

// Config 是节点/面板共用的根配置;字段是否必填取决于运行模式。
type Config struct {
	// 通用
	Addr              string // 监听地址,默认 :8080
	WebDir            string // 前端面板静态目录,默认 ./web
	PrimaryResDir     string // 本地资源目录(可选)
	OfflineDir        string // 离线整包打包产物目录(默认 ./data/offline)
	OfflineURLPath    string // 离线整包对外路径前缀(默认 /dl/offline-pack)
	HotUpdateDir      string // 热更新包托管目录(默认 ./data/hotupdate)
	HotUpdateURLPath  string // 热更新包对外路径前缀(默认 /dl/hot-update)
	HotUpdateMaxBytes int64  // 热更新包上传/下载大小上限初值(CNV_HOTUPDATE_MAX_MB,默认 1024MiB);运行时可在后台改
	BodyLimitMaxBytes int64  // 全局请求体上限初值(CNV_BODY_LIMIT_MB,默认 8MiB);运行时可在后台改
	PrimaryResPath    string // 资源 URL 前缀,默认 /res
	TrustProxy        string // express 风格:loopback / true / N / IP 列表
	TLSCert           string
	TLSKey            string
	SignatureAllowed  []string // 客户端 signature 白名单(逗号分隔的 SHA-256 hex)
	ChannelAllowed    []string // 渠道白名单(空表示放行所有)
	RequireSignature  bool     // 强制要求请求带非空 signature(即便白名单为空)

	// DevMode 开发模式(CNV_DEV_MODE,默认 false = 生产)。
	//
	// 协议里有若干**开发期临时值**——待决项定稿前的占位形状,让两侧能先并行开工。
	// 协议文档 06-dev-mode 的「生产守卫」要求:**生产环境不得下发任何临时值**。
	// 本开关就是那道守卫在服务端侧的落点。
	//
	// 默认 false 是有意的:临时值的危险不在于它们存在,而在于**它们可能不被发现地
	// 留在生产里**。忘了配这个变量的后果是功能不可用(显眼),而不是临时值悄悄
	// 泄进生产(不显眼)。
	DevMode bool

	// SceneManifestFile 场景资产清单文件路径(CNV_SCENE_MANIFEST_FILE)。
	// 空 = 场景清单功能未启用,/client/scene-manifest 明确返回 503。
	//
	// 清单是**构建管线的产物**,随部署挂载或同步,不入版本库(铁律三)。
	// 内容格式见 internal/scenemanifest.File。
	SceneManifestFile string

	// BootstrapEndpoint 下发给 Android 底包的业务服务器地址
	// (CNV_BOOTSTRAP_ENDPOINT)。空 = 本节点不接管 Android 底包,
	// /magica/api/snaa 返回 503。源码不得硬编码(铁律二)。
	BootstrapEndpoint string
	// BootstrapMaxThreads 下发给底包的并发下载线程数建议值
	// (CNV_BOOTSTRAP_MAX_THREADS,默认 4)。
	BootstrapMaxThreads int
	// BootstrapVersion 当前底包版本号(CNV_BOOTSTRAP_VERSION,r128 → 128)。
	BootstrapVersion int

	// 业务节点专用
	DBURL string // postgres://...
	// TotentanzDiscoveryURL 上游 Totentanz 的端点发现接口完整 URL
	// (CNV_TOTENTANZ_DISCOVERY_URL)。空 = 不启用,services 完全沿用 KV 配置。
	TotentanzDiscoveryURL string
	// TotentanzClientVersion 向发现接口上报的版本号(CNV_TOTENTANZ_CLIENT_VERSION),
	// 对应底包的 rNNN。缺省 0 交由上游按默认处理。
	TotentanzClientVersion int
	// TotentanzRefreshSec 发现结果的后台刷新间隔秒数
	// (CNV_TOTENTANZ_REFRESH_SEC),缺省 300。
	TotentanzRefreshSec int

	ResourceTokenSecret []byte // CNV_RESOURCE_TOKEN_SECRET, resource_token 的 HMAC 签名根密钥;不设则首次启动自动生成并持久化
	// ResourceTokenWindowSec 资产令牌的时间桶长度,秒
	// (CNV_RESOURCE_TOKEN_WINDOW_SEC,默认 300)。
	//
	// **本节点与资源分发服务端(签发方与校验方)必须配成同一个值。** 桶号是
	// unix秒/窗口 算出来的,两边窗口不一致就会算出不同的桶,结果是每个资产请求
	// 都 401——而错误信息只会说"令牌签名校验失败",完全指不到这里。
	ResourceTokenWindowSec int
	AdminJWTSecret         []byte        // 管理后台 JWT / cookie 签名(cookie 完整性校验)
	CapWorkerURL           string        // cap-worker 部署 URL(可空,内置自实现)
	ClientSessionTTL       time.Duration // 客户端会话有效期
	AdminSessionTTL        time.Duration // 管理员会话有效期
	AccountSessionTTL      time.Duration // 玩家会话有效期

	// 客户端会话令牌(internal/clienttoken)。令牌自包含并由 Ed25519 签名,
	// 校验方只需公钥,不必与签发方共库——资源分发服务端因此能在不连本服务端、
	// 不共享数据库的前提下认得这里签出的身份。
	//
	// ClientTokenIssuer 本节点签发令牌时写进 iss 的标识(CNV_CLIENT_TOKEN_ISSUER),
	// 空则回退到 NodeID。校验方按 iss 挑公钥,所以同一部署内必须稳定唯一。
	ClientTokenIssuer string
	// ClientTokenSeed 签名私钥种子,32 字节十六进制(CNV_CLIENT_TOKEN_SEED)。
	// 留空则首次启动自动生成并持久化到 config 表,与 resource_token_secret 同款做法。
	// 这把钥匙**必须在线**,与离线的目录私钥是两把不同的钥匙,不可复用。
	ClientTokenSeed string
	// ClientTokenTrusted 额外信任的签发方公钥,格式 "标识:公钥hex,标识:公钥hex"
	// (CNV_CLIENT_TOKEN_TRUSTED_KEYS)。本服务端是身份源头,通常留空;
	// 配了它就等于承认另有一方也能造出会话身份,要清楚自己在做什么。
	ClientTokenTrusted map[string]string

	// 节点 PKI(internal/pki)。身份来自离线根签发的证书链;进程启动时自检,
	// 不通过就拒绝启动——PKI 配错的症状几乎总是延迟且指错方向的。
	//
	// PKIAnchors 钉住的根证书文件,逗号分隔(CNV_PKI_ANCHORS)。
	// **可以配多把**:根轮换的重叠期要同时信任现用与下一把,只配一把的话
	// 轮换那天所有旧链一起失效。
	PKIAnchors []string
	// PKICertFile 本节点证书(CNV_PKI_CERT)。本服务端在信任树里是 role=api 的
	// 子 CA,与资源分发服务端**平级**——击穿其中一个签不出另一个的身份。
	PKICertFile string
	// PKIChainFiles 中间证书,自底向上,不含根(CNV_PKI_CHAIN)。
	// 由根直签的子 CA 留空。
	PKIChainFiles []string
	// PKIKeyFile 本节点身份私钥种子(CNV_PKI_KEY,默认 ./data/pki.key)。
	// 由 `node emit-csr` 生成,**绝不外传**。
	PKIKeyFile string

	// 节点新架构
	NodeRole      string // CNV_NODE_ROLE: business(default) | edge
	NodeID        string // CNV_NODE_ID, 默认 hostname
	NodeKeyFile   string // CNV_NODE_KEY_FILE, 节点密钥文件(默认 ./data/node.key)
	ControlAddr   string // CNV_CONTROL_ADDR, 管控 WS 监听地址(默认 127.0.0.1:9090)
	DirectoryFile string // CNV_DIRECTORY_FILE, 已签名目录 JSON 文件(空=不下发)
	PublicURL     string // CNV_PUBLIC_URL, 节点对外资源 URL 前缀

	// 面板托管前端后的协作配置
	PanelPublicURL string // CNV_PANEL_PUBLIC_URL, 面板对外 URL;业务节点据此 302 登录/注册等人类页面,并作为 CORS 放行来源

	// 面板新架构
	PanelDBFile string // CNV_PANEL_DB_FILE, 面板本地 SQLite 路径(默认 ./data/panel.db)
	PanelKey    string // CNV_PANEL_KEY, 面板管理员 JWT/cookie 签名密钥(≥16 字节)

	// SMTP 邮件(发送邮箱验证码、密码重置等)
	SMTPHost     string // CNV_SMTP_HOST,空 = 禁用
	SMTPPort     string // CNV_SMTP_PORT,默认 "587"
	SMTPUser     string
	SMTPPass     string
	SMTPFrom     string // 发件人地址
	SMTPFromName string // 发件人显示名,默认 "魔法纪录复兴计划"

	// 其它
	SkipMigrate bool // CNV_SKIP_MIGRATE=1 时不在启动时执行内嵌迁移
}

// LoadFromEnv 读取所有 CNV_* 与若干通用变量;失败时返回非 nil error。
// 调用方根据 NodeRole 自行判断需要哪些字段。
func LoadFromEnv() (*Config, error) {
	c := &Config{
		Addr:                   envOr("CNV_ADDR", ":8080"),
		WebDir:                 envOr("CNV_WEB_DIR", "./web"),
		PrimaryResDir:          os.Getenv("CNV_PRIMARY_RES_DIR"),
		OfflineDir:             envOr("CNV_OFFLINE_DIR", "./data/offline"),
		OfflineURLPath:         envOr("CNV_OFFLINE_URL_PATH", "/dl/offline-pack"),
		HotUpdateDir:           envOr("CNV_HOTUPDATE_DIR", "./data/hotupdate"),
		HotUpdateURLPath:       envOr("CNV_HOTUPDATE_URL_PATH", "/dl/hot-update"),
		HotUpdateMaxBytes:      int64(intOr("CNV_HOTUPDATE_MAX_MB", 1024)) << 20,
		BodyLimitMaxBytes:      int64(intOr("CNV_BODY_LIMIT_MB", 8)) << 20,
		PrimaryResPath:         envOr("CNV_PRIMARY_RES_PATH", "/res"),
		TrustProxy:             os.Getenv("CNV_TRUST_PROXY"),
		TotentanzDiscoveryURL:  strings.TrimSpace(os.Getenv("CNV_TOTENTANZ_DISCOVERY_URL")),
		TotentanzClientVersion: atoiOr(os.Getenv("CNV_TOTENTANZ_CLIENT_VERSION"), 0),
		TotentanzRefreshSec:    atoiOr(os.Getenv("CNV_TOTENTANZ_REFRESH_SEC"), 300),
		TLSCert:                os.Getenv("CNV_TLS_CERT"),
		TLSKey:                 os.Getenv("CNV_TLS_KEY"),
		SignatureAllowed:       splitCSV(os.Getenv("CNV_SIGNATURE_WHITELIST")),
		ChannelAllowed:         splitCSV(os.Getenv("CNV_CHANNEL_WHITELIST")),
		RequireSignature:       boolOr("CNV_REQUIRE_SIGNATURE", false),
		DevMode:                boolOr("CNV_DEV_MODE", false),
		SceneManifestFile:      strings.TrimSpace(os.Getenv("CNV_SCENE_MANIFEST_FILE")),
		BootstrapEndpoint:      strings.TrimSpace(os.Getenv("CNV_BOOTSTRAP_ENDPOINT")),
		BootstrapMaxThreads:    intOr("CNV_BOOTSTRAP_MAX_THREADS", 4),
		BootstrapVersion:       intOr("CNV_BOOTSTRAP_VERSION", 0),
		DBURL:                  os.Getenv("CNV_DB_URL"),
		CapWorkerURL:           os.Getenv("CNV_CAP_WORKER_URL"),
		ClientSessionTTL:       durOr("CNV_CLIENT_SESSION_TTL", 7*24*time.Hour),
		AdminSessionTTL:        durOr("CNV_ADMIN_SESSION_TTL", 7*24*time.Hour),
		AccountSessionTTL:      durOr("CNV_ACCOUNT_SESSION_TTL", 30*24*time.Hour),
		ResourceTokenWindowSec: intOr("CNV_RESOURCE_TOKEN_WINDOW_SEC", resourceauth.DefaultWindowSec),
		ClientTokenIssuer:      os.Getenv("CNV_CLIENT_TOKEN_ISSUER"),
		ClientTokenSeed:        os.Getenv("CNV_CLIENT_TOKEN_SEED"),
		ClientTokenTrusted:     splitKeyPairs(os.Getenv("CNV_CLIENT_TOKEN_TRUSTED_KEYS")),
		PKIAnchors:             splitPaths(os.Getenv("CNV_PKI_ANCHORS")),
		PKICertFile:            os.Getenv("CNV_PKI_CERT"),
		PKIChainFiles:          splitPaths(os.Getenv("CNV_PKI_CHAIN")),
		PKIKeyFile:             envOr("CNV_PKI_KEY", "./data/pki.key"),
		NodeRole:               envOr("CNV_NODE_ROLE", "business"),
		NodeID:                 envOr("CNV_NODE_ID", hostname()),
		NodeKeyFile:            envOr("CNV_NODE_KEY_FILE", "./data/node.key"),
		ControlAddr:            envOr("CNV_CONTROL_ADDR", "127.0.0.1:9090"),
		DirectoryFile:          os.Getenv("CNV_DIRECTORY_FILE"),
		PublicURL:              os.Getenv("CNV_PUBLIC_URL"),
		PanelPublicURL:         strings.TrimRight(os.Getenv("CNV_PANEL_PUBLIC_URL"), "/"),
		PanelDBFile:            envOr("CNV_PANEL_DB_FILE", "./data/panel.db"),
		PanelKey:               os.Getenv("CNV_PANEL_KEY"),
		SkipMigrate:            boolOr("CNV_SKIP_MIGRATE", false),
		SMTPHost:               os.Getenv("CNV_SMTP_HOST"),
		SMTPPort:               envOr("CNV_SMTP_PORT", "587"),
		SMTPUser:               os.Getenv("CNV_SMTP_USER"),
		SMTPPass:               os.Getenv("CNV_SMTP_PASS"),
		SMTPFrom:               os.Getenv("CNV_SMTP_FROM"),
		SMTPFromName:           envOr("CNV_SMTP_FROM_NAME", "魔法纪录复兴计划"),
	}
	if v := os.Getenv("CNV_RESOURCE_TOKEN_SECRET"); v != "" {
		c.ResourceTokenSecret = []byte(v)
	}
	if v := os.Getenv("CNV_ADMIN_JWT_SECRET"); v != "" {
		c.AdminJWTSecret = []byte(v)
	}
	return c, nil
}

// MustValidateNode 业务/边缘节点启动前校验必需字段。
// 业务节点须提供 CNV_DB_URL 与 CNV_ADMIN_JWT_SECRET;边缘节点只须资源目录。
func (c *Config) MustValidateNode() error {
	if c.NodeRole == "edge" {
		return nil
	}
	// business(默认)
	var miss []string
	if c.DBURL == "" {
		miss = append(miss, "CNV_DB_URL")
	}
	if len(c.AdminJWTSecret) < 16 {
		miss = append(miss, "CNV_ADMIN_JWT_SECRET (≥16 字符)")
	}
	if len(miss) > 0 {
		return fmt.Errorf("业务节点缺少必需环境变量: %s", strings.Join(miss, ", "))
	}
	return nil
}

// MustValidatePanel 面板启动前校验必需字段。
func (c *Config) MustValidatePanel() error {
	if len(c.PanelKey) < 16 {
		return errors.New("面板缺少 CNV_PANEL_KEY (≥16 字符)")
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func boolOr(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func durOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

func splitPaths(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitKeyPairs 解析 "标识:值,标识:值" 形式的环境变量。
//
// 注意这里**不能**复用 splitCSV:那个函数会把每一项 strings.ToLower。签发方标识
// 是大小写敏感的,小写化之后按 iss 挑公钥会挑不到,而症状是"签名校验失败"这种
// 完全指错方向的错。
func splitKeyPairs(s string) map[string]string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := map[string]string{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// 只按第一个冒号切:值是十六进制不含冒号,但标识里出现冒号时
		// 按最后一个切会把标识截断,这里取更不容易出错的一侧。
		i := strings.Index(item, ":")
		if i <= 0 || i == len(item)-1 {
			continue
		}
		out[strings.TrimSpace(item[:i])] = strings.TrimSpace(item[i+1:])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// atoiOr 解析十进制整数，失败时返回 def。
// 用于可选的数值型环境变量：留空或写错都退回默认值，而不是让节点起不来。
func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
