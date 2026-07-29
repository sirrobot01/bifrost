//go:build darwin

package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/sirrobot01/bifrost/internal/diagnose"
	"github.com/sirrobot01/bifrost/internal/dockerwatch"
	"github.com/sirrobot01/bifrost/internal/hostfw"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type host struct{}

func New() Host           { return host{} }
func (host) Name() string { return "macOS" }
func (host) Capabilities() Capabilities {
	return Capabilities{PCP: true, Docker: true}
}
func (host) Observer(name string) (netwatch.Observer, error) { return netwatch.NewPolling(name) }
func (host) AddressBackend(name string) (serviceaddr.AddressBackend, error) {
	return serviceaddr.NewIfconfigBackend(name, serviceaddr.IfconfigDarwin)
}
func (host) Firewall() (hostfw.Manager, error)         { return nil, hostfw.ErrUnsupported }
func (host) FirewallAuditor() diagnose.FirewallAuditor { return diagnose.DefaultFirewallAuditor() }
func (host) DockerClient(socket string) (*dockerwatch.Client, error) {
	return dockerwatch.NewClient(dockerwatch.ClientConfig{Socket: socket})
}
func (host) Privileged() bool         { return os.Geteuid() == 0 }
func (host) ReloadSignal() os.Signal  { return syscall.SIGHUP }
func (host) Services() ServiceManager { return serviceManager{} }

func (host) DefaultIPv6Gateway(interfaceName string) (netip.Addr, error) {
	output, err := exec.Command("/sbin/route", "-n", "get", "-inet6", "default").CombinedOutput()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("inspect IPv6 default route: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var gateway, device string
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "gateway":
			gateway = fields[1]
		case "interface":
			device = fields[1]
		}
	}
	if interfaceName != "" && device != "" && interfaceName != device {
		return netip.Addr{}, fmt.Errorf("IPv6 default route uses %s, not %s", device, interfaceName)
	}
	address, parseErr := netip.ParseAddr(strings.Split(gateway, "%")[0])
	if parseErr != nil || !address.Is6() || address.IsUnspecified() {
		return netip.Addr{}, errors.New("no IPv6 default gateway, so there is no router to ask")
	}
	if address.IsLinkLocalUnicast() {
		address = address.WithZone(device)
	}
	return address, nil
}

type serviceManager struct{}

var _ Host = host{}
var _ ServiceManager = serviceManager{}

func label(service string) string { return "dev.biodun." + service }
func plist(service string) string { return "/Library/LaunchDaemons/" + label(service) + ".plist" }
func (serviceManager) Active(service string) bool {
	return exec.Command("launchctl", "print", "system/"+label(service)).Run() == nil
}
func (serviceManager) Start(ctx context.Context, service string) error {
	return run(ctx, "launchctl", "bootstrap", "system", plist(service))
}
func (serviceManager) Restart(ctx context.Context, service string) error {
	return run(ctx, "launchctl", "kickstart", "-k", "system/"+label(service))
}
func (serviceManager) Reload(ctx context.Context, service string) error {
	return run(ctx, "launchctl", "kill", "HUP", "system/"+label(service))
}
func (serviceManager) StartAdvice(service string) string {
	return "sudo launchctl bootstrap system " + plist(service)
}
func (serviceManager) RestartAdvice(services []string) string {
	commands := make([]string, 0, len(services))
	for _, service := range services {
		commands = append(commands, "sudo launchctl kickstart -k system/"+label(service))
	}
	return strings.Join(commands, "; ")
}
func run(ctx context.Context, name string, arguments ...string) error {
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
