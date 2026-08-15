package dashboard

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/secrets"
)

type fakeLifecycle struct{}

func (fakeLifecycle) Kill(context.Context) (control.Result, error) {
	return control.Result{}, nil
}

func (fakeLifecycle) Resume(context.Context) error { return nil }

func TestRuntimeControllerSnapshotReportsAuthenticatedReachabilityAndDisabledMarker(t *testing.T) {
	stateDir := t.TempDir()
	endpointDir, err := os.MkdirTemp("/tmp", "executor-dashboard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(endpointDir) })
	values, err := secrets.Create(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := config.Default(stateDir)
	cfg.Domain = "executor.example.test"
	cfg.AgentAddress = listener.Addr().String()
	cfg.BrokerEndpoint = filepath.Join(endpointDir, "broker.sock")
	cfg.DesktopEndpoint = filepath.Join(endpointDir, "desktop.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := ipc.NewRPCServer(cfg.BrokerEndpoint, []byte(values.BrokerIPCKey), func(_ context.Context, method string, _ []byte) (any, error) {
		if method != desktop.RPCMethodDeviceStatus {
			t.Fatalf("broker method = %q", method)
		}
		return desktop.RPCDeviceStatus{Component: "broker"}, nil
	})
	desktopHelper := ipc.NewRPCServer(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey), func(_ context.Context, method string, _ []byte) (any, error) {
		if method != desktop.RPCMethodDeviceStatus {
			t.Fatalf("desktop method = %q", method)
		}
		return desktop.RPCDeviceStatus{Component: "desktop", Available: true}, nil
	})
	brokerErr := make(chan error, 1)
	desktopErr := make(chan error, 1)
	go func() { brokerErr <- broker.Serve(ctx) }()
	go func() { desktopErr <- desktopHelper.Serve(ctx) }()
	defer func() {
		cancel()
		assertControllerServerStopped(t, brokerErr)
		assertControllerServerStopped(t, desktopErr)
	}()

	controller := NewRuntimeController(cfg, values, fakeLifecycle{})
	var snapshot Snapshot
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err = controller.Snapshot(context.Background())
		if snapshot.Agent == "reachable" && snapshot.Broker == "reachable" && snapshot.Desktop == "reachable" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot.State != "armed" || snapshot.Domain != "executor.example.test" || snapshot.MCPURL != "https://executor.example.test/mcp" {
		t.Fatalf("snapshot identity = %#v", snapshot)
	}
	if snapshot.Agent != "reachable" || snapshot.Broker != "reachable" || snapshot.Desktop != "reachable" {
		t.Fatalf("snapshot reachability = %#v", snapshot)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "disabled"), []byte("quiesced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err = controller.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "disabled" {
		t.Fatalf("disabled snapshot state = %q, want disabled", snapshot.State)
	}
}

func assertControllerServerStopped(t *testing.T, errors <-chan error) {
	t.Helper()
	select {
	case err := <-errors:
		if err != nil {
			t.Fatalf("IPC server stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("IPC server did not stop")
	}
}
