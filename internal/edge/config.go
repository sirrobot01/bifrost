package edge

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sirrobot01/bifrost/internal/config"
)

type Config struct {
	Version          int               `yaml:"version"`
	Listen           string            `yaml:"listen"`
	Allow            []string          `yaml:"allow"`
	KeyFile          string            `yaml:"key_file"`
	HandshakeTimeout config.Duration   `yaml:"handshake_timeout"`
	IdleTimeout      config.Duration   `yaml:"idle_timeout"`
	MaxConnections   int               `yaml:"max_connections"`
	RatePerMinute    int               `yaml:"rate_per_minute"`
	RateBurst        int               `yaml:"rate_burst"`
	NegativeTTL      config.Duration   `yaml:"negative_ttl"`
	StaleOnError     config.Duration   `yaml:"stale_on_error"`
	StaticMaps       map[string]string `yaml:"static_maps"`
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open edge config: %w", err)
	}
	decoder := yaml.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.KnownFields(true)
	var configFile Config
	decodeErr := decoder.Decode(&configFile)
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil {
		return Config{}, errors.Join(decodeErr, closeErr)
	}
	configFile.applyDefaults()
	if err := configFile.Validate(); err != nil {
		return Config{}, err
	}
	return configFile, nil
}

func (c *Config) applyDefaults() {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Listen == "" {
		c.Listen = ":443"
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = config.Duration(5 * time.Second)
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = config.Duration(5 * time.Minute)
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 4096
	}
	if c.RatePerMinute == 0 {
		c.RatePerMinute = 120
	}
	if c.RateBurst == 0 {
		c.RateBurst = 20
	}
	if c.NegativeTTL == 0 {
		c.NegativeTTL = config.Duration(15 * time.Second)
	}
	if c.StaleOnError == 0 {
		c.StaleOnError = config.Duration(30 * time.Second)
	}
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported edge config version %d", c.Version)
	}
	_, tlsPort, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return errors.New("edge listen must include a host and port")
	}
	if c.KeyFile == "" || len(c.Allow) == 0 {
		return errors.New("edge key_file and at least one allow entry are required")
	}
	if c.HandshakeTimeout.Duration() <= 0 || c.IdleTimeout.Duration() <= 0 || c.NegativeTTL.Duration() <= 0 || c.StaleOnError.Duration() < 0 {
		return errors.New("edge durations must be positive")
	}
	if c.MaxConnections <= 0 || c.RatePerMinute <= 0 || c.RateBurst <= 0 {
		return errors.New("edge connection and rate limits must be positive")
	}
	allowed := make(map[string]struct{}, len(c.Allow))
	for index, name := range c.Allow {
		normalized, err := normalizeHostname(name)
		if err != nil {
			return fmt.Errorf("allow[%d]: %w", index, err)
		}
		if _, exists := allowed[normalized]; exists {
			return fmt.Errorf("duplicate allow entry %q", normalized)
		}
		allowed[normalized] = struct{}{}
	}
	for port, target := range c.StaticMaps {
		parsedPort, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsedPort == 0 {
			return fmt.Errorf("invalid static map port %q", port)
		}
		if port == tlsPort {
			return fmt.Errorf("static map port %s conflicts with the TLS listener", port)
		}
		host, targetPort, err := net.SplitHostPort(target)
		if err != nil || targetPort == "" {
			return fmt.Errorf("static map %s target must include a DNS name and port", port)
		}
		if _, err := normalizeHostname(host); err != nil {
			return fmt.Errorf("static map %s: %w", port, err)
		}
	}
	return nil
}

func normalizeHostname(name string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" || len(name) > 253 {
		return "", errors.New("dns name is empty or too long")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid dns name %q", name)
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return "", fmt.Errorf("invalid dns name %q", name)
			}
		}
	}
	return name, nil
}
