package control

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateKey 从 path 读取连接密钥；不存在或过短则生成 32 字节随机密钥
// （64 位 hex）持久化到 path（0600），返回密钥与是否为新建。
//
// 这是子节点的「自持密钥」——首次启动生成、打到日志，管理员复制到面板完成配对。
func LoadOrCreateKey(path string) (key string, created bool, err error) {
	if b, e := os.ReadFile(path); e == nil {
		if k := strings.TrimSpace(string(b)); len(k) >= 32 {
			return k, false, nil
		}
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", false, err
	}
	key = hex.EncodeToString(raw)
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			return "", false, err
		}
	}
	if err = os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return key, true, nil
}

// NewKey 生成一把新的连接密钥而不落盘（用于轮换）。
func NewKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
