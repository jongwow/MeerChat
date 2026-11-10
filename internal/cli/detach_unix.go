//go:build !windows

package cli

import "syscall"

// detachAttr puts the daemon in its own session so closing the terminal
// does not take it down.
func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
