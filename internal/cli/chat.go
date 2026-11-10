package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"golang.org/x/time/rate"

	"github.com/jongwow/meerchat/internal/chat"
	"github.com/jongwow/meerchat/internal/ipc"
	"github.com/jongwow/meerchat/internal/protocol"
	"github.com/jongwow/meerchat/internal/tui"
)

func runTUI(ctx context.Context, g *globals) error {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("the chat UI needs a terminal; for scripts use meer send / meer tail")
	}
	c, err := g.connect(ctx, true)
	if err != nil {
		return err
	}
	defer c.Close()
	return tui.Run(ctx, c, tui.Options{
		DetachHint: "detach (rooms stay connected)",
		Shutdown:   c.Shutdown,
	})
}

func joinCmd(g *globals) *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "join ADDRESS",
		Short: "Join a room",
		Long: `Join a room and keep it connected in the background.

ADDRESS is meer://host[:port]/room, or meers:// for a relay behind TLS.

The password is read from the terminal, from stdin with --password-stdin, or
from $MEER_PASSWORD. It is never passed on the command line, so it does not
end up in your shell history or the process list.`,
		Example: `  meer join meer://chat.example.com/lobby
  pass show meer/lobby | meer join --password-stdin meers://chat.example.com/lobby`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := chat.ParseAddress(args[0])
			if err != nil {
				return err
			}
			pw, err := readPassword(cmd, addr.String(), passwordStdin)
			if err != nil {
				return err
			}
			c, err := g.connect(cmd.Context(), true)
			if err != nil {
				return err
			}
			defer c.Close()
			info, err := c.Join(cmd.Context(), addr.String(), pw)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "joined %s\nkey fingerprint: %s  (everyone who typed the same password sees the same fingerprint)\n", info.URL, info.Fingerprint)
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from the first line of stdin")
	return cmd
}

func readPassword(cmd *cobra.Command, addr string, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	if pw, ok := os.LookupEnv("MEER_PASSWORD"); ok {
		return pw, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("no password: use a terminal, --password-stdin, or $MEER_PASSWORD")
	}
	fmt.Fprintf(os.Stderr, "password for %s: ", addr)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		fmt.Fprintln(os.Stderr, "meer: warning: empty password. anyone who guesses the room name can read along.")
	}
	return string(b), nil
}

func leaveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "leave [ROOM]",
		Short: "Leave a room (default: the most recently joined)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.connect(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer c.Close()
			return c.Leave(cmd.Context(), firstArg(args))
		},
	}
}

