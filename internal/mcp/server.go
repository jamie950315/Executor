package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	SessionHeader         = "Mcp-Session-Id"
	defaultProtocol       = "2025-06-18"
	errCodeInvalidSession = -32001
	errCodeToolFailure    = -32010
	maxHTTPMessage        = 80 << 20
)

type ToolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint,omitempty"`
	DestructiveHint bool `json:"destructiveHint,omitempty"`
}

type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  map[string]any  `json:"inputSchema,omitempty"`
	OutputSchema map[string]any  `json:"outputSchema,omitempty"`
	Annotations  ToolAnnotations `json:"annotations,omitempty"`
}

type ToolCall struct {
	SessionID string
	Name      string
	Arguments map[string]any
}

type Dispatcher func(ctx context.Context, call ToolCall) (any, error)

type ServerConfig struct {
	ServerName      string
	ServerVersion   string
	ProtocolVersion string
	Tools           []Tool
	Dispatcher      Dispatcher
	NewSessionID    func() string
}

type Server struct {
	serverName      string
	serverVersion   string
	protocolVersion string
	tools           []Tool
	dispatcher      Dispatcher
	newSessionID    func() string

	mu       sync.Mutex
	sessions map[string]*sessionState
}

type sessionState struct {
	Initialized bool
}

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id,omitempty"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type StdioHandler struct {
	server *Server

	mu        sync.Mutex
	sessionID string
}

func BuiltinTools() []Tool {
	return []Tool{
		{
			Name:        "terminal",
			Description: "Run commands in a persistent terminal session.",
			InputSchema: terminalToolSchema(),
			Annotations: ToolAnnotations{DestructiveHint: true},
		},
		{
			Name:        "terminal_output",
			Description: "Read buffered output from an existing terminal session.",
			InputSchema: terminalOutputToolSchema(),
			Annotations: ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name:        "terminal_sessions",
			Description: "Inspect running terminal sessions.",
			InputSchema: terminalSessionsToolSchema(),
			Annotations: ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name:        "filesystem_read",
			Description: "Read files from the host filesystem.",
			InputSchema: filesystemReadToolSchema(),
			Annotations: ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name:        "filesystem_write",
			Description: "Create, overwrite, move, or delete filesystem content.",
			InputSchema: filesystemWriteToolSchema(),
			Annotations: ToolAnnotations{DestructiveHint: true},
		},
		{
			Name:         "desktop_observe",
			Description:  "Observe desktop state. For Computer Use, call action=screenshot without a path to receive the current screen image and captureId before acting.",
			InputSchema:  desktopObserveToolSchema(),
			OutputSchema: desktopCaptureOutputSchema(),
			Annotations:  ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name:         "desktop_control",
			Description:  "Control the desktop. For Computer Use, send action=batch with the latest captureId and ordered actions; Executor rejects stale captures and automatically returns the updated screen image.",
			InputSchema:  desktopControlToolSchema(),
			OutputSchema: desktopCaptureOutputSchema(),
			Annotations:  ToolAnnotations{DestructiveHint: true},
		},
		{
			Name:        "device_status",
			Description: "Inspect machine and desktop availability.",
			InputSchema: deviceStatusToolSchema(),
			Annotations: ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name:         "device_permissions",
			Description:  "Inspect or request all host permissions used by Executor. A request may still require owner approval in operating-system prompts or settings.",
			InputSchema:  devicePermissionsToolSchema(),
			OutputSchema: devicePermissionsOutputSchema(),
			Annotations:  ToolAnnotations{DestructiveHint: true},
		},
	}
}

func NewServer(config ServerConfig) *Server {
	tools := config.Tools
	if len(tools) == 0 {
		tools = BuiltinTools()
	}

	protocolVersion := config.ProtocolVersion
	if protocolVersion == "" {
		protocolVersion = defaultProtocol
	}

	newSessionID := config.NewSessionID
	if newSessionID == nil {
		newSessionID = defaultSessionID
	}

	return &Server{
		serverName:      fallback(config.ServerName, "Executor"),
		serverVersion:   fallback(config.ServerVersion, "dev"),
		protocolVersion: protocolVersion,
		tools:           append([]Tool(nil), tools...),
		dispatcher:      config.Dispatcher,
		newSessionID:    newSessionID,
		sessions:        make(map[string]*sessionState),
	}
}

