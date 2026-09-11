package dispatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"unicode/utf8"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
)

const maxFilesystemOffset = int64(1<<53 - 1)

func (d *MCP) filesystemRead(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	method, ok := map[string]string{
		"read_file":      desktop.RPCMethodFilesystemReadRange,
		"read_directory": desktop.RPCMethodFilesystemList,
		"stat":           desktop.RPCMethodFilesystemStat,
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported filesystem read action %q", action)
	}
	path, err := requiredString(arguments, "path")
	if err != nil {
		return nil, err
	}
	caller := d.privilegedCaller(arguments)
	if action != "read_file" {
		return d.call(ctx, caller, method, desktop.RPCFilesystemPathParams{Path: path})
	}
	encoding, err := optionalFileEncoding(arguments)
	if err != nil {
		return nil, err
	}
	offset, err := filesystemByteInteger(arguments, "offset", 0, maxFilesystemOffset)
	if err != nil {
		return nil, err
	}
	limit, err := filesystemByteInteger(arguments, "limit", filesystem.DefaultReadLimit, filesystem.MaxReadLimit)
	if err != nil {
		return nil, err
	}
	if err := filesystem.ValidateReadRange(offset, int(limit)); err != nil {
		return nil, err
	}
	if offset > maxFilesystemOffset-limit {
		return nil, errors.New("filesystem offset plus limit must stay within 9007199254740991 bytes for exact pagination")
	}
	if caller == nil {
		return nil, errors.New("Executor helper is unavailable")
	}
	var result filesystem.ReadRangeResult
	if err := caller.Call(ctx, method, desktop.RPCFilesystemReadRangeParams{
		Path: path, OffsetBytes: offset, LimitBytes: int(limit),
	}, &result); err != nil {
		return nil, err
	}
	if len(result.Data) > int(limit) || result.OffsetBytes != offset || result.NextOffsetBytes != offset+int64(len(result.Data)) || result.EOF == result.Truncated || (result.Truncated && len(result.Data) != int(limit)) {
		return nil, errors.New("Executor helper returned invalid filesystem range metadata")
	}
	content := ""
	if encoding == "base64" {
		content = base64.StdEncoding.EncodeToString(result.Data)
	} else {
		if !utf8.Valid(result.Data) {
			return nil, errors.New("file byte range is not valid UTF-8; use encoding=base64 for exact bytes or align offset and limit to UTF-8 character boundaries")
		}
		content = string(result.Data)
	}
	return map[string]any{
		"content": content, "encoding": encoding, "size": len(result.Data),
		"offsetBytes": result.OffsetBytes, "returnedBytes": len(result.Data), "nextOffsetBytes": result.NextOffsetBytes,
		"truncated": result.Truncated, "eof": result.EOF,
	}, nil
}

func (d *MCP) filesystemWrite(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	method, ok := map[string]string{
		"write_file":  desktop.RPCMethodFilesystemWrite,
		"append_file": desktop.RPCMethodFilesystemAppend,
		"mkdir":       desktop.RPCMethodFilesystemMkdir,
		"move":        desktop.RPCMethodFilesystemMove,
		"delete":      desktop.RPCMethodFilesystemDelete,
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported filesystem write action %q", action)
	}
	caller := d.privilegedCaller(arguments)
	path, err := requiredString(arguments, "path")
	if err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "action": action}
	recursive := true // Preserve the documented legacy default.
	if raw, exists := arguments["recursive"]; exists {
		var valid bool
		recursive, valid = raw.(bool)
		if !valid || (action != "mkdir" && action != "delete") {
			return nil, errors.New("recursive must be a boolean used with mkdir or delete")
		}
	}
	var params any
	switch action {
	case "write_file", "append_file":
		content, ok := arguments["content"].(string)
		if !ok {
			return nil, errors.New("content must be a string")
		}
		data, encoding, err := decodeFileContent(arguments, content)
		if err != nil {
			return nil, err
		}
		params = desktop.RPCFilesystemWriteParams{Path: path, Data: data, Perm: fs.FileMode(0o644)}
		result["encoding"], result["size"], result["writtenBytes"] = encoding, len(data), len(data)
	case "move":
		destination, err := requiredString(arguments, "destination")
		if err != nil {
			return nil, err
		}
		params = desktop.RPCFilesystemMoveParams{Src: path, Dst: destination}
	case "delete":
		params = desktop.RPCFilesystemPathParams{Path: path}
		if !recursive {
			method = desktop.RPCMethodFilesystemDeleteOptions
			params = desktop.RPCFilesystemOptionsParams{Path: path, Recursive: false}
		}
	case "mkdir":
		params = desktop.RPCFilesystemMkdirParams{Path: path, Perm: fs.FileMode(0o755)}
		if !recursive {
			method = desktop.RPCMethodFilesystemMkdirOptions
			params = desktop.RPCFilesystemOptionsParams{Path: path, Perm: fs.FileMode(0o755), Recursive: false}
		}
	}
	if _, err := d.call(ctx, caller, method, params); err != nil {
		return nil, err
	}
	return result, nil
}

func optionalFileEncoding(arguments map[string]any) (string, error) {
	raw, exists := arguments["encoding"]
	if !exists {
		return "utf8", nil
	}
	encoding, ok := raw.(string)
	if !ok || (encoding != "utf8" && encoding != "base64") {
		return "", errors.New("filesystem encoding must be utf8 or base64")
	}
	return encoding, nil
}

func decodeFileContent(arguments map[string]any, content string) ([]byte, string, error) {
	encoding, err := optionalFileEncoding(arguments)
	if err != nil {
		return nil, "", err
	}
	if encoding == "utf8" {
		if !utf8.ValidString(content) {
			return nil, "", errors.New("file content must be valid UTF-8; use encoding=base64 for exact binary bytes")
		}
		return []byte(content), encoding, nil
	}
	data, err := base64.StdEncoding.Strict().DecodeString(content)
	if err != nil || base64.StdEncoding.EncodeToString(data) != content {
		clear(data)
		return nil, "", errors.New("file content is not strict standard base64")
	}
	return data, encoding, nil
}

// Check before converting floating-point JSON numbers to integers, before
// converting to the platform's int, and before making any helper call.
func filesystemByteInteger(arguments map[string]any, name string, defaultValue, maximum int64) (int64, error) {
	raw, exists := arguments[name]
	if !exists {
		return defaultValue, nil
	}
	invalid := fmt.Errorf("filesystem %s must be an integer byte count between 0 and %d", name, maximum)
	var value int64
	switch typed := raw.(type) {
	case int:
		value = int64(typed)
	case int64:
		value = typed
	case uint:
		if uint64(typed) > uint64(maximum) {
			return 0, invalid
		}
		value = int64(typed)
	case uint64:
		if typed > uint64(maximum) {
			return 0, invalid
		}
		value = int64(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < 0 || typed > float64(maximum) {
			return 0, invalid
		}
		value = int64(typed)
	case json.Number:
		var err error
		value, err = typed.Int64()
		if err != nil {
			return 0, invalid
		}
	default:
		return 0, invalid
	}
	if value < 0 || value > maximum {
		return 0, invalid
	}
	return value, nil
}
