package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const CurrentVersion = 1

type Config struct {
	Version        int             `yaml:"version"`
	Interface      string          `yaml:"interface"`
	PrefixOverride string          `yaml:"prefix_override,omitempty"`
	OwnerID        string          `yaml:"owner_id"`
	SecretFile     string          `yaml:"secret_file"`
	SettleWindow   Duration        `yaml:"settle_window"`
	DrainGrace     Duration        `yaml:"drain_grace"`
	DNS            DNS             `yaml:"dns"`
	Firewall       Firewall        `yaml:"firewall"`
	Probe          Probe           `yaml:"probe"`
	Metrics        Metrics         `yaml:"metrics"`
	Docker         Docker          `yaml:"docker"`
	Edge           Edge            `yaml:"edge"`
	StaticServices []StaticService `yaml:"static_services"`
}

type DNS struct {
	Provider   string     `yaml:"provider"`
	TTL        Duration   `yaml:"ttl"`
	Cloudflare Cloudflare `yaml:"cloudflare,omitempty"`
	RFC2136    RFC2136    `yaml:"rfc2136,omitempty"`
	DESEC      DESEC      `yaml:"desec,omitempty"`
	Dynv6      Dynv6      `yaml:"dynv6,omitempty"`
}

type Cloudflare struct {
	ZoneID       string `yaml:"zone_id"`
	APITokenFile string `yaml:"api_token_file"`
}

type RFC2136 struct {
	Server    string `yaml:"server"`
	Zone      string `yaml:"zone"`
	KeyName   string `yaml:"key_name,omitempty"`
	KeyFile   string `yaml:"key_file,omitempty"`
	Algorithm string `yaml:"algorithm,omitempty"`
}

type DESEC struct {
	Zone      string `yaml:"zone"`
	TokenFile string `yaml:"token_file"`
}

type Dynv6 struct {
	Zone      string `yaml:"zone"`
	TokenFile string `yaml:"token_file"`
}

type Firewall struct {
	Mode string `yaml:"mode"`
}

type Probe struct {
	Endpoint string `yaml:"endpoint,omitempty"`
}

type Metrics struct {
	Listen string `yaml:"listen"`
}

type Docker struct {
	Enabled bool   `yaml:"enabled"`
	Socket  string `yaml:"socket"`
}

type Edge struct {
	Enabled       bool     `yaml:"enabled"`
	IPv4Address   string   `yaml:"ipv4_address,omitempty"`
	KeyFile       string   `yaml:"key_file,omitempty"`
	MaxClockSkew  Duration `yaml:"max_clock_skew"`
	HeaderTimeout Duration `yaml:"header_timeout"`
}

type StaticService struct {
	Name          string `yaml:"name"`
	Backend       string `yaml:"backend"`
	Listen        uint16 `yaml:"listen"`
	DNSName       string `yaml:"dns"`
	Mode          string `yaml:"mode"`
	PublicAddress string `yaml:"public_address,omitempty"`
	ProxyProtocol bool   `yaml:"proxy_protocol"`
	Edge          bool   `yaml:"edge"`
}

type Duration time.Duration

func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

func (c *Config) ApplyDefaults() {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if c.SettleWindow == 0 {
		c.SettleWindow = Duration(10 * time.Second)
	}
	if c.DrainGrace == 0 {
		c.DrainGrace = Duration(2 * time.Minute)
	}
	if c.DNS.TTL == 0 {
		c.DNS.TTL = Duration(60 * time.Second)
	}
	if c.Firewall.Mode == "" {
		c.Firewall.Mode = "advisory"
	}
	if c.Metrics.Listen == "" {
		c.Metrics.Listen = "127.0.0.1:9098"
	}
	if c.Docker.Socket == "" {
		c.Docker.Socket = "/var/run/docker.sock"
	}
	if c.Edge.MaxClockSkew == 0 {
		c.Edge.MaxClockSkew = Duration(30 * time.Second)
	}
	if c.Edge.HeaderTimeout == 0 {
		c.Edge.HeaderTimeout = Duration(250 * time.Millisecond)
	}
	for index := range c.StaticServices {
		if c.StaticServices[index].Mode == "" {
			c.StaticServices[index].Mode = "auto"
		}
		if c.StaticServices[index].Listen == 0 {
			if backend, err := netip.ParseAddrPort(c.StaticServices[index].Backend); err == nil {
				c.StaticServices[index].Listen = backend.Port()
			}
		}
	}
}

func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Interface == "" {
		return errors.New("interface is required")
	}
	if c.OwnerID == "" {
		return errors.New("owner_id is required")
	}
	for _, character := range c.OwnerID {
		if character != '-' && character != '_' && character != '.' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return errors.New("owner_id contains an unsupported character")
		}
	}
	if c.SecretFile == "" {
		return errors.New("secret_file is required")
	}
	if c.SettleWindow.Duration() <= 0 || c.DrainGrace.Duration() <= 0 {
		return errors.New("settle_window and drain_grace must be positive")
	}
	if c.DNS.TTL.Duration() < 60*time.Second || c.DNS.TTL.Duration() > 24*time.Hour {
		return errors.New("dns.ttl must be between 60s and 24h")
	}
	if err := c.validatePrefix(); err != nil {
		return err
	}
	if err := c.DNS.validate(); err != nil {
		return fmt.Errorf("dns: %w", err)
	}
	if c.Firewall.Mode != "advisory" {
		return errors.New("firewall.mode must be advisory in v1")
	}
	if err := c.Probe.validate(); err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	if err := c.Metrics.validate(); err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	if c.Docker.Enabled && c.Docker.Socket == "" {
		return errors.New("docker.socket is required when Docker discovery is enabled")
	}
	if err := c.Edge.validate(); err != nil {
		return fmt.Errorf("edge: %w", err)
	}
	for _, service := range c.StaticServices {
		if service.Edge && !c.Edge.Enabled {
			return fmt.Errorf("service %q enables edge publication but edge.enabled is false", service.Name)
		}
	}
	return validateServices(c.StaticServices)
}

