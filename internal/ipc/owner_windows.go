//go:build windows

package ipc

import "os"

func ownedByMe(os.FileInfo) bool { return true }
