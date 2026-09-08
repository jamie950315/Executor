package desktop

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/livedesktop"
)

// This test must be explicitly opted into by an owner who authorizes reading
// the active display. It never posts input, requests permissions, or stores media.
func TestLiveCaptureOptIn(t *testing.T) {
	if os.Getenv("EXECUTOR_TEST_LIVE_CAPTURE") != "1" {
		t.Skip("set EXECUTOR_TEST_LIVE_CAPTURE=1 only with owner screen-capture authorization")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native live capture requires macOS or Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b := NewLiveBackend()
	if !b.Available(ctx) {
		t.Fatal("live capture unavailable: desktop lock or existing screen/input permission boundary; no permission request was made")
	}
	source, err := b.OpenVideo(ctx, livedesktop.Options{MaxWidth: 1280, FPS: 15, Bitrate: 2500000})
	if err != nil {
		t.Fatal("native encoder startup failed; no media or raw encoder diagnostics recorded")
	}
	defer source.Close()
	bytes := 0
	for frame := 0; frame < 3; frame++ {
		sample, err := source.Read(ctx)
		if err != nil {
			t.Fatal("native encoder did not deliver three frames before failure/deadline; no media or raw encoder diagnostics recorded")
		}
		if len(sample.Data) == 0 || sample.Duration <= 0 {
			t.Fatal("native encoder returned an invalid sample")
		}
		bytes += len(sample.Data)
	}
	if err := source.Close(); err != nil {
		t.Fatal("native encoder did not close cleanly")
	}
	t.Logf("captured and discarded 3 H264 samples (%d total bytes); no input posted", bytes)
}
