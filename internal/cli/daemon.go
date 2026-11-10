package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jongwow/meerchat/internal/client"
	"github.com/jongwow/meerchat/internal/daemon"
	"github.com/jongwow/meerchat/internal/identity"
	"github.com/jongwow/meerchat/internal/ipc"
	"github.com/jongwow/meerchat/internal/protocol"
	"github.com/jongwow/meerchat/internal/version"
)

func (g *globals) socketPath() string {
	if g.socket != "" {
		return g.socket
	}
	return ipc.SocketPath()
}

// connect dials the daemon, starting it first if autostart is set.
func (g *globals) connect(ctx context.Context, autostart bool) (*ipc.Client, error) {
	sock := g.socketPath()
	c, err := ipc.Dial(ctx, sock)
	if errors.Is(err, ipc.ErrNoDaemon) && autostart {
		if err := ipc.PrepareSocketDir(sock); err != nil {
			return nil, err
		}
		if err := spawnDaemon(g); err != nil {
			return nil, fmt.Errorf("start daemon: %w", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			c, err = ipc.Dial(ctx, sock)
			if err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	if errors.Is(err, ipc.ErrNoDaemon) {
		if autostart {
			return nil, fmt.Errorf("the daemon did not start; see %s", daemonLogPath())
		}
		return nil, errors.New("not connected to any rooms (the daemon is not running). start with: meer join meer://host/room")
	}
	if err != nil {
		return nil, err
	}
	if h, err := c.Hello(ctx); err == nil && h.Version != version.Version() {
		fmt.Fprintf(os.Stderr, "meer: note: daemon is %s but this is %s; restart it with: meer daemon stop\n", h.Version, version.Version())
	}
	return c, nil
}

// spawnDaemon starts `meer daemon` detached from this terminal.
func spawnDaemon(g *globals) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := daemonLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // our own log under the user cache dir
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "daemon", "--socket", g.socketPath(), "--log-level", g.logLevel) //nolint:gosec // re-executing ourselves
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func daemonLogPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "meer", "daemon.log")
}

// configPath returns where meer keeps a config file: $MEER_CONFIG_DIR, or
// "meer" under the platform's config directory.
func configPath(name string) string {
	if d := os.Getenv("MEER_CONFIG_DIR"); d != "" {
		return filepath.Join(d, name)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "meer", name)
}

var nickChars = regexp.MustCompile(`[^\p{L}\p{N}_\-.]`)

// defaultNick is the saved nickname, else the login name.
func defaultNick() string {
	if p := configPath("nick"); p != "" {
		if b, err := os.ReadFile(p); err == nil { //nolint:gosec // our own config file
			if n := strings.TrimSpace(string(b)); protocol.ValidateNick(n) == nil {
				return n
			}
		}
	}
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil && name == "" {
		name = u.Username
	}
	name = nickChars.ReplaceAllString(name, "")
	if len(name) > protocol.MaxNick {
		name = name[:protocol.MaxNick]
	}
	if protocol.ValidateNick(name) != nil {
		return "meer"
	}
	return name
}

func saveNick(n string) {
	p := configPath("nick")
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(n+"\n"), 0o600)
}

func daemonCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the background daemon that keeps rooms connected",
		Long: `The daemon holds your room connections and keys in memory so the chat UI and
scripts can attach and detach. It is started automatically by "meer" and
"meer join". Run it in the foreground with "meer daemon".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := g.logger()
			opts := client.Options{Nick: defaultNick()}
			// Without a usable config directory, run with a temporary
			// identity rather than not at all; others will see a new key.
			if p := configPath("identity"); p != "" {
				if id, err := identity.LoadOrCreate(p); err != nil {
					log.Warn("using a temporary identity", "err", err)
				} else {
					opts.Identity = id
				}
			}
			if p := configPath("known_keys.json"); p != "" {
				if book, err := identity.OpenBook(p); err != nil {
					log.Warn("not remembering keys across restarts", "err", err)
				} else {
					opts.Book = book
				}
			}
			return daemon.Run(cmd.Context(), daemon.Options{
				Socket:  g.socketPath(),
				Logger:  log,
				Manager: opts,
				OnNick:  saveNick,
			})
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "stop",
			Short: "Leave all rooms and stop the daemon",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := ipc.Dial(cmd.Context(), g.socketPath())
				if errors.Is(err, ipc.ErrNoDaemon) {
					fmt.Fprintln(cmd.OutOrStdout(), "daemon is not running")
					return nil
				}
				if err != nil {
					return err
				}
				defer c.Close()
				return c.Shutdown(cmd.Context())
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show whether the daemon is running",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				out := cmd.OutOrStdout()
				c, err := ipc.Dial(cmd.Context(), g.socketPath())
				if errors.Is(err, ipc.ErrNoDaemon) {
					fmt.Fprintln(out, "daemon is not running")
					return nil
				}
				if err != nil {
					return err
				}
				defer c.Close()
				h, err := c.Hello(cmd.Context())
				if err != nil {
					return err
				}
				rooms, _ := c.Rooms(cmd.Context())
				fmt.Fprintf(out, "daemon %s running as pid %d, %d room(s)\nsocket: %s\nlog:    %s\n", h.Version, h.PID, len(rooms), g.socketPath(), daemonLogPath())
				return nil
			},
		},
		&cobra.Command{
			Use:   "log",
			Short: "Print the daemon log",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				f, err := os.Open(daemonLogPath())
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(cmd.OutOrStdout(), f)
				return err
			},
		},
	)
	return cmd
}
