//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// Windows 没有 setsid,用 DETACHED_PROCESS + CREATE_NEW_PROCESS_GROUP 把子进程
// 从面板的控制台拆出来。这样面板被 Ctrl-C 时不会顺带杀掉节点。
//
// 详见:
//   https://learn.microsoft.com/en-us/windows/win32/procthread/process-creation-flags
const (
	winDetachedProcess     = 0x00000008
	winCreateNewProcessGrp = 0x00000200
)

func setDetachedAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: winDetachedProcess | winCreateNewProcessGrp,
		HideWindow:    true,
	}
}
