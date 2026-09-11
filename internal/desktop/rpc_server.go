package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	permissionmodel "github.com/jamie950315/executor/internal/permissions"
	"github.com/jamie950315/executor/internal/terminal"
)

var ErrUnknownRPCMethod = errors.New("unknown RPC method")

const maxRPCCaptureBytes = 48 << 20
const maxRawCaptureBytes = 256 << 20

type HelperTerminal interface {
	Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error)
	Write(sessionID string, input []byte) error
	Read(sessionID string, cursor int64) (terminal.OutputChunk, error)
	List() []terminal.SessionInfo
	Close(sessionID string) error
	Kill(sessionID string) error
	KillAll() error
	Signal(sessionID string, signal terminal.Signal) error
	Resize(sessionID string, columns, rows int) error
}

type HelperFilesystem interface {
	ReadFile(path string) ([]byte, error)
	List(path string) ([]filesystem.Entry, error)
	Glob(pattern string) ([]string, error)
	Stat(path string) (filesystem.FileInfo, error)
	WriteFile(path string, data []byte, perm fs.FileMode) error
	AppendFile(path string, data []byte, perm fs.FileMode) error
	Mkdir(path string, perm fs.FileMode) error
	Move(src, dst string) error
	Delete(path string) error
}

type HelperDesktop interface {
	Screenshot(ctx context.Context, path string) error
	Windows(ctx context.Context) ([]Window, error)
	Accessibility(ctx context.Context) (AccessibilityTree, error)
	Mouse(ctx context.Context, action MouseAction) error
	Keyboard(ctx context.Context, action KeyboardAction) error
	App(ctx context.Context, action AppAction) error
	Actions(ctx context.Context, actions []Action) error
	Available(ctx context.Context) bool
}

type HelperPermissionDesktop interface {
	Permissions(ctx context.Context, request bool) (permissionmodel.Report, error)
}

