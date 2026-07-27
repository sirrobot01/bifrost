package dockerwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sirrobot01/bifrost/internal/config"
)

const dockerAPIVersion = "v1.41"

type ClientConfig struct {
	Socket     string
	HTTPClient *http.Client
	BaseURL    string
}

type Client struct {
	httpClient *http.Client
	baseURL    string
}

type container struct {
	ID              string            `json:"Id"`
	Names           []string          `json:"Names"`
	Labels          map[string]string `json:"Labels"`
	NetworkSettings struct {
		Networks map[string]network `json:"Networks"`
	} `json:"NetworkSettings"`
}

type network struct {
	IPAddress         string `json:"IPAddress"`
	GlobalIPv6Address string `json:"GlobalIPv6Address"`
}

func NewClient(config ClientConfig) (*Client, error) {
	client := config.HTTPClient
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if client == nil {
		if config.Socket == "" {
			return nil, errors.New("docker socket is required")
		}
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", config.Socket)
			},
			DisableCompression: true,
			MaxIdleConns:       2,
		}
		client = &http.Client{Transport: transport}
		baseURL = "http://docker"
	}
	if baseURL == "" {
		return nil, errors.New("docker base URL is required with a custom HTTP client")
	}
	return &Client{httpClient: client, baseURL: baseURL}, nil
}

func (c *Client) ListServices(ctx context.Context) ([]config.StaticService, error) {
	filters, err := json.Marshal(map[string][]string{"status": {"running"}})
	if err != nil {
		return nil, err
	}
	query := url.Values{"filters": {string(filters)}}
	var containers []container
	if err := c.get(ctx, "/containers/json?"+query.Encode(), &containers); err != nil {
		return nil, err
	}
	services := make([]config.StaticService, 0, len(containers))
	for _, container := range containers {
		rawEnabled := container.Labels["bifrost.enable"]
		if rawEnabled == "" {
			continue
		}
		enabled, err := strconv.ParseBool(rawEnabled)
		if err != nil {
			return nil, fmt.Errorf("container %s: bifrost.enable must be true or false", containerName(container))
		}
		if !enabled {
			continue
		}
		service, err := serviceFromContainer(container)
		if err != nil {
			return nil, fmt.Errorf("container %s: %w", containerName(container), err)
		}
		services = append(services, service)
	}
	slices.SortFunc(services, func(a, b config.StaticService) int { return strings.Compare(a.Name, b.Name) })
	return services, nil
}

