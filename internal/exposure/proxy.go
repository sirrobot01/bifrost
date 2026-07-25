package exposure

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
)

var proxyV2Signature = [12]byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}

func writeProxyV2(destination io.Writer, source net.Conn) error {
	remote, ok := source.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return errors.New("client remote address is not TCP")
	}
	local, ok := source.LocalAddr().(*net.TCPAddr)
	if !ok {
		return errors.New("client local address is not TCP")
	}
	return writeProxyV2Addresses(destination, remote.AddrPort(), local.AddrPort())
}

func writeProxyV2Addresses(destination io.Writer, source, target netip.AddrPort) error {
	if !source.IsValid() || !target.IsValid() || source.Addr().BitLen() != target.Addr().BitLen() {
		return errors.New("PROXY v2 addresses must be valid and from the same family")
	}
	addressLength := 12
	family := byte(0x11)
	if source.Addr().Is6() {
		addressLength = 36
		family = 0x21
	}
	header := make([]byte, 16+addressLength)
	copy(header, proxyV2Signature[:])
	header[12] = 0x21
	header[13] = family
	binary.BigEndian.PutUint16(header[14:16], uint16(addressLength))
	if source.Addr().Is4() {
		sourceAddress := source.Addr().As4()
		targetAddress := target.Addr().As4()
		copy(header[16:20], sourceAddress[:])
		copy(header[20:24], targetAddress[:])
		binary.BigEndian.PutUint16(header[24:26], source.Port())
		binary.BigEndian.PutUint16(header[26:28], target.Port())
	} else {
		sourceAddress := source.Addr().As16()
		targetAddress := target.Addr().As16()
		copy(header[16:32], sourceAddress[:])
		copy(header[32:48], targetAddress[:])
		binary.BigEndian.PutUint16(header[48:50], source.Port())
		binary.BigEndian.PutUint16(header[50:52], target.Port())
	}
	written, err := destination.Write(header)
	if err == nil && written != len(header) {
		return io.ErrShortWrite
	}
	return err
}