func NewHelperRPCServer(endpoint string, key []byte, terminal HelperTerminal, files HelperFilesystem, desktop HelperDesktop, options ...HelperOptions) *ipc.RPCServer {
	option := HelperOptions{}
	if len(options) > 0 {
		option = options[0]
	}
	if option.Authority == nil {
		option.Authority = NewInputAuthority()
	}
	authority := option.Authority
	desktopGate := make(chan struct{}, 1)
	desktopGate <- struct{}{}
	handler := func(ctx context.Context, method string, params []byte) (any, error) {
		if strings.HasPrefix(method, "desktop.") {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-desktopGate:
			}
			defer func() { desktopGate <- struct{}{} }()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if method == "desktop.live" {
			return handleLiveRPC(ctx, option.Live, params)
		}
		mutates := method == RPCMethodDesktopMouse || method == RPCMethodDesktopKeyboard || method == RPCMethodDesktopApp || method == RPCMethodDesktopActions
		if mutates || method == RPCMethodDesktopCapture || method == RPCMethodDesktopScreenshot {
			authority.mu.Lock()
			defer authority.mu.Unlock()
			var epoch uint64
			if method == RPCMethodDesktopActions {
				var request RPCDesktopActionsParams
				if err := decodeStrictParams(method, params, &request); err != nil {
					return nil, err
				}
				epoch = request.ExpectedEpoch
			}
			if err := authority.check(epoch, mutates); err != nil {
				return nil, err
			}
			if mutates {
				authority.epoch++
			}
		}
		switch method {
		case RPCMethodTerminalStart:
			var request RPCTerminalStartParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateTerminalStartParams(request); err != nil {
				return nil, err
			}
			session, err := terminal.Start(ctx, terminalStartSpec(request))
			if err != nil {
				return nil, err
			}
			if len(request.InitialInput) > 0 {
				if err := terminal.Write(session.ID, request.InitialInput); err != nil {
					_ = terminal.Kill(session.ID)
					return nil, err
				}
			}
			return session, nil
		case RPCMethodTerminalWrite:
			var request RPCTerminalWriteParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateSessionParams(method, request.SessionID); err != nil {
				return nil, err
			}
			return nil, terminal.Write(request.SessionID, request.Input)
		case RPCMethodTerminalRead:
			var request RPCTerminalReadParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateSessionParams(method, request.SessionID); err != nil {
				return nil, err
			}
			return readTerminalPage(terminal, request)
		case RPCMethodTerminalList:
			var request struct{}
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			return terminal.List(), nil
		case RPCMethodTerminalCapabilities:
			var request struct{}
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			return terminalCapabilityReport(), nil
		case RPCMethodTerminalCloseStdin:
			var request RPCSessionParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateSessionParams(method, request.SessionID); err != nil {
				return nil, err
			}
			return nil, closeTerminalStdin(terminal, request.SessionID)
		case RPCMethodTerminalClose:
			var request RPCSessionParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateSessionParams(method, request.SessionID); err != nil {
				return nil, err
			}
			return nil, terminal.Close(request.SessionID)
		case RPCMethodTerminalKill:
			var request RPCSessionParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateSessionParams(method, request.SessionID); err != nil {
				return nil, err
			}
			return nil, terminal.Kill(request.SessionID)
		case RPCMethodTerminalKillAll:
			var request struct{}
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			return nil, terminal.KillAll()
		case RPCMethodTerminalSignal:
			var request RPCTerminalSignalParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateSessionParams(method, request.SessionID); err != nil {
				return nil, err
			}
			return nil, terminal.Signal(request.SessionID, request.Signal)
		case RPCMethodTerminalResize:
			var request RPCTerminalResizeParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateSessionParams(method, request.SessionID); err != nil {
				return nil, err
			}
			if request.Columns < 1 || request.Rows < 1 || request.Columns > 32767 || request.Rows > 32767 {
				return nil, errors.New("terminal.resize requires columns and rows between 1 and 32767")
			}
			return nil, terminal.Resize(request.SessionID, request.Columns, request.Rows)
		case RPCMethodFilesystemRead:
			var request RPCFilesystemPathParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return files.ReadFile(request.Path)
		case RPCMethodFilesystemDeleteOptions, RPCMethodFilesystemMkdirOptions:
			var request RPCFilesystemOptionsParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			if method == RPCMethodFilesystemDeleteOptions {
				return nil, filesystem.DeleteWithOptions(files, request.Path, request.Recursive)
			}
			return nil, filesystem.MkdirWithOptions(files, request.Path, request.Perm, request.Recursive)
		case RPCMethodFilesystemReadRange:
			var request RPCFilesystemReadRangeParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return filesystem.ReadRange(files, request.Path, request.OffsetBytes, request.LimitBytes)
		case RPCMethodFilesystemList:
			var request RPCFilesystemPathParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return files.List(request.Path)
		case RPCMethodFilesystemGlob:
			var request RPCFilesystemGlobParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Pattern == "" {
				return nil, errors.New("filesystem.glob requires pattern")
			}
			return files.Glob(request.Pattern)
		case RPCMethodFilesystemStat:
			var request RPCFilesystemPathParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return files.Stat(request.Path)
		case RPCMethodFilesystemWrite:
			var request RPCFilesystemWriteParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return nil, files.WriteFile(request.Path, request.Data, request.Perm)
		case RPCMethodFilesystemAppend:
			var request RPCFilesystemWriteParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return nil, files.AppendFile(request.Path, request.Data, request.Perm)
		case RPCMethodFilesystemMkdir:
			var request RPCFilesystemMkdirParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return nil, files.Mkdir(request.Path, request.Perm)
		case RPCMethodFilesystemMove:
			var request RPCFilesystemMoveParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemMove(request); err != nil {
				return nil, err
			}
			return nil, files.Move(request.Src, request.Dst)
		case RPCMethodFilesystemDelete:
			var request RPCFilesystemPathParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return nil, files.Delete(request.Path)
		case RPCMethodDeviceStatus:
			var request struct{}
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			return RPCDeviceStatus{
				Component:        "desktop",
				TerminalSessions: len(terminal.List()),
				Available:        desktop.Available(ctx),
			}, nil
		case RPCMethodDesktopCapture:
			var request struct{}
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			capture, err := captureDesktop(ctx, desktop)
			capture.Epoch = authority.epoch
			return capture, err
		case RPCMethodDesktopScreenshot:
			var request RPCDesktopScreenshotParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Path == "" {
				return nil, errors.New("desktop.screenshot requires path")
			}
			return nil, desktop.Screenshot(ctx, request.Path)
		case RPCMethodDesktopWindows:
			var request struct{}
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			return desktop.Windows(ctx)
		case RPCMethodDesktopAccessibility:
			var request struct{}
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			return desktop.Accessibility(ctx)
		case RPCMethodDesktopMouse:
			var request RPCDesktopMouseParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateMouseAction(request.Action); err != nil {
				return nil, err
			}
			return nil, desktop.Mouse(ctx, request.Action)
		case RPCMethodDesktopKeyboard:
			var request RPCDesktopKeyboardParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateKeyboardAction(request.Action); err != nil {
				return nil, err
			}
			return nil, desktop.Keyboard(ctx, request.Action)
		case RPCMethodDesktopApp:
			var request RPCDesktopAppParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateAppAction(request.Action); err != nil {
				return nil, err
			}
			return nil, desktop.App(ctx, request.Action)
		case RPCMethodDesktopActions:
			var request RPCDesktopActionsParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if len(request.Actions) == 0 {
				return nil, errors.New("desktop.actions requires at least one action")
			}
			if err := ValidateActions(request.Actions); err != nil {
				return nil, err
			}
			return nil, desktop.Actions(ctx, request.Actions)
		case RPCMethodDesktopPermissions:
			var request RPCDesktopPermissionsParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			permissionDesktop, ok := desktop.(HelperPermissionDesktop)
			if !ok {
				return nil, &UnavailableError{Reason: "permission setup is unavailable for this desktop helper"}
			}
			return permissionDesktop.Permissions(ctx, request.Request)
		default:
			return nil, ErrUnknownRPCMethod
		}
	}
	return ipc.NewRPCServer(endpoint, key, handler)
}

