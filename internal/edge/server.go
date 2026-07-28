package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/edgeauth"
	"github.com/sirrobot01/bifrost/internal/exposure"
)

type Server struct {
	config  Config
	key     []byte
	cache   *DNSCache
	allowed map[string]struct{}
	global  *exposure.Limiter
	rate    *sourceRateLimiter
	// refusal state throttles the log line for connections turned away on the
	// accept path, which is the expected behaviour under a flood.
	refusalMu       sync.Mutex
	refusalCount    int
	refusalLoggedAt time.Time
	logger          *slog.Logger
	dialHome        func(context.Context, []netip.Addr, uint16) (net.Conn, error)
}

type route struct {
	tls      bool
	host     string
	port     uint16
	identity string
}

func NewServer(configFile Config, resolver Resolver, logger *slog.Logger) (*Server, error) {
	configFile.applyDefaults()
	if err := configFile.Validate(); err != nil {
		return nil, err
	}
	key, err := config.ReadSecret(configFile.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("edge key: %w", err)
	}
	if len(key) < 32 {
		return nil, errors.New("edge key must be at least 32 bytes")
	}
	if resolver == nil {
		resolver, err = NewSystemResolver()
		if err != nil {
			return nil, err
		}
	}
	cache, err := NewDNSCache(resolver, configFile.NegativeTTL.Duration(), configFile.StaleOnError.Duration())
	if err != nil {
		return nil, err
	}
	global, err := exposure.NewLimiter(configFile.MaxConnections)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(configFile.Allow))
	for _, name := range configFile.Allow {
		normalized, _ := normalizeHostname(name)
		allowed[normalized] = struct{}{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{
		config:  configFile,
		key:     key,
		cache:   cache,
		allowed: allowed,
		global:  global,
		rate:    newSourceRateLimiter(configFile.RatePerMinute, configFile.RateBurst),
		logger:  logger,
	}
	server.dialHome = server.dial
	return server, nil
}

func (s *Server) Run(ctx context.Context) error {
	host, tlsPort, _ := net.SplitHostPort(s.config.Listen)
	tlsPortNumber, _ := strconv.ParseUint(tlsPort, 10, 16)
	type listenerSpec struct {
		address string
		route   route
	}
	specs := []listenerSpec{{address: s.config.Listen, route: route{tls: true, port: uint16(tlsPortNumber)}}}
	for port, target := range s.config.StaticMaps {
		targetHost, targetPort, _ := net.SplitHostPort(target)
		portNumber, _ := strconv.ParseUint(port, 10, 16)
		targetPortNumber, _ := strconv.ParseUint(targetPort, 10, 16)
		identity, _ := normalizeHostname(targetHost)
		specs = append(specs, listenerSpec{address: net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), route: route{host: identity, port: uint16(targetPortNumber), identity: identity}})
	}

	type boundListener struct {
		net.Listener
		route route
	}
	listeners := make([]boundListener, 0, len(specs))
	for _, spec := range specs {
		listener, err := net.Listen("tcp4", spec.address)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return fmt.Errorf("listen on %s: %w", spec.address, err)
		}
		if spec.route.tls && spec.route.port == 0 {
			spec.route.port = listener.Addr().(*net.TCPAddr).AddrPort().Port()
		}
		listeners = append(listeners, boundListener{Listener: listener, route: spec.route})
	}
	results := make(chan error, len(listeners))
	for _, listener := range listeners {
		go func() { results <- s.serve(ctx, listener.Listener, listener.route) }()
	}

	select {
	case <-ctx.Done():
		for _, listener := range listeners {
			_ = listener.Close()
		}
		for range listeners {
			<-results
		}
		return nil
	case err := <-results:
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return err
	}
}

func (s *Server) serve(ctx context.Context, listener net.Listener, listenerRoute route) error {
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if reason, admitted := s.admitConnection(connection); !admitted {
			// A dropped connection is invisible to the client and used to be
			// invisible here too, which left an operator with a dead edge and
			// an empty log. Report it, but not once per packet in a flood.
			s.reportRefusal(reason, connection.RemoteAddr())
			_ = connection.Close()
			continue
		}
		go func() {
			defer s.global.Release()
			s.handle(ctx, connection, listenerRoute)
		}()
	}
}

// admitConnection applies the global and per-source limits on the accept path,
// before a connection is given a goroutine. Shedding here rather than inside the
// handler keeps a flood from scheduling the very work needed to turn it away.
// The caller owns a global slot once this reports true and must release it.
func (s *Server) admitConnection(client net.Conn) (string, bool) {
	remote, ok := client.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return "the peer address is not TCP", false
	}
	if !s.global.Acquire() {
		return "the global connection limit is reached", false
	}
	if !s.rate.Allow(remote.AddrPort().Addr()) {
		s.global.Release()
		return "the per-source rate limit is reached", false
	}
	return "", true
}

