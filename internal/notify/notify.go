// Package notify posts operational events to an endpoint the operator chooses.
//
// Bifrost already exposes certificate expiry and readiness as Prometheus
// metrics, which only helps an operator who runs Prometheus. Most self-hosters
// do not, and the failures worth knowing about here unfold over days: a
// certificate renewal that has been failing for a fortnight, or a service that
// stopped answering from outside. This package is the path that reaches an
// operator who is not watching a dashboard.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type Kind string

const (
	// ExternalLost and ExternalRestored are transitions, never a repeated
	// statement of the current state.
	ExternalLost     Kind = "external_unreachable"
	ExternalRestored Kind = "external_restored"
	// CertificateRenewalFailed fires long before expiry, because renewal starts
	// 30 days out and failing silently until the service breaks is the whole
	// problem this package exists to solve.
	CertificateRenewalFailed Kind = "certificate_renewal_failed"
	ReconcileFailed          Kind = "reconcile_failed"
	PrefixChanged            Kind = "prefix_changed"
)

type Event struct {
	Kind    Kind      `json:"kind"`
	Service string    `json:"service,omitempty"`
	Summary string    `json:"summary"`
	Detail  string    `json:"detail,omitempty"`
	At      time.Time `json:"at"`
}

// Notifier accepts an event without blocking its caller. Delivery happens
// elsewhere, because a notification endpoint must never be able to stall
// reconciliation by being slow.
type Notifier interface {
	Notify(Event)
}

// Discard satisfies Notifier when no endpoint is configured, so callers never
// branch on nil.
type Discard struct{}

func (Discard) Notify(Event) {}

// queueDepth bounds memory when an endpoint is unreachable. Events are dropped
// rather than accumulated: a stale backlog delivered an hour late is worse than
// silence, and the drop is logged.
const queueDepth = 32

type Webhook struct {
	endpoint string
	format   string
	client   *http.Client
	logger   *slog.Logger
	queue    chan Event

	// minInterval suppresses a repeat of the same kind for the same service.
	// A flapping service must not become a notification loop.
	minInterval time.Duration
	now         func() time.Time
	mu          sync.Mutex
	lastSent    map[string]time.Time
}

type Config struct {
	Endpoint string
	// Format is "json" for a structured body, or "text" for a plain-text one.
	// JSON includes a `content` field so a Discord webhook works unmodified;
	// text suits ntfy and anything that treats the body as the message.
	Format      string
	MinInterval time.Duration
	Client      *http.Client
	Logger      *slog.Logger
	Now         func() time.Time
}

func NewWebhook(config Config) (*Webhook, error) {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("notify.webhook must be an absolute HTTPS URL")
	}
	switch config.Format {
	case "", "json":
		config.Format = "json"
	case "text":
	default:
		return nil, fmt.Errorf("notify.format %q is not json or text", config.Format)
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Webhook{
		endpoint:    config.Endpoint,
		format:      config.Format,
		client:      config.Client,
		logger:      config.Logger,
		queue:       make(chan Event, queueDepth),
		minInterval: config.MinInterval,
		now:         config.Now,
		lastSent:    make(map[string]time.Time),
	}, nil
}

// Notify queues an event. It never blocks and never returns an error, because
// every caller is on a control path where neither is actionable.
func (w *Webhook) Notify(event Event) {
	if event.At.IsZero() {
		event.At = w.now()
	}
	if w.suppressed(event) {
		return
	}
	select {
	case w.queue <- event:
	default:
		w.logger.Warn("dropped a notification because the queue is full", "kind", event.Kind, "service", event.Service)
	}
}

// suppressed reports whether an identical event was sent too recently. A
// restored event always clears the suppression for its lost counterpart, so an
// outage that ends is never withheld because its start was recent.
func (w *Webhook) suppressed(event Event) bool {
	if w.minInterval <= 0 {
		return false
	}
	key := string(event.Kind) + "\x00" + event.Service
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, seen := w.lastSent[key]; seen && event.At.Sub(last) < w.minInterval {
		return true
	}
	w.lastSent[key] = event.At
	switch event.Kind {
	case ExternalRestored:
		delete(w.lastSent, string(ExternalLost)+"\x00"+event.Service)
	case ExternalLost:
		delete(w.lastSent, string(ExternalRestored)+"\x00"+event.Service)
	}
	return false
}

// Run delivers queued events until the context ends.
func (w *Webhook) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-w.queue:
			if err := w.send(ctx, event); err != nil {
				// A failed notification is not a daemon failure. Publication
				// continues; the operator loses one message.
				w.logger.Error("notification delivery failed", "kind", event.Kind, "service", event.Service, "error", err)
			}
		}
	}
}

func (w *Webhook) send(ctx context.Context, event Event) error {
	body, contentType := w.body(event)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", contentType)
	if w.format == "text" {
		// ntfy reads these; endpoints that do not understand them ignore them.
		request.Header.Set("Title", "Bifrost: "+event.Summary)
		request.Header.Set("Tags", string(event.Kind))
	}
	response, err := w.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (w *Webhook) body(event Event) ([]byte, string) {
	message := Message(event)
	if w.format == "text" {
		return []byte(message), "text/plain; charset=utf-8"
	}
	// `content` is what a Discord webhook requires; the typed fields beside it
	// are for anything that parses the payload properly.
	payload := struct {
		Event
		Content string `json:"content"`
	}{Event: event, Content: message}
	body, err := json.Marshal(payload)
	if err != nil {
		return []byte(message), "text/plain; charset=utf-8"
	}
	return body, "application/json"
}

// Message renders an event as one human-readable line.
func Message(event Event) string {
	message := event.Summary
	if event.Service != "" {
		message = event.Service + ": " + message
	}
	if event.Detail != "" {
		message += " — " + event.Detail
	}
	return message
}
