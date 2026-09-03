//go:build darwin || linux

package desktop

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	permissionmodel "github.com/jamie950315/executor/internal/permissions"
	"github.com/jamie950315/executor/internal/terminal"
)

func TestHelperRPCServer_RejectsUnknownMethodAndUnknownFields(t *testing.T) {
	t.Parallel()

	endpoint := helperSocketPath(t)
	key := []byte("fedcba9876543210fedcba9876543210")
	server := NewHelperRPCServer(endpoint, key, &helperTerminal{}, &helperFilesystem{}, &helperDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForHelperEndpoint(t, endpoint)

	client := ipc.NewRPCClient(endpoint, key)
	if err := client.Call(context.Background(), "nope.method", struct{}{}, &struct{}{}); err == nil {
		t.Fatal("unknown method unexpectedly succeeded")
	}
	if err := client.Call(context.Background(), RPCMethodTerminalWrite, map[string]any{
		"session_id": "owner",
		"input":      []byte("x"),
		"extra":      true,
	}, &struct{}{}); err == nil {
		t.Fatal("unknown field unexpectedly succeeded")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper RPC server did not stop")
	}
}

func TestStrictRPCParamsRejectTrailingJSONValues(t *testing.T) {
	t.Parallel()
	if err := decodeStrictParams("device.test", []byte(`{} {"extra":true}`), &struct{}{}); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestValidateKeyboardActionKeepsLegacyKeyCodeWithinAdvertisedBounds(t *testing.T) {
	t.Parallel()
	for _, keyCode := range []int{-1, 256} {
		if err := validateKeyboardAction(KeyboardAction{KeyCode: keyCode}); err == nil {
			t.Fatalf("legacy key code %d was accepted", keyCode)
		}
	}
	for _, action := range []KeyboardAction{
		{KeyCode: 1},
		{KeyCode: 255},
		{Text: "hello"},
		{Keys: []string{"CTRL", "L"}},
	} {
		if err := validateKeyboardAction(action); err != nil {
			t.Fatalf("valid keyboard action %#v rejected: %v", action, err)
		}
	}
}

func TestHelperRPCServer_DeviceStatusAndNewMethods(t *testing.T) {
	t.Parallel()

	endpoint := helperSocketPath(t)
	key := []byte("00112233445566778899aabbccddeeff")
	term := &helperStatusTerminal{}
	files := &helperStatusFilesystem{}
	gui := &helperStatusDesktop{}
	server := NewHelperRPCServer(endpoint, key, term, files, gui)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForHelperEndpoint(t, endpoint)

	client := ipc.NewRPCClient(endpoint, key)
	if err := client.Call(context.Background(), RPCMethodTerminalSignal, map[string]any{
		"session_id": "owner",
		"signal":     "interrupt",
	}, &struct{}{}); err != nil {
		t.Fatalf("terminal.signal: %v", err)
	}
	if err := client.Call(context.Background(), RPCMethodTerminalResize, map[string]any{
		"session_id": "owner",
		"columns":    132,
		"rows":       43,
	}, &struct{}{}); err != nil {
		t.Fatalf("terminal.resize: %v", err)
	}
	if err := client.Call(context.Background(), RPCMethodFilesystemAppend, map[string]any{
		"path": "/tmp/a.txt",
		"data": []byte("x"),
		"perm": 384,
	}, &struct{}{}); err != nil {
		t.Fatalf("filesystem.append: %v", err)
	}
	if err := client.Call(context.Background(), RPCMethodFilesystemMkdir, map[string]any{
		"path": "/tmp/dir",
		"perm": 493,
	}, &struct{}{}); err != nil {
		t.Fatalf("filesystem.mkdir: %v", err)
	}
	if err := client.Call(context.Background(), RPCMethodDesktopKeyboard, RPCDesktopKeyboardParams{
		Action: KeyboardAction{Keys: []string{"CTRL", "L"}},
	}, &struct{}{}); err != nil {
		t.Fatalf("desktop.keyboard chord: %v", err)
	}

	var status RPCDeviceStatus
	if err := client.Call(context.Background(), RPCMethodDeviceStatus, struct{}{}, &status); err != nil {
		t.Fatalf("device.status: %v", err)
	}
	if status.Component != "desktop" || !status.Available || status.TerminalSessions != 2 {
		t.Fatalf("unexpected device status: %#v", status)
	}
	var permissionReport permissionmodel.Report
	if err := client.Call(context.Background(), RPCMethodDesktopPermissions, RPCDesktopPermissionsParams{Request: true}, &permissionReport); err != nil {
		t.Fatalf("desktop.permissions: %v", err)
	}
	if !permissionReport.Requested || permissionReport.Platform != "test" || !permissionReport.Ready {
		t.Fatalf("unexpected permission report: %#v", permissionReport)
	}
	if term.resizeSessionID != "owner" || term.resizeColumns != 132 || term.resizeRows != 43 {
		t.Fatalf("terminal resize = %#v", term)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper RPC server did not stop")
	}
}

func TestHelperRPCServerCaptureReturnsPNGAndRemovesPrivateTemporaryFile(t *testing.T) {
	t.Parallel()

	pngBytes := encodeTestPNG(t, 3, 2)
	gui := &captureHelperDesktop{png: pngBytes}
	endpoint := helperSocketPath(t)
	key := []byte("1234567890abcdef1234567890abcdef")
	server := NewHelperRPCServer(endpoint, key, &helperTerminal{}, &helperFilesystem{}, gui)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForHelperEndpoint(t, endpoint)

	var capture RPCDesktopCapture
	client := ipc.NewRPCClient(endpoint, key)
	if err := client.Call(context.Background(), RPCMethodDesktopCapture, struct{}{}, &capture); err != nil {
		t.Fatalf("desktop.capture: %v", err)
	}
	if !bytes.Equal(capture.Data, pngBytes) || capture.MimeType != "image/png" || capture.Width != 3 || capture.Height != 2 {
		t.Fatalf("unexpected desktop capture: mime=%q size=%dx%d bytes=%d", capture.MimeType, capture.Width, capture.Height, len(capture.Data))
	}
	if gui.fileExisted {
		t.Fatal("capture path existed before backend write; screenshot tools can replace its private mode")
	}
	if gui.directoryMode != 0o700 {
		t.Fatalf("capture temporary directory mode = %o, want 700", gui.directoryMode)
	}
	if _, err := os.Stat(gui.directory); !os.IsNotExist(err) {
		t.Fatalf("capture temporary directory still exists at %q: %v", gui.directory, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper RPC server did not stop")
	}
}

func TestHelperRPCServerActionsPreservesValidatedBatch(t *testing.T) {
	t.Parallel()

	gui := &actionHelperDesktop{}
	endpoint := helperSocketPath(t)
	key := []byte("abcdef1234567890abcdef1234567890")
	server := NewHelperRPCServer(endpoint, key, &helperTerminal{}, &helperFilesystem{}, gui)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForHelperEndpoint(t, endpoint)

	want := []Action{
		{Type: ActionClick, X: 10, Y: 20, Button: MouseButtonLeft, Keys: []string{"CTRL"}},
		{Type: ActionDrag, Path: []Point{{X: 1, Y: 2}, {X: 3, Y: 4}}},
		{Type: ActionScreenshot},
	}
	client := ipc.NewRPCClient(endpoint, key)
	if err := client.Call(context.Background(), RPCMethodDesktopActions, RPCDesktopActionsParams{Actions: want}, &struct{}{}); err != nil {
		t.Fatalf("desktop.actions: %v", err)
	}
	if !reflect.DeepEqual(gui.actions, want) {
		t.Fatalf("desktop actions = %#v, want %#v", gui.actions, want)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper RPC server did not stop")
	}
}

func TestHelperRPCServerSerializesDesktopMethods(t *testing.T) {
	t.Parallel()

	gui := &serializingHelperDesktop{
		actionsStarted:   make(chan struct{}),
		releaseActions:   make(chan struct{}),
		screenshotCalled: make(chan struct{}, 1),
	}
	endpoint := helperSocketPath(t)
	key := []byte("ffeeddccbbaa99887766554433221100")
	server := NewHelperRPCServer(endpoint, key, &helperTerminal{}, &helperFilesystem{}, gui)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Serve(ctx) }()
	waitForHelperEndpoint(t, endpoint)
	client := ipc.NewRPCClient(endpoint, key)

	actionsCtx, cancelActions := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelActions()
	actionsDone := make(chan error, 1)
	go func() {
		actionsDone <- client.Call(actionsCtx, RPCMethodDesktopActions, RPCDesktopActionsParams{
			Actions: []Action{{Type: ActionWait}},
		}, &struct{}{})
	}()
	waitForDesktopActionStart(t, gui.actionsStarted, actionsDone)
	screenshotCtx, cancelScreenshot := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelScreenshot()
	screenshotDone := make(chan error, 1)
	go func() {
		screenshotDone <- client.Call(screenshotCtx, RPCMethodDesktopScreenshot, RPCDesktopScreenshotParams{Path: "/tmp/test.png"}, &struct{}{})
	}()
	select {
	case <-gui.screenshotCalled:
		t.Fatal("desktop screenshot ran concurrently with an in-flight action batch")
	case <-time.After(100 * time.Millisecond):
	}
	close(gui.releaseActions)
	if err := <-actionsDone; err != nil {
		t.Fatalf("desktop.actions: %v", err)
	}
	if err := <-screenshotDone; err != nil {
		t.Fatalf("desktop.screenshot: %v", err)
	}
}

func TestHelperRPCServerDoesNotRunCanceledQueuedDesktopMethod(t *testing.T) {
	t.Parallel()

	gui := &serializingHelperDesktop{
		actionsStarted: make(chan struct{}), releaseActions: make(chan struct{}), screenshotCalled: make(chan struct{}, 1),
	}
	endpoint := helperSocketPath(t)
	key := []byte("1029384756abcdef1029384756abcdef")
	server := NewHelperRPCServer(endpoint, key, &helperTerminal{}, &helperFilesystem{}, gui)
	serverCtx, stopServer := context.WithCancel(context.Background())
	t.Cleanup(stopServer)
	go func() { _ = server.Serve(serverCtx) }()
	waitForHelperEndpoint(t, endpoint)
	client := ipc.NewRPCClient(endpoint, key)
	actionsCtx, cancelActions := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelActions()
	actionsDone := make(chan error, 1)
	go func() {
		actionsDone <- client.Call(actionsCtx, RPCMethodDesktopActions, RPCDesktopActionsParams{
			Actions: []Action{{Type: ActionWait}},
		}, &struct{}{})
	}()
	waitForDesktopActionStart(t, gui.actionsStarted, actionsDone)
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedDone := make(chan error, 1)
	go func() {
		queuedDone <- client.Call(queuedCtx, RPCMethodDesktopScreenshot, RPCDesktopScreenshotParams{Path: "/tmp/test.png"}, &struct{}{})
	}()
	cancelQueued()
	select {
	case err := <-queuedDone:
		if err == nil {
			t.Fatal("canceled queued desktop method succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled queued desktop method did not return")
	}
	close(gui.releaseActions)
	if err := <-actionsDone; err != nil {
		t.Fatalf("desktop.actions: %v", err)
	}
	select {
	case <-gui.screenshotCalled:
		t.Fatal("canceled queued screenshot executed after desktop lock was released")
	default:
	}
}

func waitForDesktopActionStart(t *testing.T, started <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-started:
	case err := <-done:
		if err == nil {
			t.Fatal("desktop action call returned before the backend started")
		}
		t.Fatalf("desktop action call failed before the backend started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("desktop action call did not reach the backend")
	}
}

func encodeTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xff})
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return encoded.Bytes()
}

