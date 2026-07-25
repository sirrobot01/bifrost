package edge

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestReadClientHelloExtractsSNI(t *testing.T) {
	t.Parallel()

	hello := testClientHello("media.example.com")
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	go func() { _, _ = client.Write(hello) }()
	wire, name, err := readClientHello(server, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if name != "media.example.com" || string(wire) != string(hello) {
		t.Fatalf("name = %q, wire length = %d", name, len(wire))
	}
}

func testClientHello(name string) []byte {
	serverName := make([]byte, 5+len(name))
	binary.BigEndian.PutUint16(serverName[:2], uint16(3+len(name)))
	serverName[2] = 0
	binary.BigEndian.PutUint16(serverName[3:5], uint16(len(name)))
	copy(serverName[5:], name)
	extension := make([]byte, 4+len(serverName))
	binary.BigEndian.PutUint16(extension[2:4], uint16(len(serverName)))
	copy(extension[4:], serverName)
	body := make([]byte, 2+32+1+2+2+1+1+2+len(extension))
	offset := 34
	body[offset] = 0
	offset++
	binary.BigEndian.PutUint16(body[offset:offset+2], 2)
	offset += 2
	binary.BigEndian.PutUint16(body[offset:offset+2], 0x1301)
	offset += 2
	body[offset] = 1
	body[offset+1] = 0
	offset += 2
	binary.BigEndian.PutUint16(body[offset:offset+2], uint16(len(extension)))
	offset += 2
	copy(body[offset:], extension)
	handshake := make([]byte, 4+len(body))
	handshake[0] = 1
	handshake[1] = byte(len(body) >> 16)
	handshake[2] = byte(len(body) >> 8)
	handshake[3] = byte(len(body))
	copy(handshake[4:], body)
	record := make([]byte, 5+len(handshake))
	record[0] = 22
	record[1] = 3
	record[2] = 1
	binary.BigEndian.PutUint16(record[3:5], uint16(len(handshake)))
	copy(record[5:], handshake)
	return record
}
