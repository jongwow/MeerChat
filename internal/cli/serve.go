package cli

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/jongwow/meerchat/internal/server"
	"github.com/jongwow/meerchat/internal/sshdemo"
	"github.com/jongwow/meerchat/internal/store"
	"github.com/jongwow/meerchat/internal/version"
)

func serveCmd(g *globals) *cobra.Command {
	var (
		addr, db, certFile, keyFile string
		history, maxConns, maxPeers int
		retention                   time.Duration
		allowBrowsers               bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a relay",
		Long: `Run a relay server.

The relay forwards ciphertext between clients in the same room. It never sees
passwords, keys, message contents, nicknames or room names. With --db it keeps
encrypted history in SQLite so clients can catch up after reconnecting;
without it, history lives in memory and is lost on restart.

For TLS, either pass --tls-cert/--tls-key or put the relay behind a reverse
proxy, and have clients use meers://.`,
		Example: `  meer serve
  meer serve --addr :443 --tls-cert cert.pem --tls-key key.pem --db /var/lib/meer/relay.db
  meer serve --history 0         # fully ephemeral, no history at all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := g.logger()
			var st store.Store = store.NewMemory(history)
			if db != "" {
				s, err := store.OpenSQLite(db, history)
				if err != nil {
					return err
				}
				st = s
			}
			defer st.Close()

			srv := server.New(server.Config{
				Store:                   st,
				Logger:                  log,
				Replay:                  min(history, 200),
				MaxConns:                maxConns,
				MaxPeersPerRoom:         maxPeers,
				Retention:               retention,
				InsecureSkipOriginCheck: allowBrowsers,
			})
			hs := &http.Server{
				Addr:              addr,
				Handler:           srv,
				ReadHeaderTimeout: 10 * time.Second,
			}
			errc := make(chan error, 1)
			go func() {
				var err error
				if certFile != "" {
					err = hs.ListenAndServeTLS(certFile, keyFile)
				} else {
					err = hs.ListenAndServe()
				}
				errc <- err
			}()
			log.Info("relay listening", "addr", addr, "tls", certFile != "", "db", db, "history", history, "version", version.Version())

			select {
			case err := <-errc:
				return err
			case <-cmd.Context().Done():
			}
			log.Info("shutting down")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Close() // WebSockets are hijacked; Shutdown alone won't close them
			if err := hs.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&addr, "addr", ":6337", "listen address")
	f.StringVar(&db, "db", "", "SQLite file for encrypted history (default: in memory)")
	f.IntVar(&history, "history", 500, "messages of history to keep per room (0 = none)")
	f.DurationVar(&retention, "retention", 7*24*time.Hour, "delete history older than this (0 = keep)")
	f.IntVar(&maxConns, "max-conns", 10000, "maximum concurrent connections")
	f.IntVar(&maxPeers, "max-peers", 512, "maximum connections per room")
	f.StringVar(&certFile, "tls-cert", "", "TLS certificate file")
	f.StringVar(&keyFile, "tls-key", "", "TLS key file")
	f.BoolVar(&allowBrowsers, "allow-browsers", false, "accept WebSocket connections from any web origin")
	cmd.MarkFlagsRequiredTogether("tls-cert", "tls-key")
	return cmd
}

func sshCmd(g *globals) *cobra.Command {
	var o sshdemo.Options
	cmd := &cobra.Command{
		Use:   "ssh",
		Short: "Serve a try-it-now demo of the chat UI over SSH",
		Long: `Serve the chat UI over SSH, so people can try meer with nothing but ssh:

    ssh -p 2222 your-host

Each visitor gets a chat client that runs on this machine, which therefore
sees their plaintext. The UI tells visitors so. Use this for demos, not for
private conversations. Visitors can only join rooms on --relay.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.Logger = g.logger()
			return sshdemo.Run(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Addr, "addr", ":2222", "SSH listen address")
	f.StringVar(&o.HostKeyPath, "host-key", ".ssh/meer_demo_ed25519", "host key path (created if missing)")
	f.StringVar(&o.Relay, "relay", "127.0.0.1:6337", "relay host:port that visitors connect to")
	f.StringVar(&o.Lobby, "lobby", "lobby", "room every visitor joins first (empty for none)")
	f.StringVar(&o.LobbyPassword, "lobby-password", "meer", "password of the lobby room")
	f.IntVar(&o.MaxSessions, "max-sessions", 100, "maximum concurrent SSH sessions")
	return cmd
}