func TestPrepareDesktopCaptureReencodesOversizedPNGWithoutChangingCoordinates(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x*y + y), G: uint8(x*17 + y*31), B: uint8(x ^ y), A: 0xff})
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatalf("encode source PNG: %v", err)
	}
	limit := source.Len() / 2
	data, mimeType, width, height, err := prepareDesktopCapture(source.Bytes(), limit)
	if err != nil {
		t.Fatalf("prepareDesktopCapture: %v", err)
	}
	if len(data) > limit || mimeType != "image/jpeg" || width != 256 || height != 256 {
		t.Fatalf("prepared capture: mime=%q size=%d limit=%d dimensions=%dx%d", mimeType, len(data), limit, width, height)
	}
}

type helperTerminal struct{}

func (helperTerminal) Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error) {
	return terminal.Session{ID: "owner"}, nil
}
func (helperTerminal) Write(sessionID string, input []byte) error { return nil }
func (helperTerminal) Read(sessionID string, cursor int64) (terminal.OutputChunk, error) {
	return terminal.OutputChunk{}, nil
}
func (helperTerminal) List() []terminal.SessionInfo { return nil }
func (helperTerminal) Close(sessionID string) error { return nil }
func (helperTerminal) Kill(sessionID string) error  { return nil }
func (helperTerminal) KillAll() error               { return nil }
func (helperTerminal) Signal(sessionID string, signal terminal.Signal) error {
	return nil
}
func (helperTerminal) Resize(sessionID string, columns, rows int) error { return nil }

