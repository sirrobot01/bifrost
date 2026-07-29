//go:build darwin

package platform

import (
	"context"
	"os/exec"
	"strings"

	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type host struct{ bsdHost }

func New() Host                         { return host{bsdHost{ifconfigStyle: serviceaddr.IfconfigDarwin}} }
func (host) Name() string               { return "macOS" }
func (host) Capabilities() Capabilities { return Capabilities{PCP: true, Docker: true} }
func (host) Services() ServiceManager   { return serviceManager{} }

type serviceManager struct{}

func launchdLabel(service string) string { return "dev.biodun." + service }

func (serviceManager) Active(service string) bool {
	return exec.Command("launchctl", "print", "system/"+launchdLabel(service)).Run() == nil
}

func (serviceManager) Start(ctx context.Context, service string) error {
	path := "/Library/LaunchDaemons/" + launchdLabel(service) + ".plist"
	return runCommand(ctx, "launchctl", "bootstrap", "system", path)
}

func (serviceManager) Restart(ctx context.Context, service string) error {
	return runCommand(ctx, "launchctl", "kickstart", "-k", "system/"+launchdLabel(service))
}

func (serviceManager) Reload(ctx context.Context, service string) error {
	return runCommand(ctx, "launchctl", "kill", "HUP", "system/"+launchdLabel(service))
}

func (serviceManager) StartAdvice(service string) string {
	return "sudo launchctl bootstrap system /Library/LaunchDaemons/" + launchdLabel(service) + ".plist"
}

func (serviceManager) RestartAdvice(services []string) string {
	commands := make([]string, 0, len(services))
	for _, service := range services {
		commands = append(commands, "sudo launchctl kickstart -k system/"+launchdLabel(service))
	}
	return strings.Join(commands, "; ")
}

var _ Host = host{}
var _ ServiceManager = serviceManager{}
