//go:build linux

package cli

import (
	"context"
	"io"
	"log/slog"

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

func platformServe(ctx context.Context, configFile config.Config, dryRun bool, logger *slog.Logger, output io.Writer) error {
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
	return runtime.Run(ctx)
}
