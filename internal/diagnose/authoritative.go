package diagnose

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// lookupAuthoritativeAAAA asks the zone's own nameservers for name, bypassing
// every recursive cache. It reports the AAAA records and which nameserver
// answered, so a finding can distinguish "not published" from "the local
// resolver has not caught up".
func lookupAuthoritativeAAAA(ctx context.Context, name string) ([]netip.Addr, string, error) {
	nameservers, err := findAuthoritativeNS(ctx, name)
	if err != nil {
		return nil, "", err
	}
	message := new(dns.Msg)
	message.SetQuestion(dns.Fqdn(name), dns.TypeAAAA)
	message.RecursionDesired = false
	client := &dns.Client{Timeout: 3 * time.Second}
	var lastError error
	for _, host := range nameservers {
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			lastError = err
			continue
		}
		for _, address := range addresses {
			response, _, err := client.ExchangeContext(ctx, message, net.JoinHostPort(address.Unmap().String(), "53"))
			if err != nil {
				lastError = err
				continue
			}
			if response.Rcode != dns.RcodeSuccess && response.Rcode != dns.RcodeNameError {
				lastError = fmt.Errorf("%s answered %s", host, dns.RcodeToString[response.Rcode])
				continue
			}
			var resolved []netip.Addr
			for _, answer := range response.Answer {
				record, ok := answer.(*dns.AAAA)
				if !ok {
					continue
				}
				if parsed, ok := netip.AddrFromSlice(record.AAAA); ok {
					resolved = append(resolved, parsed.Unmap())
				}
			}
			return resolved, host, nil
		}
	}
	if lastError == nil {
		lastError = errors.New("no authoritative nameserver answered")
	}
	return nil, "", lastError
}

// findAuthoritativeNS walks from name toward the root until a label has NS
// records. The exact name usually has none (or a cached negative answer), so
// the parent zone provides them.
func findAuthoritativeNS(ctx context.Context, name string) ([]string, error) {
	candidate := strings.TrimSuffix(strings.ToLower(name), ".")
	for candidate != "" && strings.Contains(candidate, ".") {
		records, err := net.DefaultResolver.LookupNS(ctx, candidate)
		if err == nil && len(records) > 0 {
			hosts := make([]string, 0, len(records))
			for _, record := range records {
				hosts = append(hosts, strings.TrimSuffix(record.Host, "."))
			}
			return hosts, nil
		}
		_, candidate, _ = strings.Cut(candidate, ".")
	}
	return nil, fmt.Errorf("no NS records found for %s or any parent", name)
}
