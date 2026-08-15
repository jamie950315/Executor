package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/terminal"
)

func NewAdminRPCServer(endpoint string, key []byte, executor TerminalExecutor, files filesystem.Service) *ipc.RPCServer {
	handler := func(ctx context.Context, method string, params []byte) (any, error) {
		switch method {
		case desktop.RPCMethodTerminalStart:
			var request desktop.RPCTerminalStartParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			session, err := executor.Start(ctx, terminal.SessionSpec{
				Command: append([]string(nil), request.Command...),
				Dir:     request.Dir,
				Env:     request.Env,
				Columns: request.Columns,
				Rows:    request.Rows,
			})
			if err != nil {
				return nil, err
			}
			if len(request.InitialInput) > 0 {
				if err := executor.Write(session.ID, request.InitialInput); err != nil {
					_ = executor.Kill(session.ID)
					return nil, err
				}
			}
			return session, nil
		case desktop.RPCMethodTerminalWrite:
			var request desktop.RPCTerminalWriteParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.SessionID == "" {
				return nil, errors.New("terminal.write requires session_id")
			}
			return nil, executor.Write(request.SessionID, request.Input)
		case desktop.RPCMethodTerminalRead:
			var request desktop.RPCTerminalReadParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.SessionID == "" {
				return nil, errors.New("terminal.read requires session_id")
			}
			return executor.Read(request.SessionID, request.Cursor)
		case desktop.RPCMethodTerminalList:
			var request struct{}
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			return executor.List(), nil
		case desktop.RPCMethodTerminalClose:
			var request desktop.RPCSessionParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.SessionID == "" {
				return nil, errors.New("terminal.close requires session_id")
			}
			return nil, executor.Close(request.SessionID)
		case desktop.RPCMethodTerminalKill:
			var request desktop.RPCSessionParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.SessionID == "" {
				return nil, errors.New("terminal.kill requires session_id")
			}
			return nil, executor.Kill(request.SessionID)
		case desktop.RPCMethodTerminalKillAll:
			var request struct{}
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			return nil, executor.KillAll()
		case desktop.RPCMethodTerminalSignal:
			var request desktop.RPCTerminalSignalParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.SessionID == "" {
				return nil, errors.New("terminal.signal requires session_id")
			}
			return nil, executor.Signal(request.SessionID, request.Signal)
		case desktop.RPCMethodTerminalResize:
			var request desktop.RPCTerminalResizeParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.SessionID == "" {
				return nil, errors.New("terminal.resize requires session_id")
			}
			if request.Columns < 1 || request.Rows < 1 || request.Columns > terminal.MaxDimension || request.Rows > terminal.MaxDimension {
				return nil, fmt.Errorf("terminal.resize requires columns and rows between 1 and %d", terminal.MaxDimension)
			}
			return nil, executor.Resize(request.SessionID, request.Columns, request.Rows)
		case desktop.RPCMethodFilesystemRead:
			var request desktop.RPCFilesystemPathParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Path == "" {
				return nil, errors.New("filesystem.read requires path")
			}
			return files.ReadFile(request.Path)
		case desktop.RPCMethodFilesystemList:
			var request desktop.RPCFilesystemPathParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Path == "" {
				return nil, errors.New("filesystem.list requires path")
			}
			return files.List(request.Path)
		case desktop.RPCMethodFilesystemGlob:
			var request desktop.RPCFilesystemGlobParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Pattern == "" {
				return nil, errors.New("filesystem.glob requires pattern")
			}
			return files.Glob(request.Pattern)
		case desktop.RPCMethodFilesystemStat:
			var request desktop.RPCFilesystemPathParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Path == "" {
				return nil, errors.New("filesystem.stat requires path")
			}
			return files.Stat(request.Path)
		case desktop.RPCMethodFilesystemWrite:
			var request desktop.RPCFilesystemWriteParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Path == "" {
				return nil, errors.New("filesystem.write requires path")
			}
			return nil, files.WriteFile(request.Path, request.Data, request.Perm)
		case desktop.RPCMethodFilesystemAppend:
			var request desktop.RPCFilesystemWriteParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Path == "" {
				return nil, errors.New("filesystem.append requires path")
			}
			return nil, files.AppendFile(request.Path, request.Data, request.Perm)
		case desktop.RPCMethodFilesystemMkdir:
			var request desktop.RPCFilesystemMkdirParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Path == "" {
				return nil, errors.New("filesystem.mkdir requires path")
			}
			return nil, files.Mkdir(request.Path, request.Perm)
		case desktop.RPCMethodFilesystemMove:
			var request desktop.RPCFilesystemMoveParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Src == "" || request.Dst == "" {
				return nil, errors.New("filesystem.move requires src and dst")
			}
			return nil, files.Move(request.Src, request.Dst)
		case desktop.RPCMethodFilesystemDelete:
			var request desktop.RPCFilesystemPathParams
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			if request.Path == "" {
				return nil, errors.New("filesystem.delete requires path")
			}
			return nil, files.Delete(request.Path)
		case desktop.RPCMethodDeviceStatus:
			var request struct{}
			if err := decodeAdminParams(method, params, &request); err != nil {
				return nil, err
			}
			return desktop.RPCDeviceStatus{
				Component:        "broker",
				TerminalSessions: len(executor.List()),
			}, nil
		default:
			return nil, desktop.ErrUnknownRPCMethod
		}
	}
	return ipc.NewRPCServer(endpoint, key, handler)
}

