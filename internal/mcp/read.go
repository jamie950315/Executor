package mcp

import (
	"context"
	"errors"
)

func readTool() Tool {
	schema := schemaObject(map[string]any{
		"action": enumProperty("string", "tools", "call"),
		"request": map[string]any{
			"type": "string", "minLength": 1, "maxLength": maxFetchRequestBytes,
			"description": "Required only for action=call. JSON {\"name\":\"filesystem_read\",\"arguments\":{...}} or executor://call?request=<percent-encoded JSON>; at most 8388608 UTF-8 bytes. Use a readOperations entry from action=tools.",
		},
	}, "action")
	schema["additionalProperties"] = false
	return Tool{
		Name:        "read",
		Description: "Read Executor data or discover internal operation schemas. action=tools takes no request and returns operations (invoke via fetch) and readOperations (invoke via read action=call). Internal names are request operands, not directly callable tools. action=call accepts one bounded request string for file reads, terminal output/session inspection, device status, permission status, or desktop observation without file export. Write, command execution, desktop input, screenshot export, and permission requests require the explicitly mutating fetch tool and its authorization.",
		InputSchema: schema,
		Annotations: ToolAnnotations{ReadOnlyHint: true},
	}
}

func (s *Server) dispatchProxyCall(ctx context.Context, call ToolCall) (any, error) {
	var err error
	switch call.Name {
	case "read":
		action, _ := call.Arguments["action"].(string)
		if action == "tools" && len(call.Arguments) == 1 {
			return s.operationCatalog(), nil
		}
		if action != "call" || len(call.Arguments) != 2 {
			return nil, errors.New("read requires action=tools alone, or action=call with exactly one request string")
		}
		call.Arguments = map[string]any{"request": call.Arguments["request"]}
		call, err = s.resolveFetchCall(call)
		if err == nil && !readCallAllowed(call) {
			err = errors.New("read accepts only readOperations; modifying operations require fetch and write authorization")
		}
	case "fetch":
		call, err = s.resolveFetchCall(call)
	}
	if err != nil {
		return nil, err
	}
	return s.dispatcher(ctx, call)
}

// Restrict by known operation and action, including mixed tools whose complete
// schema contains mutations. Do not infer safety from request text or a label.
func readCallAllowed(call ToolCall) bool {
	action, _ := call.Arguments["action"].(string)
	switch call.Name {
	case "filesystem_read", "terminal_output", "terminal_sessions", "device_status":
		return true // These dispatchers validate and implement read-only actions.
	case "device_permissions":
		return action == "status"
	case "desktop_observe":
		if _, hasPath := call.Arguments["path"]; hasPath {
			return false
		}
		switch action {
		case "screenshot", "accessibility_tree", "windows", "applications":
			return true
		}
	}
	return false
}

func (s *Server) operationCatalog() map[string]any {
	operations := make([]Tool, 0, len(s.operations))
	reads := make([]Tool, 0, 6)
	for _, operation := range s.operations {
		if !s.operationExists(operation.Name) {
			continue
		}
		operations = append(operations, operation)
		read := operation
		switch operation.Name {
		case "filesystem_read", "terminal_output", "terminal_sessions", "device_status":
		case "desktop_observe":
			read.Description = "Observe the desktop or return a screenshot and captureId. File export is available through fetch only; omit path entirely."
			read.InputSchema = desktopObserveToolSchema()
			delete(read.InputSchema["properties"].(map[string]any), "path")
			read.InputSchema["additionalProperties"] = false
		case "device_permissions":
			read.Description = "Inspect desktop permission prerequisites. Requesting permissions is available through fetch only."
			read.InputSchema = schemaObject(map[string]any{"action": enumProperty("string", "status")}, "action")
			read.InputSchema["additionalProperties"] = false
		default:
			continue
		}
		read.Annotations = ToolAnnotations{ReadOnlyHint: true}
		reads = append(reads, read)
	}
	return map[string]any{
		"operations": operations, "readOperations": reads,
		"usage": "Internal operation names are request operands. Use fetch(request) for operations, or read(action=call, request) for the restricted readOperations schemas. Read and fetch are the only default public tools. Modifying operations require write authorization.",
	}
}
