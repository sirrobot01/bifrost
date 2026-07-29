//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/sirrobot01/bifrost/internal/cli"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

func run(runner cli.Runner, arguments []string) int {
	isService, err := svc.IsWindowsService()
	if err != nil {
		_, _ = fmt.Fprintf(runner.Stderr, "detect Windows service: %v\n", err)
		return 1
	}
	if isService {
		return runWindowsService(runner, arguments)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runner.Run(ctx, arguments)
}

func runWindowsService(runner cli.Runner, arguments []string) int {
	name := "bifrost"
	if len(arguments) > 0 && arguments[0] == "edge" {
		name = "bifrost-edge"
	}
	log, err := eventlog.Open(name)
	if err == nil {
		defer log.Close()
		writer := eventWriter{log: log}
		runner.Stdout = writer
		runner.Stderr = writer
	}
	if err := svc.Run(name, serviceHandler{runner: runner, arguments: arguments}); err != nil {
		if log != nil {
			_ = log.Error(1, err.Error())
		}
		return 1
	}
	return 0
}

type serviceHandler struct {
	runner    cli.Runner
	arguments []string
}

func (h serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan int, 1)
	changes <- svc.Status{State: svc.StartPending}
	go func() { result <- h.runner.Run(ctx, h.arguments) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case code := <-result:
			return code != 0, uint32(code)
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				cancel()
				return waitForShutdown(result, changes)
			}
		}
	}
}

func waitForShutdown(result <-chan int, changes chan<- svc.Status) (bool, uint32) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	checkpoint := uint32(1)
	changes <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: 150_000}
	for {
		select {
		case code := <-result:
			return code != 0, uint32(code)
		case <-ticker.C:
			checkpoint++
			changes <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: 150_000}
		}
	}
}

type eventWriter struct {
	log *eventlog.Log
}

func (w eventWriter) Write(data []byte) (int, error) {
	message := strings.TrimSpace(string(data))
	if message == "" {
		return len(data), nil
	}
	return len(data), w.log.Info(1, message)
}
