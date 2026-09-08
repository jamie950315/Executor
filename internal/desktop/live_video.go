package desktop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/jamie950315/executor/internal/livedesktop"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// Each frame is delimited by an encoder-inserted access unit delimiter. Bounds
// apply before appending, including malformed streams with no start codes.
type liveAUReader struct {
	r       *bufio.Reader
	pending []byte
	limit   int
	eof     bool
}

func newLiveAUReader(r io.Reader, limit int) *liveAUReader {
	return &liveAUReader{r: bufio.NewReaderSize(r, 64*1024), limit: limit}
}
func (r *liveAUReader) next() ([]byte, error) {
	if r.eof {
		return nil, io.EOF
	}
	out := r.pending
	r.pending = nil
	seen := false
	for i := 0; i+3 < len(out); i++ {
		if out[i] == 0 && out[i+1] == 0 && out[i+2] == 1 && out[i+3]&31 == 9 {
			seen = true
		}
	}
	for {
		b, err := r.r.ReadByte()
		if err != nil {
			if err == io.EOF {
				r.eof = true
				if len(out) > 0 {
					return out, nil
				}
			}
			return nil, err
		}
		if len(out) >= r.limit {
			return nil, errors.New("H264 access unit exceeds limit")
		}
		out = append(out, b)
		n := len(out)
		if n >= 4 && out[n-4] == 0 && out[n-3] == 0 && out[n-2] == 1 && b&31 == 9 {
			start := n - 4
			if start > 0 && out[start-1] == 0 {
				start--
			}
			if seen {
				r.pending = append([]byte(nil), out[start:]...)
				return out[:start], nil
			}
			seen = true
		}
	}
}
func liveVideoOptions(o livedesktop.Options) livedesktop.Options {
	if o.FPS <= 0 {
		o.FPS = 15
	}
	if o.FPS > 30 {
		o.FPS = 30
	}
	if o.MaxWidth <= 0 || o.MaxWidth > 1280 {
		o.MaxWidth = 1280
	}
	if o.MaxWidth < 320 {
		o.MaxWidth = 320
	}
	if o.Bitrate <= 0 {
		o.Bitrate = 2000000
	}
	if o.Bitrate < 250000 {
		o.Bitrate = 250000
	}
	if o.Bitrate > 8000000 {
		o.Bitrate = 8000000
	}
	return o
}
func liveVideoArgs(platform string, g livedesktop.Geometry, o livedesktop.Options) ([]string, error) {
	o = liveVideoOptions(o)
	fps := strconv.Itoa(o.FPS)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	switch platform {
	case "darwin":
		args = append(args, "-f", "avfoundation", "-capture_cursor", "1", "-framerate", fps, "-i", "Capture screen 0:none")
	case "windows":
		args = append(args, "-f", "gdigrab", "-draw_mouse", "1", "-framerate", fps, "-offset_x", "0", "-offset_y", "0", "-video_size", fmt.Sprintf("%dx%d", g.Width, g.Height), "-i", "desktop")
	default:
		return nil, errors.New("live desktop video is supported only on macOS and Windows")
	}
	// Capture devices can report a time base that differs from the requested
	// input rate. Bound the output rate before encoding and timestamping RTP.
	args = append(args, "-an", "-vf", fmt.Sprintf("fps=%d,scale=w='min(%d,iw)':h='min(720,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2", o.FPS, o.MaxWidth), "-pix_fmt", "yuv420p")
	if platform == "darwin" {
		args = append(args, "-c:v", "h264_videotoolbox", "-realtime", "1", "-allow_sw", "0")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-x264-params", "repeat-headers=1:aud=1")
	}
	args = append(args, "-profile:v", "baseline", "-level:v", "3.1", "-bf", "0", "-g", fps, "-b:v", strconv.Itoa(o.Bitrate), "-maxrate", strconv.Itoa(o.Bitrate), "-bufsize", strconv.Itoa(o.Bitrate), "-bsf:v", "h264_metadata=aud=insert,dump_extra=freq=keyframe", "-f", "h264", "pipe:1")
	return args, nil
}
func liveFFmpeg() (string, error) {
	if p := os.Getenv("EXECUTOR_FFMPEG_PATH"); p != "" {
		resolved, err := exec.LookPath(p)
		if err != nil {
			return "", errors.New("EXECUTOR_FFMPEG_PATH is not an executable")
		}
		return resolved, nil
	}
	for _, p := range []string{"ffmpeg", "/opt/homebrew/bin/ffmpeg", "/opt/homebrew/bin/ffmpeg8.1.2", "/usr/local/bin/ffmpeg", `C:\Program Files\ffmpeg\bin\ffmpeg.exe`} {
		if resolved, err := exec.LookPath(p); err == nil {
			return resolved, nil
		}
	}
	return "", errors.New("live desktop requires FFmpeg; install it or set EXECUTOR_FFMPEG_PATH")
}

type liveVideoResult struct {
	sample livedesktop.Sample
	err    error
}
type liveVideoSource struct {
	cmd     *exec.Cmd
	pipe    io.ReadCloser
	cancel  context.CancelFunc
	once    sync.Once
	done    chan struct{}
	results chan liveVideoResult
}

func startLiveVideo(ctx context.Context, path string, args []string, fps int) (livedesktop.VideoSource, error) {
	child, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(child, path, args...)
	cmd.Stderr = io.Discard
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		pipe.Close()
		return nil, errors.New("unable to start live desktop encoder")
	}
	s := &liveVideoSource{cmd: cmd, pipe: pipe, cancel: cancel, done: make(chan struct{}), results: make(chan liveVideoResult, 2)}
	go func() {
		defer close(s.done)
		defer close(s.results)
		r := newLiveAUReader(pipe, 4*1024*1024)
		for {
			data, e := r.next()
			result := liveVideoResult{sample: livedesktop.Sample{Data: data, Duration: time.Second / time.Duration(fps)}, err: e}
			select {
			case s.results <- result:
			case <-child.Done():
			}
			if e != nil || child.Err() != nil {
				break
			}
		}
		cancel()
		pipe.Close()
		_ = cmd.Wait()
	}()
	return s, nil
}
func (s *liveVideoSource) Read(ctx context.Context) (livedesktop.Sample, error) {
	select {
	case r, ok := <-s.results:
		if !ok {
			return livedesktop.Sample{}, io.EOF
		}
		return r.sample, r.err
	case <-ctx.Done():
		return livedesktop.Sample{}, ctx.Err()
	}
}
func (s *liveVideoSource) Close() error {
	s.once.Do(func() { s.cancel(); s.pipe.Close() })
	<-s.done
	return nil
}
func (b *liveBackend) OpenVideo(ctx context.Context, o livedesktop.Options) (livedesktop.VideoSource, error) {
	if !b.Available(ctx) {
		return nil, errors.New("desktop is locked or unavailable")
	}
	g, err := b.Geometry(ctx)
	if err != nil {
		return nil, err
	}
	path, err := liveFFmpeg()
	if err != nil {
		return nil, err
	}
	args, err := liveVideoArgs(runtime.GOOS, g, o)
	if err != nil {
		return nil, err
	}
	return startLiveVideo(ctx, path, args, liveVideoOptions(o).FPS)
}
