//go:build openbsd

package platform

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/sirrobot01/bifrost/internal/dockerwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type host struct{ bsdHost }

func New() Host                         { return host{bsdHost{ifconfigStyle: serviceaddr.IfconfigOpenBSD}} }
func (host) Name() string               { return "OpenBSD" }
func (host) Capabilities() Capabilities { return Capabilities{PCP: true} }
func (host) Services() ServiceManager   { return serviceManager{} }
func (host) DockerClient(string) (*dockerwatch.Client, error) {
	return nil, errors.New("Docker discovery is not supported on OpenBSD")
}

type serviceManager struct{}

func rcService(service string) string { return strings.ReplaceAll(service, "-", "_") }

func (serviceManager) Active(service string) bool {
	return exec.Command("rcctl", "check", rcService(service)).Run() == nil
}

func (serviceManager) Start(ctx context.Context, service string) error {
	service = rcService(service)
	if err := runCommand(ctx, "rcctl", "enable", service); err != nil {
		return err
	}
	return runCommand(ctx, "rcctl", "start", service)
}

func (serviceManager) Restart(ctx context.Context, service string) error {
	return runCommand(ctx, "rcctl", "restart", rcService(service))
}

func (serviceManager) Reload(ctx context.Context, service string) error {
	return runCommand(ctx, "rcctl", "reload", rcService(service))
}

func (serviceManager) StartAdvice(service string) string {
	service = rcService(service)
	return "doas rcctl enable " + service + " && doas rcctl start " + service
}

func (serviceManager) RestartAdvice(services []string) string {
	commands := make([]string, 0, len(services))
	for _, service := range services {
		commands = append(commands, "doas rcctl restart "+rcService(service))
	}
	return strings.Join(commands, "; ")
}

var _ Host = host{}
var _ ServiceManager = serviceManager{}