func NewStdioHandler(server *Server) *StdioHandler {
	return &StdioHandler{server: server}
}

func (s *Server) HandleStreamableHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if origin := request.Header.Get("Origin"); origin != "" && !sameRequestOrigin(origin, request) {
		http.Error(writer, "forbidden origin", http.StatusForbidden)
		return
	}

	var rpcReq rpcRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxHTTPMessage))
	if err := decoder.Decode(&rpcReq); err != nil {
		writeRequestDecodeError(writer, err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeRequestDecodeError(writer, err)
		return
	}
	if rpcReq.Method != "initialize" {
		if version := request.Header.Get("MCP-Protocol-Version"); version != "" && version != s.protocolVersion {
			writeRPCResponse(writer, http.StatusBadRequest, request.Header.Get(SessionHeader), *errorResponse(rpcReq.ID, -32600, "unsupported MCP-Protocol-Version header"))
			return
		}
	}

	response, status, sessionID := s.handleRPC(request.Context(), request.Header.Get(SessionHeader), rpcReq)
	if response == nil {
		if sessionID != "" {
			writer.Header().Set(SessionHeader, sessionID)
		}
		writer.WriteHeader(status)
		return
	}

	writeRPCResponse(writer, status, sessionID, *response)
}

func writeRequestDecodeError(writer http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeRPCResponse(writer, http.StatusRequestEntityTooLarge, "", *errorResponse(nil, -32600, "request body too large"))
		return
	}
	writeRPCResponse(writer, http.StatusBadRequest, "", *errorResponse(nil, -32700, "invalid JSON payload"))
}

func sameRequestOrigin(origin string, request *http.Request) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return false
	}
	host := request.Host
	if request.URL.Host != "" {
		host = request.URL.Host
	}
	requestHost, _, err := net.SplitHostPort(host)
	if err != nil {
		requestHost = host
	}
	return strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), strings.TrimSuffix(requestHost, "."))
}

func (s *StdioHandler) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	buffered := bufio.NewReader(reader)
	for {
		frame, err := ReadFrame(buffered)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		response, err := s.HandleFrame(ctx, frame)
		if err != nil {
			return err
		}
		if len(response) == 0 {
			continue
		}
		if err := WriteFrame(writer, response); err != nil {
			return err
		}
	}
}

func (s *StdioHandler) HandleFrame(ctx context.Context, frame []byte) ([]byte, error) {
	var rpcReq rpcRequest
	if err := json.Unmarshal(frame, &rpcReq); err != nil {
		return nil, err
	}

	s.mu.Lock()
	sessionID := s.sessionID
	s.mu.Unlock()

	response, _, nextSessionID := s.server.handleRPC(ctx, sessionID, rpcReq)
	if nextSessionID != "" {
		s.mu.Lock()
		s.sessionID = nextSessionID
		s.mu.Unlock()
	}

	if response == nil {
		return nil, nil
	}

	return json.Marshal(response)
}

func ReadFrame(reader *bufio.Reader) ([]byte, error) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
			return nil, err
		}
		line = []byte(strings.TrimRight(string(line), "\r\n"))
		if len(line) == 0 {
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}
			continue
		}
		return line, nil
	}
}

func WriteFrame(writer io.Writer, payload []byte) error {
	if strings.ContainsAny(string(payload), "\r\n") {
		return errors.New("stdio JSON payload must not contain newlines")
	}
	_, err := writer.Write(append(append([]byte(nil), payload...), '\n'))
	return err
}

