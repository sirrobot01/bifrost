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

// Platform is every host operation used by the portable Bifrost core. Each
// supported operating system supplies one implementation; home and CLI code
// never select an operating system themselves.
type Platform interface {
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

// ServiceManager owns the native service-manager vocabulary. Callers use the
// stable Bifrost service names above; implementations translate them to
// systemd units, launchd labels, rc.d scripts, or Windows services.
type ServiceManager interface {
	Active(service string) bool
	Start(context.Context, string) error
	Restart(context.Context, string) error
	Reload(context.Context, string) error
	StartAdvice(string) string
	RestartAdvice([]string) string
}
