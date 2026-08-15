package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jamie950315/executor/internal/terminal"
)

const (
	RPCMethodTerminalStart   = "terminal.start"
	RPCMethodTerminalWrite   = "terminal.write"
	RPCMethodTerminalRead    = "terminal.read"
	RPCMethodTerminalList    = "terminal.list"
	RPCMethodTerminalClose   = "terminal.close"
	RPCMethodTerminalKill    = "terminal.kill"
	RPCMethodTerminalKillAll = "terminal.kill-all"
	RPCMethodTerminalSignal  = "terminal.signal"

	RPCMethodFilesystemRead   = "filesystem.read"
	RPCMethodFilesystemList   = "filesystem.list"
	RPCMethodFilesystemGlob   = "filesystem.glob"
	RPCMethodFilesystemStat   = "filesystem.stat"
	RPCMethodFilesystemWrite  = "filesystem.write"
	RPCMethodFilesystemAppend = "filesystem.append"
	RPCMethodFilesystemMkdir  = "filesystem.mkdir"
	RPCMethodFilesystemMove   = "filesystem.move"
	RPCMethodFilesystemDelete = "filesystem.delete"

	RPCMethodDeviceStatus         = "device.status"
	RPCMethodDesktopScreenshot    = "desktop.screenshot"
	RPCMethodDesktopWindows       = "desktop.windows"
	RPCMethodDesktopAccessibility = "desktop.accessibility"
	RPCMethodDesktopMouse         = "desktop.mouse"
	RPCMethodDesktopKeyboard      = "desktop.keyboard"
	RPCMethodDesktopApp           = "desktop.app"
)

type RPCTerminalStartParams struct {
	Command      []string          `json:"command,omitempty"`
	Dir          string            `json:"dir,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	InitialInput []byte            `json:"initial_input,omitempty"`
}

type RPCTerminalWriteParams struct {
	SessionID string `json:"session_id"`
	Input     []byte `json:"input"`
}

type RPCTerminalReadParams struct {
	SessionID string `json:"session_id"`
	Cursor    int64  `json:"cursor"`
}

type RPCSessionParams struct {
	SessionID string `json:"session_id"`
}

type RPCTerminalSignalParams struct {
	SessionID string          `json:"session_id"`
	Signal    terminal.Signal `json:"signal"`
}

type RPCFilesystemPathParams struct {
	Path string `json:"path"`
}

type RPCFilesystemGlobParams struct {
	Pattern string `json:"pattern"`
}

type RPCFilesystemWriteParams struct {
	Path string      `json:"path"`
	Data []byte      `json:"data"`
	Perm fs.FileMode `json:"perm"`
}

type RPCFilesystemMkdirParams struct {
	Path string      `json:"path"`
	Perm fs.FileMode `json:"perm"`
}

type RPCFilesystemMoveParams struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

type RPCDesktopScreenshotParams struct {
	Path string `json:"path"`
}

type RPCDesktopMouseParams struct {
	Action MouseAction `json:"action"`
}

type RPCDesktopKeyboardParams struct {
	Action KeyboardAction `json:"action"`
}

type RPCDesktopAppParams struct {
	Action AppAction `json:"action"`
}

type RPCDeviceStatus struct {
	Component        string `json:"component"`
	TerminalSessions int    `json:"terminal_sessions"`
	Available        bool   `json:"available,omitempty"`
}

func decodeStrictParams(method string, raw []byte, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid %s params: %w", method, err)
	}
	if decoder.More() {
		return fmt.Errorf("invalid %s params: trailing data", method)
	}
	return nil
}

func validateTerminalStartParams(params RPCTerminalStartParams) error {
	return nil
}

func validateSessionParams(method string, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("%s requires session_id", method)
	}
	return nil
}

func validateFilesystemPath(method string, path string) error {
	if path == "" {
		return fmt.Errorf("%s requires path", method)
	}
	return nil
}

func validateFilesystemMove(params RPCFilesystemMoveParams) error {
	if params.Src == "" || params.Dst == "" {
		return errors.New("filesystem.move requires src and dst")
	}
	return nil
}

func validateMouseAction(action MouseAction) error {
	if action.Type == "" {
		return errors.New("mouse action type is required")
	}
	return nil
}

func validateKeyboardAction(action KeyboardAction) error {
	if action.Text == "" && action.KeyCode == 0 {
		return errors.New("keyboard action requires text or key_code")
	}
	return nil
}

func validateAppAction(action AppAction) error {
	if action.Type == "" || action.Name == "" {
		return errors.New("app action requires type and name")
	}
	return nil
}

func terminalStartSpec(params RPCTerminalStartParams) terminal.SessionSpec {
	return terminal.SessionSpec{
		Command: append([]string(nil), params.Command...),
		Dir:     params.Dir,
		Env:     params.Env,
	}
}