func (s *Server) handleRPC(ctx context.Context, sessionID string, request rpcRequest) (*rpcResponse, int, string) {
	switch request.ID.(type) {
	case nil, string, float64:
		// JSON-RPC IDs may only be strings, numbers, or null.
	default:
		return errorResponse(nil, -32600, "invalid JSON-RPC id"), http.StatusBadRequest, sessionID
	}
	if request.JSONRPC != "2.0" {
		return errorResponse(request.ID, -32600, "invalid JSON-RPC version"), http.StatusBadRequest, sessionID
	}
	switch request.Method {
	case "initialize":
		protocolVersion, err := s.negotiateProtocolVersion(request.Params)
		if err != nil {
			return errorResponse(request.ID, -32602, err.Error()), http.StatusBadRequest, ""
		}
		nextSessionID := s.newSession()
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{
						"listChanged": false,
					},
				},
				"serverInfo": map[string]any{
					"name":    s.serverName,
					"version": s.serverVersion,
				},
				"sessionId": nextSessionID,
			},
		}, http.StatusOK, nextSessionID
	case "notifications/initialized":
		if !s.hasSession(sessionID) {
			return s.invalidSession(request.ID, sessionID)
		}
		s.mu.Lock()
		s.sessions[sessionID].Initialized = true
		s.mu.Unlock()
		return nil, http.StatusAccepted, sessionID
	case "ping":
		if !s.hasSession(sessionID) {
			return s.invalidSession(request.ID, sessionID)
		}
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result:  map[string]any{},
		}, http.StatusOK, sessionID
	case "tools/list":
		if !s.hasSession(sessionID) {
			return s.invalidSession(request.ID, sessionID)
		}
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: map[string]any{
				"tools": s.tools,
			},
		}, http.StatusOK, sessionID
	case "tools/call":
		if !s.hasSession(sessionID) {
			return s.invalidSession(request.ID, sessionID)
		}

		name, _ := request.Params["name"].(string)
		if name == "" {
			return errorResponse(request.ID, -32602, "missing tool name"), http.StatusBadRequest, sessionID
		}
		if !s.toolExists(name) {
			return errorResponse(request.ID, -32602, "unknown tool"), http.StatusBadRequest, sessionID
		}
		if s.dispatcher == nil {
			return errorResponse(request.ID, errCodeToolFailure, "tool dispatcher unavailable"), http.StatusInternalServerError, sessionID
		}

		arguments := map[string]any{}
		if rawArguments, present := request.Params["arguments"]; present {
			parsedArguments, ok := rawArguments.(map[string]any)
			if !ok {
				return errorResponse(request.ID, -32602, "tool arguments must be an object"), http.StatusBadRequest, sessionID
			}
			arguments = parsedArguments
		}

		result, err := s.dispatcher(ctx, ToolCall{
			SessionID: sessionID,
			Name:      name,
			Arguments: arguments,
		})
		if err != nil {
			return errorResponse(request.ID, errCodeToolFailure, err.Error()), http.StatusInternalServerError, sessionID
		}
		structuredResult := result
		var content []any
		isError := false
		if toolResult, ok := result.(ToolResult); ok {
			structuredResult = toolResult.StructuredContent
			if toolResult.Content != nil {
				content = toolResult.Content
			}
			isError = toolResult.IsError
		}

		structuredContent, err := normalizeStructuredContent(structuredResult)
		if err != nil {
			return errorResponse(request.ID, errCodeToolFailure, "tool result is not valid JSON"), http.StatusInternalServerError, sessionID
		}
		if content == nil {
			encoded, err := json.Marshal(structuredContent)
			if err != nil {
				return errorResponse(request.ID, errCodeToolFailure, "tool result is not valid JSON"), http.StatusInternalServerError, sessionID
			}
			content = []any{map[string]any{
				"type": "text",
				"text": string(encoded),
			}}
		}

		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: map[string]any{
				"toolName":          name,
				"structuredContent": structuredContent,
				"content":           content,
				"isError":           isError,
			},
		}, http.StatusOK, sessionID
	default:
		return errorResponse(request.ID, -32601, "method not found"), http.StatusNotFound, sessionID
	}
}

