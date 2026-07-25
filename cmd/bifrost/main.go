package main

import (
	"context"
	"os"

	"github.com/sirrobot01/bifrost/internal/cli"
)

var version = "dev"

func main() {
	runner := cli.Runner{Stdout: os.Stdout, Stderr: os.Stderr, Version: version}
	os.Exit(runner.Run(context.Background(), os.Args[1:]))
}
