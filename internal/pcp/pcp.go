// Package pcp requests inbound pinholes from a customer-edge router using the
// Port Control Protocol, RFC 6887.
//
// The router is the one hop Bifrost cannot configure by any other means, and
// the manual pinhole step is where real deployments lose days. PCP is the only
// standard that asks for an IPv6 pinhole rather than an IPv4 port mapping, and
// support is patchy, so every operation here is best effort: a router that
// stays silent, refuses, or answers with a result the operator should see
// leaves the deployment exactly as it was, with advisory output as the
// fallback.
package pcp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	version      = 2
	opcodeMap    = 1
	requestSize  = 24
	mapPayload   = 36
	responseSize = 1100
	serverPort   = 5351
	// protocolTCP is the IANA number carried in a MAP request.
	protocolTCP = 6
)

// Result codes worth distinguishing, from RFC 6887 section 7.4.
const (
	resultSuccess              = 0
	resultUnsupportedVersion   = 1
	resultNotAuthorized        = 2
	resultNoResources          = 8
	resultUnsupportedProtocol  = 9
	resultUnsupportedOpcode    = 4
	resultAddressMismatch      = 12
	resultCannotProvideExtenal = 11
)

// ErrUnsupported reports that the router did not answer or does not speak PCP.
// It is not a failure of the deployment, only of this optimization.
var ErrUnsupported = errors.New("no PCP server answered")

// Mapping is one requested pinhole.
type Mapping struct {
	// Internal is the address traffic should reach.
	Internal netip.Addr
	// Port is the TCP port, used for both internal and suggested external.
	Port uint16
	// Lifetime is how long the router should hold the pinhole.
	Lifetime time.Duration
	// Nonce identifies this mapping across renewals. RFC 6887 requires the
	// same nonce to refresh rather than create a second mapping.
	Nonce [12]byte
}

// Client talks to one PCP server, normally the default gateway.
type Client struct {
	server  netip.Addr
	timeout time.Duration
	// serverPortOverride lets tests point at a local server. Production always
	// uses the assigned PCP port.
	serverPortOverride uint16
}

func NewClient(server netip.Addr) *Client {
	return &Client{server: server, timeout: 3 * time.Second}
}

// Request asks the router to permit inbound TCP to the mapping. It returns the
// lifetime the router granted, which may be shorter than requested.
func (c *Client) Request(ctx context.Context, mapping Mapping) (time.Duration, error) {
	if !mapping.Internal.Is6() {
		return 0, errors.New("PCP mappings here are IPv6 pinholes")
	}
	if mapping.Port == 0 {
		return 0, errors.New("a mapping needs a port")
	}

	payload := buildMapRequest(mapping)
	response, err := c.exchange(ctx, payload)
	if err != nil {
		return 0, err
	}
	if len(response) < 24 {
		return 0, fmt.Errorf("%w: response was %d bytes", ErrUnsupported, len(response))
	}
	if response[0] != version {
		return 0, fmt.Errorf("%w: server speaks PCP version %d", ErrUnsupported, response[0])
	}
	if response[1]&0x80 == 0 {
		return 0, fmt.Errorf("%w: reply was not marked as a response", ErrUnsupported)
	}
	if code := response[3]; code != resultSuccess {
		return 0, resultError(code)
	}
	granted := time.Duration(binary.BigEndian.Uint32(response[8:12])) * time.Second
	return granted, nil
}

// Release asks the router to drop the mapping, which RFC 6887 expresses as a
// request with a zero lifetime.
func (c *Client) Release(ctx context.Context, mapping Mapping) error {
	mapping.Lifetime = 0
	_, err := c.Request(ctx, mapping)
	return err
}

func (c *Client) exchange(ctx context.Context, payload []byte) ([]byte, error) {
	dialer := &net.Dialer{Timeout: c.timeout}
	port := uint16(serverPort)
	if c.serverPortOverride != 0 {
		port = c.serverPortOverride
	}
	connection, err := dialer.DialContext(ctx, "udp", netip.AddrPortFrom(c.server, port).String())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	defer func() { _ = connection.Close() }()

	deadline := time.Now().Add(c.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if _, err := connection.Write(payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	buffer := make([]byte, responseSize)
	read, err := connection.Read(buffer)
	if err != nil {
		// Silence is the common case on routers without PCP.
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return buffer[:read], nil
}

// buildMapRequest renders a MAP request per RFC 6887 section 11.1.
func buildMapRequest(mapping Mapping) []byte {
	payload := make([]byte, requestSize+mapPayload)
	payload[0] = version
	payload[1] = opcodeMap
	binary.BigEndian.PutUint32(payload[4:8], uint32(mapping.Lifetime.Seconds()))
	internal := mapping.Internal.As16()
	copy(payload[8:24], internal[:])

	body := payload[24:]
	copy(body[0:12], mapping.Nonce[:])
	body[12] = protocolTCP
	binary.BigEndian.PutUint16(body[16:18], mapping.Port)
	binary.BigEndian.PutUint16(body[18:20], mapping.Port)
	// Suggested external address: the same IPv6 address, since a pinhole does
	// not translate.
	copy(body[20:36], internal[:])
	return payload
}

func resultError(code uint8) error {
	switch code {
	case resultUnsupportedVersion:
		return fmt.Errorf("%w: the router rejected PCP version %d", ErrUnsupported, version)
	case resultUnsupportedOpcode, resultUnsupportedProtocol:
		return fmt.Errorf("%w: the router does not support this request", ErrUnsupported)
	case resultNotAuthorized:
		return errors.New("the router refused the request; PCP may be disabled in its configuration")
	case resultNoResources:
		return errors.New("the router has no capacity for another mapping")
	case resultAddressMismatch:
		return errors.New("the router saw a different source address than the one requested, which usually means a NAT sits in between")
	case resultCannotProvideExtenal:
		return errors.New("the router cannot provide the requested external address")
	default:
		return fmt.Errorf("the router returned PCP result code %d", code)
	}
}