type helperFilesystem struct{}

func (helperFilesystem) ReadFile(path string) ([]byte, error) { return nil, nil }
func (helperFilesystem) List(path string) ([]filesystem.Entry, error) {
	return nil, nil
}
func (helperFilesystem) Glob(pattern string) ([]string, error) { return nil, nil }
func (helperFilesystem) Stat(path string) (filesystem.FileInfo, error) {
	return filesystem.FileInfo{}, nil
}
func (helperFilesystem) WriteFile(path string, data []byte, perm fs.FileMode) error  { return nil }
func (helperFilesystem) AppendFile(path string, data []byte, perm fs.FileMode) error { return nil }
func (helperFilesystem) Mkdir(path string, perm fs.FileMode) error                   { return nil }
func (helperFilesystem) Move(src, dst string) error                                  { return nil }
func (helperFilesystem) Delete(path string) error                                    { return nil }

type helperDesktop struct{}

func (helperDesktop) Screenshot(ctx context.Context, path string) error { return nil }
func (helperDesktop) Windows(ctx context.Context) ([]Window, error)     { return nil, nil }
func (helperDesktop) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, nil
}
func (helperDesktop) Mouse(ctx context.Context, action MouseAction) error       { return nil }
func (helperDesktop) Keyboard(ctx context.Context, action KeyboardAction) error { return nil }
func (helperDesktop) App(ctx context.Context, action AppAction) error           { return nil }
func (helperDesktop) Actions(ctx context.Context, actions []Action) error       { return nil }
func (helperDesktop) Available(ctx context.Context) bool                        { return true }

