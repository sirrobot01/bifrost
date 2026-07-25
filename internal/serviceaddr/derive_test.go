package serviceaddr

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestDeriverAddress(t *testing.T) {
	t.Parallel()

	deriver, err := NewDeriver(bytes.Repeat([]byte{0x42}, minimumSecretSize))
	if err != nil {
		t.Fatal(err)
	}

	prefix := netip.MustParsePrefix("2001:db8:1234:5678::/64")
	address, err := deriver.Address(prefix, "photos.example.com", 0)
	if err != nil {
		t.Fatal(err)
	}

	if !prefix.Contains(address) {
		t.Fatalf("derived address %s is outside %s", address, prefix)
	}
	if address.As16()[8]&0x02 != 0 {
		t.Fatalf("derived address %s has the universal bit set", address)
	}
	if want := netip.MustParseAddr("2001:db8:1234:5678:21dc:e346:79d:e5d6"); address != want {
		t.Fatalf("derived address = %s, want %s", address, want)
	}

	again, err := deriver.Address(prefix, "photos.example.com", 0)
	if err != nil {
		t.Fatal(err)
	}
	if address != again {
		t.Fatalf("derivation is not stable: %s != %s", address, again)
	}
}

func TestDeriverAddressSeparatesInputs(t *testing.T) {
	t.Parallel()

	deriver, err := NewDeriver(bytes.Repeat([]byte{0x24}, minimumSecretSize))
	if err != nil {
		t.Fatal(err)
	}

	prefix := netip.MustParsePrefix("2001:db8:1::/64")
	base, err := deriver.Address(prefix, "photos.example.com", 0)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		prefix     netip.Prefix
		serviceID  string
		dadCounter uint32
	}{
		{name: "prefix", prefix: netip.MustParsePrefix("2001:db8:2::/64"), serviceID: "photos.example.com"},
		{name: "service", prefix: prefix, serviceID: "media.example.com"},
		{name: "DAD counter", prefix: prefix, serviceID: "photos.example.com", dadCounter: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			address, err := deriver.Address(test.prefix, test.serviceID, test.dadCounter)
			if err != nil {
				t.Fatal(err)
			}
			if address == base {
				t.Fatalf("input change produced the same address %s", address)
			}
		})
	}
}

func TestDeriverRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	validSecret := bytes.Repeat([]byte{0x42}, minimumSecretSize)
	validPrefix := netip.MustParsePrefix("2001:db8::/64")

	tests := []struct {
		name      string
		secret    []byte
		prefix    netip.Prefix
		serviceID string
	}{
		{name: "short secret", secret: validSecret[:minimumSecretSize-1], prefix: validPrefix, serviceID: "photos"},
		{name: "empty service ID", secret: validSecret, prefix: validPrefix},
		{name: "IPv4 prefix", secret: validSecret, prefix: netip.MustParsePrefix("192.0.2.0/24"), serviceID: "photos"},
		{name: "non-64 prefix", secret: validSecret, prefix: netip.MustParsePrefix("2001:db8::/56"), serviceID: "photos"},
		{name: "invalid prefix", secret: validSecret, serviceID: "photos"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			deriver, err := NewDeriver(test.secret)
			if err != nil {
				if test.name != "short secret" {
					t.Fatalf("NewDeriver: %v", err)
				}
				return
			}

			if _, err := deriver.Address(test.prefix, test.serviceID, 0); err == nil {
				t.Fatal("Address succeeded with invalid input")
			}
		})
	}
}
