//go:build unix

package config

func defaultConfigDirectory() string { return "/etc/bifrost" }
func defaultStateDirectory() string  { return "/var/lib/bifrost" }
func defaultDockerSocket() string    { return "/var/run/docker.sock" }