func (c *Client) Watch(ctx context.Context, changes chan<- struct{}) error {
	filters, err := json.Marshal(map[string][]string{
		"type":  {"container"},
		"event": {"start", "die", "destroy", "connect", "disconnect", "rename"},
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/"+dockerAPIVersion+"/events?"+url.Values{"filters": {string(filters)}}.Encode(), nil)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		closeErr := response.Body.Close()
		return errors.Join(fmt.Errorf("docker events returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body))), readErr, closeErr)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		var event struct {
			Type   string `json:"Type"`
			Action string `json:"Action"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			closeErr := response.Body.Close()
			return errors.Join(fmt.Errorf("decode Docker event: %w", err), closeErr)
		}
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	scanErr := scanner.Err()
	closeErr := response.Body.Close()
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), closeErr)
	}
	if scanErr == nil {
		scanErr = io.ErrUnexpectedEOF
	}
	return errors.Join(fmt.Errorf("read Docker events: %w", scanErr), closeErr)
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/"+dockerAPIVersion+path, nil)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("docker API returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if err := json.Unmarshal(responseBody, result); err != nil {
		return fmt.Errorf("decode Docker response: %w", err)
	}
	return nil
}

func serviceFromContainer(container container) (config.StaticService, error) {
	labels := container.Labels
	backendPort, err := parsePort(labels["bifrost.port"])
	if err != nil {
		return config.StaticService{}, fmt.Errorf("bifrost.port: %w", err)
	}
	listenPort := backendPort
	if labels["bifrost.listen"] != "" {
		listenPort, err = parsePort(labels["bifrost.listen"])
		if err != nil {
			return config.StaticService{}, fmt.Errorf("bifrost.listen: %w", err)
		}
	}
	mode := labels["bifrost.mode"]
	if mode == "" {
		mode = "auto"
	}
	name := labels["bifrost.name"]
	if name == "" {
		name = containerName(container)
	}
	if name == "" {
		return config.StaticService{}, errors.New("service name is unavailable")
	}

	backend := labels["bifrost.backend"]
	publicAddress := ""
	if backend == "" {
		networkName, selected, err := selectNetwork(container.NetworkSettings.Networks, labels["bifrost.network"])
		if err != nil {
			return config.StaticService{}, err
		}
		switch mode {
		case "direct":
			if selected.GlobalIPv6Address == "" {
				return config.StaticService{}, fmt.Errorf("network %q has no global IPv6 address for direct mode", networkName)
			}
			backend = net.JoinHostPort(selected.GlobalIPv6Address, strconv.Itoa(int(backendPort)))
			publicAddress = selected.GlobalIPv6Address
		case "auto":
			if selected.GlobalIPv6Address != "" && backendPort == listenPort {
				backend = net.JoinHostPort(selected.GlobalIPv6Address, strconv.Itoa(int(backendPort)))
				publicAddress = selected.GlobalIPv6Address
			} else if selected.IPAddress != "" {
				backend = net.JoinHostPort(selected.IPAddress, strconv.Itoa(int(backendPort)))
			} else {
				backend = net.JoinHostPort(selected.GlobalIPv6Address, strconv.Itoa(int(backendPort)))
			}
		case "splice":
			address := selected.IPAddress
			if address == "" {
				address = selected.GlobalIPv6Address
			}
			backend = net.JoinHostPort(address, strconv.Itoa(int(backendPort)))
		default:
			return config.StaticService{}, fmt.Errorf("unsupported bifrost.mode %q", mode)
		}
	}
	if _, _, err := net.SplitHostPort(backend); err != nil {
		return config.StaticService{}, errors.New("bifrost.backend must include an IP address and port")
	}
	proxyProtocol, err := optionalBool(labels, "bifrost.proxy_protocol")
	if err != nil {
		return config.StaticService{}, err
	}
	edge, err := optionalBool(labels, "bifrost.edge")
	if err != nil {
		return config.StaticService{}, err
	}
	// Splice services terminate TLS by default, so a containerized backend
	// that speaks TLS itself needs a way to opt out.
	tls := labels["bifrost.tls"]
	if tls == "" {
		tls = "auto"
	}
	service := config.StaticService{Name: name, Backend: backend, Listen: listenPort, DNSName: labels["bifrost.dns"], Mode: mode, PublicAddress: publicAddress, ProxyProtocol: proxyProtocol, Edge: edge, TLS: tls}
	if err := service.Validate(); err != nil {
		return config.StaticService{}, err
	}
	return service, nil
}

func selectNetwork(networks map[string]network, configured string) (string, network, error) {
	if configured != "" {
		selected, exists := networks[configured]
		if !exists {
			return "", network{}, fmt.Errorf("configured Docker network %q is not attached", configured)
		}
		return configured, selected, nil
	}
	if len(networks) == 0 {
		return "", network{}, errors.New("container has no addressable Docker network; set bifrost.backend")
	}
	if len(networks) > 1 {
		return "", network{}, errors.New("container joins multiple Docker networks; set bifrost.network")
	}
	for name, selected := range networks {
		return name, selected, nil
	}
	return "", network{}, errors.New("container has no selectable Docker network")
}

func containerName(container container) string {
	if len(container.Names) > 0 {
		return strings.TrimPrefix(container.Names[0], "/")
	}
	if len(container.ID) > 12 {
		return container.ID[:12]
	}
	return container.ID
}

func parsePort(value string) (uint16, error) {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, errors.New("must be a TCP port between 1 and 65535")
	}
	return uint16(port), nil
}

func optionalBool(labels map[string]string, name string) (bool, error) {
	if labels[name] == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(labels[name])
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return value, nil
}