func (c Config) validatePrefix() error {
	if c.PrefixOverride == "" {
		return nil
	}
	prefix, err := netip.ParsePrefix(c.PrefixOverride)
	if err != nil || !prefix.Addr().Is6() || prefix.Bits() != 64 {
		return errors.New("prefix_override must be an IPv6 /64")
	}
	return nil
}

func (d DNS) validate() error {
	providers := []string{"cloudflare", "rfc2136", "desec", "dynv6"}
	if !slices.Contains(providers, d.Provider) {
		return fmt.Errorf("provider must be one of %s", strings.Join(providers, ", "))
	}
	switch d.Provider {
	case "cloudflare":
		if d.Cloudflare.ZoneID == "" || d.Cloudflare.APITokenFile == "" {
			return errors.New("cloudflare.zone_id and cloudflare.api_token_file are required")
		}
	case "rfc2136":
		if d.RFC2136.Server == "" || d.RFC2136.Zone == "" {
			return errors.New("rfc2136.server and rfc2136.zone are required")
		}
		if _, _, err := net.SplitHostPort(d.RFC2136.Server); err != nil {
			return errors.New("rfc2136.server must include a host and port")
		}
		if (d.RFC2136.KeyName == "") != (d.RFC2136.KeyFile == "") {
			return errors.New("rfc2136.key_name and rfc2136.key_file must be configured together")
		}
	case "desec":
		if d.DESEC.Zone == "" || d.DESEC.TokenFile == "" {
			return errors.New("desec.zone and desec.token_file are required")
		}
	case "dynv6":
		if d.Dynv6.Zone == "" || d.Dynv6.TokenFile == "" {
			return errors.New("dynv6.zone and dynv6.token_file are required")
		}
	}
	return nil
}

func (p Probe) validate() error {
	if p.Endpoint == "" {
		return nil
	}
	endpoint, err := url.Parse(p.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return errors.New("endpoint must be an absolute HTTPS URL")
	}
	return nil
}

func (m Metrics) validate() error {
	host, port, err := net.SplitHostPort(m.Listen)
	if err != nil || port == "" {
		return errors.New("listen must contain a host and port")
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("listen host must be an IP address")
	}
	if !address.IsLoopback() {
		return errors.New("listen must be loopback in v1")
	}
	return nil
}

func (e Edge) validate() error {
	if !e.Enabled {
		return nil
	}
	address, err := netip.ParseAddr(e.IPv4Address)
	if err != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() {
		return errors.New("ipv4_address must be public IPv4 when edge is enabled")
	}
	if e.KeyFile == "" {
		return errors.New("key_file is required when edge is enabled")
	}
	if e.MaxClockSkew.Duration() <= 0 || e.HeaderTimeout.Duration() <= 0 {
		return errors.New("max_clock_skew and header_timeout must be positive")
	}
	return nil
}

func validateServices(services []StaticService) error {
	seenNames := make(map[string]struct{}, len(services))
	seenSockets := make(map[string]string, len(services))
	for index, service := range services {
		if err := service.Validate(); err != nil {
			return fmt.Errorf("static_services[%d]: %w", index, err)
		}
		if _, exists := seenNames[service.Name]; exists {
			return fmt.Errorf("duplicate service name %q", service.Name)
		}
		seenNames[service.Name] = struct{}{}
		if service.PublicAddress != "" {
			key := net.JoinHostPort(service.PublicAddress, fmt.Sprint(service.Listen))
			if previous, exists := seenSockets[key]; exists {
				return fmt.Errorf("services %q and %q use the same public socket %s", previous, service.Name, key)
			}
			seenSockets[key] = service.Name
		}
	}
	return nil
}

func (s StaticService) Validate() error {
	if s.Name == "" || s.DNSName == "" {
		return errors.New("name and dns are required")
	}
	backend, err := netip.ParseAddrPort(s.Backend)
	if err != nil || backend.Port() == 0 {
		return errors.New("backend must be an IP address and port")
	}
	if s.Listen == 0 {
		return errors.New("listen is required")
	}
	if s.Mode != "auto" && s.Mode != "direct" && s.Mode != "splice" {
		return errors.New("mode must be auto, direct, or splice")
	}
	if s.Mode == "direct" {
		if !backend.Addr().Is6() || backend.Port() != s.Listen || backend.Addr().IsPrivate() {
			return errors.New("direct mode requires an IPv6 backend whose port matches listen")
		}
	}
	if s.Edge && s.Mode != "splice" {
		return errors.New("edge-enabled services must use splice mode in v1")
	}
	if s.PublicAddress != "" {
		address, err := netip.ParseAddr(s.PublicAddress)
		if err != nil || !address.Is6() || address.IsPrivate() || !address.IsGlobalUnicast() {
			return errors.New("public_address must be public IPv6")
		}
		if s.Mode == "direct" && !backend.Addr().IsUnspecified() && address != backend.Addr() {
			return errors.New("direct public_address must match the backend address")
		}
	}
	return nil
}

func ReadSecret(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secret path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret file permissions %04o allow group or other access", info.Mode().Perm())
	}
	if info.Size() > 64<<10 {
		return nil, errors.New("secret file exceeds 64 KiB")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	content = []byte(strings.TrimSpace(string(content)))
	if len(content) == 0 {
		return nil, errors.New("secret file is empty")
	}
	return content, nil
}
