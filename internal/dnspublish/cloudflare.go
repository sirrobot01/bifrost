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
	"time"
)

const cloudflareAPIURL = "https://api.cloudflare.com/client/v4"

// CloudflareConfig configures the Cloudflare DNS provider.
type CloudflareConfig struct {
	ZoneID     string
	APIToken   string
	HTTPClient *http.Client
	BaseURL    string
}

// Cloudflare implements Provider using the Cloudflare v4 API.
type Cloudflare struct {
	zoneID      string
	apiToken    string
	httpClient  *http.Client
	baseURL     string
	maxAttempts int
	baseDelay   time.Duration
}

// NewCloudflare returns a Cloudflare DNS provider.
func NewCloudflare(config CloudflareConfig) (*Cloudflare, error) {
	if config.ZoneID == "" {
		return nil, errors.New("Cloudflare zone ID is required")
	}
	if config.APIToken == "" {
		return nil, errors.New("Cloudflare API token is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = cloudflareAPIURL
	}

	return &Cloudflare{
		zoneID:      config.ZoneID,
		apiToken:    config.APIToken,
		httpClient:  client,
		baseURL:     baseURL,
		maxAttempts: 4,
		baseDelay:   100 * time.Millisecond,
	}, nil
}

// cloudflarePageSize is the page size requested when listing records. The API
// reference documents no ceiling for per_page and reported limits differ between
// versions, so this stays at the documented default that every version accepts.
// Walking pages, not enlarging them, is what makes a large zone list correctly.
const cloudflarePageSize = 100

// cloudflareMaxPages bounds a listing walk so a provider that keeps answering
// with full pages cannot spin forever.
const cloudflareMaxPages = 1000

// List returns records of recordType with the exact name.
func (c *Cloudflare) List(ctx context.Context, name string, recordType RecordType) ([]Record, error) {
	query := url.Values{"type": {string(recordType)}}
	query.Set("name", name)
	return c.list(ctx, query)
}

func (c *Cloudflare) ListZone(ctx context.Context, recordType RecordType) ([]Record, error) {
	return c.list(ctx, url.Values{"type": {string(recordType)}})
}

// list walks every page of a record query. Reading only the first page would
// truncate the result silently, which leaves Prune blind to ownership markers
// beyond the first page and strands the records they cover.
func (c *Cloudflare) list(ctx context.Context, query url.Values) ([]Record, error) {
	query.Set("per_page", strconv.Itoa(cloudflarePageSize))
	records := make([]Record, 0, cloudflarePageSize)
	for page := 1; page <= cloudflareMaxPages; page++ {
		query.Set("page", strconv.Itoa(page))
		var response cloudflareResponse[[]cloudflareRecord]
		if err := c.request(ctx, http.MethodGet, c.recordsPath(), query, nil, &response); err != nil {
			return nil, err
		}
		for _, record := range response.Result {
			records = append(records, record.providerRecord())
		}
		// Every result_info field is optional, so a short page is the primary
		// signal that the walk is done and the reported totals are consulted
		// only when the provider actually sends them.
		if len(response.Result) < cloudflarePageSize {
			return records, nil
		}
		if response.ResultInfo.TotalPages > 0 && page >= response.ResultInfo.TotalPages {
			return records, nil
		}
		if response.ResultInfo.TotalCount > 0 && len(records) >= response.ResultInfo.TotalCount {
			return records, nil
		}
	}
	return nil, fmt.Errorf("Cloudflare record listing exceeded %d pages", cloudflareMaxPages)
}

// Create adds a DNS record.
func (c *Cloudflare) Create(ctx context.Context, record Record) (Record, error) {
	payload := cloudflareRecordFromProvider(record)
	var response cloudflareResponse[cloudflareRecord]
	if err := c.request(ctx, http.MethodPost, c.recordsPath(), nil, payload, &response); err != nil {
		return Record{}, err
	}
	return response.Result.providerRecord(), nil
}

// Update replaces a DNS record by ID.
func (c *Cloudflare) Update(ctx context.Context, record Record) error {
	if record.ID == "" {
		return errors.New("Cloudflare record ID is required for update")
	}
	payload := cloudflareRecordFromProvider(record)
	var response cloudflareResponse[cloudflareRecord]
	return c.request(ctx, http.MethodPut, c.recordsPath()+"/"+url.PathEscape(record.ID), nil, payload, &response)
}

// Delete removes a DNS record by ID.
func (c *Cloudflare) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("Cloudflare record ID is required for deletion")
	}
	var response cloudflareResponse[json.RawMessage]
	return c.request(ctx, http.MethodDelete, c.recordsPath()+"/"+url.PathEscape(id), nil, nil, &response)
}

type cloudflareRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

func (r cloudflareRecord) providerRecord() Record {
	return Record{ID: r.ID, Type: RecordType(r.Type), Name: r.Name, Content: r.Content, TTL: r.TTL, Proxied: r.Proxied}
}

func cloudflareRecordFromProvider(record Record) cloudflareRecord {
	return cloudflareRecord{ID: record.ID, Type: string(record.Type), Name: record.Name, Content: record.Content, TTL: record.TTL, Proxied: record.Proxied}
}

type cloudflareResponse[T any] struct {
	Success    bool                 `json:"success"`
	Errors     []cloudflareError    `json:"errors"`
	Result     T                    `json:"result"`
	ResultInfo cloudflareResultInfo `json:"result_info"`
}

// cloudflareResultInfo carries pagination totals. Every field is optional in the
// API reference, so a zero value means the provider did not report it.
type cloudflareResultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

type cloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Cloudflare) recordsPath() string {
	return "/zones/" + url.PathEscape(c.zoneID) + "/dns_records"
}

func (c *Cloudflare) request(ctx context.Context, method, path string, query url.Values, payload any, result any) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode Cloudflare request: %w", err)
		}
	}

	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		requestURL := c.baseURL + path
		if len(query) > 0 {
			requestURL += "?" + query.Encode()
		}
		request, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create Cloudflare request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+c.apiToken)
		request.Header.Set("Content-Type", "application/json")

		response, err := c.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt+1 < c.maxAttempts {
				if err := waitForRetry(ctx, c.baseDelay, attempt); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("send Cloudflare request: %w", err)
		}

		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		closeErr := response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read Cloudflare response: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close Cloudflare response: %w", closeErr)
		}

		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
			if attempt+1 < c.maxAttempts {
				delay := retryDelay(response.Header.Get("Retry-After"), c.baseDelay, attempt)
				if err := waitForRetry(ctx, delay, 0); err != nil {
					return err
				}
				continue
			}
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("Cloudflare API returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
		}
		if err := json.Unmarshal(responseBody, result); err != nil {
			return fmt.Errorf("decode Cloudflare response: %w", err)
		}

		switch response := result.(type) {
		case *cloudflareResponse[[]cloudflareRecord]:
			if !response.Success {
				return cloudflareFailure(response.Errors)
			}
		case *cloudflareResponse[cloudflareRecord]:
			if !response.Success {
				return cloudflareFailure(response.Errors)
			}
		case *cloudflareResponse[json.RawMessage]:
			if !response.Success {
				return cloudflareFailure(response.Errors)
			}
		default:
			return errors.New("unsupported Cloudflare response type")
		}
		return nil
	}

	return errors.New("Cloudflare request exhausted retries")
}

func cloudflareFailure(apiErrors []cloudflareError) error {
	if len(apiErrors) == 0 {
		return errors.New("Cloudflare API reported failure")
	}
	messages := make([]string, 0, len(apiErrors))
	for _, apiError := range apiErrors {
		messages = append(messages, fmt.Sprintf("%d: %s", apiError.Code, apiError.Message))
	}
	return fmt.Errorf("Cloudflare API: %s", strings.Join(messages, "; "))
}

func retryDelay(header string, base time.Duration, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return base << attempt
}

func waitForRetry(ctx context.Context, base time.Duration, attempt int) error {
	timer := time.NewTimer(base << attempt)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
