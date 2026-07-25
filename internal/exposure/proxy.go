package exposure

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
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
	remoteIP := remote.IP.To16()
	localIP := local.IP.To16()
	if remoteIP == nil || localIP == nil || remote.IP.To4() != nil || local.IP.To4() != nil {
		return errors.New("PROXY v2 source and destination must be IPv6")
	}
	header := make([]byte, 16+36)
	copy(header, proxyV2Signature[:])
	header[12] = 0x21
	header[13] = 0x21
	binary.BigEndian.PutUint16(header[14:16], 36)
	copy(header[16:32], remoteIP)
	copy(header[32:48], localIP)
	binary.BigEndian.PutUint16(header[48:50], uint16(remote.Port))
	binary.BigEndian.PutUint16(header[50:52], uint16(local.Port))
	written, err := destination.Write(header)
	if err == nil && written != len(header) {
		return io.ErrShortWrite
	}
	return err
}
