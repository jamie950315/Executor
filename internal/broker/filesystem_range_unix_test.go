//go:build darwin || linux

package broker

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
)

type countedRangeFilesystem struct {
	*filesystem.LocalService
	calls atomic.Int64
}

func (c *countedRangeFilesystem) ReadFileRange(path string, offset int64, limit int) (filesystem.ReadRangeResult, error) {
	c.calls.Add(1)
	return c.LocalService.ReadFileRange(path, offset, limit)
}

func TestFilesystemRangeRPCClientAndCoreValidateBoundsAcrossHelpers(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"owner", "admin"} {
		t.Run(target, func(t *testing.T) {
			endpoint := brokerSocketPath(t)
			key := []byte("0123456789abcdef0123456789abcdef")
			files := &countedRangeFilesystem{LocalService: filesystem.NewLocalService()}
			var server *ipc.RPCServer
			if target == "admin" {
				server = NewAdminRPCServer(endpoint, key, &rpcTestTerminal{}, files)
			} else {
				server = desktop.NewHelperRPCServer(endpoint, key, &rpcTestTerminal{}, files, &rpcTestDesktop{})
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("helper shutdown: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Error("helper shutdown timed out")
				}
			})
			waitForBrokerEndpoint(t, endpoint)
			path := filepath.Join(t.TempDir(), "range.txt")
			if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
				t.Fatal(err)
			}
			remote := NewDesktopRPCClient(endpoint, key).Filesystem
			core := NewCore(nil, nil, remote, nil)
			result, err := core.ReadFileRange(path, 2, 3)
			if err != nil || string(result.Data) != "234" || result.OffsetBytes != 2 || result.NextOffsetBytes != 5 || !result.Truncated || result.EOF {
				t.Fatalf("core -> remote -> %s range: %#v, %v", target, result, err)
			}
			if data, err := remote.ReadFile(path); err != nil || string(data) != "0123456789" {
				t.Fatalf("legacy remote read: %q, %v", data, err)
			}
			before := files.calls.Load()
			if _, err := remote.ReadFileRange(path, -1, 3); err == nil || files.calls.Load() != before {
				t.Fatalf("remote invalid range reached filesystem: %v", err)
			}
			client := ipc.NewRPCClient(endpoint, key)
			for _, invalid := range []map[string]any{
				{"offset_bytes": 0, "limit_bytes": 1},
				{"path": path, "offset_bytes": -1, "limit_bytes": 1},
				{"path": path, "offset_bytes": 0},
				{"path": path, "offset_bytes": 0, "limit_bytes": 0},
				{"path": path, "offset_bytes": 0, "limit_bytes": filesystem.MaxReadLimit + 1},
				{"path": path, "offset_bytes": 1.5, "limit_bytes": 1},
				{"path": path, "offset_bytes": 0, "limit_bytes": 1.5},
				{"path": path, "offset_bytes": math.MaxInt64, "limit_bytes": 1},
				{"path": path, "offset_bytes": 0, "limit_bytes": 1, "extra": true},
			} {
				var rejected filesystem.ReadRangeResult
				err := client.Call(ctx, desktop.RPCMethodFilesystemReadRange, invalid, &rejected)
				if err == nil || files.calls.Load() != before {
					t.Errorf("invalid RPC params reached filesystem: %#v error=%v", invalid, err)
				}
			}
		})
	}
}

func TestFilesystemRangeRPCFailsExplicitlyForLegacyOnlyService(t *testing.T) {
	t.Parallel()
	legacy := &rpcTestFilesystem{readData: []byte("legacy")}
	core := NewCore(nil, nil, legacy, nil)
	if _, err := core.ReadFileRange("/fixture", 0, 1); !errors.Is(err, filesystem.ErrRangeUnavailable) {
		t.Fatalf("core legacy range: %v", err)
	}
	endpoint := brokerSocketPath(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	server := NewAdminRPCServer(endpoint, key, &rpcTestTerminal{}, legacy)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("helper shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("helper shutdown timed out")
		}
	})
	waitForBrokerEndpoint(t, endpoint)
	remote := NewDesktopRPCClient(endpoint, key).Filesystem
	if _, err := remote.ReadFileRange("/fixture", 0, 1); err == nil || !strings.Contains(err.Error(), "update the Executor helper") {
		t.Fatalf("legacy range error: %v", err)
	}
	if data, err := remote.ReadFile("/fixture"); err != nil || string(data) != "legacy" {
		t.Fatalf("legacy read changed: %q, %v", data, err)
	}
}
