package diagnose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

type ProbeRequest struct {
	Address netip.Addr `json:"address"`
	Port    uint16     `json:"port"`
	// ServerName lets a prober that dispatches on TLS names reach the right
	// service. Probers that dial the address directly ignore it.
	ServerName string `json:"server_name,omitempty"`
}

type ProbeResult struct {
	Reachable bool `json:"reachable"`
	PathMTU   int  `json:"path_mtu,omitempty"`
	// PathMTUMeasured distinguishes "the probe found no blackhole" from "the
	// probe never looked", so a prober that only tests reachability does not
	// produce a false PMTU verdict.
	PathMTUMeasured   bool `json:"path_mtu_measured"`
	PacketTooBigWorks bool `json:"packet_too_big_works"`
}

type ExternalProber interface {
	Probe(context.Context, ProbeRequest) (ProbeResult, error)
}

type HTTPProber struct {
	endpoint *url.URL
	client   *http.Client
}

func NewHTTPProber(endpoint string, client *http.Client) (*HTTPProber, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("probe endpoint must be an absolute HTTPS URL")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &HTTPProber{endpoint: parsed, client: client}, nil
}

func (p *HTTPProber) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if !request.Address.IsValid() || !request.Address.Is6() || request.Port == 0 {
		return ProbeResult{}, errors.New("probe requires an IPv6 address and port")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("encode probe request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return ProbeResult{}, fmt.Errorf("create probe request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("send probe request: %w", err)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("read probe response: %w", err)
	}
	if closeErr != nil {
		return ProbeResult{}, fmt.Errorf("close probe response: %w", closeErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ProbeResult{}, fmt.Errorf("probe returned HTTP %d: %s", response.StatusCode, bytes.TrimSpace(responseBody))
	}
	var result ProbeResult
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return ProbeResult{}, fmt.Errorf("decode probe response: %w", err)
	}
	// A remote probe reports path MTU, so its verdict on Packet Too Big counts.
	result.PathMTUMeasured = true
	return result, nil
}
