package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/ipc"
	permissionmodel "github.com/jamie950315/executor/internal/permissions"
	"github.com/jamie950315/executor/internal/secrets"
)

type fakeLifecycle struct{}

func (fakeLifecycle) Kill(context.Context) (control.Result, error) {
	return control.Result{}, nil
}

func TestRuntimeControllerPermissionsCallsActiveUserDesktopHelper(t *testing.T) {
	stateDir := t.TempDir()
	endpointDir, err := os.MkdirTemp("", "executor-dashboard-permissions-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(endpointDir) })
	values, err := secrets.Create(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(stateDir)
	if runtime.GOOS == "windows" {
		cfg.DesktopEndpoint = `\\.\pipe\` + filepath.Base(endpointDir) + "-desktop"
	} else {
		cfg.DesktopEndpoint = filepath.Join(endpointDir, "desktop.sock")
	}

	requests := make(chan bool, 2)
	server := ipc.NewRPCServer(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey), func(_ context.Context, method string, raw []byte) (any, error) {
		if method != desktop.RPCMethodDesktopPermissions {
			t.Fatalf("desktop method = %q", method)
		}
		var params desktop.RPCDesktopPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatalf("decode desktop permission params: %v", err)
		}
		requests <- params.Request
		return permissionmodel.NewReport("darwin", params.Request, []permissionmodel.Item{{
			ID: "screen_recording", Label: "Screen Recording", State: permissionmodel.StatePending, Required: true,
		}}), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(ctx) }()
	defer func() {
		cancel()
		assertControllerServerStopped(t, serverErr)
	}()

	controller := NewRuntimeController(cfg, values, fakeLifecycle{})
	for _, requested := range []bool{false, true} {
		var report permissionmodel.Report
		deadline := time.Now().Add(3 * time.Second)
		for {
			report, err = controller.Permissions(context.Background(), requested)
			if err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("Permissions(%v): %v", requested, err)
		}
		if report.Requested != requested || report.Platform != "darwin" || report.Ready {
			t.Fatalf("Permissions(%v) = %#v", requested, report)
		}
		if got := <-requests; got != requested {
			t.Fatalf("Permissions(%v) IPC request = %v", requested, got)
		}
	}
}

func (fakeLifecycle) Resume(context.Context) error { return nil }

type recordingLifecycle struct {
	calls     []string
	killError error
	result    control.Result
}

func (l *recordingLifecycle) Kill(context.Context) (control.Result, error) {
	l.calls = append(l.calls, "kill")
	return l.result, l.killError
}

func (l *recordingLifecycle) Resume(context.Context) error {
	l.calls = append(l.calls, "resume")
	return nil
}

func TestRuntimeControllerRotateReturnsOneTimeMaterialAndResumesAfterKill(t *testing.T) {
	lifecycle := &recordingLifecycle{result: control.Result{
		RecoveryKey: "recovery-once", URLSecret: "url-once", Dashboard: "http://127.0.0.1:8788/?token=dashboard-once",
	}}
	controller := NewRuntimeController(config.Default(t.TempDir()), secrets.Values{}, lifecycle)
	result, err := controller.Rotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.RecoveryKey != "recovery-once" || result.URLSecret != "url-once" || result.Dashboard == "" {
		t.Fatalf("Rotate result = %#v", result)
	}
	if got := strings.Join(lifecycle.calls, ","); got != "kill,resume" {
		t.Fatalf("Rotate lifecycle calls = %q", got)
	}

	lifecycle = &recordingLifecycle{result: control.Result{RecoveryKey: "partial-recovery", URLSecret: "partial-url"}, killError: errors.New("stop failed")}
	controller = NewRuntimeController(config.Default(t.TempDir()), secrets.Values{}, lifecycle)
	result, err = controller.Rotate(context.Background())
	if err == nil || result.RecoveryKey != "partial-recovery" {
		t.Fatalf("partial Rotate result=%#v err=%v", result, err)
	}
	if got := strings.Join(lifecycle.calls, ","); got != "kill" {
		t.Fatalf("partial Rotate must not resume: %q", got)
	}
}

func TestRuntimeControllerSnapshotReportsAuthenticatedReachabilityAndDisabledMarker(t *testing.T) {
	stateDir := t.TempDir()
	endpointDir, err := os.MkdirTemp("", "executor-dashboard-")
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
	if runtime.GOOS == "windows" {
		name := filepath.Base(endpointDir)
		cfg.BrokerEndpoint = `\\.\pipe\` + name + "-broker"
		cfg.DesktopEndpoint = `\\.\pipe\` + name + "-desktop"
	} else {
		cfg.BrokerEndpoint = filepath.Join(endpointDir, "broker.sock")
		cfg.DesktopEndpoint = filepath.Join(endpointDir, "desktop.sock")
	}

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
		if err != nil {
			t.Fatal(err)
		}
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
