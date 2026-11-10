package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jongwow/meerchat/internal/chat"
	"github.com/jongwow/meerchat/internal/client"
	"github.com/jongwow/meerchat/internal/crypt"
	"github.com/jongwow/meerchat/internal/ipc"
	"github.com/jongwow/meerchat/internal/server"
)

func startRelay(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(server.Config{})
	hs := &http.Server{Handler: s}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close(); s.Close() })
	return ln.Addr().String()
}

// shortTempDir keeps socket paths under the ~104 byte sun_path limit.
func shortTempDir(t *testing.T) string {
	d, err := os.MkdirTemp("", "meer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func startDaemon(t *testing.T, nick string) (*ipc.Client, string) {
	sock := filepath.Join(shortTempDir(t), "d", "s.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Socket: sock, Manager: client.Options{Nick: nick, Params: crypt.TestParams}})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	var c *ipc.Client
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		c, err = ipc.Dial(ctx, sock)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { c.Close() })
	return c, sock
}

func TestDaemonRoundTrip(t *testing.T) {
	relay := startRelay(t)
	ctx := context.Background()
	alice, _ := startDaemon(t, "alice")
	bob, _ := startDaemon(t, "bob")

	h, err := alice.Hello(ctx)
	if err != nil || h.PID == 0 {
		t.Fatalf("hello: %+v %v", h, err)
	}

	room := "meer://" + relay + "/lobby"
	if _, err := alice.Join(ctx, room, "pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Join(ctx, room, "pw"); err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := bob.Subscribe(sctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.Send(ctx, "lobby", chat.KindMessage, "via the daemon"); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Kind == chat.KindMessage {
				if e.Text != "via the daemon" || e.Nick != "alice" {
					t.Fatalf("got %+v", e)
				}
				goto rooms
			}
		case <-timeout:
			t.Fatal("no message")
		}
	}
rooms:
	rs, err := bob.Rooms(ctx)
	if err != nil || len(rs) != 1 || rs[0].Name != "lobby" || rs[0].Status != chat.StatusOnline {
		t.Fatalf("rooms: %+v %v", rs, err)
	}

	if err := bob.SetNick(ctx, "robert"); err != nil {
		t.Fatal(err)
	}
	if n, _ := bob.Nick(ctx); n != "robert" {
		t.Fatalf("nick %q", n)
	}
	if err := bob.SetNick(ctx, "bad nick"); err == nil {
		t.Fatal("invalid nick accepted")
	}
	if err := bob.Send(ctx, "nowhere", chat.KindMessage, "x"); err == nil {
		t.Fatal("send to unknown room succeeded")
	}
	if err := bob.Leave(ctx, "lobby"); err != nil {
		t.Fatal(err)
	}
}

func TestSecondDaemonRefused(t *testing.T) {
	_, sock := startDaemon(t, "alice")
	err := Run(context.Background(), Options{Socket: sock})
	if err == nil {
		t.Fatal("second daemon started on the same socket")
	}
}

func TestStaleSocketReclaimed(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "d", "s.sock")
	_ = ipc.PrepareSocketDir(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: the file stays but nobody listens.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, Options{Socket: sock, Manager: client.Options{Params: crypt.TestParams}}) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := ipc.Dial(ctx, sock)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not reclaim stale socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestShutdown(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "d", "s.sock")
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{Socket: sock, Manager: client.Options{Params: crypt.TestParams}})
	}()
	var c *ipc.Client
	for range 500 {
		var err error
		if c, err = ipc.Dial(context.Background(), sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c == nil {
		t.Fatal("daemon never came up")
	}
	defer c.Close()
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket left behind")
	}
}

func TestSocketPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix permissions on Windows")
	}
	_, sock := startDaemon(t, "alice")
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket mode %v is too open", fi.Mode().Perm())
	}
	dir := filepath.Join(shortTempDir(t), "open")
	_ = os.Mkdir(dir, 0o777)
	_ = os.Chmod(dir, 0o777)
	if err := ipc.PrepareSocketDir(filepath.Join(dir, "s.sock")); err == nil {
		t.Fatal("world-writable socket dir accepted")
	}
}