// refusalLogInterval bounds how often refusals are logged. A flood is the
// expected reason to refuse, so logging every one would turn the limiter into
// an amplifier against the operator's disk.
const refusalLogInterval = 10 * time.Second

// reportRefusal logs at most one refusal per interval, with the number
// suppressed since the last line so a flood is still visible in its size.
func (s *Server) reportRefusal(reason string, remote net.Addr) {
	s.refusalMu.Lock()
	s.refusalCount++
	count := s.refusalCount
	now := time.Now()
	if now.Sub(s.refusalLoggedAt) < refusalLogInterval {
		s.refusalMu.Unlock()
		return
	}
	s.refusalLoggedAt = now
	s.refusalCount = 0
	s.refusalMu.Unlock()
	s.logger.Warn("edge refused connections", "reason", reason, "client", remote.String(), "refused_since_last_report", count)
}

func (s *Server) handle(ctx context.Context, client net.Conn, listenerRoute route) {
	defer func() { _ = client.Close() }()
	remote, ok := client.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return
	}

	host := listenerRoute.host
	identity := listenerRoute.identity
	var initial []byte
	if listenerRoute.tls {
		var err error
		initial, host, err = readClientHello(client, s.config.HandshakeTimeout.Duration())
		if err != nil {
			// This ends the connection, so it belongs at the same level as
			// every other terminating failure rather than hidden at debug.
			s.logger.Warn("edge ClientHello rejected", "client", remote, "error", err)
			return
		}
		if _, allowed := s.allowed[host]; !allowed {
			s.logger.Warn("edge SNI rejected", "client", remote, "sni", host)
			return
		}
		identity = host
	}
	addresses, err := s.cache.Lookup(ctx, host)
	if err != nil {
		s.logger.Warn("edge DNS lookup failed", "host", host, "error", err)
		return
	}
	backend, err := s.dialHome(ctx, addresses, listenerRoute.port)
	if err != nil {
		// Every cached address failed. The usual cause is a home prefix
		// change: the records already carry the new addresses, but this entry
		// is pinned until its TTL. Drop it and resolve again so the next
		// request follows the move instead of waiting out the TTL.
		s.cache.Invalidate(host)
		if retryAddresses, retryErr := s.cache.Lookup(ctx, host); retryErr == nil && !sameAddresses(retryAddresses, addresses) {
			s.logger.Info("edge re-resolved after a dial failure", "host", host, "addresses", retryAddresses)
			backend, err = s.dialHome(ctx, retryAddresses, listenerRoute.port)
		}
		if err != nil {
			s.logger.Warn("edge home dial failed", "host", host, "error", err)
			return
		}
	}
	defer func() { _ = backend.Close() }()

	local := client.LocalAddr().(*net.TCPAddr).AddrPort()
	header, err := edgeauth.Build(s.key, identity, remote.AddrPort(), local, time.Now())
	if err != nil {
		s.logger.Warn("edge authentication header failed", "error", err)
		return
	}
	if err := backend.SetWriteDeadline(time.Now().Add(s.config.HandshakeTimeout.Duration())); err != nil {
		return
	}
	if err := writeAll(backend, header); err != nil {
		return
	}
	if err := writeAll(backend, initial); err != nil {
		return
	}
	if err := backend.SetWriteDeadline(time.Time{}); err != nil {
		return
	}
	clientWithTimeout := &edgeIdleConn{Conn: client, timeout: s.config.IdleTimeout.Duration()}
	backendWithTimeout := &edgeIdleConn{Conn: backend, timeout: s.config.IdleTimeout.Duration()}
	if err := exposure.Bridge(clientWithTimeout, backendWithTimeout); err != nil {
		s.logger.Debug("edge connection closed", "client", remote, "host", host, "error", err)
	}
}

func (s *Server) dial(ctx context.Context, addresses []netip.Addr, port uint16) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: s.config.HandshakeTimeout.Duration()}
	var dialErrors []error
	for _, address := range addresses {
		connection, err := dialer.DialContext(ctx, "tcp6", netip.AddrPortFrom(address, port).String())
		if err == nil {
			return connection, nil
		}
		dialErrors = append(dialErrors, err)
	}
	return nil, errors.Join(dialErrors...)
}

func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

type edgeIdleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *edgeIdleConn) Read(buffer []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(buffer)
}

func (c *edgeIdleConn) Write(buffer []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(buffer)
}

func (c *edgeIdleConn) CloseRead() error {
	if connection, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return connection.CloseRead()
	}
	return nil
}

func (c *edgeIdleConn) CloseWrite() error {
	if connection, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return connection.CloseWrite()
	}
	return nil
}

