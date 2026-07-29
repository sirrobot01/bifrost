package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/platform"
)

const (
	// defaultConfigDir is where the packages and the systemd unit expect the
	// configuration and its secrets to live.
	defaultConfigDir = "/etc/bifrost"
	// serviceAccount owns those files when init runs as root.
	serviceAccount = "bifrost"
	// addressSecretBytes is the entropy behind the address-derivation secret.
	// Deriver requires at least 32 bytes and the file is stored hex encoded, so
	// this yields a 64 byte secret.
	addressSecretBytes = 32
)

// pendingFile is a file init intends to create. Collecting them before any
// write lets init show the operator the whole plan and lets a refusal leave the
// host untouched.
type pendingFile struct {
	path    string
	content []byte
	mode    os.FileMode
	label   string
}

func (r Runner) runInit(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	interactive := flags.Bool("interactive", false, "ask for the required settings and create every file")
	configDir := flags.String("config-dir", defaultConfigDir, "directory for the configuration and its secrets in interactive mode")
	interfaceName := flags.String("interface", "", "publication interface; auto-detected when unambiguous")
	output := flags.String("output", "-", "config output path or - for stdout")
	force := flags.Bool("force", false, "replace existing files")
	ownerID := flags.String("owner-id", defaultOwnerID(), "DNS ownership identity")
	secretFile := flags.String("secret-file", filepath.Join(defaultConfigDir, "address-secret"), "host address-derivation secret")
	zoneID := flags.String("cloudflare-zone-id", "CHANGE_ME", "Cloudflare zone ID")
	tokenFile := flags.String("cloudflare-token-file", filepath.Join(defaultConfigDir, "cloudflare-token"), "Cloudflare token file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init accepts flags only")
	}

	if *interactive {
		return r.runInitInteractive(ctx, *configDir, *interfaceName, *ownerID, *force)
	}

	if *interfaceName == "" {
		var err error
		*interfaceName, err = discoverInterface()
		if err != nil {
			return err
		}
	}

	configFile := config.Config{
		Version:    config.CurrentVersion,
		Interface:  *interfaceName,
		OwnerID:    *ownerID,
		SecretFile: *secretFile,
		DNS: config.DNS{
			Provider: "cloudflare",
			Cloudflare: config.Cloudflare{
				ZoneID:       *zoneID,
				APITokenFile: *tokenFile,
			},
		},
	}
	encoded, err := config.Encode(configFile)
	if err != nil {
		return err
	}
	if *output == "-" {
		_, err = r.Stdout.Write(encoded)
		return err
	}
	if err := writePendingFile(pendingFile{path: *output, content: encoded, mode: 0o600, label: "configuration"}, *force); err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.Stdout, "wrote %s\n\nThis template is not yet usable. Still required:\n"+
		"  - a DNS credential at %s\n"+
		"  - an address secret at %s\n"+
		"  - the real Cloudflare zone ID\n"+
		"  - at least one entry under static_services\n\n"+
		"Run bifrost init --interactive to have Bifrost create all of these.\n",
		*output, *tokenFile, *secretFile)
	return err
}