type DesktopRPCClient struct {
	Terminal   *RemoteTerminalRPCClient
	Filesystem *RemoteFilesystemRPCClient
	Desktop    *RemoteDesktopRPCClient
}

type RemoteTerminalRPCClient struct {
	client *ipc.RPCClient
}

type RemoteFilesystemRPCClient struct {
	client *ipc.RPCClient
}

type RemoteDesktopRPCClient struct {
	client *ipc.RPCClient
}

func NewDesktopRPCClient(endpoint string, key []byte) *DesktopRPCClient {
	client := ipc.NewRPCClient(endpoint, key)
	return &DesktopRPCClient{
		Terminal:   &RemoteTerminalRPCClient{client: client},
		Filesystem: &RemoteFilesystemRPCClient{client: client},
		Desktop:    &RemoteDesktopRPCClient{client: client},
	}
}

func (c *RemoteTerminalRPCClient) Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error) {
	var session terminal.Session
	err := c.client.Call(ctx, desktop.RPCMethodTerminalStart, desktop.RPCTerminalStartParams{
		Command: append([]string(nil), spec.Command...),
		Dir:     spec.Dir,
		Env:     spec.Env,
		Columns: spec.Columns,
		Rows:    spec.Rows,
	}, &session)
	return session, err
}

func (c *RemoteTerminalRPCClient) Write(sessionID string, input []byte) error {
	return c.client.Call(context.Background(), desktop.RPCMethodTerminalWrite, desktop.RPCTerminalWriteParams{SessionID: sessionID, Input: input}, nil)
}

func (c *RemoteTerminalRPCClient) Read(sessionID string, cursor int64) (terminal.OutputChunk, error) {
	var chunk terminal.OutputChunk
	err := c.client.Call(context.Background(), desktop.RPCMethodTerminalRead, desktop.RPCTerminalReadParams{SessionID: sessionID, Cursor: cursor}, &chunk)
	return chunk, err
}

func (c *RemoteTerminalRPCClient) List() []terminal.SessionInfo {
	var list []terminal.SessionInfo
	if err := c.client.Call(context.Background(), desktop.RPCMethodTerminalList, struct{}{}, &list); err != nil {
		return nil
	}
	return list
}

func (c *RemoteTerminalRPCClient) Close(sessionID string) error {
	return c.client.Call(context.Background(), desktop.RPCMethodTerminalClose, desktop.RPCSessionParams{SessionID: sessionID}, nil)
}

func (c *RemoteTerminalRPCClient) Kill(sessionID string) error {
	return c.client.Call(context.Background(), desktop.RPCMethodTerminalKill, desktop.RPCSessionParams{SessionID: sessionID}, nil)
}

func (c *RemoteTerminalRPCClient) KillAll() error {
	return c.client.Call(context.Background(), desktop.RPCMethodTerminalKillAll, struct{}{}, nil)
}

func (c *RemoteTerminalRPCClient) Signal(sessionID string, signal terminal.Signal) error {
	return c.client.Call(context.Background(), desktop.RPCMethodTerminalSignal, desktop.RPCTerminalSignalParams{
		SessionID: sessionID,
		Signal:    signal,
	}, nil)
}

func (c *RemoteTerminalRPCClient) Resize(sessionID string, columns, rows int) error {
	return c.client.Call(context.Background(), desktop.RPCMethodTerminalResize, desktop.RPCTerminalResizeParams{
		SessionID: sessionID,
		Columns:   columns,
		Rows:      rows,
	}, nil)
}

func (c *RemoteFilesystemRPCClient) ReadFile(path string) ([]byte, error) {
	var data []byte
	err := c.client.Call(context.Background(), desktop.RPCMethodFilesystemRead, desktop.RPCFilesystemPathParams{Path: path}, &data)
	return data, err
}

