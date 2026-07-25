package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirrobot01/bifrost/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := cli.Runner{Stdout: os.Stdout, Stderr: os.Stderr, Version: version}
	os.Exit(runner.Run(ctx, os.Args[1:]))
}
