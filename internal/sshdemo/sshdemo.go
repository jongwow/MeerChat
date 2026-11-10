// Package sshdemo serves the MeerChat TUI over SSH so people can try it
// without installing anything:
//
//	ssh -p 2222 demo.example.com
//
// Each SSH session gets its own in-process chat client. That client runs on
// the demo host, so the demo host sees plaintext: this is a try-before-you-
// install convenience, NOT end-to-end encryption from your machine. The TUI
// says so on screen. Visitors can only join rooms on the configured relay.
package sshdemo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"charm.land/wish/v2/activeterm"
	bm "charm.land/wish/v2/bubbletea"

	"github.com/jongwow/meerchat/internal/chat"
	"github.com/jongwow/meerchat/internal/client"
	"github.com/jongwow/meerchat/internal/protocol"
	"github.com/jongwow/meerchat/internal/tui"
)

// Options configure the demo server.
type Options struct {
	Addr        string
	HostKeyPath string
	// Relay is the only relay visitors may join, e.g. "127.0.0.1:6337".
	Relay string
	// Lobby is the room every visitor lands in, with its password.
	Lobby, LobbyPassword string
	MaxSessions          int
	IdleTimeout          time.Duration
	Logger               *slog.Logger
}

const banner = "demo mode: this SSH server runs your chat client, so it can read what you type here. for real end-to-end encryption, install meer locally."

// Run serves until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.MaxSessions == 0 {
		o.MaxSessions = 100
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = 30 * time.Minute
	}
	relay, err := chat.ParseAddress("meer://" + o.Relay + "/x")
	if err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	var sessions atomic.Int64

	handler := func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
		ctx := sess.Context()
		m, err := client.NewManager(client.Options{
			Nick:   guestNick(sess.User()),
			Logger: o.Logger,
			AllowAddress: func(a chat.Address) error {
				if a.Host != relay.Host || a.TLS != relay.TLS {
					return errors.New("the demo can only join rooms on its own relay")
				}
				return nil
			},
		})
		if err != nil {
			wish.Fatalln(sess, err)
			return nil, nil
		}
		go func() {
			<-ctx.Done()
			m.Close()
		}()
		lobby := ""
		if o.Lobby != "" {
			lobby = chat.Address{TLS: relay.TLS, Host: relay.Host, Room: o.Lobby}.String()
		}
		model := tui.New(ctx, client.Local{M: m}, tui.Options{
			Title:            "meer demo",
			DetachHint:       "disconnect",
			AutoJoin:         lobby,
			AutoJoinPassword: o.LobbyPassword,
			Banner:           banner,
		})
		return model, nil
	}

	limit := func(next ssh.Handler) ssh.Handler {
		return func(sess ssh.Session) {
			if sessions.Add(1) > int64(o.MaxSessions) {
				sessions.Add(-1)
				wish.Fatalln(sess, "the demo is full right now, try again in a bit")
				return
			}
			defer sessions.Add(-1)
			o.Logger.Info("ssh session", "user", sess.User(), "remote", sess.RemoteAddr().String())
			next(sess)
		}
	}

	srv, err := wish.NewServer(
		wish.WithAddress(o.Addr),
		wish.WithHostKeyPath(o.HostKeyPath),
		wish.WithIdleTimeout(o.IdleTimeout),
		wish.WithMiddleware(
			bm.Middleware(handler),
			activeterm.Middleware(),
			limit,
		),
	)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	o.Logger.Info("ssh demo listening", "addr", o.Addr, "relay", o.Relay, "lobby", o.Lobby)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			return err
		}
		return nil
	}
}

var nonNick = regexp.MustCompile(`[^A-Za-z0-9_\-.]`)

// guestNick derives a nickname from the SSH user name.
func guestNick(user string) string {
	n := nonNick.ReplaceAllString(user, "")
	if n == "" || n == "root" {
		n = "guest-" + protocol.NewPeerID()[:4]
	}
	if len(n) > protocol.MaxNick {
		n = n[:protocol.MaxNick]
	}
	return strings.ToLower(n)
}
