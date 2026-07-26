package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"magirecocn-revival/cnv-server/internal/directory"
)

// gen-directory-key 生成节点目录的 Ed25519 信任根密钥对。
// 公钥钉扎进客户端，私钥离线保管 / 存入 CI Secret。
func runGenDirectoryKey(args []string) int {
	fs := flag.NewFlagSet("gen-directory-key", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	pub, priv, err := directory.GenerateKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("# Ed25519 节点目录信任根密钥对")
	fmt.Println("# 公钥：钉扎进客户端（与 IntegrityGuard 同级），可公开")
	fmt.Printf("DIRECTORY_PUBKEY=%s\n", pub)
	fmt.Println("# 私钥：离线保管 / 存入 CI Secret —— 绝不上线、绝不进仓库")
	fmt.Printf("DIRECTORY_PRIVKEY=%s\n", priv)
	return 0
}

// sign-directory 读入未签名的目录 JSON（seq / nodes 等），补全时间戳后离线签名，
// 输出 JWS 风格的 {"payload": "<base64url>", "sig": "<base64>"} 格式。
func runSignDirectory(args []string) int {
	fs := flag.NewFlagSet("sign-directory", flag.ContinueOnError)
	in := fs.String("in", "", "未签名目录 JSON 文件（默认 stdin）")
	out := fs.String("out", "", "输出已签名 JSON（默认 stdout）")
	privFlag := fs.String("privkey", "", "十六进制私钥（也可用环境变量 CNV_DIRECTORY_PRIVKEY）")
	privFile := fs.String("privkey-file", "", "从文件读私钥")
	seq := fs.Int64("seq", 0, ">0 时覆盖目录里的 seq")
	ttl := fs.Duration("ttl", 48*time.Hour, "expires_at 缺省时取 now+ttl")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	raw, err := readInput(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取输入失败:", err)
		return 1
	}
	var d directory.Directory
	if err := json.Unmarshal(raw, &d); err != nil {
		fmt.Fprintln(os.Stderr, "解析目录 JSON 失败:", err)
		return 1
	}
	if *seq > 0 {
		d.Seq = *seq
	}
	now := time.Now().Unix()
	if d.IssuedAt == 0 {
		d.IssuedAt = now
	}
	if d.ExpiresAt == 0 {
		d.ExpiresAt = now + int64(ttl.Seconds())
	}

	privHex, err := loadDirectoryPrivKey(*privFlag, *privFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	priv, err := directory.ParsePrivateKey(privHex)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sd, err := directory.Sign(&d, priv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "签名失败:", err)
		return 1
	}

	outBytes, _ := json.MarshalIndent(sd, "", "  ")
	if *out != "" {
		if err := os.WriteFile(*out, append(outBytes, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "已签名并写入 %s（seq=%d, expires_at=%d, 节点=%d）\n", *out, d.Seq, d.ExpiresAt, len(d.Nodes))
	} else {
		_, _ = os.Stdout.Write(append(outBytes, '\n'))
	}
	return 0
}

// verify-directory 用公钥校验一份已签名目录（CI 自检 / 排障用）。
func runVerifyDirectory(args []string) int {
	fs := flag.NewFlagSet("verify-directory", flag.ContinueOnError)
	in := fs.String("in", "", "已签名目录 JSON 文件（默认 stdin）")
	pubFlag := fs.String("pubkey", "", "十六进制公钥（也可用环境变量 CNV_DIRECTORY_PUBKEY）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	raw, err := readInput(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var sd directory.SignedDirectory
	if err := json.Unmarshal(raw, &sd); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pubHex := *pubFlag
	if pubHex == "" {
		pubHex = os.Getenv("CNV_DIRECTORY_PUBKEY")
	}
	if pubHex == "" {
		fmt.Fprintln(os.Stderr, "缺少公钥：-pubkey 或环境变量 CNV_DIRECTORY_PUBKEY")
		return 2
	}
	pub, err := directory.ParsePublicKey(pubHex)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	d, err := directory.Verify(sd, pub)
	if err != nil {
		fmt.Fprintln(os.Stderr, "校验失败:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "✔ 签名有效（seq=%d, 节点=%d, expires_at=%d）\n", d.Seq, len(d.Nodes), d.ExpiresAt)
	return 0
}

func readInput(path string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	return io.ReadAll(os.Stdin)
}

func loadDirectoryPrivKey(flagVal, fileVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if v := os.Getenv("CNV_DIRECTORY_PRIVKEY"); v != "" {
		return v, nil
	}
	if fileVal != "" {
		b, err := os.ReadFile(fileVal)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", errors.New("缺少私钥：用 -privkey / -privkey-file 或环境变量 CNV_DIRECTORY_PRIVKEY")
}