func captureDesktop(ctx context.Context, desktop HelperDesktop) (RPCDesktopCapture, error) {
	directory, err := os.MkdirTemp("", "executor-desktop-*")
	if err != nil {
		return RPCDesktopCapture{}, fmt.Errorf("create desktop capture directory: %w", err)
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return RPCDesktopCapture{}, fmt.Errorf("secure desktop capture directory: %w", err)
	}
	path := filepath.Join(directory, "capture.png")
	if err := desktop.Screenshot(ctx, path); err != nil {
		return RPCDesktopCapture{}, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return RPCDesktopCapture{}, fmt.Errorf("secure desktop capture file: %w", err)
	}

	file, err := os.Open(path)
	if err != nil {
		return RPCDesktopCapture{}, fmt.Errorf("open desktop capture: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxRawCaptureBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return RPCDesktopCapture{}, fmt.Errorf("read desktop capture: %w", readErr)
	}
	if closeErr != nil {
		return RPCDesktopCapture{}, fmt.Errorf("close desktop capture: %w", closeErr)
	}
	if len(data) == 0 || len(data) > maxRawCaptureBytes {
		return RPCDesktopCapture{}, fmt.Errorf("raw desktop capture size must be between 1 and %d bytes", maxRawCaptureBytes)
	}
	data, mimeType, width, height, err := prepareDesktopCapture(data, maxRPCCaptureBytes)
	if err != nil {
		return RPCDesktopCapture{}, err
	}
	return RPCDesktopCapture{
		Data: data, MimeType: mimeType, Width: width, Height: height,
	}, nil
}

func prepareDesktopCapture(data []byte, limit int) ([]byte, string, int, int, error) {
	configuration, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("decode desktop capture PNG: %w", err)
	}
	if configuration.Width < 1 || configuration.Height < 1 {
		return nil, "", 0, 0, errors.New("desktop capture dimensions are invalid")
	}
	if len(data) <= limit {
		return data, "image/png", configuration.Width, configuration.Height, nil
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("decode oversized desktop capture PNG: %w", err)
	}
	for _, quality := range []int{85, 75, 60, 45, 30, 20} {
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, decoded, &jpeg.Options{Quality: quality}); err != nil {
			return nil, "", 0, 0, fmt.Errorf("encode desktop capture JPEG: %w", err)
		}
		if encoded.Len() <= limit {
			return encoded.Bytes(), "image/jpeg", configuration.Width, configuration.Height, nil
		}
	}
	return nil, "", 0, 0, fmt.Errorf("desktop capture cannot be encoded within %d bytes", limit)
}