type captureHelperDesktop struct {
	png           []byte
	path          string
	directory     string
	directoryMode fs.FileMode
	fileExisted   bool
}

func (h *captureHelperDesktop) Screenshot(_ context.Context, path string) error {
	h.path = path
	h.directory = filepath.Dir(path)
	info, err := os.Stat(h.directory)
	if err != nil {
		return err
	}
	h.directoryMode = info.Mode().Perm()
	_, err = os.Stat(path)
	h.fileExisted = err == nil
	return os.WriteFile(path, h.png, 0o644)
}
func (*captureHelperDesktop) Windows(context.Context) ([]Window, error) { return nil, nil }
func (*captureHelperDesktop) Accessibility(context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, nil
}
func (*captureHelperDesktop) Mouse(context.Context, MouseAction) error       { return nil }
func (*captureHelperDesktop) Keyboard(context.Context, KeyboardAction) error { return nil }
func (*captureHelperDesktop) App(context.Context, AppAction) error           { return nil }
func (*captureHelperDesktop) Actions(context.Context, []Action) error        { return nil }
func (*captureHelperDesktop) Available(context.Context) bool                 { return true }

type actionHelperDesktop struct {
	helperDesktop
	actions []Action
}

type serializingHelperDesktop struct {
	helperDesktop
	actionsStarted   chan struct{}
	releaseActions   chan struct{}
	screenshotCalled chan struct{}
}

