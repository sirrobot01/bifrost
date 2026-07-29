//go:build linux

package platform

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
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
func (host) Name() string { return "Linux" }
func (host) Capabilities() Capabilities {
	return Capabilities{ManagedFirewall: true, FirewallAudit: true, PCP: true, Docker: true}
}
func (host) Observer(name string) (netwatch.Observer, error) { return netwatch.NewLinux(name) }
func (host) AddressBackend(name string) (serviceaddr.AddressBackend, error) {
	return serviceaddr.NewNetlinkBackend(name)
}
func (host) Firewall() (hostfw.Manager, error)         { return hostfw.New() }
func (host) FirewallAuditor() diagnose.FirewallAuditor { return diagnose.DefaultFirewallAuditor() }
func (host) DockerClient(socket string) (*dockerwatch.Client, error) {
	return dockerwatch.NewClient(dockerwatch.ClientConfig{Socket: socket})
}
func (host) Privileged() bool         { return os.Geteuid() == 0 }
func (host) ReloadSignal() os.Signal  { return syscall.SIGHUP }
func (host) Services() ServiceManager { return serviceManager{} }

func (host) DefaultIPv6Gateway(interfaceName string) (netip.Addr, error) {
	file, err := os.Open("/proc/net/ipv6_route")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("read IPv6 routes: %w", err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || (interfaceName != "" && fields[9] != interfaceName) {
			continue
		}
		length, parseErr := strconv.ParseInt(fields[1], 16, 32)
		if parseErr != nil || length != 0 || strings.Trim(fields[0], "0") != "" {
			continue
		}
		gateway, ok := parseHexAddress(fields[4])
		if !ok || gateway.IsUnspecified() {
			continue
		}
		if gateway.IsLinkLocalUnicast() {
			gateway = gateway.WithZone(fields[9])
		}
		return gateway, nil
	}
	return netip.Addr{}, errors.New("no IPv6 default route, so there is no router to ask")
}

func parseHexAddress(value string) (netip.Addr, bool) {
	if len(value) != 32 {
		return netip.Addr{}, false
	}
	var raw [16]byte
	for index := range raw {
		octet, err := strconv.ParseUint(value[index*2:index*2+2], 16, 8)
		if err != nil {
			return netip.Addr{}, false
		}
		raw[index] = byte(octet)
	}
	return netip.AddrFrom16(raw), true
}

type serviceManager struct{}

var _ Host = host{}
var _ ServiceManager = serviceManager{}

func (serviceManager) Active(service string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", service).Run() == nil
}
func (serviceManager) Start(ctx context.Context, service string) error {
	return run(ctx, "systemctl", "enable", "--now", service)
}
func (serviceManager) Restart(ctx context.Context, service string) error {
	return run(ctx, "systemctl", "restart", service)
}
func (serviceManager) Reload(ctx context.Context, service string) error {
	return run(ctx, "systemctl", "reload", service)
}
func (serviceManager) StartAdvice(service string) string {
	return "sudo systemctl enable --now " + service
}
func (serviceManager) RestartAdvice(services []string) string {
	return "sudo systemctl restart " + strings.Join(services, " ")
}
func run(ctx context.Context, name string, arguments ...string) error {
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