func (c *RemoteFilesystemRPCClient) List(path string) ([]filesystem.Entry, error) {
	var entries []filesystem.Entry
	err := c.client.Call(context.Background(), desktop.RPCMethodFilesystemList, desktop.RPCFilesystemPathParams{Path: path}, &entries)
	return entries, err
}

func (c *RemoteFilesystemRPCClient) Glob(pattern string) ([]string, error) {
	var matches []string
	err := c.client.Call(context.Background(), desktop.RPCMethodFilesystemGlob, desktop.RPCFilesystemGlobParams{Pattern: pattern}, &matches)
	return matches, err
}

func (c *RemoteFilesystemRPCClient) Stat(path string) (filesystem.FileInfo, error) {
	var info filesystem.FileInfo
	err := c.client.Call(context.Background(), desktop.RPCMethodFilesystemStat, desktop.RPCFilesystemPathParams{Path: path}, &info)
	return info, err
}

func (c *RemoteFilesystemRPCClient) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return c.client.Call(context.Background(), desktop.RPCMethodFilesystemWrite, desktop.RPCFilesystemWriteParams{Path: path, Data: data, Perm: perm}, nil)
}

func (c *RemoteFilesystemRPCClient) AppendFile(path string, data []byte, perm fs.FileMode) error {
	return c.client.Call(context.Background(), desktop.RPCMethodFilesystemAppend, desktop.RPCFilesystemWriteParams{Path: path, Data: data, Perm: perm}, nil)
}

func (c *RemoteFilesystemRPCClient) Mkdir(path string, perm fs.FileMode) error {
	return c.client.Call(context.Background(), desktop.RPCMethodFilesystemMkdir, desktop.RPCFilesystemMkdirParams{Path: path, Perm: perm}, nil)
}

func (c *RemoteFilesystemRPCClient) Move(src, dst string) error {
	return c.client.Call(context.Background(), desktop.RPCMethodFilesystemMove, desktop.RPCFilesystemMoveParams{Src: src, Dst: dst}, nil)
}

func (c *RemoteFilesystemRPCClient) Delete(path string) error {
	return c.client.Call(context.Background(), desktop.RPCMethodFilesystemDelete, desktop.RPCFilesystemPathParams{Path: path}, nil)
}

func (c *RemoteDesktopRPCClient) Screenshot(ctx context.Context, path string) error {
	return c.client.Call(ctx, desktop.RPCMethodDesktopScreenshot, desktop.RPCDesktopScreenshotParams{Path: path}, nil)
}

func (c *RemoteDesktopRPCClient) Windows(ctx context.Context) ([]desktop.Window, error) {
	var windows []desktop.Window
	err := c.client.Call(ctx, desktop.RPCMethodDesktopWindows, struct{}{}, &windows)
	return windows, err
}

func (c *RemoteDesktopRPCClient) Accessibility(ctx context.Context) (desktop.AccessibilityTree, error) {
	var tree desktop.AccessibilityTree
	err := c.client.Call(ctx, desktop.RPCMethodDesktopAccessibility, struct{}{}, &tree)
	return tree, err
}

func (c *RemoteDesktopRPCClient) Mouse(ctx context.Context, action desktop.MouseAction) error {
	return c.client.Call(ctx, desktop.RPCMethodDesktopMouse, desktop.RPCDesktopMouseParams{Action: action}, nil)
}

func (c *RemoteDesktopRPCClient) Keyboard(ctx context.Context, action desktop.KeyboardAction) error {
	return c.client.Call(ctx, desktop.RPCMethodDesktopKeyboard, desktop.RPCDesktopKeyboardParams{Action: action}, nil)
}

func (c *RemoteDesktopRPCClient) App(ctx context.Context, action desktop.AppAction) error {
	return c.client.Call(ctx, desktop.RPCMethodDesktopApp, desktop.RPCDesktopAppParams{Action: action}, nil)
}

func (c *RemoteDesktopRPCClient) Status(ctx context.Context) (desktop.RPCDeviceStatus, error) {
	var status desktop.RPCDeviceStatus
	err := c.client.Call(ctx, desktop.RPCMethodDeviceStatus, struct{}{}, &status)
	return status, err
}

func decodeAdminParams(method string, params []byte, dst any) error {
	if len(params) == 0 {
		params = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid %s params: %w", method, err)
	}
	if decoder.More() {
		return fmt.Errorf("invalid %s params: trailing data", method)
	}
	return nil
}
