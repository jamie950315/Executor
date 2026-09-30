package ipc

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestServerCancellationClosesConnectionBeforeRequestIsDecoded(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	t.Cleanup(func() { _ = serverConnection.Close(); _ = clientConnection.Close() })
	server := NewRPCServer("unused", []byte("0123456789abcdef0123456789abcdef"), func(context.Context, string, []byte) (any, error) {
		t.Error("incomplete request reached handler")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { server.handleConnection(ctx, serverConnection); close(done) }()
	// Synchronize with decoding through the real connection, then leave its JSON
	// value incomplete. Cancellation must release the accepted connection.
	if _, err := clientConnection.Write([]byte(`{"id":`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("incomplete IPC request remained open after server cancellation")
	}
}
