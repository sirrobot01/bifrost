package platform

import (
	"context"
	"net/netip"
	"os"

	"github.com/sirrobot01/bifrost/internal/diagnose"
	"github.com/sirrobot01/bifrost/internal/dockerwatch"
	"github.com/sirrobot01/bifrost/internal/hostfw"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

const (
	HomeService = "bifrost"
	EdgeService = "bifrost-edge"
)

type Host interface {
	Name() string
	Capabilities() Capabilities
	Observer(interfaceName string) (netwatch.Observer, error)
	AddressBackend(interfaceName string) (serviceaddr.AddressBackend, error)
	Firewall() (hostfw.Manager, error)
	FirewallAuditor() diagnose.FirewallAuditor
	DefaultIPv6Gateway(interfaceName string) (netip.Addr, error)
	DockerClient(socket string) (*dockerwatch.Client, error)
	Privileged() bool
	ReloadSignal() os.Signal
	Services() ServiceManager
}

type Capabilities struct {
	ManagedFirewall bool
	FirewallAudit   bool
	PCP             bool
	Docker          bool
}

type ServiceManager interface {
	Active(service string) bool
	Start(context.Context, string) error
	Restart(context.Context, string) error
	Reload(context.Context, string) error
	StartAdvice(string) string
	RestartAdvice([]string) string
}
