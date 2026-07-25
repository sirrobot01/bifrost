package edgeauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"sync"
	"time"
)

const HeaderSize = 87

var signature = [12]byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}
var contextLabel = []byte("bifrost-edge-proxy-v1\x00")

type Metadata struct {
	Source      netip.AddrPort
	Destination netip.AddrPort
}

type Verifier struct {
	key    []byte
	skew   time.Duration
	now    func() time.Time
	mu     sync.Mutex
	nonces map[[16]byte]time.Time
}

func NewVerifier(key []byte, skew time.Duration) (*Verifier, error) {
	if len(key) < 32 {
		return nil, errors.New("edge authentication key must be at least 32 bytes")
	}
	if skew <= 0 {
		return nil, errors.New("edge authentication clock skew must be positive")
	}
	return &Verifier{key: append([]byte(nil), key...), skew: skew, now: time.Now, nonces: make(map[[16]byte]time.Time)}, nil
}

func Build(key []byte, serviceID string, source, destination netip.AddrPort, now time.Time) ([]byte, error) {
	return build(key, serviceID, source, destination, now, rand.Reader)
}

func build(key []byte, serviceID string, source, destination netip.AddrPort, now time.Time, random io.Reader) ([]byte, error) {
	if len(key) < 32 || serviceID == "" {
		return nil, errors.New("edge authentication key and service ID are required")
	}
	if !source.IsValid() || !destination.IsValid() || !source.Addr().Is4() || !destination.Addr().Is4() {
		return nil, errors.New("edge metadata requires IPv4 source and destination addresses")
	}
	header := make([]byte, HeaderSize)
	copy(header, signature[:])
	header[12] = 0x21
	header[13] = 0x11
	binary.BigEndian.PutUint16(header[14:16], HeaderSize-16)
	sourceAddress := source.Addr().As4()
	destinationAddress := destination.Addr().As4()
	copy(header[16:20], sourceAddress[:])
	copy(header[20:24], destinationAddress[:])
	binary.BigEndian.PutUint16(header[24:26], source.Port())
	binary.BigEndian.PutUint16(header[26:28], destination.Port())
	header[28] = 0xe0
	binary.BigEndian.PutUint16(header[29:31], 56)
	binary.BigEndian.PutUint64(header[31:39], uint64(now.Unix()))
	if _, err := io.ReadFull(random, header[39:55]); err != nil {
		return nil, err
	}
	mac := authenticationCode(key, serviceID, header[:55])
	copy(header[55:], mac)
	return header, nil
}

func (v *Verifier) Verify(serviceID string, header []byte) (Metadata, error) {
	if len(header) != HeaderSize || subtle.ConstantTimeCompare(header[:12], signature[:]) != 1 || header[12] != 0x21 || header[13] != 0x11 || binary.BigEndian.Uint16(header[14:16]) != HeaderSize-16 || header[28] != 0xe0 || binary.BigEndian.Uint16(header[29:31]) != 56 {
		return Metadata{}, errors.New("invalid authenticated PROXY v2 header")
	}
	expected := authenticationCode(v.key, serviceID, header[:55])
	if subtle.ConstantTimeCompare(expected, header[55:]) != 1 {
		return Metadata{}, errors.New("authenticated PROXY v2 signature is invalid")
	}
	timestamp := time.Unix(int64(binary.BigEndian.Uint64(header[31:39])), 0)
	now := v.now()
	if timestamp.Before(now.Add(-v.skew)) || timestamp.After(now.Add(v.skew)) {
		return Metadata{}, errors.New("authenticated PROXY v2 timestamp is outside the allowed skew")
	}
	var nonce [16]byte
	copy(nonce[:], header[39:55])
	v.mu.Lock()
	for existing, expires := range v.nonces {
		if !now.Before(expires) {
			delete(v.nonces, existing)
		}
	}
	if _, replayed := v.nonces[nonce]; replayed {
		v.mu.Unlock()
		return Metadata{}, errors.New("authenticated PROXY v2 nonce was replayed")
	}
	v.nonces[nonce] = timestamp.Add(v.skew)
	v.mu.Unlock()

	sourceAddress := [4]byte(header[16:20])
	destinationAddress := [4]byte(header[20:24])
	return Metadata{
		Source:      netip.AddrPortFrom(netip.AddrFrom4(sourceAddress), binary.BigEndian.Uint16(header[24:26])),
		Destination: netip.AddrPortFrom(netip.AddrFrom4(destinationAddress), binary.BigEndian.Uint16(header[26:28])),
	}, nil
}

func HasSignature(header []byte) bool {
	return len(header) >= len(signature) && subtle.ConstantTimeCompare(header[:12], signature[:]) == 1
}

func authenticationCode(key []byte, serviceID string, header []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(contextLabel)
	serviceLength := [4]byte{}
	binary.BigEndian.PutUint32(serviceLength[:], uint32(len(serviceID)))
	mac.Write(serviceLength[:])
	mac.Write([]byte(serviceID))
	mac.Write(header)
	return mac.Sum(nil)
}
