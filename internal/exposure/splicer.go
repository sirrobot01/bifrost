package exposure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// Config defines one IPv6 TCP listener and its backend.
type Config struct {
	ServiceID      string
	ListenAddress  netip.AddrPort
	BackendAddress netip.AddrPort
	MaxConnections int
	DialTimeout    time.Duration
	IdleTimeout    time.Duration
	ProxyProtocol  bool
	GlobalLimiter  *Limiter
	Logger         *slog.Logger
}

// Status is a point-in-time view of splice activity.
type Status struct {
	ActiveConnections int64
	Accepted          uint64
	Rejected          uint64
	DialFailures      uint64
	ClientIPPreserved bool
}

// Splicer forwards one exact IPv6 listener to a TCP backend.
type Splicer struct {
	config   Config
	listener net.Listener
	logger   *slog.Logger

	mu          sync.Mutex
	closing     bool
	serving     bool
	connections map[*connection]struct{}
	wg          sync.WaitGroup

	active       atomic.Int64
	accepted     atomic.Uint64
	rejected     atomic.Uint64
	dialFailures atomic.Uint64
}

type Limiter struct {
	maximum int64
	active  atomic.Int64
}

func NewLimiter(maximum int) (*Limiter, error) {
	if maximum <= 0 {
		return nil, errors.New("connection limit must be positive")
	}
	return &Limiter{maximum: int64(maximum)}, nil
}

func (l *Limiter) acquire() bool {
	for {
		active := l.active.Load()
		if active >= l.maximum {
			return false
		}
		if l.active.CompareAndSwap(active, active+1) {
			return true
		}
	}
}

func (l *Limiter) release() {
	l.active.Add(-1)
}

func (l *Limiter) Active() int64 {
	return l.active.Load()
}

// Listen binds the configured IPv6 service address.
func Listen(ctx context.Context, config Config) (*Splicer, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp6", config.ListenAddress.String())
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", config.ListenAddress, err)
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Splicer{
		config:      config,
		listener:    listener,
		logger:      logger.With("service", config.ServiceID),
		connections: make(map[*connection]struct{}),
	}, nil
}

// Address returns the bound listener address.
func (s *Splicer) Address() netip.AddrPort {
	return s.listener.Addr().(*net.TCPAddr).AddrPort()
}

// Serve accepts connections until ctx is cancelled or Shutdown is called.
func (s *Splicer) Serve(ctx context.Context) error {
	s.mu.Lock()
	if s.serving {
		s.mu.Unlock()
		return errors.New("splicer is already serving")
	}
	if s.closing {
		s.mu.Unlock()
		return errors.New("splicer is closed")
	}
	s.serving = true
	s.mu.Unlock()

	stop := context.AfterFunc(ctx, func() { _ = s.stopAccepting() })
	defer stop()

	for {
		client, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing || ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			_ = client.Close()
			return nil
		}
		if len(s.connections) >= s.config.MaxConnections {
			s.mu.Unlock()
			s.rejected.Add(1)
			_ = client.Close()
			continue
		}
		if s.config.GlobalLimiter != nil && !s.config.GlobalLimiter.acquire() {
			s.mu.Unlock()
			s.rejected.Add(1)
			_ = client.Close()
			continue
		}

		connection := &connection{client: client, limited: s.config.GlobalLimiter != nil}
		s.connections[connection] = struct{}{}
		s.wg.Add(1)
		s.active.Add(1)
		s.accepted.Add(1)
		s.mu.Unlock()

		go s.serveConnection(connection)
	}
}

// Shutdown stops accepting and waits for active connections to finish.
func (s *Splicer) Shutdown(ctx context.Context) error {
	closeErr := s.stopAccepting()
	wait := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(wait)
	}()

	select {
	case <-wait:
		return closeErr
	case <-ctx.Done():
		s.mu.Lock()
		connections := make([]*connection, 0, len(s.connections))
		for connection := range s.connections {
			connections = append(connections, connection)
		}
		s.mu.Unlock()
		for _, connection := range connections {
			connection.close()
		}
		return errors.Join(closeErr, ctx.Err())
	}
}

// Status returns current connection counters and source-address behavior.
func (s *Splicer) Status() Status {
	return Status{
		ActiveConnections: s.active.Load(),
		Accepted:          s.accepted.Load(),
		Rejected:          s.rejected.Load(),
		DialFailures:      s.dialFailures.Load(),
		ClientIPPreserved: s.config.ProxyProtocol,
	}
}

