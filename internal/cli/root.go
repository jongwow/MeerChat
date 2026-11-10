// Package cli implements the `meer` command.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/jongwow/meerchat/internal/version"
)

type globals struct {
	socket   string
	logLevel string
}

// Execute runs the CLI.
func Execute(ctx context.Context) int {
	g := &globals{}
	root := &cobra.Command{
		Use:   "meer",
		Short: "End-to-end encrypted terminal chat. Wrong password? Enjoy the meerkats.",
		Long: `meer is an end-to-end encrypted chat for the terminal.

Rooms are protected by a password that never leaves your machine. The relay
only ever sees ciphertext, and does not even learn the room's name. Anyone who
types the wrong password can still join, but to them the room is full of
meerkats, and to you, so are they.

Running "meer" with no arguments opens the chat UI. Rooms stay connected in a
background daemon, so you can detach, come back later, and script the chat
from your shell with "meer send" and "meer tail".`,
		Example: `  meer serve                                   # run a relay on :6337
  meer join meer://localhost/lobby             # join a room (prompts for the password)
  meer                                         # open the chat UI
  make && meer send "build passed"             # post from scripts
  meer tail --json | jq .text                  # stream messages as JSON`,
		Version:       version.Version(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cmd.Context(), g)
		},
	}
	root.PersistentFlags().StringVar(&g.socket, "socket", "", "daemon socket path (default $MEER_SOCKET or a per-user runtime dir)")
	root.PersistentFlags().StringVar(&g.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	root.SetVersionTemplate("meer {{.Version}}\n")

	root.AddCommand(
		joinCmd(g), leaveCmd(g), sendCmd(g), tailCmd(g), roomsCmd(g), nickCmd(g), whoamiCmd(g), trustCmd(g),
		daemonCmd(g), serveCmd(g), sshCmd(g), versionCmd(),
	)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "meer:", err)
		return 1
	}
	return 0
}

func (g *globals) logger() *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(g.logLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "meer", version.Version())
		},
	}
}
