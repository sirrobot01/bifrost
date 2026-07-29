//go:build darwin || freebsd || openbsd

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

type bsdHost struct {
	ifconfigStyle serviceaddr.IfconfigStyle
}

func (bsdHost) Observer(name string) (netwatch.Observer, error) {
	return netwatch.NewPolling(name)
}

func (h bsdHost) AddressBackend(name string) (serviceaddr.AddressBackend, error) {
	return serviceaddr.NewIfconfigBackend(name, h.ifconfigStyle)
}

func (bsdHost) Firewall() (hostfw.Manager, error) {
	return nil, hostfw.ErrUnsupported
}

func (bsdHost) FirewallAuditor() diagnose.FirewallAuditor {
	return diagnose.DefaultFirewallAuditor()
}

func (bsdHost) DockerClient(socket string) (*dockerwatch.Client, error) {
	return dockerwatch.NewClient(dockerwatch.ClientConfig{Socket: socket})
}

func (bsdHost) Privileged() bool        { return os.Geteuid() == 0 }
func (bsdHost) ReloadSignal() os.Signal { return syscall.SIGHUP }

func (bsdHost) DefaultIPv6Gateway(interfaceName string) (netip.Addr, error) {
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
	gateway, _, _ = strings.Cut(gateway, "%")
	address, err := netip.ParseAddr(gateway)
	if err != nil || !address.Is6() || address.IsUnspecified() {
		return netip.Addr{}, errors.New("no IPv6 default gateway, so there is no router to ask")
	}
	if address.IsLinkLocalUnicast() {
		address = address.WithZone(device)
	}
	return address, nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
