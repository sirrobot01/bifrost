package exposure

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestSplicerPreservesTCPHalfClose(t *testing.T) {
	t.Parallel()

	backend := startReplyAfterEOFServer(t)
	splicer := startSplicer(t, backend)

	client, err := net.DialTCP("tcp6", nil, net.TCPAddrFromAddrPort(splicer.Address()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if _, err := io.WriteString(client, "request body"); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(response), "received: request body"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}

	status := splicer.Status()
	if status.ClientIPPreserved {
		t.Fatal("splice incorrectly reports client IP preservation")
	}
}

func TestSplicerEnforcesConnectionLimit(t *testing.T) {
	t.Parallel()

	backend := startBlockingServer(t)
	splicer := startSplicer(t, backend)

	first, err := net.DialTCP("tcp6", nil, net.TCPAddrFromAddrPort(splicer.Address()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	waitFor(t, func() bool { return splicer.Status().ActiveConnections == 1 })

	second, err := net.DialTCP("tcp6", nil, net.TCPAddrFromAddrPort(splicer.Address()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if err := second.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := second.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("limited connection read error = %v, want EOF", err)
	}
	if got := splicer.Status().Rejected; got != 1 {
		t.Fatalf("rejected connections = %d, want 1", got)
	}
}

func TestSplicerShutdownForcesExpiredDrain(t *testing.T) {
	t.Parallel()

	backend := startBlockingServer(t)
	splicer := startSplicer(t, backend)
	client, err := net.DialTCP("tcp6", nil, net.TCPAddrFromAddrPort(splicer.Address()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	waitFor(t, func() bool { return splicer.Status().ActiveConnections == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := splicer.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want deadline exceeded", err)
	}
	waitFor(t, func() bool { return splicer.Status().ActiveConnections == 0 })
}

func TestSplicersShareGlobalConnectionLimit(t *testing.T) {
	t.Parallel()

	backend := startBlockingServer(t)
	limiter, err := NewLimiter(1)
	if err != nil {
		t.Fatal(err)
	}
	firstSplicer := startConfiguredSplicer(t, backend, false, limiter)
	secondSplicer := startConfiguredSplicer(t, backend, false, limiter)
	first, err := net.DialTCP("tcp6", nil, net.TCPAddrFromAddrPort(firstSplicer.Address()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	waitFor(t, func() bool { return limiter.Active() == 1 })

	second, err := net.DialTCP("tcp6", nil, net.TCPAddrFromAddrPort(secondSplicer.Address()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if err := second.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := second.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("limited connection read error = %v, want EOF", err)
	}
	if secondSplicer.Status().Rejected != 1 {
		t.Fatalf("status = %+v", secondSplicer.Status())
	}
}

func startSplicer(t *testing.T, backend netip.AddrPort) *Splicer {
	return startConfiguredSplicer(t, backend, false, nil)
}

func startConfiguredSplicer(t *testing.T, backend netip.AddrPort, proxyProtocol bool, limiter *Limiter) *Splicer {
	t.Helper()
	splicer, err := Listen(t.Context(), Config{
		ServiceID:      "photos",
		ListenAddress:  netip.MustParseAddrPort("[::1]:0"),
		BackendAddress: backend,
		MaxConnections: 1,
		DialTimeout:    time.Second,
		IdleTimeout:    time.Second,
		ProxyProtocol:  proxyProtocol,
		GlobalLimiter:  limiter,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- splicer.Serve(serveContext) }()
	t.Cleanup(func() {
		cancelServe()
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		_ = splicer.Shutdown(shutdownContext)
		if err := <-serveErrors; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return splicer
}

func startReplyAfterEOFServer(t *testing.T) netip.AddrPort {
	t.Helper()
	return startTCP4Server(t, func(connection net.Conn) {
		defer func() { _ = connection.Close() }()
		request, err := io.ReadAll(connection)
		if err != nil {
			return
		}
		_, _ = io.WriteString(connection, "received: "+string(request))
	})
}

func startBlockingServer(t *testing.T) netip.AddrPort {
	t.Helper()
	return startTCP4Server(t, func(connection net.Conn) {
		defer func() { _ = connection.Close() }()
		_, _ = bufio.NewReader(connection).ReadString('\n')
	})
}

func startTCP4Server(t *testing.T, handle func(net.Conn)) netip.AddrPort {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go handle(connection)
		}
	}()
	return listener.Addr().(*net.TCPAddr).AddrPort()
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	config := Config{
		ServiceID:      "photos",
		ListenAddress:  netip.MustParseAddrPort("[::1]:443"),
		BackendAddress: netip.MustParseAddrPort("127.0.0.1:8080"),
		MaxConnections: 1,
		DialTimeout:    time.Second,
		IdleTimeout:    time.Second,
	}
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}

	config.ServiceID = ""
	if err := config.validate(); err == nil {
		t.Fatal("empty service ID was accepted")
	}
}
