package edge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const maxClientHelloSize = 64 << 10

func readClientHello(connection net.Conn, timeout time.Duration) ([]byte, string, error) {
	if err := connection.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, "", err
	}
	defer func() { _ = connection.SetReadDeadline(time.Time{}) }()

	var wire []byte
	var handshake []byte
	for {
		if len(handshake) >= 4 {
			handshakeLength := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
			if len(handshake) >= 4+handshakeLength {
				break
			}
		}
		recordHeader := make([]byte, 5)
		if _, err := io.ReadFull(connection, recordHeader); err != nil {
			return nil, "", fmt.Errorf("read TLS record header: %w", err)
		}
		if recordHeader[0] != 22 {
			return nil, "", errors.New("connection did not begin with a TLS handshake record")
		}
		recordLength := int(binary.BigEndian.Uint16(recordHeader[3:5]))
		if recordLength == 0 || len(wire)+5+recordLength > maxClientHelloSize {
			return nil, "", errors.New("TLS ClientHello exceeds the configured limit")
		}
		payload := make([]byte, recordLength)
		if _, err := io.ReadFull(connection, payload); err != nil {
			return nil, "", fmt.Errorf("read TLS record payload: %w", err)
		}
		wire = append(wire, recordHeader...)
		wire = append(wire, payload...)
		handshake = append(handshake, payload...)
	}
	if handshake[0] != 1 {
		return nil, "", errors.New("first TLS handshake message is not ClientHello")
	}
	handshakeLength := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
	serverName, err := parseServerName(handshake[4 : 4+handshakeLength])
	if err != nil {
		return nil, "", err
	}
	return wire, serverName, nil
}

func parseServerName(clientHello []byte) (string, error) {
	if len(clientHello) < 35 {
		return "", errors.New("TLS ClientHello is truncated")
	}
	offset := 34
	sessionLength := int(clientHello[offset])
	offset++
	if offset+sessionLength+2 > len(clientHello) {
		return "", errors.New("TLS ClientHello session ID is truncated")
	}
	offset += sessionLength
	cipherLength := int(binary.BigEndian.Uint16(clientHello[offset : offset+2]))
	offset += 2
	if cipherLength == 0 || cipherLength%2 != 0 || offset+cipherLength+1 > len(clientHello) {
		return "", errors.New("TLS ClientHello cipher suites are invalid")
	}
	offset += cipherLength
	compressionLength := int(clientHello[offset])
	offset++
	if offset+compressionLength+2 > len(clientHello) {
		return "", errors.New("TLS ClientHello compression methods are truncated")
	}
	offset += compressionLength
	extensionsLength := int(binary.BigEndian.Uint16(clientHello[offset : offset+2]))
	offset += 2
	if offset+extensionsLength != len(clientHello) {
		return "", errors.New("TLS ClientHello extensions are truncated")
	}
	end := offset + extensionsLength
	for offset+4 <= end {
		extensionType := binary.BigEndian.Uint16(clientHello[offset : offset+2])
		extensionLength := int(binary.BigEndian.Uint16(clientHello[offset+2 : offset+4]))
		offset += 4
		if offset+extensionLength > end {
			return "", errors.New("TLS ClientHello extension is truncated")
		}
		if extensionType == 0 {
			return parseServerNameExtension(clientHello[offset : offset+extensionLength])
		}
		offset += extensionLength
	}
	return "", errors.New("TLS ClientHello has no SNI hostname")
}

func parseServerNameExtension(extension []byte) (string, error) {
	if len(extension) < 5 || int(binary.BigEndian.Uint16(extension[:2])) != len(extension)-2 {
		return "", errors.New("TLS SNI extension is invalid")
	}
	offset := 2
	for offset+3 <= len(extension) {
		nameType := extension[offset]
		nameLength := int(binary.BigEndian.Uint16(extension[offset+1 : offset+3]))
		offset += 3
		if offset+nameLength > len(extension) {
			return "", errors.New("TLS SNI name is truncated")
		}
		if nameType == 0 {
			return normalizeHostname(string(extension[offset : offset+nameLength]))
		}
		offset += nameLength
	}
	return "", errors.New("TLS SNI extension has no hostname")
}
