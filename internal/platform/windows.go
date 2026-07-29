//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/sirrobot01/bifrost/internal/diagnose"
	"github.com/sirrobot01/bifrost/internal/dockerwatch"
	"github.com/sirrobot01/bifrost/internal/hostfw"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type host struct{}

func New() Host                         { return host{} }
func (host) Name() string               { return "Windows" }
func (host) Capabilities() Capabilities { return Capabilities{PCP: true, Docker: true} }
func (host) Observer(name string) (netwatch.Observer, error) {
	return netwatch.NewWindows(name)
}
func (host) AddressBackend(name string) (serviceaddr.AddressBackend, error) {
	return serviceaddr.NewWindowsBackend(name)
}
func (host) Firewall() (hostfw.Manager, error) { return nil, hostfw.ErrUnsupported }
func (host) FirewallAuditor() diagnose.FirewallAuditor {
	return diagnose.DefaultFirewallAuditor()
}
func (host) DockerClient(socket string) (*dockerwatch.Client, error) {
	socket = windowsPipePath(socket)
	return dockerwatch.NewClient(dockerwatch.ClientConfig{
		Socket: socket,
		DialSocket: func(ctx context.Context, socket string) (net.Conn, error) {
			return winio.DialPipeContext(ctx, socket)
		},
	})
}
func (host) Privileged() bool         { return windows.GetCurrentProcessToken().IsElevated() }
func (host) ReloadSignal() os.Signal  { return nil }
func (host) Services() ServiceManager { return serviceManager{} }

func (host) DefaultIPv6Gateway(interfaceName string) (netip.Addr, error) {
	device, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("find network interface %q: %w", interfaceName, err)
	}
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET6, &table); err != nil {
		return netip.Addr{}, fmt.Errorf("inspect IPv6 routes: %w", err)
	}
	if table == nil {
		return netip.Addr{}, errors.New("no IPv6 default route, so there is no router to ask")
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	metric := uint32(math.MaxUint32)
	var gateway netip.Addr
	for _, route := range table.Rows() {
		if route.InterfaceIndex != uint32(device.Index) || route.DestinationPrefix.PrefixLength != 0 || route.DestinationPrefix.Prefix.Family != windows.AF_INET6 || route.NextHop.Family != windows.AF_INET6 {
			continue
		}
		destination := (*windows.RawSockaddrInet6)(unsafe.Pointer(&route.DestinationPrefix.Prefix))
		nextHop := (*windows.RawSockaddrInet6)(unsafe.Pointer(&route.NextHop))
		address := netip.AddrFrom16(nextHop.Addr)
		if !netip.AddrFrom16(destination.Addr).IsUnspecified() || address.IsUnspecified() || route.Metric >= metric {
			continue
		}
		gateway = address
		metric = route.Metric
	}
	if !gateway.IsValid() {
		return netip.Addr{}, errors.New("no IPv6 default route, so there is no router to ask")
	}
	if gateway.IsLinkLocalUnicast() {
		gateway = gateway.WithZone(interfaceName)
	}
	return gateway, nil
}

func windowsPipePath(socket string) string {
	if strings.HasPrefix(socket, "npipe://") {
		name := strings.TrimPrefix(socket, "npipe://")
		name = strings.TrimLeft(name, "/\\")
		name = strings.TrimPrefix(name, ".")
		name = strings.TrimLeft(name, "/\\")
		name = strings.ReplaceAll(name, "/", `\`)
		if strings.HasPrefix(strings.ToLower(name), `pipe\`) {
			name = name[len(`pipe\`):]
		}
		return `\\.\pipe\` + name
	}
	return socket
}

type serviceManager struct{}

func (serviceManager) Active(service string) bool {
	manager, unit, err := openService(service)
	if err != nil {
		return false
	}
	defer manager.Disconnect()
	defer unit.Close()
	status, err := unit.Query()
	return err == nil && status.State == svc.Running
}

func (serviceManager) Start(ctx context.Context, service string) error {
	manager, unit, err := openService(service)
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer unit.Close()
	configuration, err := unit.Config()
	if err != nil {
		return err
	}
	if configuration.StartType != mgr.StartAutomatic {
		configuration.StartType = mgr.StartAutomatic
		if err := unit.UpdateConfig(configuration); err != nil {
			return err
		}
	}
	status, err := unit.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		if err := unit.Start(); err != nil {
			return err
		}
	}
	return waitForService(ctx, unit, svc.Running)
}

func (serviceManager) Restart(ctx context.Context, service string) error {
	manager, unit, err := openService(service)
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer unit.Close()
	status, err := unit.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		if _, err := unit.Control(svc.Stop); err != nil {
			return err
		}
		if err := waitForService(ctx, unit, svc.Stopped); err != nil {
			return err
		}
	}
	if err := unit.Start(); err != nil {
		return err
	}
	return waitForService(ctx, unit, svc.Running)
}

func (m serviceManager) Reload(ctx context.Context, service string) error {
	return m.Restart(ctx, service)
}

func (serviceManager) StartAdvice(service string) string {
	return "Set-Service -Name " + service + " -StartupType Automatic; Start-Service -Name " + service
}

func (serviceManager) RestartAdvice(services []string) string {
	return "Restart-Service -Name " + strings.Join(services, ",")
}

func openService(name string) (*mgr.Mgr, *mgr.Service, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	service, err := manager.OpenService(name)
	if err != nil {
		manager.Disconnect()
		return nil, nil, fmt.Errorf("open Windows service %s: %w", name, err)
	}
	return manager, service, nil
}

func waitForService(ctx context.Context, service *mgr.Service, state svc.State) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Minute)
	defer timeout.Stop()
	for {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == state {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("Windows service %s did not reach state %d", service.Name, state)
		case <-ticker.C:
		}
	}
}

var _ Host = host{}
var _ ServiceManager = serviceManager{}
