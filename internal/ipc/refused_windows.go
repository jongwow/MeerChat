//go:build windows

package ipc

import (
	"errors"
	"syscall"
)

// wsaeconnrefused is what Windows returns when a stale socket file has no
// listener behind it.
const wsaeconnrefused syscall.Errno = 10061

func isRefused(err error) bool { return errors.Is(err, wsaeconnrefused) }
