package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/home"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/platform"
)

func platformSnapshot(interfaceName string) (netwatch.Snapshot, error) {
	observer, err := platform.New().Observer(interfaceName)
	if err != nil {
		return netwatch.Snapshot{}, err
	}
	return observer.Snapshot()
}

func platformServe(ctx context.Context, configPath string, configFile config.Config, dryRun bool, logger *slog.Logger, output io.Writer) error {
	host := platform.New()
	runtime, err := home.NewRuntime(configFile, logger, host)
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

	if reloadSignal := host.ReloadSignal(); reloadSignal != nil {
		hangups := make(chan os.Signal, 1)
		signal.Notify(hangups, reloadSignal)
		defer signal.Stop(hangups)
		go watchReloads(ctx, hangups, configPath, runtime, logger)
	}
	return runtime.Run(ctx)
}

func watchReloads(ctx context.Context, reloads <-chan os.Signal, configPath string, runtime *home.Runtime, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-reloads:
			next, err := config.Load(configPath)
			if err != nil {
				logger.Error("reload rejected: the configuration did not load", "path", configPath, "error", err)
				continue
			}
			if err := runtime.Reload(next); err != nil {
				logger.Error("reload rejected", "path", configPath, "error", err)
			}
		}
	}
}
