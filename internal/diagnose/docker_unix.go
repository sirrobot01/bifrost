//go:build unix

package diagnose

import (
	"context"
	"net"
	"time"
)

func dialDockerSocket(ctx context.Context, socket string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return dialer.DialContext(ctx, "unix", socket)
}
