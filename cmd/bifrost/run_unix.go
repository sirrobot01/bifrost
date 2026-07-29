//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirrobot01/bifrost/internal/cli"
)

func run(runner cli.Runner, arguments []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runner.Run(ctx, arguments)
}
