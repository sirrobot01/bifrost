package edge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/sync/singleflight"
)

var unsafeDestinationPrefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

type Resolver interface {
	Lookup(context.Context, string) ([]netip.Addr, time.Duration, error)
}

type DNSCache struct {
	resolver    Resolver
	negativeTTL time.Duration
	stale       time.Duration
	now         func() time.Time
	mu          sync.Mutex
	entries     map[string]cacheEntry
	lookups     singleflight.Group
}

type cacheEntry struct {
	addresses  []netip.Addr
	expires    time.Time
	staleUntil time.Time
	err        error
}

func NewDNSCache(resolver Resolver, negativeTTL, stale time.Duration) (*DNSCache, error) {
	if resolver == nil || negativeTTL <= 0 || stale < 0 {
		return nil, errors.New("dns resolver and valid cache durations are required")
	}
	return &DNSCache{resolver: resolver, negativeTTL: negativeTTL, stale: stale, now: time.Now, entries: make(map[string]cacheEntry)}, nil
}

func (c *DNSCache) Lookup(ctx context.Context, name string) ([]netip.Addr, error) {
	now := c.now()
	c.mu.Lock()
	entry, exists := c.entries[name]
	if exists && now.Before(entry.expires) {
		c.mu.Unlock()
		return append([]netip.Addr(nil), entry.addresses...), entry.err
	}
	c.mu.Unlock()

	result := c.lookups.DoChan(name, func() (any, error) {
		addresses, ttl, err := c.resolver.Lookup(ctx, name)
		return lookupResult{addresses: addresses, ttl: ttl}, err
	})
	var addresses []netip.Addr
	var ttl time.Duration
	var err error
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resolved := <-result:
		err = resolved.Err
		if err == nil {
			lookup := resolved.Val.(lookupResult)
			addresses = lookup.addresses
			ttl = lookup.ttl
		}
	}
	if err == nil {
		addresses = validDestinations(addresses)
		if len(addresses) == 0 {
			err = errors.New("dns response contains no acceptable global IPv6 destination")
		}
	}
	if err != nil {
		if exists && len(entry.addresses) > 0 && now.Before(entry.staleUntil) {
			return append([]netip.Addr(nil), entry.addresses...), nil
		}
		c.mu.Lock()
		c.entries[name] = cacheEntry{expires: now.Add(c.negativeTTL), staleUntil: now.Add(c.negativeTTL), err: err}
		c.mu.Unlock()
		return nil, err
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	entry = cacheEntry{addresses: append([]netip.Addr(nil), addresses...), expires: now.Add(ttl), staleUntil: now.Add(ttl + c.stale)}
	c.mu.Lock()
	c.entries[name] = entry
	c.mu.Unlock()
	return addresses, nil
}

type systemResolver struct {
	servers []string
}

type lookupResult struct {
	addresses []netip.Addr
	ttl       time.Duration
}

func NewSystemResolver() (Resolver, error) {
	config, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil, fmt.Errorf("read resolver config: %w", err)
	}
	servers := make([]string, 0, len(config.Servers))
	for _, server := range config.Servers {
		servers = append(servers, net.JoinHostPort(server, config.Port))
	}
	if len(servers) == 0 {
		return nil, errors.New("resolver config contains no nameserver")
	}
	return &systemResolver{servers: servers}, nil
}

func (r *systemResolver) Lookup(ctx context.Context, name string) ([]netip.Addr, time.Duration, error) {
	message := new(dns.Msg)
	message.SetQuestion(dns.Fqdn(name), dns.TypeAAAA)
	message.RecursionDesired = true
	var lastError error
	for _, server := range r.servers {
		client := &dns.Client{Timeout: 5 * time.Second}
		response, _, err := client.ExchangeContext(ctx, message, server)
		if err != nil {
			lastError = err
			continue
		}
		if response.Truncated {
			client.Net = "tcp"
			response, _, err = client.ExchangeContext(ctx, message, server)
		}
		if err != nil {
			lastError = err
			continue
		}
		if response.Rcode != dns.RcodeSuccess {
			lastError = fmt.Errorf("dns response code %s", dns.RcodeToString[response.Rcode])
			continue
		}
		var addresses []netip.Addr
		var ttl uint32
		for _, answer := range response.Answer {
			record, ok := answer.(*dns.AAAA)
			if !ok {
				continue
			}
			address, ok := netip.AddrFromSlice(record.AAAA)
			if !ok {
				continue
			}
			addresses = append(addresses, address)
			if ttl == 0 || record.Hdr.Ttl < ttl {
				ttl = record.Hdr.Ttl
			}
		}
		if len(addresses) == 0 {
			lastError = errors.New("dns response has no AAAA record")
			continue
		}
		return addresses, time.Duration(ttl) * time.Second, nil
	}
	return nil, 0, lastError
}

func validDestinations(addresses []netip.Addr) []netip.Addr {
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if !address.Is6() || address.Is4In6() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || slices.ContainsFunc(unsafeDestinationPrefixes, func(prefix netip.Prefix) bool { return prefix.Contains(address) }) {
			continue
		}
		result = append(result, address)
	}
	slices.SortFunc(result, netip.Addr.Compare)
	return slices.Compact(result)
}
