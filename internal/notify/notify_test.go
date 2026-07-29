package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type capture struct {
	mu     sync.Mutex
	bodies []string
	titles []string
}

func (c *capture) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, string(body))
		c.titles = append(c.titles, request.Header.Get("Title"))
		c.mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

// drain runs the worker until it has delivered want events or the deadline
// passes, so the test never sleeps for a fixed duration.
func drain(t *testing.T, hook *Webhook, sink *capture, want int) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = hook.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for sink.count() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func TestWebhookSendsJSONThatDiscordAccepts(t *testing.T) {
	t.Parallel()

	sink := &capture{}
	server := sink.server(t)
	hook, err := NewWebhook(Config{Endpoint: server.URL, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	hook.Notify(Event{Kind: ExternalLost, Service: "media", Summary: "not reachable from outside", Detail: "edge dial timed out"})
	drain(t, hook, sink, 1)

	if sink.count() != 1 {
		t.Fatalf("delivered %d events, want 1", sink.count())
	}
	var payload struct {
		Kind    string `json:"kind"`
		Service string `json:"service"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(sink.bodies[0]), &payload); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if payload.Kind != string(ExternalLost) || payload.Service != "media" {
		t.Fatalf("payload = %+v", payload)
	}
	// Discord rejects a webhook body without `content`.
	if !strings.Contains(payload.Content, "media") || !strings.Contains(payload.Content, "edge dial timed out") {
		t.Fatalf("content = %q", payload.Content)
	}
}

func TestWebhookTextFormatSuitsNtfy(t *testing.T) {
	t.Parallel()

	sink := &capture{}
	server := sink.server(t)
	hook, err := NewWebhook(Config{Endpoint: server.URL, Format: "text", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	hook.Notify(Event{Kind: ExternalRestored, Service: "media", Summary: "reachable again"})
	drain(t, hook, sink, 1)

	if sink.count() != 1 {
		t.Fatalf("delivered %d events, want 1", sink.count())
	}
	if strings.HasPrefix(sink.bodies[0], "{") {
		t.Fatalf("text format sent JSON: %q", sink.bodies[0])
	}
	if !strings.Contains(sink.titles[0], "reachable again") {
		t.Fatalf("Title = %q", sink.titles[0])
	}
}

// A service that flaps must not generate a message per tick.
func TestWebhookSuppressesRepeatsWithinTheInterval(t *testing.T) {
	t.Parallel()

	sink := &capture{}
	server := sink.server(t)
	now := time.Unix(1700000000, 0)
	hook, err := NewWebhook(Config{
		Endpoint:    server.URL,
		Client:      server.Client(),
		MinInterval: 10 * time.Minute,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		hook.Notify(Event{Kind: ExternalLost, Service: "media", Summary: "not reachable"})
	}
	drain(t, hook, sink, 1)

	if sink.count() != 1 {
		t.Fatalf("delivered %d events, want 1", sink.count())
	}
}

// Recovery is the message an operator most wants, so it must never be withheld
// because the outage that preceded it was recent.
func TestWebhookNeverSuppressesRecoveryAfterAnOutage(t *testing.T) {
	t.Parallel()

	sink := &capture{}
	server := sink.server(t)
	now := time.Unix(1700000000, 0)
	hook, err := NewWebhook(Config{
		Endpoint:    server.URL,
		Client:      server.Client(),
		MinInterval: time.Hour,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	hook.Notify(Event{Kind: ExternalLost, Service: "media", Summary: "not reachable"})
	hook.Notify(Event{Kind: ExternalRestored, Service: "media", Summary: "reachable again"})
	// A second outage inside the interval is a new transition too.
	hook.Notify(Event{Kind: ExternalLost, Service: "media", Summary: "not reachable"})
	drain(t, hook, sink, 3)

	if sink.count() != 3 {
		t.Fatalf("delivered %d events, want 3", sink.count())
	}
}

// Suppression is per service: one broken service must not silence another.
func TestWebhookSuppressionIsPerService(t *testing.T) {
	t.Parallel()

	sink := &capture{}
	server := sink.server(t)
	now := time.Unix(1700000000, 0)
	hook, err := NewWebhook(Config{
		Endpoint:    server.URL,
		Client:      server.Client(),
		MinInterval: time.Hour,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	hook.Notify(Event{Kind: ExternalLost, Service: "media", Summary: "not reachable"})
	hook.Notify(Event{Kind: ExternalLost, Service: "photos", Summary: "not reachable"})
	drain(t, hook, sink, 2)

	if sink.count() != 2 {
		t.Fatalf("delivered %d events, want 2", sink.count())
	}
}

func TestNewWebhookRejectsPlaintextAndUnknownFormats(t *testing.T) {
	t.Parallel()

	if _, err := NewWebhook(Config{Endpoint: "http://example.com/hook"}); err == nil {
		t.Fatal("a plaintext endpoint was accepted")
	}
	if _, err := NewWebhook(Config{Endpoint: "https://example.com/hook", Format: "xml"}); err == nil {
		t.Fatal("an unknown format was accepted")
	}
}

// A blocked endpoint must never block the caller, because Notify runs on the
// reconciliation path.
func TestNotifyNeverBlocksWhenTheQueueIsFull(t *testing.T) {
	t.Parallel()

	hook, err := NewWebhook(Config{Endpoint: "https://example.invalid/hook"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := range queueDepth * 4 {
			hook.Notify(Event{Kind: ReconcileFailed, Summary: "failed", Detail: string(rune('a' + index%26))})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Notify blocked once the queue filled")
	}
}
