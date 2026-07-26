package dnspublish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const desecAPIURL = "https://desec.io/api/v1"

type DESECConfig struct {
	Zone       string
	Token      string
	HTTPClient *http.Client
	BaseURL    string
}

type DESECProvider struct {
	zone    string
	token   string
	client  *http.Client
	baseURL string
}

type desecRRSet struct {
	Subname string   `json:"subname"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl"`
	Records []string `json:"records"`
}

func NewDESEC(config DESECConfig) (*DESECProvider, error) {
	zone := strings.TrimSuffix(strings.ToLower(config.Zone), ".")
	if zone == "" || config.Token == "" {
		return nil, errors.New("deSEC zone and token are required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = desecAPIURL
	}
	return &DESECProvider{zone: zone, token: config.Token, client: client, baseURL: baseURL}, nil
}

// LookupDESECZone returns the account domain that should hold dnsName: the
// longest domain that is the name itself or a parent of it. Textual guessing
// cannot do this, because suffixes like dedyn.io host many customer zones.
func LookupDESECZone(ctx context.Context, config DESECConfig, dnsName string) (string, error) {
	dnsName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(dnsName)), ".")
	if dnsName == "" {
		return "", errors.New("a DNS name is required to look up a deSEC zone")
	}
	config.Zone = dnsName // unused by the domain listing, but required by the constructor
	provider, err := NewDESEC(config)
	if err != nil {
		return "", err
	}
	var domains []struct {
		Name string `json:"name"`
	}
	if _, err := provider.request(ctx, http.MethodGet, "/domains/", nil, nil, &domains); err != nil {
		return "", err
	}
	names := make([]string, len(domains))
	for index, domain := range domains {
		names[index] = domain.Name
	}
	zone := longestCoveringZone(dnsName, names)
	if zone == "" {
		return "", fmt.Errorf("no deSEC domain in this account covers %q", dnsName)
	}
	return zone, nil
}

// longestCoveringZone returns the candidate that is dnsName itself or a parent
// of it. Records belong in the most specific zone, so the longest match wins.
func longestCoveringZone(dnsName string, candidates []string) string {
	var zone string
	for _, candidate := range candidates {
		name := strings.TrimSuffix(strings.ToLower(candidate), ".")
		if name == "" || (dnsName != name && !strings.HasSuffix(dnsName, "."+name)) {
			continue
		}
		if len(name) > len(zone) {
			zone = name
		}
	}
	return zone
}

func (p *DESECProvider) List(ctx context.Context, name string, recordType RecordType) ([]Record, error) {
	subname, err := p.subname(name)
	if err != nil {
		return nil, err
	}
	pathSubname := subname
	if pathSubname == "" {
		pathSubname = "@"
	}
	path := p.rrsetsPath() + url.PathEscape(pathSubname) + "/" + url.PathEscape(string(recordType)) + "/"
	var rrset desecRRSet
	status, err := p.request(ctx, http.MethodGet, path, nil, nil, &rrset)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	return p.records(name, rrset), nil
}

func (p *DESECProvider) ListZone(ctx context.Context, recordType RecordType) ([]Record, error) {
	query := url.Values{"type": {string(recordType)}, "cursor": {""}}
	var rrsets []desecRRSet
	_, err := p.request(ctx, http.MethodGet, p.rrsetsPath(), query, nil, &rrsets)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, rrset := range rrsets {
		name := p.zone
		if rrset.Subname != "" {
			name = rrset.Subname + "." + p.zone
		}
		records = append(records, p.records(name, rrset)...)
	}
	return records, nil
}

func (p *DESECProvider) Create(ctx context.Context, record Record) (Record, error) {
	records, err := p.List(ctx, record.Name, record.Type)
	if err != nil {
		return Record{}, err
	}
	for _, existing := range records {
		if existing.Content == record.Content {
			return existing, nil
		}
	}
	records = append(records, record)
	if err := p.replace(ctx, record.Name, record.Type, record.TTL, records); err != nil {
		return Record{}, err
	}
	record.ID = providerRecordID(record)
	return record, nil
}

