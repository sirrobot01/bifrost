package exposure

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

type addressedWriter struct {
	bytes.Buffer
	remote net.Addr
	local  net.Addr
}

func (c *addressedWriter) Read([]byte) (int, error)         { return 0, nil }
func (c *addressedWriter) Close() error                     { return nil }
func (c *addressedWriter) LocalAddr() net.Addr              { return c.local }
func (c *addressedWriter) RemoteAddr() net.Addr             { return c.remote }
func (c *addressedWriter) SetDeadline(time.Time) error      { return nil }
func (c *addressedWriter) SetReadDeadline(time.Time) error  { return nil }
func (c *addressedWriter) SetWriteDeadline(time.Time) error { return nil }

func TestWriteProxyV2IPv6(t *testing.T) {
	t.Parallel()

	connection := &addressedWriter{
		remote: &net.TCPAddr{IP: net.ParseIP("2001:db8::10"), Port: 12345},
		local:  &net.TCPAddr{IP: net.ParseIP("2001:db8::20"), Port: 443},
	}
	var output bytes.Buffer
	if err := writeProxyV2(&output, connection); err != nil {
		t.Fatal(err)
	}
	header := output.Bytes()
	if len(header) != 52 || !bytes.Equal(header[:12], proxyV2Signature[:]) || header[12] != 0x21 || header[13] != 0x21 {
		t.Fatalf("header = %x", header)
	}
}

func TestSplicerEmitsProxyV2BeforePayload(t *testing.T) {
	t.Parallel()

	received := make(chan []byte, 1)
	backend := startTCP4Server(t, func(connection net.Conn) {
		defer func() { _ = connection.Close() }()
		content, _ := io.ReadAll(connection)
		received <- content
	})
	splicer := startConfiguredSplicer(t, backend, true, nil)
	client, err := net.DialTCP("tcp6", nil, net.TCPAddrFromAddrPort(splicer.Address()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	content := <-received
	if len(content) != 52+len("payload") || !bytes.Equal(content[:12], proxyV2Signature[:]) || string(content[52:]) != "payload" {
		t.Fatalf("content = %x", content)
	}
	if !splicer.Status().ClientIPPreserved {
		t.Fatal("splicer did not report PROXY metadata preservation")
	}
}
