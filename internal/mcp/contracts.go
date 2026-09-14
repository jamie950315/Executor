package mcp

func bytePageLimitSchema() map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 1, "maximum": 1048576, "default": 65536,
		"description": "Maximum raw bytes returned in this page, before base64 encoding (default 65536; maximum 1048576).",
	}
}

func terminalOutputResultSchema() map[string]any {
	return schemaObject(map[string]any{
		"Data":           map[string]any{"type": []string{"string", "null"}, "description": "Base64 encoded raw terminal bytes; an empty value contains no bytes."},
		"StartCursor":    map[string]any{"type": "integer", "minimum": 0},
		"NextCursor":     map[string]any{"type": "integer", "minimum": 0},
		"Running":        map[string]any{"type": "boolean", "description": "Legacy alias of sessionRunning."},
		"Truncated":      map[string]any{"type": "boolean", "description": "Requested bytes were already evicted from the output buffer. Use hasMore to detect normal pagination."},
		"encoding":       enumProperty("string", "base64"),
		"returnedBytes":  map[string]any{"type": "integer", "minimum": 0},
		"hasMore":        map[string]any{"type": "boolean", "description": "Additional bytes were buffered at the time of this read."},
		"mode":           enumProperty("string", "interactive", "command"),
		"tty":            map[string]any{"type": "boolean"},
		"stream":         enumProperty("string", "combined", "stdout", "stderr"),
		"sessionRunning": map[string]any{"type": "boolean", "description": "True until the session process has exited and final output has been captured."},
		"commandRunning": map[string]any{"type": []string{"boolean", "null"}, "description": "Actual command-mode process state; null for an interactive shell."},
		"exitCode":       map[string]any{"type": []string{"integer", "null"}, "description": "Observed command-mode process exit code; null while running, for interactive shells, or when unavailable."},
	}, "Data", "StartCursor", "NextCursor", "Running", "Truncated", "encoding", "returnedBytes", "hasMore", "mode", "sessionRunning", "commandRunning", "exitCode")
}

func filesystemReadResultSchema() map[string]any {
	entry := schemaObject(map[string]any{
		"Name": map[string]any{"type": "string"}, "Path": map[string]any{"type": "string"}, "IsDir": map[string]any{"type": "boolean"},
	}, "Name", "Path", "IsDir")
	schema := schemaObject(map[string]any{
		"content":         map[string]any{"type": "string"},
		"encoding":        enumProperty("string", "utf8", "base64"),
		"size":            map[string]any{"type": "integer", "minimum": 0, "description": "Raw bytes in this page; alias of returnedBytes."},
		"offsetBytes":     map[string]any{"type": "integer", "minimum": 0},
		"returnedBytes":   map[string]any{"type": "integer", "minimum": 0},
		"nextOffsetBytes": map[string]any{"type": "integer", "minimum": 0},
		"truncated":       map[string]any{"type": "boolean", "description": "More file bytes were observed after this page."},
		"eof":             map[string]any{"type": "boolean", "description": "End of file observed during this read; subsequent calls may observe file changes."},
		"items":           map[string]any{"type": "array", "items": entry},
		"Path":            map[string]any{"type": "string"},
		"Size":            map[string]any{"type": "integer"},
		"Mode":            map[string]any{"type": "integer"},
		"ModTime":         map[string]any{"type": "string"},
		"IsDir":           map[string]any{"type": "boolean"},
	})
	schema["anyOf"] = []any{
		map[string]any{"required": []string{"content", "encoding", "size", "offsetBytes", "returnedBytes", "nextOffsetBytes", "truncated", "eof"}},
		map[string]any{"required": []string{"items"}},
		map[string]any{"required": []string{"Path", "Size", "Mode", "ModTime", "IsDir"}},
	}
	return schema
}

func filesystemWriteResultSchema() map[string]any {
	return schemaObject(map[string]any{
		"ok":           map[string]any{"type": "boolean", "const": true},
		"action":       enumProperty("string", "write_file", "append_file", "mkdir", "move", "delete"),
		"encoding":     enumProperty("string", "utf8", "base64"),
		"size":         map[string]any{"type": "integer", "minimum": 0, "description": "Raw bytes written or appended; alias of writtenBytes."},
		"writtenBytes": map[string]any{"type": "integer", "minimum": 0},
	}, "ok", "action")
}
