//go:build windows

package platform

import "testing"

func TestWindowsPipePath(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		`\\.\pipe\docker_engine`:         `\\.\pipe\docker_engine`,
		"npipe:////./pipe/docker_engine": `\\.\pipe\docker_engine`,
	} {
		if got := windowsPipePath(input); got != want {
			t.Errorf("windowsPipePath(%q) = %q, want %q", input, got, want)
		}
	}
}
