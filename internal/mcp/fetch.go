package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

// This limit applies to the entire UTF-8 request string, including URI escaping.
// The original tools remain available for payloads above the proxy's bound.
const maxFetchRequestBytes = 8 << 20

func fetchTool() Tool {
	return Tool{
		Name:        "fetch",
		Description: "Execute one advertised Executor tool from a request string. Accepts JSON {\"name\":\"filesystem_write\",\"arguments\":{...}} or executor://call?request=<percent-encoded JSON>. This tool can modify files, run commands, and control the host; it requires write authorization. The URI is a local request envelope, never a network fetch. Original tool arguments, privileges, session context, results, and image blocks are preserved. Use the original tools/list schemas for target arguments. Each request dispatches at most one tool call with no automatic retries.",
		InputSchema: schemaObject(map[string]any{
			"request": map[string]any{
				"type": "string", "minLength": 1, "maxLength": maxFetchRequestBytes,
				"description": "One JSON object with exactly name and arguments (an object), optionally percent-encoded as executor://call?request=...; maximum 8388608 UTF-8 bytes including URI escaping. Target an advertised tool other than fetch. Keep credentials outside this string.",
			},
		}, "request"),
		Annotations: ToolAnnotations{DestructiveHint: true},
	}
}

// Resolve before calling the dispatcher so authorization context and the audit
// wrapper see the real operation and its original privilege and arguments.
func (s *Server) resolveFetchCall(call ToolCall) (ToolCall, error) {
	request, ok := call.Arguments["request"].(string)
	if !ok || len(call.Arguments) != 1 {
		return ToolCall{}, errors.New("fetch requires exactly one string argument: request")
	}
	if len(request) == 0 || len(request) > maxFetchRequestBytes || !utf8.ValidString(request) {
		return ToolCall{}, errors.New("fetch request must be valid UTF-8 between 1 and 8388608 bytes")
	}
	request = strings.TrimSpace(request)
	if strings.HasPrefix(request, "executor:") {
		parsed, err := url.Parse(request)
		if err != nil || parsed.Scheme != "executor" || parsed.Host != "call" || parsed.User != nil || parsed.Path != "" || parsed.Opaque != "" || strings.Contains(request, "#") {
			return ToolCall{}, errors.New("fetch URI must be executor://call?request=<percent-encoded JSON>")
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		values := query["request"]
		if err != nil || len(query) != 1 || len(values) != 1 || values[0] == "" {
			return ToolCall{}, errors.New("fetch URI must contain exactly one request query parameter")
		}
		request = values[0]
		if !utf8.ValidString(request) {
			return ToolCall{}, errors.New("fetch URI request must decode to valid UTF-8")
		}
	}

	invalid := errors.New("fetch request must be one JSON object with exactly name (a string) and arguments (an object)")
	decoder := json.NewDecoder(strings.NewReader(request))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return ToolCall{}, invalid
	}
	resolved := ToolCall{SessionID: call.SessionID}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return ToolCall{}, invalid
		}
		seen[key] = true
		switch key {
		case "name":
			err = decoder.Decode(&resolved.Name)
		case "arguments":
			err = decoder.Decode(&resolved.Arguments)
		default:
			return ToolCall{}, invalid
		}
		if err != nil {
			return ToolCall{}, invalid
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return ToolCall{}, invalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ToolCall{}, invalid
	}
	if !seen["name"] || !seen["arguments"] || resolved.Name == "" || resolved.Arguments == nil {
		return ToolCall{}, invalid
	}
	if resolved.Name == "fetch" || !s.toolExists(resolved.Name) {
		return ToolCall{}, errors.New("fetch target must be an advertised tool other than fetch")
	}
	return resolved, nil
}