func (c Config) validate() error {
	if c.ServiceID == "" {
		return errors.New("service ID is required")
	}
	if !c.ListenAddress.IsValid() || !c.ListenAddress.Addr().Is6() || c.ListenAddress.Addr().Is4In6() || c.ListenAddress.Addr().IsUnspecified() {
		return errors.New("listen address must be a specific IPv6 address")
	}
	if !c.BackendAddress.IsValid() || c.BackendAddress.Port() == 0 {
		return errors.New("backend address and port are required")
	}
	if c.MaxConnections <= 0 {
		return errors.New("maximum connections must be positive")
	}
	if c.DialTimeout <= 0 {
		return errors.New("dial timeout must be positive")
	}
	if c.IdleTimeout <= 0 {
		return errors.New("idle timeout must be positive")
	}
	return nil
}

func (s *Splicer) stopAccepting() error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	s.mu.Unlock()

	if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("close listener: %w", err)
	}
	return nil
}

func (s *Splicer) serveConnection(connection *connection) {
	defer func() {
		connection.close()
		s.mu.Lock()
		delete(s.connections, connection)
		s.mu.Unlock()
		s.active.Add(-1)
		if connection.limited {
			s.config.GlobalLimiter.release()
		}
		s.wg.Done()
	}()

	dialer := net.Dialer{Timeout: s.config.DialTimeout}
	backend, err := dialer.Dial("tcp", s.config.BackendAddress.String())
	if err != nil {
		s.dialFailures.Add(1)
		s.logger.Warn("backend connection failed", "backend", s.config.BackendAddress, "error", err)
		return
	}
	if !connection.setBackend(backend) {
		return
	}
	if s.config.ProxyProtocol {
		if err := backend.SetWriteDeadline(time.Now().Add(s.config.DialTimeout)); err != nil {
			s.dialFailures.Add(1)
			s.logger.Warn("PROXY protocol deadline failed", "backend", s.config.BackendAddress, "error", err)
			return
		}
		if err := writeProxyV2(backend, connection.client); err != nil {
			s.dialFailures.Add(1)
			s.logger.Warn("PROXY protocol header failed", "backend", s.config.BackendAddress, "error", err)
			return
		}
		if err := backend.SetWriteDeadline(time.Time{}); err != nil {
			s.dialFailures.Add(1)
			s.logger.Warn("PROXY protocol deadline reset failed", "backend", s.config.BackendAddress, "error", err)
			return
		}
	}

	client := &idleConn{Conn: connection.client, timeout: s.config.IdleTimeout}
	upstream := &idleConn{Conn: backend, timeout: s.config.IdleTimeout}
	if err := splice(client, upstream); err != nil {
		s.logger.Debug("connection closed with error", "client", connection.client.RemoteAddr(), "error", err)
	}
}

type connection struct {
	client  net.Conn
	mu      sync.Mutex
	backend net.Conn
	closed  bool
	limited bool
}

func (c *connection) setBackend(backend net.Conn) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = backend.Close()
		return false
	}
	c.backend = backend
	c.mu.Unlock()
	return true
}

func (c *connection) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	backend := c.backend
	c.mu.Unlock()

	_ = c.client.Close()
	if backend != nil {
		_ = backend.Close()
	}
}

type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(buffer []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(buffer)
}

func (c *idleConn) Write(buffer []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(buffer)
}

func (c *idleConn) CloseRead() error {
	if connection, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return connection.CloseRead()
	}
	return nil
}

func (c *idleConn) CloseWrite() error {
	if connection, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return connection.CloseWrite()
	}
	return nil
}

func splice(client, backend net.Conn) error {
	errorsByDirection := make(chan error, 2)
	go copyHalf(backend, client, errorsByDirection)
	go copyHalf(client, backend, errorsByDirection)
	return errors.Join(<-errorsByDirection, <-errorsByDirection)
}

func copyHalf(destination, source net.Conn, result chan<- error) {
	_, copyErr := io.Copy(destination, source)
	var closeErrors []error
	if connection, ok := destination.(interface{ CloseWrite() error }); ok {
		closeErrors = append(closeErrors, connection.CloseWrite())
	}
	if connection, ok := source.(interface{ CloseRead() error }); ok {
		closeErrors = append(closeErrors, connection.CloseRead())
	}
	result <- errors.Join(copyErr, errors.Join(closeErrors...))
}
