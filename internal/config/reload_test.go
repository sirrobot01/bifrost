package config

import (
	"strings"
	"testing"
	"time"
)

func reloadableBase() Config {
	base := Config{
		Version:    CurrentVersion,
		Interface:  "eth0",
		OwnerID:    "home-1",
		SecretFile: "/etc/bifrost/address-secret",
		DNS:        DNS{Provider: "desec", TTL: Duration(60 * time.Second), DESEC: DESEC{Zone: "example.com", TokenFile: "/etc/bifrost/desec-token"}},
		StaticServices: []StaticService{
			{Name: "media", Backend: "127.0.0.1:8096", Listen: 443, DNSName: "media.example.com"},
		},
	}
	base.ApplyDefaults()
	return base
}

// Adding a service is the reason reload exists, so it must not require a
// restart.
func TestReloadableAcceptsServiceChanges(t *testing.T) {
	t.Parallel()

	current := reloadableBase()
	next := reloadableBase()
	next.StaticServices = append(next.StaticServices, StaticService{
		Name: "photos", Backend: "127.0.0.1:2283", Listen: 443, DNSName: "photos.example.com",
	})
	next.ApplyDefaults()
	if err := current.Reloadable(next); err != nil {
		t.Fatalf("adding a service was rejected: %v", err)
	}
}

func TestReloadableAcceptsTheSettingsAppliedPerReconcile(t *testing.T) {
	t.Parallel()

	current := reloadableBase()
	next := reloadableBase()
	next.DNS.TTL = Duration(300 * time.Second)
	next.SettleWindow = Duration(30 * time.Second)
	next.DrainGrace = Duration(5 * time.Minute)
	next.Verify.Interval = Duration(15 * time.Minute)
	next.Firewall.AllowPorts = []uint16{22, 2222}
	next.Firewall.TrustedInterfaces = []string{"tailscale0"}
	if err := current.Reloadable(next); err != nil {
		t.Fatalf("a reloadable change was rejected: %v", err)
	}
}

// Anything consumed while building the runtime has to be named, not silently
// half-applied.
func TestReloadableNamesEverySettingThatNeedsARestart(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Config){
		"interface":       func(c *Config) { c.Interface = "eth1" },
		"owner_id":        func(c *Config) { c.OwnerID = "home-2" },
		"secret_file":     func(c *Config) { c.SecretFile = "/etc/bifrost/other" },
		"dns.provider":    func(c *Config) { c.DNS.Provider = "dynv6" },
		"dns credentials": func(c *Config) { c.DNS.DESEC.Zone = "other.example.com" },
		"firewall.mode":   func(c *Config) { c.Firewall.Mode = "managed" },
		"firewall.pcp":    func(c *Config) { c.Firewall.PCP = true },
		"metrics.listen":  func(c *Config) { c.Metrics.Listen = "127.0.0.1:9099" },
		"docker":          func(c *Config) { c.Docker.Enabled = true },
		"acme":            func(c *Config) { c.ACME.Email = "you@example.com" },
		"probe.endpoint":  func(c *Config) { c.Probe.Endpoint = "https://probe.example.com/" },
		"notify":          func(c *Config) { c.Notify.Webhook = "https://ntfy.sh/topic" },
		"edge":            func(c *Config) { c.Edge.Enabled = true },
		"verify.enabled":  func(c *Config) { off := false; c.Verify.Enabled = &off },
	}
	for field, mutate := range tests {
		current := reloadableBase()
		next := reloadableBase()
		mutate(&next)
		err := current.Reloadable(next)
		if err == nil {
			t.Errorf("changing %s was accepted as reloadable", field)
			continue
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("changing %s produced %q, which does not name the field", field, err)
		}
	}
}

// A reload that changes several fixed settings has to report all of them, so
// one restart clears the lot.
func TestReloadableReportsEveryOffendingSettingAtOnce(t *testing.T) {
	t.Parallel()

	current := reloadableBase()
	next := reloadableBase()
	next.Interface = "eth1"
	next.OwnerID = "home-2"
	err := current.Reloadable(next)
	if err == nil {
		t.Fatal("two fixed changes were accepted")
	}
	for _, field := range []string{"interface", "owner_id"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not name %s", err, field)
		}
	}
}
