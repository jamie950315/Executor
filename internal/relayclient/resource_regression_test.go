package relayclient

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
)

func TestInactiveClientChecksConfigurationAtMostOncePerSecond(t *testing.T) {
	for _, state := range []string{"unenrolled", "disabled"} {
		t.Run(state, func(t *testing.T) {
			configPath, cfg, _ := relayFixture(t)
			if state == "disabled" {
				cfg.UnifiedDashboard.URL = "https://dashboard.example.test"
				cfg.UnifiedDashboard.Enrolled = true
				if err := config.Save(configPath, cfg); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.StateDir, "disabled"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var delay time.Duration
			client, err := NewClient(ClientOptions{
				ConfigPath: configPath, ExecutorVersion: "test-version",
				Dial: func(context.Context, string, *http.Client) (relaySocket, error) {
					t.Fatal("inactive client attempted to connect")
					return nil, nil
				},
				Sleep: func(_ context.Context, duration time.Duration) error {
					delay = duration
					return context.Canceled
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if delay != time.Second {
				t.Fatalf("inactive configuration check delay = %v, want 1s", delay)
			}
		})
	}
}

// Hide the underlying cancellation context's internal value so WithCancel uses
// AfterFunc. Count registrations without depending on runtime goroutine timing.
type trackingCancellationContext struct {
	context.Context
	active atomic.Int32
}

func (c *trackingCancellationContext) Value(any) any { return nil }

func (c *trackingCancellationContext) AfterFunc(f func()) func() bool {
	c.active.Add(1)
	var released sync.Once
	stop := context.AfterFunc(c.Context, func() {
		released.Do(func() { c.active.Add(-1) })
		f()
	})
	return func() bool {
		stopped := stop()
		if stopped {
			released.Do(func() { c.active.Add(-1) })
		}
		return stopped
	}
}

func TestDuplicateRequestReleasesCancellationRegistration(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &trackingCancellationContext{Context: parent}
	connection := newConnection(nil, newFakeSocket(), config.Config{})
	if !connection.requests.Add("duplicate", func() {}) {
		t.Fatal("could not reserve request ID")
	}
	connection.startRequest(ctx, "duplicate", "status", nil, nil)
	if got := ctx.active.Load(); got != 0 {
		t.Fatalf("rejected duplicate retained %d parent cancellation registrations", got)
	}
}
