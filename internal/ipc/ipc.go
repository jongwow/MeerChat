// Package ipc is the protocol between `meer` front ends and the local daemon.
//
// It is newline-delimited JSON over a Unix socket that only the owning user
// can open. One request gets one response, except "subscribe", after which the
// connection carries a stream of events until either side closes it.
package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jongwow/meerchat/internal/chat"
)

// Methods.
const (
	MethodHello     = "hello"
	MethodJoin      = "join"
	MethodLeave     = "leave"
	MethodSend      = "send"
	MethodRooms     = "rooms"
	MethodNick      = "nick"
	MethodSetNick   = "set_nick"
	MethodSubscribe = "subscribe"
	MethodShutdown  = "shutdown"
	MethodIdentity  = "identity"
	MethodTrust     = "trust"
)

// Request is one call.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers a Request.
type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// StreamItem is sent after a successful subscribe.
type StreamItem struct {
	Event *chat.Event `json:"event"`
}

// Param and result types.
type (
	HelloResult struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	JoinParams struct {
		Address  string `json:"address"`
		Password string `json:"password"`
	}
	RoomParams struct {
		Room string `json:"room"`
	}
	SendParams struct {
		Room string `json:"room"`
		Kind string `json:"kind"`
		Text string `json:"text"`
		// WaitMS, if positive, waits for the relay to echo the message.
		WaitMS int64 `json:"wait_ms,omitempty"`
	}
	NickParams struct {
		Nick string `json:"nick"`
	}
	SubscribeParams struct {
		Backlog int `json:"backlog"`
	}
	TrustParams struct {
		Room string `json:"room"`
		Nick string `json:"nick"`
	}
	KeyResult struct {
		Key string `json:"key"`
	}
)

// SocketPath returns where the daemon listens: $MEER_SOCKET, else a
// per-user runtime directory.
func SocketPath() string {
	if p := os.Getenv("MEER_SOCKET"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "meer", "daemon.sock")
	}
	// macOS and others: keep the path short, sockets have a ~104 byte limit.
	uid := "user"
	if runtime.GOOS != "windows" {
		uid = fmt.Sprint(os.Getuid())
	}
	return filepath.Join(os.TempDir(), "meer-"+uid, "daemon.sock")
}

// PrepareSocketDir creates the socket's directory with owner-only
// permissions and refuses to use one that others can access.
func PrepareSocketDir(sock string) error {
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	// Others must not be able to swap the socket for their own.
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("socket directory %s is writable by other users (mode %v); use a private directory", dir, fi.Mode().Perm())
	}
	if !ownedByMe(fi) {
		return fmt.Errorf("socket directory %s belongs to another user", dir)
	}
	return nil
}

// ErrNoDaemon means nothing is listening on the socket.
var ErrNoDaemon = errors.New("meer daemon is not running")
