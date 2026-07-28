package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	ID                string         `json:"id"`
	DNSName           string         `json:"dns"`
	Mode              string         `json:"mode"`
	Addresses         []netip.Addr   `json:"addresses"`
	EdgeAddresses     []netip.Addr   `json:"edge_addresses,omitempty"`
	Backend           netip.AddrPort `json:"backend"`
	ClientIPPreserved bool           `json:"client_ip_preserved"`
	ActiveConnections int64          `json:"active_connections"`
	Accepted          uint64         `json:"accepted_total"`
	Rejected          uint64         `json:"rejected_total"`
	DialFailures      uint64         `json:"dial_failures_total"`
}

// Certificate reports one managed certificate's expiry, so a failing renewal
// is visible to monitoring rather than only to a later handshake failure.
type Certificate struct {
	Name     string    `json:"name"`
	NotAfter time.Time `json:"not_after"`
}

type Snapshot struct {
	Ready         bool          `json:"ready"`
	StartedAt     time.Time     `json:"started_at"`
	LastReconcile time.Time     `json:"last_reconcile,omitempty"`
	LastError     string        `json:"last_error,omitempty"`
	Services      []Service     `json:"services"`
	Certificates  []Certificate `json:"certificates,omitempty"`
}

type Server struct {
	listen   string
	snapshot func() Snapshot
}

func NewServer(listen string, snapshot func() Snapshot) (*Server, error) {
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return nil, errors.New("observability listen address must contain a host and port")
	}
	if snapshot == nil {
		return nil, errors.New("observability snapshot function is required")
	}
	return &Server{listen: listen, snapshot: snapshot}, nil
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return fmt.Errorf("listen for observability: %w", err)
	}
	server := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	shutdown := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = server.Shutdown(shutdownContext)
			cancel()
		case <-shutdown:
		}
	}()
	err = server.Serve(listener)
	close(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		snapshot := s.snapshot()
		writer.Header().Set("Content-Type", "application/json")
		if !snapshot.Ready {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"ready": snapshot.Ready, "last_error": snapshot.LastError})
	})
	mux.HandleFunc("GET /status", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(s.snapshot())
	})
	mux.HandleFunc("GET /metrics", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = writer.Write([]byte(metrics(s.snapshot())))
	})
	return mux
}

func metrics(snapshot Snapshot) string {
	ready := 0
	if snapshot.Ready {
		ready = 1
	}
	var output strings.Builder
	fmt.Fprintf(&output, "# HELP bifrost_ready Whether the home runtime has reconciled successfully.\n# TYPE bifrost_ready gauge\nbifrost_ready %d\n", ready)
	fmt.Fprintf(&output, "# HELP bifrost_services Number of published services.\n# TYPE bifrost_services gauge\nbifrost_services %d\n", len(snapshot.Services))
	output.WriteString("# HELP bifrost_connections_active Current accepted TCP connections.\n# TYPE bifrost_connections_active gauge\n")
	output.WriteString("# HELP bifrost_connections_accepted_total Accepted TCP connections.\n# TYPE bifrost_connections_accepted_total counter\n")
	output.WriteString("# HELP bifrost_connections_rejected_total TCP connections rejected by a limit.\n# TYPE bifrost_connections_rejected_total counter\n")
	output.WriteString("# HELP bifrost_backend_dial_failures_total Failed backend TCP dials.\n# TYPE bifrost_backend_dial_failures_total counter\n")
	if len(snapshot.Certificates) > 0 {
		output.WriteString("# HELP bifrost_certificate_expiry_seconds Unix time when a managed certificate expires.\n# TYPE bifrost_certificate_expiry_seconds gauge\n")
		for _, certificate := range snapshot.Certificates {
			fmt.Fprintf(&output, "bifrost_certificate_expiry_seconds{name=%s} %d\n", strconv.Quote(certificate.Name), certificate.NotAfter.Unix())
		}
	}
	for _, service := range snapshot.Services {
		labels := "service=" + strconv.Quote(service.ID) + ",mode=" + strconv.Quote(service.Mode)
		fmt.Fprintf(&output, "bifrost_connections_active{%s} %d\n", labels, service.ActiveConnections)
		fmt.Fprintf(&output, "bifrost_connections_accepted_total{%s} %d\n", labels, service.Accepted)
		fmt.Fprintf(&output, "bifrost_connections_rejected_total{%s} %d\n", labels, service.Rejected)
		fmt.Fprintf(&output, "bifrost_backend_dial_failures_total{%s} %d\n", labels, service.DialFailures)
	}
	return output.String()
}