// runInitInteractive asks for everything Bifrost needs and creates the files
// itself. The flag-driven path above writes a template that still needs five
// manual steps, and every one of them is a chance to leave a secret world
// readable or a zone ID wrong.
func (r Runner) runInitInteractive(ctx context.Context, configDir, interfaceName, ownerID string, force bool) error {
	prompt := newPrompter(r.Stdin, r.Stdout)
	_ = prompt.say("This creates the Bifrost configuration, the address secret, and the DNS credential.")
	_ = prompt.say("Run bifrost doctor first if you have not confirmed this host has usable IPv6.")
	_ = prompt.say("")

	// Only ask about the interface when the host is ambiguous. A question with
	// one possible answer is ceremony, and this one has a right answer on
	// almost every machine.
	if interfaceName == "" {
		discovered, err := discoverInterface()
		if err != nil {
			_ = prompt.say("Could not pick an interface automatically: %v", err)
			answer, promptErr := prompt.required("Publication interface", "")
			if promptErr != nil {
				return promptErr
			}
			interfaceName = answer
		} else {
			interfaceName = discovered
			_ = prompt.say("Publishing from %s.", interfaceName)
		}
	}
	configPath := filepath.Join(configDir, "config.yaml")
	secretPath := filepath.Join(configDir, "address-secret")

	_ = prompt.say("")
	_ = prompt.say("Describe the first service to publish. More can be added later in %s.", configPath)
	service, err := promptService(prompt)
	if err != nil {
		return err
	}

	_ = prompt.say("")
	provider, err := prompt.choice("DNS provider", []string{"cloudflare", "desec", "dynv6", "rfc2136"}, "cloudflare")
	if err != nil {
		return err
	}
	lookup := r.zoneLookup
	if lookup == nil {
		lookup = providerZoneLookup
	}
	dnsConfig, files, err := promptDNSProvider(ctx, prompt, provider, service.DNSName, configDir, lookup)
	if err != nil {
		return err
	}

	configFile := config.Config{
		Version:        config.CurrentVersion,
		Interface:      interfaceName,
		OwnerID:        ownerID,
		SecretFile:     secretPath,
		DNS:            dnsConfig,
		StaticServices: []config.StaticService{service},
	}
	configFile.ApplyDefaults()
	if err := configFile.Validate(); err != nil {
		return fmt.Errorf("these answers do not form a usable configuration: %w", err)
	}
	encoded, err := config.Encode(configFile)
	if err != nil {
		return err
	}
	secret, err := generateAddressSecret()
	if err != nil {
		return err
	}
	files = append(files,
		pendingFile{path: secretPath, content: secret, mode: 0o600, label: "address secret"},
		pendingFile{path: configPath, content: encoded, mode: 0o600, label: "configuration"},
	)

	_ = prompt.say("")
	_ = prompt.say("About to create %s and these files:", configDir)
	for _, file := range files {
		_ = prompt.say("  %-40s %s", file.path, file.label)
	}
	confirmed, err := prompt.confirm("Write them?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("aborted before writing any file")
	}

	if err := os.MkdirAll(configDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", configDir, err)
	}
	for _, file := range files {
		if err := writePendingFile(file, force); err != nil {
			return err
		}
	}
	owned, err := applyServiceOwnership(configDir, files)
	if err != nil {
		return err
	}

	return writeInitNextSteps(prompt, configPath, service, owned)
}

// promptService asks only for what cannot be worked out. The service name
// comes from the DNS name, the public port is the one browsers use, and auto
// mode picks the exposure that fits the backend. Each derived value is shown
// rather than asked about, so the operator can still see what was chosen.
func promptService(prompt *prompter) (config.StaticService, error) {
	dnsName, err := prompt.required("Public DNS name (for example media.example.com)", "")
	if err != nil {
		return config.StaticService{}, err
	}
	backend, err := prompt.addrPort("Address the service already listens on", "127.0.0.1:8096")
	if err != nil {
		return config.StaticService{}, err
	}
	name := serviceNameFromDNS(dnsName)
	const listen uint16 = 443
	mode := "auto"
	_ = prompt.say("  service %q, published on port %d, mode %s", name, listen, mode)
	if !backend.Addr().Is6() || backend.Addr().IsPrivate() {
		_ = prompt.say("  Bifrost will terminate TLS and forward to the backend")
	}
	return config.StaticService{
		Name:    name,
		Backend: backend.String(),
		Listen:  listen,
		DNSName: dnsName,
		Mode:    mode,
	}, nil
}

// zoneLookupFunc asks a provider account which zone covers dnsName.
type zoneLookupFunc func(ctx context.Context, provider, token, dnsName string) (string, error)

func providerZoneLookup(ctx context.Context, provider, token, dnsName string) (string, error) {
	switch provider {
	case "desec":
		return dnspublish.LookupDESECZone(ctx, dnspublish.DESECConfig{Token: token}, dnsName)
	case "dynv6":
		return dnspublish.LookupDynv6Zone(ctx, dnspublish.Dynv6Config{Token: token}, dnsName)
	}
	return "", fmt.Errorf("no zone lookup for provider %q", provider)
}

