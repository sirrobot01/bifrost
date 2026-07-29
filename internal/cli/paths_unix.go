//go:build unix

package cli

func platformConfigDirectory() string       { return "/etc/bifrost" }
func elevatedCommand(command string) string { return "sudo " + command }
