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
	"strconv"
	"strings"
	"sync"
	"time"
)

const dynv6APIURL = "https://dynv6.com/api/v2"

type Dynv6Config struct {
	Zone       string
	Token      string
	HTTPClient *http.Client
	BaseURL    string
}

type Dynv6Provider struct {
	zone    string
	token   string
	client  *http.Client
	baseURL string
	mu      sync.Mutex
	zoneID  int64
}

type dynv6Zone struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type dynv6Record struct {
	ID     int64  `json:"id,omitempty"`
	ZoneID int64  `json:"zoneID,omitempty"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Data   string `json:"data"`
}

func NewDynv6(config Dynv6Config) (*Dynv6Provider, error) {
	zone := strings.TrimSuffix(strings.ToLower(config.Zone), ".")
	if zone == "" || config.Token == "" {
		return nil, errors.New("dynv6 zone and token are required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = dynv6APIURL
	}
	return &Dynv6Provider{zone: zone, token: config.Token, client: client, baseURL: baseURL}, nil
}

func (p *Dynv6Provider) List(ctx context.Context, name string, recordType RecordType) ([]Record, error) {
	records, err := p.ListZone(ctx, recordType)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	filtered := records[:0]
	for _, record := range records {
		if strings.TrimSuffix(strings.ToLower(record.Name), ".") == name {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

func (p *Dynv6Provider) ListZone(ctx context.Context, recordType RecordType) ([]Record, error) {
	zoneID, err := p.resolveZoneID(ctx)
	if err != nil {
		return nil, err
	}
	var response []dynv6Record
	if _, err := p.request(ctx, http.MethodGet, p.recordsPath(zoneID), nil, &response); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(response))
	for _, record := range response {
		if record.Type != string(recordType) {
			continue
		}
		records = append(records, Record{ID: strconv.FormatInt(record.ID, 10), Type: RecordType(record.Type), Name: strings.TrimSuffix(strings.ToLower(record.Name), "."), Content: record.Data})
	}
	return records, nil
}

func (p *Dynv6Provider) Create(ctx context.Context, record Record) (Record, error) {
	zoneID, err := p.resolveZoneID(ctx)
	if err != nil {
		return Record{}, err
	}
	payload := dynv6Record{Name: record.Name, Type: string(record.Type), Data: record.Content}
	var created dynv6Record
	if _, err := p.request(ctx, http.MethodPost, p.recordsPath(zoneID), payload, &created); err != nil {
		return Record{}, err
	}
	return Record{ID: strconv.FormatInt(created.ID, 10), Type: RecordType(created.Type), Name: created.Name, Content: created.Data}, nil
}

func (p *Dynv6Provider) Update(ctx context.Context, record Record) error {
	zoneID, err := p.resolveZoneID(ctx)
	if err != nil {
		return err
	}
	recordID, err := strconv.ParseInt(record.ID, 10, 64)
	if err != nil {
		return errors.New("invalid dynv6 record ID")
	}
	payload := dynv6Record{Name: record.Name, Type: string(record.Type), Data: record.Content}
	_, err = p.request(ctx, http.MethodPatch, p.recordsPath(zoneID)+"/"+strconv.FormatInt(recordID, 10), payload, nil)
	return err
}

func (p *Dynv6Provider) Delete(ctx context.Context, id string) error {
	zoneID, err := p.resolveZoneID(ctx)
	if err != nil {
		return err
	}
	recordID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return errors.New("invalid dynv6 record ID")
	}
	_, err = p.request(ctx, http.MethodDelete, p.recordsPath(zoneID)+"/"+strconv.FormatInt(recordID, 10), nil, nil)
	return err
}

func (p *Dynv6Provider) resolveZoneID(ctx context.Context) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.zoneID != 0 {
		return p.zoneID, nil
	}
	var zone dynv6Zone
	path := "/zones/by-name/" + url.PathEscape(p.zone)
	if _, err := p.request(ctx, http.MethodGet, path, nil, &zone); err != nil {
		return 0, err
	}
	if zone.ID == 0 {
		return 0, errors.New("dynv6 returned an invalid zone ID")
	}
	p.zoneID = zone.ID
	return zone.ID, nil
}

func (p *Dynv6Provider) recordsPath(zoneID int64) string {
	return "/zones/" + strconv.FormatInt(zoneID, 10) + "/records"
}

func (p *Dynv6Provider) request(ctx context.Context, method, path string, payload, result any) (int, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
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
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response.StatusCode, fmt.Errorf("dynv6 API returned HTTP %d: %s", response.StatusCode, bytes.TrimSpace(responseBody))
	}
	if result != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, result); err != nil {
			return response.StatusCode, err
		}
	}
	return response.StatusCode, nil
}
