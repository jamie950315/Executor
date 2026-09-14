package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/terminal"
)

func (d *MCP) terminal(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	caller, err := d.terminalCaller(arguments)
	if err != nil {
		return nil, err
	}
	if _, hasArgv := arguments["argv"]; hasArgv && action != "create" {
		return nil, errors.New("argv is supported only for terminal create")
	}
	if _, exists := arguments["tty"]; exists && action != "create" {
		return nil, errors.New("tty is supported only for terminal create")
	}
	if action == "create" {
		params, err := terminalCreateParams(arguments)
		if err != nil {
			return nil, err
		}
		return d.call(ctx, caller, desktop.RPCMethodTerminalStart, params)
	}
	if action != "write" && action != "signal" && action != "resize" && action != "close" && action != "close_stdin" {
		return nil, fmt.Errorf("unsupported terminal action %q", action)
	}
	sessionID, err := requiredString(arguments, "sessionId")
	if err != nil {
		return nil, err
	}
	switch action {
	case "close_stdin":
		return d.call(ctx, caller, desktop.RPCMethodTerminalCloseStdin, desktop.RPCSessionParams{SessionID: sessionID})
	case "write":
		input, ok := arguments["input"].(string)
		if !ok {
			return nil, errors.New("terminal write requires input as a string")
		}
		if _, err := d.call(ctx, caller, desktop.RPCMethodTerminalWrite, desktop.RPCTerminalWriteParams{SessionID: sessionID, Input: []byte(input)}); err != nil {
			return nil, err
		}
		// This acknowledges delivery to stdin, including a successful empty write.
		// The process may still be running, waiting for input, or already finishing.
		return map[string]any{"ok": true, "inputAccepted": true, "completion": "unknown"}, nil
	case "signal":
		signalName, err := requiredString(arguments, "signal")
		if err != nil {
			return nil, err
		}
		signal := terminal.Signal(signalName)
		if signal != terminal.SignalInterrupt && signal != terminal.SignalTerminate && signal != terminal.SignalKill {
			return nil, fmt.Errorf("unsupported terminal signal %q", signalName)
		}
		return d.call(ctx, caller, desktop.RPCMethodTerminalSignal, desktop.RPCTerminalSignalParams{SessionID: sessionID, Signal: signal})
	case "resize":
		columns, err := terminalInteger(arguments, "columns", 0, 1, terminal.MaxDimension)
		if err != nil {
			return nil, err
		}
		rows, err := terminalInteger(arguments, "rows", 0, 1, terminal.MaxDimension)
		if err != nil {
			return nil, err
		}
		if columns == 0 || rows == 0 {
			return nil, errors.New("terminal resize requires columns and rows")
		}
		return d.call(ctx, caller, desktop.RPCMethodTerminalResize, desktop.RPCTerminalResizeParams{SessionID: sessionID, Columns: int(columns), Rows: int(rows)})
	case "close":
		return d.call(ctx, caller, desktop.RPCMethodTerminalClose, desktop.RPCSessionParams{SessionID: sessionID})
	default:
		return nil, fmt.Errorf("unsupported terminal action %q", action)
	}
}

func terminalCreateParams(arguments map[string]any) (desktop.RPCTerminalStartParams, error) {
	var params desktop.RPCTerminalStartParams
	if raw, exists := arguments["tty"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return params, errors.New("terminal tty must be a boolean")
		}
		params.TTY = &value
	}
	if raw, exists := arguments["argv"]; exists {
		if _, hasCommand := arguments["command"]; hasCommand {
			return params, errors.New("terminal create accepts either argv or command")
		}
		switch values := raw.(type) {
		case []string:
			params.Command = append([]string(nil), values...)
		case []any:
			for _, value := range values {
				text, ok := value.(string)
				if !ok {
					return params, errors.New("terminal argv must be an array of strings")
				}
				params.Command = append(params.Command, text)
			}
		default:
			return params, errors.New("terminal argv must be an array of strings")
		}
		if len(params.Command) == 0 {
			return params, errors.New("terminal argv requires an executable")
		}
	}
	if raw, exists := arguments["command"]; exists {
		command, ok := raw.(string)
		if !ok {
			return params, errors.New("terminal command must be a string")
		}
		if command != "" {
			params.InitialInput = []byte(command + "\n")
		}
	}
	if raw, exists := arguments["cwd"]; exists {
		cwd, ok := raw.(string)
		if !ok {
			return params, errors.New("terminal cwd must be a string")
		}
		params.Dir = cwd
	}
	if raw, exists := arguments["environment"]; exists {
		params.Env = make(map[string]string)
		switch values := raw.(type) {
		case map[string]string:
			for key, value := range values {
				params.Env[key] = value
			}
		case map[string]any:
			for key, value := range values {
				text, ok := value.(string)
				if !ok {
					return params, errors.New("terminal environment must contain string values")
				}
				params.Env[key] = text
			}
		default:
			return params, errors.New("terminal environment must be an object of strings")
		}
	}
	columns, err := terminalInteger(arguments, "columns", 0, 0, terminal.MaxDimension)
	if err != nil {
		return params, err
	}
	rows, err := terminalInteger(arguments, "rows", 0, 0, terminal.MaxDimension)
	if err != nil {
		return params, err
	}
	params.Columns, params.Rows = int(columns), int(rows)
	return params, terminal.ValidateSessionSpec(terminal.SessionSpec{
		Command: params.Command, Dir: params.Dir, Env: params.Env, Columns: params.Columns, Rows: params.Rows, TTY: params.TTY,
	})
}

