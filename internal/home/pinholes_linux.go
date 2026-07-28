//go:build linux

package home

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirrobot01/bifrost/internal/hostfw"
	"github.com/sirrobot01/bifrost/internal/pcp"
)

// pinholeLifetime is what Bifrost asks the router to hold. Routers commonly
// grant less, so the manager renews at half of whatever it was given.
const pinholeLifetime = time.Hour

// pinholeManager asks the customer-edge router to permit inbound traffic to
// the published sockets.
//
// This is the only hop Bifrost cannot configure by other means, and leaving it
// manual is where deployments lose days. Most routers do not answer, so the
// manager is deliberately quiet about absence: it reports once that PCP is
// unavailable and stops asking, leaving the advisory guidance as the answer.
type pinholeManager struct {
	client  *pcp.Client
	logger  *slog.Logger
	secret  []byte
	mu      sync.Mutex
	granted map[netip.AddrPort]time.Duration
	// unsupported latches once the router has shown it does not speak PCP, so
	// a reconcile loop does not retry a conversation that will not happen.
	unsupported bool
}

func newPinholeManager(secret []byte, logger *slog.Logger) (*pinholeManager, error) {
	gateway, err := defaultIPv6Gateway()
	if err != nil {
		return nil, err
	}
	return &pinholeManager{client: pcp.NewClient(gateway), logger: logger, secret: secret, granted: make(map[netip.AddrPort]time.Duration)}, nil
}

// Apply requests a pinhole for every endpoint in the spec. Failures never stop
// publication: a service reachable only because the operator opened the router
// by hand must keep working.
func (m *pinholeManager) Apply(ctx context.Context, spec hostfw.Spec) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unsupported {
		return
	}
	for _, endpoint := range spec.Endpoints {
		socket := netip.AddrPortFrom(endpoint.Address, endpoint.Port)
		granted, err := m.client.Request(ctx, pcp.Mapping{
			Internal: endpoint.Address,
			Port:     endpoint.Port,
			Lifetime: pinholeLifetime,
			Nonce:    m.nonce(socket),
		})
		if err != nil {
			if errors.Is(err, pcp.ErrUnsupported) {
				m.unsupported = true
				m.logger.Info("router does not support PCP, so inbound rules stay manual", "error", err)
				return
			}
			m.logger.Warn("router refused a pinhole request", "socket", socket.String(), "error", err)
			continue
		}
		if _, known := m.granted[socket]; !known {
			m.logger.Info("router granted an inbound pinhole", "socket", socket.String(), "lifetime", granted.String())
		}
		m.granted[socket] = granted
	}
}

// Release drops every mapping, so stopping Bifrost closes what it opened.
func (m *pinholeManager) Release(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for socket := range m.granted {
		mapping := pcp.Mapping{Internal: socket.Addr(), Port: socket.Port(), Nonce: m.nonce(socket)}
		if err := m.client.Release(ctx, mapping); err != nil {
			m.logger.Warn("router pinhole could not be released", "socket", socket.String(), "error", err)
		}
		delete(m.granted, socket)
	}
}

// nonce derives the per-mapping identifier RFC 6887 uses to distinguish a
// refresh from a new mapping. Deriving it from the address secret keeps it
// stable across restarts, so a restarted daemon refreshes its own mappings
// instead of accumulating duplicates.
func (m *pinholeManager) nonce(socket netip.AddrPort) [12]byte {
	sum := sha256.Sum256(append([]byte("bifrost-pcp-nonce\x00"+socket.String()), m.secret...))
	var nonce [12]byte
	copy(nonce[:], sum[:12])
	return nonce
}

// defaultIPv6Gateway reads the next hop of the IPv6 default route, which is
// where a PCP server listens if there is one. The kernel exposes routes as
// hex text, and the next hop is normally link-local, so it carries the
// outgoing interface as its zone.
func defaultIPv6Gateway() (netip.Addr, error) {
	file, err := os.Open("/proc/net/ipv6_route")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("read IPv6 routes: %w", err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}
		destinationLength, err := strconv.ParseInt(fields[1], 16, 32)
		if err != nil || destinationLength != 0 || strings.Trim(fields[0], "0") != "" {
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

// parseHexAddress decodes the 32 hex digit form used throughout
// /proc/net/ipv6_route.
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

// pinholeRequester adapts a possibly-absent manager to the controller's
// interface. A typed nil would satisfy the interface and then panic, so the
// conversion is explicit.
func pinholeRequester(manager *pinholeManager) PinholeRequester {
	if manager == nil {
		return nil
	}
	return manager
}