func (p *DESECProvider) Update(ctx context.Context, record Record) error {
	old, err := parseProviderRecordID(record.ID)
	if err != nil {
		return err
	}
	records, err := p.List(ctx, old.Name, old.Type)
	if err != nil {
		return err
	}
	replaced := false
	for index := range records {
		if records[index].Content == old.Content {
			records[index] = record
			replaced = true
		}
	}
	if !replaced {
		return errors.New("deSEC record to update was not found")
	}
	return p.replace(ctx, record.Name, record.Type, record.TTL, records)
}

func (p *DESECProvider) Delete(ctx context.Context, id string) error {
	record, err := parseProviderRecordID(id)
	if err != nil {
		return err
	}
	records, err := p.List(ctx, record.Name, record.Type)
	if err != nil {
		return err
	}
	ttl := 60
	if len(records) > 0 {
		ttl = records[0].TTL
	}
	filtered := records[:0]
	for _, existing := range records {
		if existing.Content != record.Content {
			filtered = append(filtered, existing)
		}
	}
	return p.replace(ctx, record.Name, record.Type, ttl, filtered)
}

func (p *DESECProvider) replace(ctx context.Context, name string, recordType RecordType, ttl int, records []Record) error {
	subname, err := p.subname(name)
	if err != nil {
		return err
	}
	contents := make([]string, 0, len(records))
	for _, record := range records {
		content := record.Content
		if recordType == RecordTXT && !strings.HasPrefix(content, "\"") {
			content = strconv.Quote(content)
		}
		contents = append(contents, content)
	}
	slices.Sort(contents)
	payload := []desecRRSet{{Subname: subname, Type: string(recordType), TTL: ttl, Records: contents}}
	_, err = p.request(ctx, http.MethodPatch, p.rrsetsPath(), nil, payload, nil)
	return err
}

func (p *DESECProvider) records(name string, rrset desecRRSet) []Record {
	records := make([]Record, 0, len(rrset.Records))
	for _, content := range rrset.Records {
		normalized := content
		if rrset.Type == string(RecordTXT) {
			if unquoted, err := strconv.Unquote(content); err == nil {
				normalized = unquoted
			}
		}
		record := Record{Type: RecordType(rrset.Type), Name: strings.TrimSuffix(strings.ToLower(name), "."), Content: normalized, TTL: rrset.TTL}
		record.ID = providerRecordID(record)
		records = append(records, record)
	}
	return records
}

func (p *DESECProvider) subname(name string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == p.zone {
		return "", nil
	}
	suffix := "." + p.zone
	if !strings.HasSuffix(name, suffix) {
		return "", fmt.Errorf("DNS name %q is outside deSEC zone %q", name, p.zone)
	}
	return strings.TrimSuffix(name, suffix), nil
}

func (p *DESECProvider) rrsetsPath() string {
	return "/domains/" + url.PathEscape(p.zone) + "/rrsets/"
}

func (p *DESECProvider) request(ctx context.Context, method, path string, query url.Values, payload, result any) (int, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}
	requestURL := p.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Token "+p.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return 0, err
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return response.StatusCode, errors.Join(readErr, closeErr)
	}
	if response.StatusCode == http.StatusNotFound && method == http.MethodGet {
		return response.StatusCode, nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// deSEC enforces a per-domain minimum TTL (3600s unless support has
		// lowered it), and the raw validation error does not say what to do.
		if response.StatusCode == http.StatusBadRequest &&
			bytes.Contains(responseBody, []byte(`"ttl"`)) &&
			bytes.Contains(responseBody, []byte("greater than or equal to")) {
			return response.StatusCode, fmt.Errorf(
				"deSEC rejected the record TTL (%s): the domain enforces a minimum TTL; raise dns.ttl to satisfy it, or ask deSEC support to lower the domain minimum",
				bytes.TrimSpace(responseBody))
		}
		return response.StatusCode, fmt.Errorf("deSEC API returned HTTP %d: %s", response.StatusCode, bytes.TrimSpace(responseBody))
	}
	if result != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, result); err != nil {
			return response.StatusCode, err
		}
	}
	return response.StatusCode, nil
}

func providerRecordID(record Record) string {
	return string(record.Type) + "\x00" + record.Name + "\x00" + record.Content
}

func parseProviderRecordID(id string) (Record, error) {
	parts := strings.SplitN(id, "\x00", 3)
	if len(parts) != 3 {
		return Record{}, errors.New("invalid provider record ID")
	}
	return Record{Type: RecordType(parts[0]), Name: parts[1], Content: parts[2]}, nil
}
