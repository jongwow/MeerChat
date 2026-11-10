package cli

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jongwow/meerchat/internal/ipc"
	"github.com/jongwow/meerchat/internal/server"
)

// TestAutostart builds the real binary and checks that `meer join` starts
// a detached daemon when none is running. The scripts can't cover this:
// under testscript, the daemon would re-execute the test binary.
func TestAutostart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("process detaching differs on Windows; covered manually")
	}
	bin := filepath.Join(t.TempDir(), "meer")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/meer")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	addr := freePort(t)
	relay := server.New(server.Config{})
	hs := &http.Server{Addr: addr, Handler: relay, ReadHeaderTimeout: time.Second}
	go func() { _ = hs.ListenAndServe() }()
	t.Cleanup(func() { _ = hs.Close(); relay.Close() })

	sockDir, err := os.MkdirTemp("", "ms")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "d", "s.sock")
	env := append(os.Environ(),
		"MEER_SOCKET="+sock,
		"MEER_PASSWORD=pw",
		"XDG_CONFIG_HOME="+t.TempDir(),
		"XDG_CACHE_HOME="+t.TempDir(),
	)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("meer %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	t.Cleanup(func() {
		cmd := exec.Command(bin, "daemon", "stop")
		cmd.Env = env
		_ = cmd.Run()
	})

	out := run("join", "meer://"+addr+"/lobby")
	if !strings.Contains(out, "joined meer://") {
		t.Fatalf("join output: %s", out)
	}
	// The daemon outlives the command that started it.
	c, err := ipc.Dial(context.Background(), sock)
	if err != nil {
		t.Fatalf("daemon not running after join: %v", err)
	}
	defer c.Close()
	rooms, err := c.Rooms(context.Background())
	if err != nil || len(rooms) != 1 {
		t.Fatalf("rooms: %v %v", rooms, err)
	}
	if out := run("daemon", "status"); !strings.Contains(out, "running as pid") {
		t.Fatalf("status: %s", out)
	}
}
