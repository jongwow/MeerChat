package cli

import (
	"context"
	"net"
	"os"
	"os/signal"
	"regexp"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/jongwow/meerchat/internal/ipc"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"meer": func() {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			code := Execute(ctx)
			stop()
			os.Exit(code)
		},
	})
}

// TestScripts runs the end-to-end scenarios in testdata/script against the
// real command, with a real relay and daemons on loopback.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		Setup: func(env *testscript.Env) error {
			// Unix socket paths are limited to ~104 bytes; $WORK can be long.
			dir, err := os.MkdirTemp("", "ms")
			if err != nil {
				return err
			}
			env.Defer(func() { os.RemoveAll(dir) })
			env.Setenv("SOCKDIR", dir)
			env.Setenv("RELAY", freePort(t))
			env.Setenv("MEER_CONFIG_DIR", env.WorkDir+"/config")
			env.Setenv("XDG_CACHE_HOME", env.WorkDir+"/cache")
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"waitport":   waitPort,
			"waitsock":   waitSock,
			"eventually": eventually,
		},
	})
}

func freePort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

const waitTimeout = 15 * time.Second

// waitport ADDR: wait until something listens on ADDR.
func waitPort(ts *testscript.TestScript, _ bool, args []string) {
	deadline := time.Now().Add(waitTimeout)
	for {
		c, err := net.Dial("tcp", args[0])
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			ts.Fatalf("nothing listening on %s", args[0])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitsock PATH: wait until a daemon answers on PATH.
func waitSock(ts *testscript.TestScript, _ bool, args []string) {
	deadline := time.Now().Add(waitTimeout)
	for {
		c, err := ipc.Dial(context.Background(), args[0])
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			ts.Fatalf("no daemon on %s: %v", args[0], err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// eventually REGEXP CMD ARGS...: rerun CMD until its stdout matches REGEXP.
// Delivery between daemons is asynchronous, so checks of what someone else
// sees need a little patience.
func eventually(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) < 2 {
		ts.Fatalf("usage: eventually REGEXP CMD [ARGS...]")
	}
	re := regexp.MustCompile("(?m)" + args[0])
	deadline := time.Now().Add(waitTimeout)
	for {
		err := ts.Exec(args[1], args[2:]...)
		if err == nil && re.MatchString(ts.ReadFile("stdout")) {
			return
		}
		if time.Now().After(deadline) {
			ts.Fatalf("stdout never matched %q (last error: %v)", args[0], err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
