// Command meer is an end-to-end encrypted terminal chat.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/jongwow/meerchat/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
