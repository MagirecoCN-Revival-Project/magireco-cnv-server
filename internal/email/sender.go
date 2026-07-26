// Package email 提供 SMTP 邮件发送能力。
// 若 SMTP Host 未配置,Send 返回 ErrNotConfigured;调用方可降级为开发模式日志输出。
package email

import (
	"errors"
	"fmt"
	"net/smtp"
	"strings"
)

// ErrNotConfigured 表示 SMTP Host 未配置,Send 降级为静默跳过。
var ErrNotConfigured = errors.New("SMTP 未配置")

// Config SMTP 连接与发件人配置。
type Config struct {
	Host     string // SMTP 服务器主机,空 = 禁用
	Port     string // 端口,默认 "587"
	User     string
	Pass     string
	From     string // 发件人地址
	FromName string // 发件人显示名(可选)
}

// Sender SMTP 邮件发送器。nil 值可安全调用,返回 ErrNotConfigured。
type Sender struct {
	cfg Config
}

// New 创建发送器。cfg.Host 为空时所有 Send 调用均返回 ErrNotConfigured。
func New(cfg Config) *Sender {
	return &Sender{cfg: cfg}
}

// Send 发送一封纯文本邮件。
func (s *Sender) Send(to, subject, body string) error {
	if s == nil || s.cfg.Host == "" {
		return ErrNotConfigured
	}
	port := s.cfg.Port
	if port == "" {
		port = "587"
	}
	addr := s.cfg.Host + ":" + port

	fromAddr := s.cfg.From
	fromLine := fromAddr
	if s.cfg.FromName != "" {
		fromLine = s.cfg.FromName + " <" + fromAddr + ">"
	}

	msg := []byte(strings.Join([]string{
		"From: " + fromLine,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n"))

	auth := smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, s.cfg.Host)
	return smtp.SendMail(addr, auth, fromAddr, []string{to}, msg)
}

// SendVerification 发送注册后邮箱验证码邮件。
func (s *Sender) SendVerification(to, code string) error {
	subject := "魔法纪录复兴计划 · 邮箱验证码"
	body := fmt.Sprintf(
		"您好！\n\n您的邮箱验证码为：%s\n\n请在 10 分钟内完成验证。\n验证后即可使用云存档与客户端登录功能。\n\n若非本人操作，请忽略此邮件。",
		code,
	)
	return s.Send(to, subject, body)
}

// SendPasswordReset 发送密码重置验证码邮件。
func (s *Sender) SendPasswordReset(to, code string) error {
	subject := "魔法纪录复兴计划 · 密码重置验证码"
	body := fmt.Sprintf(
		"您好！\n\n您的密码重置验证码为：%s\n\n请在 10 分钟内完成操作。\n若非本人操作，请忽略此邮件。",
		code,
	)
	return s.Send(to, subject, body)
}
