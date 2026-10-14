//go:build !windows

package app

import "syscall"

// detachAttr starts the background daemon in its own session so closing the
// launching terminal (or the agent's shell) does not take it down.
func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