func normalizeStructuredContent(result any) (any, error) {
	if result == nil {
		return map[string]any{}, nil
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	encoded = bytes.TrimSpace(encoded)
	if len(encoded) == 0 {
		return nil, errors.New("empty JSON result")
	}

	switch encoded[0] {
	case '{':
		return json.RawMessage(encoded), nil
	case '[':
		return map[string]any{"items": json.RawMessage(encoded)}, nil
	case 'n':
		return map[string]any{}, nil
	default:
		return map[string]any{"value": json.RawMessage(encoded)}, nil
	}
}

func (s *Server) invalidSession(id any, sessionID string) (*rpcResponse, int, string) {
	if sessionID == "" {
		return errorResponse(id, errCodeInvalidSession, "missing session"), http.StatusBadRequest, ""
	}
	return errorResponse(id, errCodeInvalidSession, "unknown session"), http.StatusNotFound, ""
}

func (s *Server) hasSession(sessionID string) bool {
	if sessionID == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[sessionID]
	return ok
}

func (s *Server) newSession() string {
	sessionID := s.newSessionID()

	s.mu.Lock()
	s.sessions[sessionID] = &sessionState{}
	s.mu.Unlock()

	return sessionID
}

func (s *Server) toolExists(name string) bool {
	for _, tool := range s.tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func writeRPCResponse(writer http.ResponseWriter, status int, sessionID string, response rpcResponse) {
	payload, err := json.Marshal(response)
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	if sessionID != "" {
		writer.Header().Set(SessionHeader, sessionID)
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(payload)
}

func errorResponse(id any, code int, message string) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
}

func defaultSessionID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes[:])
}

func fallback(value string, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}

func (s *Server) negotiateProtocolVersion(params map[string]any) (string, error) {
	if params == nil {
		return s.protocolVersion, nil
	}

	requested, _ := params["protocolVersion"].(string)
	if requested == "" {
		return s.protocolVersion, nil
	}
	if requested == s.protocolVersion {
		return requested, nil
	}
	return s.protocolVersion, nil
}

func terminalToolSchema() map[string]any {
	return schemaObject(
		map[string]any{
			"action":    enumProperty("string", "create", "write", "signal", "resize", "close"),
			"privilege": enumProperty("string", "owner", "admin"),
			"sessionId": map[string]any{
				"type": "string",
			},
			"command": map[string]any{
				"type": "string",
			},
			"input": map[string]any{
				"type": "string",
			},
			"signal": map[string]any{
				"type": "string",
				"enum": []string{"interrupt", "terminate", "kill"},
			},
			"cwd": map[string]any{
				"type": "string",
			},
			"columns": map[string]any{"type": "integer", "minimum": 1, "maximum": 32767},
			"rows":    map[string]any{"type": "integer", "minimum": 1, "maximum": 32767},
			"environment": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
		},
		"action",
	)
}

func terminalOutputToolSchema() map[string]any {
	return schemaObject(
		map[string]any{
			"sessionId": map[string]any{"type": "string"},
			"cursor":    map[string]any{"type": "integer", "minimum": 0},
			"limit":     map[string]any{"type": "integer", "minimum": 1},
			"privilege": enumProperty("string", "owner", "admin"),
		},
		"sessionId",
	)
}

func terminalSessionsToolSchema() map[string]any {
	return schemaObject(
		map[string]any{
			"action":    enumProperty("string", "list", "inspect"),
			"privilege": enumProperty("string", "owner", "admin"),
			"sessionId": map[string]any{
				"type": "string",
			},
		},
		"action",
	)
}

func filesystemReadToolSchema() map[string]any {
	return schemaObject(
		map[string]any{
			"action":    enumProperty("string", "read_file", "read_directory", "stat"),
			"privilege": enumProperty("string", "owner", "admin"),
			"path":      map[string]any{"type": "string"},
			"encoding":  enumProperty("string", "utf8", "base64"),
			"offset":    map[string]any{"type": "integer", "minimum": 0},
			"limit":     map[string]any{"type": "integer", "minimum": 1},
		},
		"action",
		"path",
	)
}

