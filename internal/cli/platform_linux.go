//go:build linux

package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/home"
	"github.com/sirrobot01/bifrost/internal/netwatch"
)

func platformSnapshot(interfaceName string) (netwatch.Snapshot, error) {
	observer, err := netwatch.New(interfaceName)
	if err != nil {
		return netwatch.Snapshot{}, err
	}
	return observer.Snapshot()
}

func platformServe(ctx context.Context, configPath string, configFile config.Config, dryRun bool, logger *slog.Logger, output io.Writer) error {
	runtime, err := home.NewRuntime(configFile, logger)
	if err != nil {
		return err
	}
	if dryRun {
		actions, err := runtime.DryRun(ctx)
		if err != nil {
			return err
		}
		return writeJSON(output, actions)
	}

	// SIGHUP re-reads the file. A bad file is reported and ignored: the running
	// configuration is known to work, and replacing it with one that does not
	// parse would turn an editing mistake into an outage.
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)
	defer signal.Stop(hangups)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hangups:
				next, loadErr := config.Load(configPath)
				if loadErr != nil {
					logger.Error("reload rejected: the configuration did not load", "path", configPath, "error", loadErr)
					continue
				}
				if reloadErr := runtime.Reload(next); reloadErr != nil {
					logger.Error("reload rejected", "path", configPath, "error", reloadErr)
				}
			}
		}
	}()
	return runtime.Run(ctx)
}
