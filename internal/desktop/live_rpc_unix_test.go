//go:build darwin || linux

package desktop

import (
	"context"
	"github.com/jamie950315/executor/internal/ipc"
	"testing"
)

func TestHelperCaptureEpochRejectsAnotherTransportMutation(t *testing.T) {
	endpoint := helperSocketPath(t)
	key := []byte("1234567890abcdef1234567890abcdef")
	gui := &captureHelperDesktop{png: encodeTestPNG(t, 2, 2)}
	authority := NewInputAuthority()
	server := NewHelperRPCServer(endpoint, key, &helperTerminal{}, &helperFilesystem{}, gui, HelperOptions{Authority: authority})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	waitForHelperEndpoint(t, endpoint)
	first, second := ipc.NewRPCClient(endpoint, key), ipc.NewRPCClient(endpoint, key)
	var capture RPCDesktopCapture
	if err := first.Call(ctx, RPCMethodDesktopCapture, struct{}{}, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Epoch == 0 {
		t.Fatal("capture omitted helper epoch")
	}
	if err := second.Call(ctx, RPCMethodDesktopMouse, RPCDesktopMouseParams{Action: MouseAction{Type: MouseActionMove}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := first.Call(ctx, RPCMethodDesktopActions, RPCDesktopActionsParams{Actions: []Action{{Type: ActionClick}}, ExpectedEpoch: capture.Epoch}, nil); err == nil {
		t.Fatal("old capture survived another transport's control")
	}
	authority.SetLiveControl(true)
	if err := first.Call(ctx, RPCMethodDesktopMouse, RPCDesktopMouseParams{Action: MouseAction{Type: MouseActionMove}}, nil); err == nil {
		t.Fatal("snapshot input competed with live input")
	}
	authority.SetLiveControl(false)
}