func filesystemWriteToolSchema() map[string]any {
	return schemaObject(
		map[string]any{
			"action":      enumProperty("string", "write_file", "append_file", "mkdir", "move", "delete"),
			"privilege":   enumProperty("string", "owner", "admin"),
			"path":        map[string]any{"type": "string"},
			"destination": map[string]any{"type": "string"},
			"content":     map[string]any{"type": "string"},
			"encoding":    enumProperty("string", "utf8", "base64"),
			"recursive":   map[string]any{"type": "boolean"},
		},
		"action",
		"path",
	)
}

func desktopObserveToolSchema() map[string]any {
	return schemaObject(
		map[string]any{
			"action": enumProperty("string", "screenshot", "accessibility_tree", "windows", "applications"),
			"path":   map[string]any{"type": "string"},
			"windowId": map[string]any{
				"type": "string",
			},
			"includeImage": map[string]any{
				"type": "boolean",
			},
		},
		"action",
	)
}

func desktopControlToolSchema() map[string]any {
	computerAction := schemaObject(
		map[string]any{
			"type":    enumProperty("string", "click", "double_click", "move", "drag", "scroll", "type", "keypress", "wait", "screenshot"),
			"x":       map[string]any{"type": "integer"},
			"y":       map[string]any{"type": "integer"},
			"button":  map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}},
			"text":    map[string]any{"type": "string"},
			"keys":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"scrollX": map[string]any{"type": "integer"},
			"scrollY": map[string]any{"type": "integer"},
			"path": map[string]any{
				"type": "array",
				"items": schemaObject(map[string]any{
					"x": map[string]any{"type": "integer"},
					"y": map[string]any{"type": "integer"},
				}, "x", "y"),
			},
		},
		"type",
	)
	return schemaObject(
		map[string]any{
			"action":    enumProperty("string", "mouse_move", "mouse_click", "key_press", "type_text", "window_focus", "batch"),
			"x":         map[string]any{"type": "integer"},
			"y":         map[string]any{"type": "integer"},
			"button":    map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}},
			"keyCode":   map[string]any{"type": "integer", "minimum": 1, "maximum": 255},
			"modifiers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"name":      map[string]any{"type": "string"},
			"text":      map[string]any{"type": "string"},
			"captureId": map[string]any{"type": "string"},
			"actions":   map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": computerAction},
		},
		"action",
	)
}

func desktopCaptureOutputSchema() map[string]any {
	return schemaObject(map[string]any{
		"captureId":  map[string]any{"type": "string"},
		"width":      map[string]any{"type": "integer", "minimum": 1},
		"height":     map[string]any{"type": "integer", "minimum": 1},
		"mimeType":   map[string]any{"type": "string"},
		"capturedAt": map[string]any{"type": "string"},
	})
}

func deviceStatusToolSchema() map[string]any {
	return schemaObject(
		map[string]any{
			"action": enumProperty("string", "summary", "desktop", "terminals"),
		},
		"action",
	)
}

func devicePermissionsToolSchema() map[string]any {
	return schemaObject(
		map[string]any{
			"action": enumProperty("string", "status", "request_all"),
		},
		"action",
	)
}

func devicePermissionsOutputSchema() map[string]any {
	item := schemaObject(map[string]any{
		"id":           map[string]any{"type": "string"},
		"label":        map[string]any{"type": "string"},
		"state":        enumProperty("string", "granted", "pending", "denied", "manual", "unavailable", "not_required"),
		"required":     map[string]any{"type": "boolean"},
		"detail":       map[string]any{"type": "string"},
		"settings_url": map[string]any{"type": "string"},
	}, "id", "label", "state", "required")
	return schemaObject(map[string]any{
		"platform":         map[string]any{"type": "string"},
		"requested":        map[string]any{"type": "boolean"},
		"ready":            map[string]any{"type": "boolean"},
		"restart_required": map[string]any{"type": "boolean"},
		"permissions":      map[string]any{"type": "array", "items": item},
	}, "platform", "requested", "ready", "permissions")
}

func schemaObject(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func enumProperty(kind string, values ...string) map[string]any {
	return map[string]any{
		"type": kind,
		"enum": values,
	}
}
