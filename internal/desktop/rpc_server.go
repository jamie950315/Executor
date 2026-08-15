package desktop

import (
	"context"
	"errors"
	"io/fs"

	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/terminal"
)

var ErrUnknownRPCMethod = errors.New("unknown RPC method")

type HelperTerminal interface {
	Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error)
	Write(sessionID string, input []byte) error
	Read(sessionID string, cursor int64) (terminal.OutputChunk, error)
	List() []terminal.SessionInfo
	Close(sessionID string) error
	Kill(sessionID string) error
	KillAll() error
}

type HelperFilesystem interface {
	ReadFile(path string) ([]byte, error)
	List(path string) ([]filesystem.Entry, error)
	Glob(pattern string) ([]string, error)
	Stat(path string) (filesystem.FileInfo, error)
	WriteFile(path string, data []byte, perm fs.FileMode) error
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
}

func NewHelperRPCServer(endpoint string, key []byte, terminal HelperTerminal, files HelperFilesystem, desktop HelperDesktop) *ipc.RPCServer {
	handler := func(ctx context.Context, method string, params []byte) (any, error) {
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
			return terminal.Read(request.SessionID, request.Cursor)
		case RPCMethodTerminalList:
			var request struct{}
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			return terminal.List(), nil
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
		case RPCMethodFilesystemRead:
			var request RPCFilesystemPathParams
			if err := decodeStrictParams(method, params, &request); err != nil {
				return nil, err
			}
			if err := validateFilesystemPath(method, request.Path); err != nil {
				return nil, err
			}
			return files.ReadFile(request.Path)
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
		default:
			return nil, ErrUnknownRPCMethod
		}
	}
	return ipc.NewRPCServer(endpoint, key, handler)
}
