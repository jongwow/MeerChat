//go:build !windows

package ipc

func isRefused(error) bool { return false }
