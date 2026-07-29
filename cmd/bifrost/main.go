package main

import (
	"os"

	"github.com/sirrobot01/bifrost/internal/cli"
)

var version = "dev"

func main() {
	runner := cli.Runner{Stdout: os.Stdout, Stderr: os.Stderr, Version: version}
	os.Exit(run(runner, os.Args[1:]))
}
