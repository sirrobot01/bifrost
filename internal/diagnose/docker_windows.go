//go:build windows

package diagnose

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

func dialDockerSocket(ctx context.Context, socket string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, socket)
}
