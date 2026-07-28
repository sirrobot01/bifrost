package cli

import (
	"testing"

	"github.com/sirrobot01/bifrost/internal/config"
)

// A configuration that lists edges only under the plural key is valid and
// documented. Both the resolution and the prober selection have to read it,
// because neither can fall back to the singular key that was never set.
func TestEdgeAddressesReadThePluralKey(t *testing.T) {
	t.Parallel()

	configFile := config.Config{Edge: config.Edge{
		Enabled:       true,
		IPv4Addresses: []string{"203.0.113.10", "198.51.100.4"},
	}}
	configFile.ApplyDefaults()

	addresses, err := edgeIPv4Addresses(configFile)
	if err != nil {
		t.Fatalf("edgeIPv4Addresses: %v", err)
	}
	if len(addresses) != 2 || addresses[0].String() != "203.0.113.10" {
		t.Fatalf("addresses = %v", addresses)
	}

	prober, err := externalProber(configFile)
	if err != nil {
		t.Fatalf("externalProber: %v", err)
	}
	if prober == nil {
		t.Fatal("a configured edge must act as the external prober")
	}
}

// The singular key is the older spelling. ApplyDefaults folds it into the list,
// so the same readers cover it without a second code path.
func TestEdgeAddressesFoldTheSingularKey(t *testing.T) {
	t.Parallel()

	configFile := config.Config{Edge: config.Edge{Enabled: true, IPv4Address: "203.0.113.10"}}
	configFile.ApplyDefaults()

	addresses, err := edgeIPv4Addresses(configFile)
	if err != nil {
		t.Fatalf("edgeIPv4Addresses: %v", err)
	}
	if len(addresses) != 1 || addresses[0].String() != "203.0.113.10" {
		t.Fatalf("addresses = %v", addresses)
	}
}
