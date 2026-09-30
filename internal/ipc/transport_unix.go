//go:build darwin || linux

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
)

func listenEndpoint(endpoint string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(endpoint); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("IPC endpoint exists and is not a socket: %s", endpoint)
		}
		if connection, dialErr := net.Dial("unix", endpoint); dialErr == nil {
			_ = connection.Close()
			return nil, fmt.Errorf("IPC endpoint is already active: %s", endpoint)
		}
		if err := os.Remove(endpoint); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, err
	}
	// The socket may cross owner/root/service identities. Possessing the path is
	// not authority: every request and response still requires HMAC, freshness,
	// and nonce verification.
	if err := os.Chmod(endpoint, 0o666); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return &unixListener{Listener: listener}, nil
}

func dialEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
}

type unixListener struct {
	net.Listener
	closeOnce sync.Once
	closeErr  error
}

func (l *unixListener) Close() error {
	l.closeOnce.Do(func() {
		// net.UnixListener owns unlinking its endpoint. A second removal can
		// delete a replacement server's socket during a concurrent restart.
		l.closeErr = l.Listener.Close()
	})
	return l.closeErr
}
