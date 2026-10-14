//go:build windows

package app

import "syscall"

// detachAttr starts the background daemon without a console and outside the
// caller's process group (DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP).
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200, HideWindow: true}
}
