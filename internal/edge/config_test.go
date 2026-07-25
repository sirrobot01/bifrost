package edge

import "testing"

func TestEdgeConfigValidation(t *testing.T) {
	t.Parallel()

	config := Config{Version: 1, Listen: ":443", Allow: []string{"media.example.com"}, KeyFile: "/key", StaticMaps: map[string]string{"2222": "ssh.example.com:22"}}
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEdgeConfigRejectsStaticTLSPortConflict(t *testing.T) {
	t.Parallel()

	config := Config{Version: 1, Listen: ":443", Allow: []string{"media.example.com"}, KeyFile: "/key", StaticMaps: map[string]string{"443": "ssh.example.com:22"}}
	config.applyDefaults()
	if err := config.Validate(); err == nil {
		t.Fatal("config accepted a conflicting port")
	}
}

func TestExampleConfigLoads(t *testing.T) {
	t.Parallel()

	if _, err := LoadConfig("../../configs/edge.example.yaml"); err != nil {
		t.Fatal(err)
	}
}
