package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sirrobot01/bifrost/internal/platform"
	"github.com/sirrobot01/bifrost/internal/selfupdate"
)

// managedUnits are the services that run this binary. An upgrade restarts only
// the ones already running, so an edge host is never handed a home daemon.
var managedUnits = []string{"bifrost", "bifrost-edge"}

func (r Runner) runUpgrade(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	checkOnly := flags.Bool("check", false, "report the latest release without installing it")
	restart := flags.Bool("restart", false, "restart the running bifrost services afterwards")
	force := flags.Bool("force", false, "reinstall even when the versions match")
	baseURL := flags.String("base-url", selfupdate.DefaultBaseURL, "release asset base URL")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("upgrade accepts flags only")
	}

	client := &selfupdate.Client{BaseURL: *baseURL}
	release, err := client.Latest(ctx)
	if err != nil {
		return err
	}

	current := strings.TrimPrefix(r.Version, "v")
	latest := strings.TrimPrefix(release.Version, "v")
	if _, err := fmt.Fprintf(r.Stdout, "installed %s, latest %s\n", current, latest); err != nil {
		return err
	}
	if *checkOnly {
		if current != latest {
			_, err := fmt.Fprintf(r.Stdout, "\nUpgrade with: %s\n", elevatedCommand("bifrost upgrade"))
			return err
		}
		return nil
	}
	if current == latest && !*force {
		_, err := fmt.Fprintln(r.Stdout, "\nAlready on the latest release. Use --force to reinstall it.")
		return err
	}

	path, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err := client.Binary(ctx, release)
	if err != nil {
		return err
	}
	if err := selfupdate.Replace(path, binary); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("replace %s: %w (re-run with sudo)", path, err)
		}
		return err
	}
	if _, err := fmt.Fprintf(r.Stdout, "\nReplaced %s with %s.\n", path, latest); err != nil {
		return err
	}

	services := platform.New().Services()
	running := runningUnits(services)
	if len(running) == 0 {
		_, err := fmt.Fprintln(r.Stdout, "No bifrost service is running, so nothing was restarted.")
		return err
	}
	if !*restart {
		_, err := fmt.Fprintf(r.Stdout, "The new binary starts serving after: %s\n", services.RestartAdvice(running))
		return err
	}
	for _, unit := range running {
		if err := services.Restart(ctx, unit); err != nil {
			return fmt.Errorf("restart %s: %w", unit, err)
		}
		if _, err := fmt.Fprintf(r.Stdout, "Restarted %s.\n", unit); err != nil {
			return err
		}
	}
	return nil
}

// runningUnits reports which Bifrost units systemd currently has active. A host
// without systemd, or with neither unit running, yields none.
func runningUnits(services platform.ServiceManager) []string {
	var active []string
	for _, unit := range managedUnits {
		if services.Active(unit) {
			active = append(active, unit)
		}
	}
	return active
}
