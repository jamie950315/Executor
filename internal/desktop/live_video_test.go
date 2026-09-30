package desktop

import (
	"bytes"
	"context"
	"github.com/jamie950315/executor/internal/livedesktop"
	"io"
	"strconv"
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

func TestLiveWindowsPipelineFrameCount(t *testing.T) {
	path, err := liveFFmpeg()
	if err != nil {
		t.Skip(err)
	}
	args, err := liveVideoArgs("windows", livedesktop.Geometry{Width: 320, Height: 240}, livedesktop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	output := 0
	for i, arg := range args {
		if arg == "-an" {
			output = i
			break
		}
	}
	args = append([]string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x240:r=15", "-frames:v", "4"}, args[output:]...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := startLiveVideo(ctx, path, args, 15)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	frames := 0
	for {
		_, err = s.Read(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		frames++
	}
	if frames != 4 {
		t.Fatalf("four encoded frames produced %d access units", frames)
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

func TestLiveAUAtLimitDoesNotCountNextDelimiter(t *testing.T) {
	for _, delimiter := range [][]byte{{0, 0, 1, 9}, {0, 0, 0, 1, 9}} {
		frame := append(append([]byte(nil), delimiter...), bytes.Repeat([]byte{0x55}, 32-len(delimiter))...)
		next := append(append([]byte(nil), delimiter...), 0xf0, 0, 0, 1, 0x41, 3)
		r := newLiveAUReader(bytes.NewReader(append(append([]byte(nil), frame...), next...)), len(frame))
		got, err := r.next()
		if err != nil || !bytes.Equal(got, frame) {
			t.Fatalf("%d-byte delimiter: exact-limit frame rejected: length=%d, error=%v", len(delimiter), len(got), err)
		}
		got, err = r.next()
		if err != nil || !bytes.Equal(got, next) {
			t.Fatalf("%d-byte delimiter: following frame = %x, error=%v", len(delimiter), got, err)
		}
	}
}

func TestLiveAUOversizeAtEOF(t *testing.T) {
	frame := append([]byte{0, 0, 0, 1, 9}, bytes.Repeat([]byte{0x55}, 28)...)
	if _, err := newLiveAUReader(bytes.NewReader(frame), 32).next(); err == nil {
		t.Fatal("oversized final frame accepted")
	}
}

func TestLiveAUChunkBoundariesAndIndependentFrames(t *testing.T) {
	delimiters := [][]byte{{0, 0, 1, 9}, {0, 0, 0, 1, 9}}
	var input []byte
	var frames [][]byte
	for i := 0; i < 4; i++ {
		frame := append(append([]byte(nil), delimiters[i%2]...), bytes.Repeat([]byte{byte(0x51 + i)}, 64*1024+i)...)
		frames = append(frames, frame)
		input = append(input, frame...)
	}
	for _, chunkSize := range []int{1, 2, 3, 7, 4093, 65536} {
		t.Run(strconv.Itoa(chunkSize), func(t *testing.T) {
			r := newLiveAUReader(fragmentedLiveReader{r: bytes.NewReader(input), limit: chunkSize}, 128*1024)
			var returned [][]byte
			for i, frame := range frames {
				got, err := r.next()
				if err != nil || !bytes.Equal(got, frame) {
					t.Fatalf("frame %d: length=%d, error=%v", i, len(got), err)
				}
				returned = append(returned, got)
			}
			if _, err := r.next(); err != io.EOF {
				t.Fatalf("final EOF = %v", err)
			}
			for i := range returned {
				if !bytes.Equal(returned[i], frames[i]) {
					t.Fatalf("later read overwrote frame %d", i)
				}
			}
		})
	}
}

type fragmentedLiveReader struct {
	r     io.Reader
	limit int
}

func (r fragmentedLiveReader) Read(p []byte) (int, error) {
	return r.r.Read(p[:min(len(p), r.limit)])
}

func BenchmarkLiveAccessUnits(b *testing.B) {
	frame := append([]byte{0, 0, 0, 1, 9}, bytes.Repeat([]byte{0x55}, 32*1024-5)...)
	input := bytes.Repeat(frame, 32)
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := newLiveAUReader(bytes.NewReader(input), 4*1024*1024)
		for {
			_, err := r.next()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}
func TestLiveVideoArgs(t *testing.T) {
	for _, platform := range []string{"darwin", "windows"} {
		args, err := liveVideoArgs(platform, livedesktop.Geometry{Width: 2560, Height: 1440}, livedesktop.Options{})
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Join(args, " ")
		for _, want := range []string{"-f h264", "1280", "-g 15", "fps=15,"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %s: %s", want, s)
			}
		}
	}
}
