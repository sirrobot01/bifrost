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
	"strconv"
	"strings"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/dnspublish"
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

	if interfaceName == "" {
		discovered, err := discoverInterface()
		if err != nil {
			_ = prompt.say("Could not pick an interface automatically: %v", err)
		}
		answer, err := prompt.required("Publication interface", discovered)
		if err != nil {
			return err
		}
		interfaceName = answer
	}
	configDir, err := prompt.required("Configuration directory", configDir)
	if err != nil {
		return err
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
	dnsConfig, files, err := promptDNSProvider(ctx, prompt, provider, service.DNSName, configDir)
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

func promptService(prompt *prompter) (config.StaticService, error) {
	name, err := prompt.required("Service name", "myservice")
	if err != nil {
		return config.StaticService{}, err
	}
	dnsName, err := prompt.required("Public DNS name (for example media.example.com)", "")
	if err != nil {
		return config.StaticService{}, err
	}
	backend, err := prompt.addrPort("Address the service already listens on", "127.0.0.1:8096")
	if err != nil {
		return config.StaticService{}, err
	}
	listen, err := prompt.port("Public port clients connect to", 443)
	if err != nil {
		return config.StaticService{}, err
	}

	// Direct mode publishes the backend's own address, so it is only offered
	// when the backend could actually hold a public IPv6 address. Offering it
	// for an IPv4 backend would only produce a configuration that fails
	// validation after every other question has been answered.
	modes := []string{"auto", "splice"}
	if backend.Addr().Is6() && !backend.Addr().IsPrivate() {
		modes = []string{"auto", "direct", "splice"}
	} else {
		_ = prompt.say("  direct mode is unavailable: it needs the backend to own the public IPv6 address")
	}
	mode, err := prompt.choice("Service mode", modes, "auto")
	if err != nil {
		return config.StaticService{}, err
	}
	if mode == "direct" && backend.Port() != listen {
		_ = prompt.say("  direct mode cannot translate ports, so the public port must match the backend port")
		listen, err = prompt.port("Public port clients connect to", backend.Port())
		if err != nil {
			return config.StaticService{}, err
		}
	}

	return config.StaticService{
		Name:    name,
		Backend: backend.String(),
		Listen:  listen,
		DNSName: dnsName,
		Mode:    mode,
	}, nil
}

func promptDNSProvider(ctx context.Context, prompt *prompter, provider, dnsName, configDir string) (config.DNS, []pendingFile, error) {
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
		zone, err := prompt.required("deSEC zone", guessZone(dnsName))
		if err != nil {
			return config.DNS{}, nil, err
		}
		token, err := prompt.secret("deSEC token")
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
		zone, err := prompt.required("dynv6 zone", guessZone(dnsName))
		if err != nil {
			return config.DNS{}, nil, err
		}
		token, err := prompt.secret("dynv6 token")
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
	_ = prompt.say("Your router must forward inbound IPv6 TCP %d to this host before %s answers.", service.Listen, service.DNSName)
	_ = prompt.say("")
	_ = prompt.say("Next:")
	_ = prompt.say("  sudo bifrost serve --config %s --dry-run   # review the planned changes", configPath)
	_ = prompt.say("  sudo systemctl enable --now bifrost")
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
func guessZone(dnsName string) string {
	labels := strings.Split(strings.TrimSuffix(dnsName, "."), ".")
	if len(labels) < 2 {
		return dnsName
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
