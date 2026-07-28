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
	client  pcpRequester
	logger  *slog.Logger
	secret  []byte
	now     func() time.Time
	mu      sync.Mutex
	granted map[netip.AddrPort]pinholeLease
	// unsupported latches once the router has shown it does not speak PCP, so
	// a reconcile loop does not retry a conversation that will not happen.
	unsupported bool
}

type pcpRequester interface {
	Request(context.Context, pcp.Mapping) (time.Duration, error)
	Release(context.Context, pcp.Mapping) error
}

type pinholeLease struct {
	granted time.Duration
	renewAt time.Time
}

func newPinholeManager(interfaceName string, secret []byte, logger *slog.Logger) (*pinholeManager, error) {
	gateway, err := defaultIPv6Gateway(interfaceName)
	if err != nil {
		return nil, err
	}
	return &pinholeManager{
		client:  pcp.NewClient(gateway),
		logger:  logger,
		secret:  secret,
		now:     time.Now,
		granted: make(map[netip.AddrPort]pinholeLease),
	}, nil
}

// Ensure requests or renews one endpoint's pinhole. Failures never stop
// publication: a service reachable only because the operator opened the router
// by hand must keep working.
func (m *pinholeManager) Ensure(ctx context.Context, endpoint hostfw.Endpoint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unsupported {
		return
	}
	socket := netip.AddrPortFrom(endpoint.Address, endpoint.Port)
	if lease, known := m.granted[socket]; known && m.now().Before(lease.renewAt) {
		return
	}
	granted, err := m.client.Request(ctx, pcp.Mapping{
		Internal: endpoint.Address,
		Port:     endpoint.Port,
		Lifetime: pinholeLifetime,
		Nonce:    m.nonce(socket),
	})
	if err != nil {
		lease, renewing := m.granted[socket]
		if errors.Is(err, pcp.ErrUnsupported) && !renewing {
			m.unsupported = true
			m.logger.Info("router does not support PCP, so inbound rules stay manual", "error", err)
			return
		}
		if renewing {
			// A single lost renewal response must neither disable PCP forever nor
			// retry every sweep tick. Keep the lease and retry with a bounded
			// interval while its mapping may still exist.
			retryAfter := lease.granted / 8
			if retryAfter < 5*time.Second {
				retryAfter = 5 * time.Second
			}
			if retryAfter > time.Minute {
				retryAfter = time.Minute
			}
			lease.renewAt = m.now().Add(retryAfter)
			m.granted[socket] = lease
		}
		m.logger.Warn("router refused a pinhole request", "socket", socket.String(), "error", err)
		return
	}
	if granted <= 0 {
		m.logger.Warn("router granted a pinhole with no usable lifetime", "socket", socket.String())
		delete(m.granted, socket)
		return
	}
	if _, known := m.granted[socket]; !known {
		m.logger.Info("router granted an inbound pinhole", "socket", socket.String(), "lifetime", granted.String())
	}
	m.granted[socket] = pinholeLease{granted: granted, renewAt: m.now().Add(granted / 2)}
}

// Renew refreshes every mapping whose half-lifetime timer has elapsed.
func (m *pinholeManager) Renew(ctx context.Context) {
	m.mu.Lock()
	now := m.now()
	due := make([]hostfw.Endpoint, 0, len(m.granted))
	for socket, lease := range m.granted {
		if !now.Before(lease.renewAt) {
			due = append(due, hostfw.Endpoint{Address: socket.Addr(), Port: socket.Port()})
		}
	}
	m.mu.Unlock()
	for _, endpoint := range due {
		m.Ensure(ctx, endpoint)
	}
}

// Remove drops one mapping while its source address is still assigned.
func (m *pinholeManager) Remove(ctx context.Context, endpoint hostfw.Endpoint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	socket := netip.AddrPortFrom(endpoint.Address, endpoint.Port)
	if _, known := m.granted[socket]; !known {
		return
	}
	mapping := pcp.Mapping{Internal: socket.Addr(), Port: socket.Port(), Nonce: m.nonce(socket)}
	if err := m.client.Release(ctx, mapping); err != nil {
		m.logger.Warn("router pinhole could not be released", "socket", socket.String(), "error", err)
	}
	delete(m.granted, socket)
}

// Release drops every mapping, so stopping Bifrost closes what it opened.
func (m *pinholeManager) Release(ctx context.Context) {
	m.mu.Lock()
	endpoints := make([]hostfw.Endpoint, 0, len(m.granted))
	for socket := range m.granted {
		endpoints = append(endpoints, hostfw.Endpoint{Address: socket.Addr(), Port: socket.Port()})
	}
	m.mu.Unlock()
	for _, endpoint := range endpoints {
		m.Remove(ctx, endpoint)
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
func defaultIPv6Gateway(interfaceName string) (netip.Addr, error) {
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
		if interfaceName != "" && fields[9] != interfaceName {
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
