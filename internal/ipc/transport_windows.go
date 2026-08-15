//go:build windows

package ipc

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

func listenEndpoint(endpoint string) (net.Listener, error) {
	return winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)",
		MessageMode:        false,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
}

func dialEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}