func (d *MCP) terminalOutput(ctx context.Context, arguments map[string]any) (any, error) {
	sessionID, err := requiredString(arguments, "sessionId")
	if err != nil {
		return nil, err
	}
	caller, err := d.terminalCaller(arguments)
	if err != nil {
		return nil, err
	}
	cursor, err := terminalInteger(arguments, "cursor", 0, 0, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	limit, err := terminalInteger(arguments, "limit", terminal.DefaultOutputLimit, 1, terminal.MaxOutputLimit)
	if err != nil {
		return nil, err
	}
	stream := ""
	if raw, exists := arguments["stream"]; exists {
		var ok bool
		stream, ok = raw.(string)
		if !ok || (stream != "combined" && stream != "stdout" && stream != "stderr") {
			return nil, errors.New("stream must be combined, stdout or stderr")
		}
	}
	return d.call(ctx, caller, desktop.RPCMethodTerminalRead, desktop.RPCTerminalReadParams{
		SessionID: sessionID, Cursor: cursor, Limit: int(limit), Stream: stream,
	})
}

func (d *MCP) terminalSessions(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	if action != "list" && action != "inspect" && action != "capabilities" {
		return nil, fmt.Errorf("unsupported terminal sessions action %q", action)
	}
	caller, err := d.terminalCaller(arguments)
	if err != nil {
		return nil, err
	}
	if action == "capabilities" {
		return d.call(ctx, caller, desktop.RPCMethodTerminalCapabilities, struct{}{})
	}
	var sessionID string
	if action == "inspect" {
		sessionID, err = requiredString(arguments, "sessionId")
		if err != nil {
			return nil, err
		}
	}
	listed, err := d.call(ctx, caller, desktop.RPCMethodTerminalList, struct{}{})
	if err != nil || action == "list" {
		return listed, err
	}
	for _, item := range anySlice(listed) {
		entry, _ := item.(map[string]any)
		session, _ := entry["Session"].(map[string]any)
		id, _ := session["ID"].(string)
		if id == "" {
			session, _ = entry["session"].(map[string]any)
			id, _ = session["id"].(string)
		}
		if id == sessionID {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("terminal session %q not found", sessionID)
}

func (d *MCP) terminalCaller(arguments map[string]any) (Caller, error) {
	if raw, exists := arguments["privilege"]; exists {
		privilege, ok := raw.(string)
		if !ok || (privilege != "owner" && privilege != "admin") {
			return nil, errors.New("terminal privilege must be owner or admin")
		}
	}
	return d.privilegedCaller(arguments), nil
}

func terminalInteger(arguments map[string]any, key string, fallback, minimum, maximum int64) (int64, error) {
	raw, exists := arguments[key]
	if !exists {
		return fallback, nil
	}
	invalid := func() (int64, error) {
		return 0, fmt.Errorf("terminal %s must be an integer between %d and %d", key, minimum, maximum)
	}
	var value int64
	switch typed := raw.(type) {
	case int:
		value = int64(typed)
	case int64:
		value = typed
	case float64:
		// Reject the rounded-up float representation of MaxInt64 before conversion.
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < 0 || typed >= float64(uint64(1)<<63) {
			return invalid()
		}
		value = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return invalid()
		}
		value = parsed
	default:
		return invalid()
	}
	if value < minimum || value > maximum {
		return invalid()
	}
	return value, nil
}