func promptDNSProvider(ctx context.Context, prompt *prompter, provider, dnsName, configDir string, lookup zoneLookupFunc) (config.DNS, []pendingFile, error) {
	switch provider {
	case "cloudflare":
		token, err := prompt.secret("Cloudflare API token")
		if err != nil {
			return config.DNS{}, nil, err
		}
		if token == "" {
			return config.DNS{}, nil, errors.New("a Cloudflare API token is required")
		}
		tokenPath := filepath.Join(configDir, "cloudflare-token")
		zoneID, err := resolveCloudflareZone(ctx, prompt, token, dnsName)
		if err != nil {
			return config.DNS{}, nil, err
		}
		return config.DNS{
				Provider:   "cloudflare",
				Cloudflare: config.Cloudflare{ZoneID: zoneID, APITokenFile: tokenPath},
			}, []pendingFile{
				{path: tokenPath, content: secretContent(token), mode: 0o600, label: "Cloudflare API token"},
			}, nil

	case "desec":
		token, err := prompt.secret("deSEC token")
		if err != nil {
			return config.DNS{}, nil, err
		}
		zone, err := resolveProviderZone(prompt, "deSEC", dnsName, func() (string, error) {
			return lookup(ctx, "desec", token, dnsName)
		})
		if err != nil {
			return config.DNS{}, nil, err
		}
		tokenPath := filepath.Join(configDir, "desec-token")
		return config.DNS{
				Provider: "desec",
				DESEC:    config.DESEC{Zone: zone, TokenFile: tokenPath},
			}, []pendingFile{
				{path: tokenPath, content: secretContent(token), mode: 0o600, label: "deSEC token"},
			}, nil

	case "dynv6":
		token, err := prompt.secret("dynv6 token")
		if err != nil {
			return config.DNS{}, nil, err
		}
		zone, err := resolveProviderZone(prompt, "dynv6", dnsName, func() (string, error) {
			return lookup(ctx, "dynv6", token, dnsName)
		})
		if err != nil {
			return config.DNS{}, nil, err
		}
		tokenPath := filepath.Join(configDir, "dynv6-token")
		return config.DNS{
				Provider: "dynv6",
				Dynv6:    config.Dynv6{Zone: zone, TokenFile: tokenPath},
			}, []pendingFile{
				{path: tokenPath, content: secretContent(token), mode: 0o600, label: "dynv6 token"},
			}, nil

	case "rfc2136":
		server, err := prompt.required("Authoritative server (host:port)", "127.0.0.1:53")
		if err != nil {
			return config.DNS{}, nil, err
		}
		zone, err := prompt.required("Zone", guessZone(dnsName))
		if err != nil {
			return config.DNS{}, nil, err
		}
		useKey, err := prompt.confirm("Authenticate updates with a TSIG key?", true)
		if err != nil {
			return config.DNS{}, nil, err
		}
		dnsConfig := config.DNS{Provider: "rfc2136", RFC2136: config.RFC2136{Server: server, Zone: zone}}
		if !useKey {
			return dnsConfig, nil, nil
		}
		keyName, err := prompt.required("TSIG key name", "bifrost.")
		if err != nil {
			return config.DNS{}, nil, err
		}
		algorithm, err := prompt.choice("TSIG algorithm", []string{"hmac-sha256", "hmac-sha512"}, "hmac-sha256")
		if err != nil {
			return config.DNS{}, nil, err
		}
		key, err := prompt.secret("TSIG key secret")
		if err != nil {
			return config.DNS{}, nil, err
		}
		keyPath := filepath.Join(configDir, "rfc2136-key")
		dnsConfig.RFC2136.KeyName = keyName
		dnsConfig.RFC2136.KeyFile = keyPath
		dnsConfig.RFC2136.Algorithm = algorithm
		return dnsConfig, []pendingFile{
			{path: keyPath, content: secretContent(key), mode: 0o600, label: "TSIG key"},
		}, nil
	}
	return config.DNS{}, nil, fmt.Errorf("unsupported DNS provider %q", provider)
}

// resolveCloudflareZone turns the service DNS name into a zone ID. Finding that
// ID by hand means opening the Cloudflare dashboard, and it is the one setting
// an operator cannot derive from anything they already know.
func resolveCloudflareZone(ctx context.Context, prompt *prompter, token, dnsName string) (string, error) {
	_ = prompt.say("  looking up the Cloudflare zone for %s", dnsName)
	zoneID, zoneName, err := dnspublish.LookupCloudflareZone(ctx, dnspublish.CloudflareConfig{APIToken: token}, dnsName)
	if err == nil {
		_ = prompt.say("  found zone %s (%s)", zoneName, zoneID)
		return zoneID, nil
	}
	_ = prompt.say("  lookup failed: %v", err)
	return prompt.required("Cloudflare zone ID (Overview tab of the zone in the dashboard)", "")
}