// maxSourceBuckets bounds the memory held for per-source rate limiting.
const maxSourceBuckets = 65536

// maxBucketEvictions bounds the reclaim work one Allow call may do, so the
// accept path stays constant time no matter how large the table has grown.
const maxBucketEvictions = 8

// sourceRateLimiter applies a token bucket per source address. Buckets are held
// in an intrusive list ordered by last use so that expiry reclaims from the idle
// end instead of scanning every entry on the accept path.
type sourceRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	idle    time.Duration
	buckets map[netip.Addr]*sourceBucket
	newest  *sourceBucket
	oldest  *sourceBucket
}

type sourceBucket struct {
	source netip.Addr
	tokens float64
	last   time.Time
	newer  *sourceBucket
	older  *sourceBucket
}

func newSourceRateLimiter(perMinute, burst int) *sourceRateLimiter {
	rate := float64(perMinute) / 60
	// A bucket idle for longer than its own refill time has returned to full
	// burst, which makes it indistinguishable from a bucket that does not exist.
	// Dropping it therefore cannot change any later decision.
	idle := time.Hour
	if rate > 0 {
		idle = time.Duration(float64(burst) / rate * float64(time.Second))
	}
	return &sourceRateLimiter{rate: rate, burst: float64(burst), idle: idle, buckets: make(map[netip.Addr]*sourceBucket)}
}

func (l *sourceRateLimiter) Allow(source netip.Addr) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.evictIdle(now)
	bucket, exists := l.buckets[source]
	if exists {
		l.unlink(bucket)
	} else {
		bucket = l.admit(source, now)
	}
	bucket.tokens = min(l.burst, bucket.tokens+now.Sub(bucket.last).Seconds()*l.rate)
	bucket.last = now
	l.linkNewest(bucket)
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// evictIdle reclaims fully refilled buckets from the idle end of the list. The
// list is ordered by last use, so the walk stops at the first live bucket.
func (l *sourceRateLimiter) evictIdle(now time.Time) {
	for range maxBucketEvictions {
		if l.oldest == nil || now.Sub(l.oldest.last) < l.idle {
			return
		}
		l.remove(l.oldest)
	}
}

// admit creates a bucket for an unseen source, reclaiming a slot when the table
// is full. Refusing the new source instead would let any flood wide enough to
// fill the table lock out every client not already in it, turning a rate limit
// into a total outage.
func (l *sourceRateLimiter) admit(source netip.Addr, now time.Time) *sourceBucket {
	if len(l.buckets) >= maxSourceBuckets {
		l.reclaim(now)
	}
	bucket := &sourceBucket{source: source, tokens: l.burst, last: now}
	l.buckets[source] = bucket
	return bucket
}

// reclaim frees one slot by evicting whichever of the least recently used
// buckets has the most tokens left. A bucket back at full burst carries no
// state at all, so evicting it changes nothing, whereas evicting a source that
// is currently being throttled would hand it a fresh burst. Picking by tokens
// rather than by age alone keeps a flood from resetting the very sources the
// limiter is holding down. Only a table saturated with throttled sources forces
// a throttled victim, and aggregate load like that is what MaxConnections caps.
func (l *sourceRateLimiter) reclaim(now time.Time) {
	victim := l.oldest
	if victim == nil {
		return
	}
	available := func(bucket *sourceBucket) float64 {
		return min(l.burst, bucket.tokens+now.Sub(bucket.last).Seconds()*l.rate)
	}
	best := available(victim)
	candidate := victim.newer
	for range maxBucketEvictions - 1 {
		if candidate == nil || best >= l.burst {
			break
		}
		if tokens := available(candidate); tokens > best {
			victim, best = candidate, tokens
		}
		candidate = candidate.newer
	}
	l.remove(victim)
}

func (l *sourceRateLimiter) remove(bucket *sourceBucket) {
	l.unlink(bucket)
	delete(l.buckets, bucket.source)
}

func (l *sourceRateLimiter) linkNewest(bucket *sourceBucket) {
	bucket.newer = nil
	bucket.older = l.newest
	if l.newest != nil {
		l.newest.newer = bucket
	}
	l.newest = bucket
	if l.oldest == nil {
		l.oldest = bucket
	}
}

func (l *sourceRateLimiter) unlink(bucket *sourceBucket) {
	if bucket.newer != nil {
		bucket.newer.older = bucket.older
	} else if l.newest == bucket {
		l.newest = bucket.older
	}
	if bucket.older != nil {
		bucket.older.newer = bucket.newer
	} else if l.oldest == bucket {
		l.oldest = bucket.newer
	}
	bucket.newer = nil
	bucket.older = nil
}

// sameAddresses reports whether two resolved sets are equal, so a re-resolve
// that returns the same dead answers is not retried pointlessly.
func sameAddresses(left, right []netip.Addr) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
