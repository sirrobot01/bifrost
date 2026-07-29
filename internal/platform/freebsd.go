//go:build freebsd

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

func New() Host                         { return host{bsdHost{ifconfigStyle: serviceaddr.IfconfigFreeBSD}} }
func (host) Name() string               { return "FreeBSD" }
func (host) Capabilities() Capabilities { return Capabilities{PCP: true} }
func (host) Services() ServiceManager   { return serviceManager{} }
func (host) DockerClient(string) (*dockerwatch.Client, error) {
	return nil, errors.New("Docker discovery is not supported on FreeBSD")
}

type serviceManager struct{}

func (serviceManager) Active(service string) bool {
	return exec.Command("service", service, "status").Run() == nil
}

func (serviceManager) Start(ctx context.Context, service string) error {
	rcvar := strings.ReplaceAll(service, "-", "_") + "_enable=YES"
	if err := runCommand(ctx, "sysrc", rcvar); err != nil {
		return err
	}
	return runCommand(ctx, "service", service, "start")
}

func (serviceManager) Restart(ctx context.Context, service string) error {
	return runCommand(ctx, "service", service, "restart")
}

func (serviceManager) Reload(ctx context.Context, service string) error {
	return runCommand(ctx, "service", service, "reload")
}

func (serviceManager) StartAdvice(service string) string {
	rcvar := strings.ReplaceAll(service, "-", "_") + "_enable=YES"
	return "sudo sysrc " + rcvar + " && sudo service " + service + " start"
}

func (serviceManager) RestartAdvice(services []string) string {
	commands := make([]string, 0, len(services))
	for _, service := range services {
		commands = append(commands, "sudo service "+service+" restart")
	}
	return strings.Join(commands, "; ")
}

var _ Host = host{}
var _ ServiceManager = serviceManager{}
