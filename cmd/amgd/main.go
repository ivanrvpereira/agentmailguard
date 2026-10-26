// Command amgd runs the agentmailguard daemon and manages it on the daemon host.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
)

func main() {
	// SIGHUP keeps its meaning: the terminal closed, so the daemon stops. SIGUSR1 reloads it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := daemon.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, daemon.Options{})
	stop()
	os.Exit(code)
}
