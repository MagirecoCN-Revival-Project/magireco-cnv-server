//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// setDetachedAttrs 让子进程脱离面板的进程组与控制终端:
//   - Setsid=true:新会话 + 新进程组,父进程死了不会带走它
//   - 不设 Pgid:让它自成一组,与面板互不影响信号传播
//   - Setpgid 用 false,等同于继承也行,但 setsid 已经隐含了 setpgrp
func setDetachedAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}
