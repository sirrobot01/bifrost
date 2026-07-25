package dnspublish

import (
	"fmt"

	"github.com/sirrobot01/bifrost/internal/config"
)

// NewProvider builds the configured provider and loads its credential file.
func NewProvider(dnsConfig config.DNS) (Provider, error) {
	switch dnsConfig.Provider {
	case "cloudflare":
		token, err := config.ReadSecret(dnsConfig.Cloudflare.APITokenFile)
		if err != nil {
			return nil, fmt.Errorf("cloudflare token: %w", err)
		}
		return NewCloudflare(CloudflareConfig{ZoneID: dnsConfig.Cloudflare.ZoneID, APIToken: string(token)})
	case "desec":
		token, err := config.ReadSecret(dnsConfig.DESEC.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("deSEC token: %w", err)
		}
		return NewDESEC(DESECConfig{Zone: dnsConfig.DESEC.Zone, Token: string(token)})
	case "dynv6":
		token, err := config.ReadSecret(dnsConfig.Dynv6.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("dynv6 token: %w", err)
		}
		return NewDynv6(Dynv6Config{Zone: dnsConfig.Dynv6.Zone, Token: string(token)})
	case "rfc2136":
		secret := ""
		if dnsConfig.RFC2136.KeyFile != "" {
			key, err := config.ReadSecret(dnsConfig.RFC2136.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("rfc 2136 key: %w", err)
			}
			secret = string(key)
		}
		return NewRFC2136(RFC2136Config{Server: dnsConfig.RFC2136.Server, Zone: dnsConfig.RFC2136.Zone, KeyName: dnsConfig.RFC2136.KeyName, KeySecret: secret, Algorithm: dnsConfig.RFC2136.Algorithm})
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", dnsConfig.Provider)
	}
}