func (h *serializingHelperDesktop) Actions(context.Context, []Action) error {
	close(h.actionsStarted)
	<-h.releaseActions
	return nil
}

func (h *serializingHelperDesktop) Screenshot(context.Context, string) error {
	h.screenshotCalled <- struct{}{}
	return nil
}

func (h *actionHelperDesktop) Actions(_ context.Context, actions []Action) error {
	h.actions = append([]Action(nil), actions...)
	return nil
}

type helperStatusTerminal struct {
	signalSessionID string
	signal          terminal.Signal
	resizeSessionID string
	resizeColumns   int
	resizeRows      int
}

func (h *helperStatusTerminal) Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error) {
	return terminal.Session{}, nil
}
func (h *helperStatusTerminal) Write(sessionID string, input []byte) error { return nil }
func (h *helperStatusTerminal) Read(sessionID string, cursor int64) (terminal.OutputChunk, error) {
	return terminal.OutputChunk{}, nil
}
func (h *helperStatusTerminal) List() []terminal.SessionInfo { return []terminal.SessionInfo{{}, {}} }
func (h *helperStatusTerminal) Close(sessionID string) error { return nil }
func (h *helperStatusTerminal) Kill(sessionID string) error  { return nil }
func (h *helperStatusTerminal) KillAll() error               { return nil }
func (h *helperStatusTerminal) Signal(sessionID string, signal terminal.Signal) error {
	h.signalSessionID = sessionID
	h.signal = signal
	return nil
}
func (h *helperStatusTerminal) Resize(sessionID string, columns, rows int) error {
	h.resizeSessionID = sessionID
	h.resizeColumns = columns
	h.resizeRows = rows
	return nil
}

type helperStatusFilesystem struct{}

func (helperStatusFilesystem) ReadFile(path string) ([]byte, error) { return nil, nil }
func (helperStatusFilesystem) List(path string) ([]filesystem.Entry, error) {
	return nil, nil
}
func (helperStatusFilesystem) Glob(pattern string) ([]string, error) { return nil, nil }
func (helperStatusFilesystem) Stat(path string) (filesystem.FileInfo, error) {
	return filesystem.FileInfo{}, nil
}
func (helperStatusFilesystem) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return nil
}
func (helperStatusFilesystem) AppendFile(path string, data []byte, perm fs.FileMode) error {
	return nil
}
func (helperStatusFilesystem) Mkdir(path string, perm fs.FileMode) error { return nil }
func (helperStatusFilesystem) Move(src, dst string) error                { return nil }
func (helperStatusFilesystem) Delete(path string) error                  { return nil }

type helperStatusDesktop struct{}

func (helperStatusDesktop) Screenshot(ctx context.Context, path string) error { return nil }
func (helperStatusDesktop) Windows(ctx context.Context) ([]Window, error)     { return nil, nil }
func (helperStatusDesktop) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, nil
}
func (helperStatusDesktop) Mouse(ctx context.Context, action MouseAction) error       { return nil }
func (helperStatusDesktop) Keyboard(ctx context.Context, action KeyboardAction) error { return nil }
func (helperStatusDesktop) App(ctx context.Context, action AppAction) error           { return nil }
func (helperStatusDesktop) Actions(ctx context.Context, actions []Action) error       { return nil }
func (helperStatusDesktop) Available(ctx context.Context) bool                        { return true }
func (helperStatusDesktop) Permissions(ctx context.Context, request bool) (permissionmodel.Report, error) {
	return permissionmodel.NewReport("test", request, []permissionmodel.Item{{
		ID: "desktop_session", Label: "Desktop Session", State: permissionmodel.StateGranted, Required: true,
	}}), nil
}

func waitForHelperEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(endpoint); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("endpoint did not appear: %s", endpoint)
}

func helperSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "executor-helper-rpc-")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "rpc.sock")
}
