package sshdemo

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jongwow/meerchat/internal/server"
)

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestDemoOverSSH(t *testing.T) {
	// Relay.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay := server.New(server.Config{})
	hs := &http.Server{Handler: relay}
	go func() { _ = hs.Serve(ln) }()
	defer func() { _ = hs.Close(); relay.Close() }()

	// Demo.
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Addr:          addr,
			HostKeyPath:   filepath.Join(t.TempDir(), "host_ed25519"),
			Relay:         ln.Addr().String(),
			Lobby:         "lobby",
			LobbyPassword: "meer",
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	var client *ssh.Client
	deadline := time.Now().Add(5 * time.Second)
	for {
		client, err = ssh.Dial("tcp", addr, &ssh.ClientConfig{
			User:            "Visitor!",
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // test against our own server
			Timeout:         time.Second,
		})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm-256color", 30, 100, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	var out syncBuf
	sess.Stdout = &out
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !strings.Contains(out.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("never saw %q in output (%d bytes)", want, len(out.String()))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor("meer demo")
	waitFor("#lobby")  // auto-joined the lobby
	waitFor("visitor") // nick derived from the SSH user, sanitized

	_, _ = stdin.Write([]byte("hello from ssh\r"))
	waitFor("hello from ssh")

	// Visitors cannot point the demo at other hosts.
	_, _ = stdin.Write([]byte("/join meer://example.com/x\r"))
	_, _ = stdin.Write([]byte("pw\r"))
	waitFor("own relay")

	_, _ = stdin.Write([]byte{4}) // ctrl+d
}

func TestGuestNick(t *testing.T) {
	for in, want := range map[string]string{
		"Alice":     "alice",
		"bob smith": "bobsmith",
		"":          "guest-",
		"root":      "guest-",
		"💥":         "guest-",
	} {
		if got := guestNick(in); !strings.HasPrefix(got, want) {
			t.Errorf("guestNick(%q) = %q", in, got)
		}
	}
}
