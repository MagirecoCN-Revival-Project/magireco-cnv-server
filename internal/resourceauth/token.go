// Package resourceauth 实现资产分发用的短时令牌(asset_auth / resource_token)。
//
// **本文件在 magirecocn-resource-server 里有一份完全相同的拷贝。** 令牌在这边签发、
// 在那边的边缘节点校验,两边必须字节级一致;两个仓库不共享 Go module,做不到只留
// 一份实现。token_test.go 里的跨仓库测试向量负责在两边同时钉住这件事——改本文件
// 之前先读那段注释。
//
// # 它和会话令牌不是一回事
//
// 会话令牌(internal/clienttoken)证明"这个设备是谁",生命周期以天计,由 API 服务端
// 签发、资源分发服务端凭公钥采信。本包的令牌只证明"可以读资产",生命周期以分钟计。
//
// **刻意不让边缘节点拿到会话令牌**:边缘节点是信任树里最外一层(小时级证书,可能
// 是第三方镜像),把客户端的身份凭证交给它,等于每取一个文件就把身份泄露一次。
// 拆成两把令牌之后,边缘节点最多知道"某个设备正在取文件"。
//
// # 为什么用 HMAC 而不是 Ed25519
//
// 与会话令牌相反,这里对称是**合适**的:令牌的全部权限就是"读资产",而校验方
// (边缘节点)本来就持有资产。它能自己签一个也不会因此多拿到任何东西,不存在
// 会话令牌那种"校验方能凭空造出身份"的问题。对称方案省一次签名运算、省一套
// 密钥分发,在每个文件请求都要验一次的路径上这是实打实的。
//
// # 线格式
//
//	cnva1.<base64url(device_id)>.<时间桶>.<base64url(HMAC-SHA256)>
//
// MAC 覆盖前三段拼成的字符串(**含 cnva1. 前缀**),与 internal/clienttoken 同一条
// 纪律:版本前缀签进去,将来出 cnva2 时不能把新载荷搬到旧前缀下复用签名。
//
// 令牌**自描述**:校验方从令牌本身取得 device_id,不需要客户端额外送一个头,
// 也不需要边缘节点连数据库。边缘节点只要有密钥就能独立完成校验。
package resourceauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Prefix 是令牌的版本前缀。
const Prefix = "cnva1."

// DefaultWindowSec 是默认时间桶长度(秒)。
const DefaultWindowSec = 300

// MinSecretLen 是密钥长度下限。低于它一律拒绝签发/校验——
// 短密钥的 HMAC 可以离线暴力破解,而症状是"一切正常"。
const MinSecretLen = 16

var (
	// ErrMalformed 令牌结构不对(段数、前缀、编码)。
	ErrMalformed = errors.New("resourceauth: 令牌格式非法")
	// ErrBadMAC 签名对不上。
	ErrBadMAC = errors.New("resourceauth: 令牌签名校验失败")
	// ErrExpired 令牌所在的时间桶已经过去。
	ErrExpired = errors.New("resourceauth: 令牌已过期")
	// ErrNoSecret 未配置密钥或密钥过短。
	ErrNoSecret = fmt.Errorf("resourceauth: 未配置资产令牌密钥,或长度不足 %d 字节", MinSecretLen)
)

// Signer 签发资产令牌。
type Signer struct {
	Secret    []byte
	WindowSec int
}

// Verifier 校验资产令牌。字段与 Signer 相同,分成两个类型是为了让调用点一眼看出
// 自己处在哪一侧——边缘节点只该构造 Verifier,它没有签发的理由。
type Verifier struct {
	Secret    []byte
	WindowSec int
}

func window(sec int) int64 {
	if sec <= 0 {
		return DefaultWindowSec
	}
	return int64(sec)
}

// Sign 为某设备签发一枚资产令牌,返回令牌与过期时刻(**Unix 秒**)。
//
// 密钥缺失或过短时返回空串与 0,由调用方决定怎么表现。协议上「省略 asset_auth」
// 的语义是"客户端拿不到资产",而不是"不需要鉴权"——所以签不出来时省略字段,
// 正是 fail-closed。
func (s Signer) Sign(deviceID string, now time.Time) (string, int64) {
	if len(s.Secret) < MinSecretLen {
		return "", 0
	}
	win := window(s.WindowSec)
	bucket := now.Unix() / win
	payload := payloadOf(deviceID, bucket)
	return payload + "." + b64(mac(s.Secret, payload)), (bucket + 1) * win
}

// Verify 校验令牌,通过时返回令牌里携带的 device_id。
//
// 接受**当前桶与上一个桶**两格。只认当前桶的话,签发方与校验方之间哪怕几秒的
// 时钟差,都会让恰好在桶边界签出的令牌当场失效;而这类失败在日志里看起来就是
// 随机的、无法复现的 401。放宽一格的代价是有效期在 win 到 2×win 之间浮动,
// 对一枚"只能读资产、分钟级"的令牌是划算的。
func (v Verifier) Verify(token string, now time.Time) (string, error) {
	if len(v.Secret) < MinSecretLen {
		return "", ErrNoSecret
	}
	if !strings.HasPrefix(token, Prefix) {
		return "", ErrMalformed
	}
	// 前缀里含一个 '.',所以整串共 4 段:cnva1 / 设备 / 桶 / MAC。
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return "", ErrMalformed
	}
	rawDev, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrMalformed
	}
	bucket, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", ErrMalformed
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return "", ErrMalformed
	}

	// 先验签再看时间:时间是令牌里的明文字段,没验签之前它只是攻击者写的数字。
	payload := parts[0] + "." + parts[1] + "." + parts[2]
	if !hmac.Equal(got, mac(v.Secret, payload)) {
		return "", ErrBadMAC
	}

	cur := now.Unix() / window(v.WindowSec)
	if bucket != cur && bucket != cur-1 {
		return "", ErrExpired
	}
	return string(rawDev), nil
}

func payloadOf(deviceID string, bucket int64) string {
	return Prefix + b64([]byte(deviceID)) + "." + strconv.FormatInt(bucket, 10)
}

func mac(secret []byte, payload string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
