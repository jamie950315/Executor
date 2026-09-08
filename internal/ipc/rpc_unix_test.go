//go:build darwin || linux

package ipc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRPCServerAndClientUseAuthenticatedUnixSocket(t *testing.T) {
	t.Parallel()

	endpoint := shortSocketPath(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	server := NewRPCServer(endpoint, key, func(_ context.Context, method string, params []byte) (any, error) {
		if method != "device.status" {
			return nil, errors.New("unexpected method")
		}
		return map[string]any{"state": "ready", "request": string(params)}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ready := make(chan error, 1)
	go func() { ready <- server.Serve(ctx) }()
	waitForEndpoint(t, endpoint)

	info, err := os.Stat(endpoint)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode = %o, want 666 for cross-user authenticated RPC", info.Mode().Perm())
	}

	client := NewRPCClient(endpoint, key)
	var result struct {
		State   string `json:"state"`
		Request string `json:"request"`
	}
	if err := client.Call(context.Background(), "device.status", map[string]string{"source": "agent"}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.State != "ready" || result.Request != `{"source":"agent"}` {
		t.Fatalf("unexpected response: %#v", result)
	}

	cancel()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("serve shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestWindowsPipeDescriptorAllowsAuthenticatedServiceAccountsButNotAnonymous(t *testing.T) {
	t.Parallel()
	if !strings.Contains(windowsPipeSecurityDescriptor, ";;;AU)") {
		t.Fatalf("pipe descriptor does not allow authenticated service accounts: %s", windowsPipeSecurityDescriptor)
	}
	if strings.Contains(windowsPipeSecurityDescriptor, ";;;WD)") || strings.Contains(windowsPipeSecurityDescriptor, ";;;AN)") {
		t.Fatalf("pipe descriptor allows anonymous/everyone: %s", windowsPipeSecurityDescriptor)
	}
}

func TestRPCServerRejectsClientWithWrongKey(t *testing.T) {
	t.Parallel()

	endpoint := shortSocketPath(t)
	serverKey := []byte("0123456789abcdef0123456789abcdef")
	server := NewRPCServer(endpoint, serverKey, func(context.Context, string, []byte) (any, error) {
		return map[string]bool{"called": true}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Serve(ctx) }()
	waitForEndpoint(t, endpoint)

	client := NewRPCClient(endpoint, []byte("abcdef0123456789abcdef0123456789"))
	if err := client.Call(context.Background(), "device.status", nil, &struct{}{}); err == nil {
		t.Fatal("call with wrong key succeeded")
	}
}

func TestRPCReservedHealthBypassesHandlerAfterAuthentication(t *testing.T) {
	endpoint := shortSocketPath(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	server := NewRPCServer(endpoint, key, func(context.Context, string, []byte) (any, error) {
		return nil, errors.New("application handler must not be called for health")
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Serve(ctx) }()
	waitForEndpoint(t, endpoint)
	var result struct {
		Protocol string `json:"protocol"`
	}
	if err := NewRPCClient(endpoint, key).Call(ctx, "executor.health", struct{}{}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Protocol != "executor-ipc-v1" {
		t.Fatalf("unexpected IPC health marker %q", result.Protocol)
	}
	if err := NewRPCClient(endpoint, key).Health(ctx); err != nil {
		t.Fatalf("valid authenticated health: %v", err)
	}
	if err := NewRPCClient(endpoint, []byte(strings.Repeat("x", 32))).Call(ctx, "executor.health", struct{}{}, &result); err == nil {
		t.Fatal("health accepted unauthenticated request")
	}
}

func TestRPCHealthRejectsInvalidAuthenticatedResponses(t *testing.T) {
	for _, payload := range []any{
		nil,
		map[string]string{},
		map[string]string{"protocol": "foreign-service"},
		map[string]string{"protocol": healthProtocol, "unexpected": "field"},
		"ready",
	} {
		endpoint := shortSocketPath(t)
		key := []byte("0123456789abcdef0123456789abcdef")
		listener, err := listenEndpoint(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		server := NewRPCServer(endpoint, key, nil)
		done := make(chan error, 1)
		go func() {
			defer listener.Close()
			connection, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer connection.Close()
			var request Message
			if err := decodeLimited(connection, &request); err != nil {
				done <- err
				return
			}
			done <- server.writeResponse(connection, request, payload, "")
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = NewRPCClient(endpoint, key).Health(ctx)
		cancel()
		if err == nil {
			t.Errorf("invalid health response accepted: %#v", payload)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRPCServerCancelsHandlerWhenClientDisconnects(t *testing.T) {
	t.Parallel()

	endpoint := shortSocketPath(t)
	key := []byte("00112233445566778899aabbccddeeff")
	handlerStarted := make(chan struct{})
	handlerCanceled := make(chan struct{})
	server := NewRPCServer(endpoint, key, func(ctx context.Context, _ string, _ []byte) (any, error) {
		close(handlerStarted)
		<-ctx.Done()
		close(handlerCanceled)
		return nil, ctx.Err()
	})
	serverCtx, stopServer := context.WithCancel(context.Background())
	t.Cleanup(stopServer)
	go func() { _ = server.Serve(serverCtx) }()
	waitForEndpoint(t, endpoint)

	callCtx, cancelCall := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		callDone <- NewRPCClient(endpoint, key).Call(callCtx, "desktop.actions", struct{}{}, &struct{}{})
	}()
	<-handlerStarted
	cancelCall()
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("canceled IPC call succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("IPC client did not close its connection after context cancellation")
	}
	select {
	case <-handlerCanceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("server handler continued after client disconnected")
	}
}

func waitForEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(endpoint); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("endpoint did not appear: %s", endpoint)
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "executor-ipc-")
	if err != nil {
		t.Fatalf("create short socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "broker.sock")
}