func sendCmd(g *globals) *cobra.Command {
	var room string
	var action bool
	cmd := &cobra.Command{
		Use:   "send [TEXT...]",
		Short: "Send a message; with no TEXT, send each line of stdin",
		Example: `  meer send "deploy finished"
  meer send -r ops --me "is rolling back"
  tail -f /var/log/app.log | grep ERROR | meer send -r alerts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.connect(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer c.Close()
			kind := chat.KindMessage
			if action {
				kind = chat.KindAction
			}
			if len(args) > 0 && (len(args) != 1 || args[0] != "-") {
				for _, part := range chunk(strings.Join(args, " "), protocol.MaxText) {
					if err := c.Send(cmd.Context(), room, kind, part); err != nil {
						return err
					}
				}
				return nil
			}
			// Stay under the relay's rate limit when piping.
			lim := rate.NewLimiter(4, 15)
			sc := bufio.NewScanner(cmd.InOrStdin())
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				line := strings.TrimRight(sc.Text(), "\r")
				if strings.TrimSpace(line) == "" {
					continue
				}
				for _, part := range chunk(line, protocol.MaxText) {
					if err := lim.Wait(cmd.Context()); err != nil {
						return err
					}
					if err := c.Send(cmd.Context(), room, kind, part); err != nil {
						return err
					}
				}
			}
			return sc.Err()
		},
	}
	cmd.Flags().StringVarP(&room, "room", "r", "", "room name or address (default: most recently joined)")
	cmd.Flags().BoolVar(&action, "me", false, "send as an action, like /me")
	return cmd
}

// chunk splits s into pieces of at most n bytes without breaking UTF-8.
func chunk(s string, n int) []string {
	var out []string
	for len(s) > n {
		cut := n
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}

func tailCmd(g *globals) *cobra.Command {
	var (
		room     string
		n        int
		noFollow bool
		asJSON   bool
		all      bool
	)
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Print recent messages and follow new ones",
		Example: `  meer tail                       # all rooms, follow
  meer tail -r lobby -n 50 --no-follow
  meer tail --json | jq -r 'select(.kind=="message") | .text'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			c, err := g.connect(ctx, false)
			if err != nil {
				return err
			}
			defer c.Close()
			filter := ""
			if room != "" {
				if filter, err = resolveRoom(ctx, c, room); err != nil {
					return err
				}
			}
			events, err := c.Subscribe(ctx, n)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			enc := json.NewEncoder(out)
			for e := range events {
				if e.Kind == chat.KindSync {
					if noFollow {
						return nil
					}
					continue
				}
				if filter != "" && e.Room != filter {
					continue
				}
				if !all && !tailable(e) {
					continue
				}
				if asJSON {
					if err := enc.Encode(e); err != nil {
						return err
					}
					continue
				}
				fmt.Fprintln(out, formatEvent(e, filter == ""))
			}
			if ctx.Err() == nil {
				return errors.New("lost connection to the daemon")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&room, "room", "r", "", "only this room (name or address)")
	cmd.Flags().IntVarP(&n, "lines", "n", 20, "how many recent events per room to print first")
	cmd.Flags().BoolVar(&noFollow, "no-follow", false, "print recent events and exit")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object per event")
	cmd.Flags().BoolVar(&all, "all", false, "include status and system events")
	return cmd
}

func tailable(e chat.Event) bool {
	switch e.Kind {
	case chat.KindMessage, chat.KindAction, chat.KindMeerkat, chat.KindJoin, chat.KindLeave, chat.KindNick:
		return true
	}
	return false
}

func formatEvent(e chat.Event, showRoom bool) string {
	ts := e.Time.Local().Format("15:04")
	prefix := ts
	if showRoom {
		if a, err := chat.ParseAddress(e.Room); err == nil {
			prefix += " #" + a.Room
		}
	}
	nick := chat.Sanitize(e.Nick, false)
	text := chat.Sanitize(e.Text, false)
	switch e.Kind {
	case chat.KindMessage:
		return fmt.Sprintf("%s <%s> %s", prefix, nick, text)
	case chat.KindAction:
		return fmt.Sprintf("%s * %s %s", prefix, nick, text)
	case chat.KindMeerkat:
		return fmt.Sprintf("%s <%s> %s", prefix, nick, text)
	case chat.KindJoin:
		return fmt.Sprintf("%s → %s joined", prefix, nick)
	case chat.KindLeave:
		return fmt.Sprintf("%s ← %s left", prefix, nick)
	case chat.KindNick:
		return fmt.Sprintf("%s ~ %s is now %s", prefix, text, nick)
	case chat.KindStatus:
		return fmt.Sprintf("%s ! %s %s", prefix, e.Status, text)
	}
	return fmt.Sprintf("%s - %s", prefix, text)
}

// resolveRoom maps a room name or address to the URL the daemon uses.
func resolveRoom(ctx context.Context, c *ipc.Client, ref string) (string, error) {
	rooms, err := c.Rooms(ctx)
	if err != nil {
		return "", err
	}
	var match []string
	for _, r := range rooms {
		if r.URL == ref || r.Name == ref {
			match = append(match, r.URL)
		} else if a, err := chat.ParseAddress(ref); err == nil && strings.Contains(ref, "/") && a.String() == r.URL {
			match = append(match, r.URL)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return "", fmt.Errorf("not in a room called %q", ref)
	}
	return "", fmt.Errorf("%q is ambiguous: %s", ref, strings.Join(match, ", "))
}

func roomsCmd(g *globals) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "rooms",
		Short: "List joined rooms",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.connect(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer c.Close()
			rooms, err := c.Rooms(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if rooms == nil {
					rooms = []chat.RoomInfo{}
				}
				return json.NewEncoder(out).Encode(rooms)
			}
			if len(rooms) == 0 {
				fmt.Fprintln(out, "not in any rooms. join one with: meer join meer://host/room")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ROOM\tSTATUS\tPEERS\tFINGERPRINT\tADDRESS")
			for _, r := range rooms {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", r.Name, r.Status, r.Peers, r.Fingerprint, r.URL)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func nickCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "nick [NAME]",
		Short: "Show or change your nickname",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := protocol.ValidateNick(args[0]); err != nil {
					return err
				}
			}
			c, err := g.connect(cmd.Context(), false)
			if err != nil {
				// No daemon: the nickname is just a saved preference.
				if len(args) == 1 {
					saveNick(args[0])
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), defaultNick())
				return nil
			}
			defer c.Close()
			if len(args) == 0 {
				n, err := c.Nick(cmd.Context())
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), n)
				return nil
			}
			return c.SetNick(cmd.Context(), args[0])
		},
	}
}

func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}
