package desktop

import (
	"bytes"
	"context"
	"github.com/jamie950315/executor/internal/livedesktop"
	"io"
	"strings"
	"testing"
	"time"
)

func TestLiveSyntheticEncoder(t *testing.T) {
	path, err := liveFFmpeg()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := startLiveVideo(ctx, path, []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x240:r=15", "-frames:v", "4", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-x264-params", "aud=1:repeat-headers=1", "-f", "h264", "pipe:1"}, 15)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 4; i++ {
		v, err := s.Read(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(v.Data) < 5 || v.Duration != time.Second/15 {
			t.Fatalf("bad sample %d", i)
		}
	}
	if _, err := s.Read(ctx); err != io.EOF {
		t.Fatalf("encoder EOF: %v", err)
	}
}

func TestLiveAccessUnits(t *testing.T) {
	input := []byte{0, 0, 0, 1, 9, 0xf0, 0, 0, 1, 0x67, 1, 0, 0, 1, 0x65, 2, 0, 0, 0, 1, 9, 0xf0, 0, 0, 1, 0x41, 3}
	r := newLiveAUReader(bytes.NewReader(input), 1024)
	a, err := r.next()
	if err != nil || !bytes.Contains(a, []byte{0x65, 2}) {
		t.Fatalf("first AU %x %v", a, err)
	}
	a, err = r.next()
	if err != nil || !bytes.Contains(a, []byte{0x41, 3}) {
		t.Fatalf("second AU %x %v", a, err)
	}
	if _, err = r.next(); err != io.EOF {
		t.Fatalf("EOF: %v", err)
	}
}
func TestLiveAUBound(t *testing.T) {
	r := newLiveAUReader(bytes.NewReader(bytes.Repeat([]byte{1}, 100)), 32)
	if _, err := r.next(); err == nil {
		t.Fatal("unbounded AU accepted")
	}
}
func TestLiveVideoArgs(t *testing.T) {
	for _, platform := range []string{"darwin", "windows"} {
		args, err := liveVideoArgs(platform, livedesktop.Geometry{Width: 2560, Height: 1440}, livedesktop.Options{})
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Join(args, " ")
		for _, want := range []string{"-f h264", "aud=insert", "1280", "-g 15", "fps=15,"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %s: %s", want, s)
			}
		}
	}
}