func writeInitNextSteps(prompt *prompter, configPath string, service config.StaticService, owned bool) error {
	_ = prompt.say("")
	_ = prompt.say("Configuration written to %s.", configPath)
	if !owned {
		_ = prompt.say("These files are owned by the current user. If Bifrost runs as the %s account, chown them to it.", serviceAccount)
	}
	_ = prompt.say("")
	_ = prompt.say("Your router must permit inbound IPv6 TCP %d to this host before %s answers.", service.Listen, service.DNSName)
	host := platform.New()
	if host.Capabilities().ManagedFirewall {
		_ = prompt.say("Set firewall.mode to managed in the configuration to have Bifrost scope inbound")
		_ = prompt.say("IPv6 to the published services, and firewall.pcp to ask the router itself.")
	} else {
		_ = prompt.say("This platform uses advisory firewall mode; add a host rule for the service address and port.")
		_ = prompt.say("Set firewall.pcp to ask the router itself when PCP is available.")
	}
	_ = prompt.say("")
	_ = prompt.say("Next:")
	_ = prompt.say("  sudo bifrost serve --config %s --dry-run   # review the planned changes", configPath)
	_ = prompt.say("  %s", host.Services().StartAdvice(platform.HomeService))
	_ = prompt.say("  sudo bifrost check --config %s             # confirm the published path", configPath)
	return nil
}

// applyServiceOwnership hands the created files to the service account when
// init runs as root and that account exists. It reports whether ownership was
// transferred so the caller can tell the operator to do it themselves.
func applyServiceOwnership(configDir string, files []pendingFile) (bool, error) {
	if os.Geteuid() != 0 {
		return false, nil
	}
	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return false, nil
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return false, nil
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return false, nil
	}
	if err := os.Chown(configDir, uid, gid); err != nil {
		return false, fmt.Errorf("set ownership of %s: %w", configDir, err)
	}
	for _, file := range files {
		if err := os.Chown(file.path, uid, gid); err != nil {
			return false, fmt.Errorf("set ownership of %s: %w", file.path, err)
		}
	}
	return true, nil
}

func writePendingFile(file pendingFile, force bool) error {
	openFlags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		openFlags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	handle, err := os.OpenFile(file.path, openFlags, file.mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; move it aside or re-run with --force", file.path)
		}
		return fmt.Errorf("create %s: %w", file.path, err)
	}
	if _, err := handle.Write(file.content); err != nil {
		_ = handle.Close()
		return fmt.Errorf("write %s: %w", file.path, err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close %s: %w", file.path, err)
	}
	// O_CREATE honours the process umask, so an existing permissive umask could
	// otherwise leave a secret readable by group or other.
	if err := os.Chmod(file.path, file.mode); err != nil {
		return fmt.Errorf("set permissions on %s: %w", file.path, err)
	}
	return nil
}

func generateAddressSecret() ([]byte, error) {
	raw := make([]byte, addressSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate address secret: %w", err)
	}
	return secretContent(hex.EncodeToString(raw)), nil
}

func secretContent(value string) []byte {
	return []byte(strings.TrimSpace(value) + "\n")
}

// guessZone proposes the registrable zone for a service name. It is only a
// prompt default: providers differ on where the zone boundary sits, so the
// operator confirms it.
// resolveProviderZone asks the provider which account zone covers dnsName and
// offers it as the default. The offline guess is only a fallback: shared
// suffixes like dedyn.io make the textual answer wrong, which is how a
// misconfigured zone survives until the first reconcile.
func resolveProviderZone(prompt *prompter, providerName, dnsName string, lookup func() (string, error)) (string, error) {
	_ = prompt.say("  looking up the %s zone for %s", providerName, dnsName)
	zone, err := lookup()
	if err == nil {
		_ = prompt.say("  found zone %s", zone)
	} else {
		_ = prompt.say("  lookup failed: %v", err)
		zone = guessZone(dnsName)
	}
	return prompt.required(providerName+" zone", zone)
}

// sharedZoneSuffixes are public suffixes whose customer zones sit one label
// deeper than a registrable domain looks. The provider account lookup is the
// authoritative answer; this only shapes the offline default.
var sharedZoneSuffixes = []string{"dedyn.io", "dynv6.net", "duckdns.org"}

func guessZone(dnsName string) string {
	labels := strings.Split(strings.TrimSuffix(dnsName, "."), ".")
	depth := 2
	if len(labels) >= 3 && slices.Contains(sharedZoneSuffixes, strings.ToLower(strings.Join(labels[len(labels)-2:], "."))) {
		depth = 3
	}
	if len(labels) < depth {
		return dnsName
	}
	return strings.Join(labels[len(labels)-depth:], ".")
}

// serviceNameFromDNS derives the service identity from the name being
// published. The first label is what an operator calls the service anyway, so
// asking for it separately invites a mismatch between the two.
func serviceNameFromDNS(dnsName string) string {
	label, _, _ := strings.Cut(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(dnsName)), "."), ".")
	sanitized := make([]rune, 0, len(label))
	for _, character := range label {
		if character == '-' || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			sanitized = append(sanitized, character)
		}
	}
	if len(sanitized) == 0 {
		return "service"
	}
	return string(sanitized)
}
