package serviceaddr

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/netip"
)

const minimumSecretSize = 32

var derivationContext = []byte("bifrost-service-address-v1\x00")

// Deriver creates stable service addresses without exposing hardware identifiers.
type Deriver struct {
	secret []byte
}

// NewDeriver returns a Deriver backed by a host-local secret.
func NewDeriver(secret []byte) (Deriver, error) {
	if len(secret) < minimumSecretSize {
		return Deriver{}, errors.New("service address secret must be at least 32 bytes")
	}

	return Deriver{secret: append([]byte(nil), secret...)}, nil
}

// Address derives an IPv6 address for serviceID within an on-link /64.
func (d Deriver) Address(prefix netip.Prefix, serviceID string, dadCounter uint32) (netip.Addr, error) {
	if len(d.secret) < minimumSecretSize {
		return netip.Addr{}, errors.New("service address deriver is not initialized")
	}
	if serviceID == "" {
		return netip.Addr{}, errors.New("service ID is required")
	}
	if !prefix.IsValid() || !prefix.Addr().Is6() || prefix.Bits() != 64 {
		return netip.Addr{}, errors.New("service address prefix must be an IPv6 /64")
	}

	prefix = prefix.Masked()
	network := prefix.Addr().As16()
	serviceLength := [8]byte{}
	counter := [4]byte{}
	binary.BigEndian.PutUint64(serviceLength[:], uint64(len(serviceID)))
	binary.BigEndian.PutUint32(counter[:], dadCounter)

	mac := hmac.New(sha256.New, d.secret)
	mac.Write(derivationContext)
	mac.Write(network[:8])
	mac.Write(serviceLength[:])
	mac.Write([]byte(serviceID))
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	copy(network[8:], sum[:8])
	network[8] &^= 0x02

	return netip.AddrFrom16(network), nil
}
