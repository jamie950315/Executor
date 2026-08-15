package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestBuiltinToolsExposeExpectedAnnotations(t *testing.T) {
	t.Parallel()

	want := map[string]ToolAnnotations{
		"terminal":         {DestructiveHint: true},
		"terminal_output":  {ReadOnlyHint: true},
		"terminal_sessions": {ReadOnlyHint: true},
		"filesystem_read":  {ReadOnlyHint: true},
		"filesystem_write": {DestructiveHint: true},
		"desktop_observe":  {ReadOnlyHint: true},
		"desktop_control":  {DestructiveHint: true},
		"device_status":    {ReadOnlyHint: true},
	}

	tools := BuiltinTools()
	if len(tools) != len(want) {
		t.Fatalf("BuiltinTools() returned %d tools, want %d", len(tools), len(want))
	}

	for _, tool := range tools {
		annotations, ok := want[tool.Name]
		if !ok {
			t.Fatalf("BuiltinTools() returned unexpected tool %q", tool.Name)
		}
		if tool.Annotations != annotations {
			t.Fatalf("tool %q annotations = %#v, want %#v", tool.Name, tool.Annotations, annotations)
		}
	}
}

func TestStreamableHTTPInitializePingToolsAndNotifications(t *testing.T) {
	t.Parallel()

	var dispatched ToolCall
	server := NewServer(ServerConfig{
		ServerName:    "Executor",
		ServerVersion: "dev",
		Dispatcher: func(_ context.Context, call ToolCall) (any, error) {
			dispatched = call
			return map[string]any{"ok": true}, nil
		},
	})

	initialize := rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "initialize",
		Params: map[string]any{
			"clientInfo": map[string]any{"name": "tester"},
		},
	}

	initializeResponse := performHTTPRequest(t, server, "", initialize)
	sessionID := initializeResponse.Header.Get(SessionHeader)
	if sessionID == "" {
		t.Fatal("initialize response missing session id header")
	}

	var initBody rpcResponse
	decodeJSON(t, initializeResponse.Body.Bytes(), &initBody)
	if initBody.Error != nil {
		t.Fatalf("initialize returned error: %#v", initBody.Error)
	}

	initResult := initBody.Result.(map[string]any)
	if initResult["sessionId"] != sessionID {
		t.Fatalf("initialize sessionId = %v, want %q", initResult["sessionId"], sessionID)
	}

	if got := initResult["protocolVersion"]; got == "" {
		t.Fatal("initialize protocolVersion is empty")
	}

	notification := rpcRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	notificationResponse := performHTTPRequest(t, server, sessionID, notification)
	if notificationResponse.Code != http.StatusNoContent {
		t.Fatalf("notifications/initialized status = %d, want %d", notificationResponse.Code, http.StatusNoContent)
	}

	ping := rpcRequest{
		JSONRPC: "2.0",
		ID:      "2",
		Method:  "ping",
	}
	pingResponse := performHTTPRequest(t, server, sessionID, ping)
	if got := pingResponse.Header.Get(SessionHeader); got != sessionID {
		t.Fatalf("ping session header = %q, want %q", got, sessionID)
	}

	var pingBody rpcResponse
	decodeJSON(t, pingResponse.Body.Bytes(), &pingBody)
	if pingBody.Error != nil {
		t.Fatalf("ping returned error: %#v", pingBody.Error)
	}

	if !reflect.DeepEqual(pingBody.Result, map[string]any{}) {
		t.Fatalf("ping result = %#v, want empty object", pingBody.Result)
	}

	toolsList := rpcRequest{
		JSONRPC: "2.0",
		ID:      "3",
		Method:  "tools/list",
	}
	toolsListResponse := performHTTPRequest(t, server, sessionID, toolsList)

	var toolsListBody rpcResponse
	decodeJSON(t, toolsListResponse.Body.Bytes(), &toolsListBody)
	if toolsListBody.Error != nil {
		t.Fatalf("tools/list returned error: %#v", toolsListBody.Error)
	}

	result := toolsListBody.Result.(map[string]any)
	gotTools := result["tools"].([]any)
	if len(gotTools) != len(BuiltinTools()) {
		t.Fatalf("tools/list returned %d tools, want %d", len(gotTools), len(BuiltinTools()))
	}

	call := rpcRequest{
		JSONRPC: "2.0",
		ID:      "4",
		Method:  "tools/call",
		Params: map[string]any{
			"name": "filesystem_read",
			"arguments": map[string]any{
				"path": "/tmp/demo.txt",
			},
		},
	}
	callResponse := performHTTPRequest(t, server, sessionID, call)

	var callBody rpcResponse
	decodeJSON(t, callResponse.Body.Bytes(), &callBody)
	if callBody.Error != nil {
		t.Fatalf("tools/call returned error: %#v", callBody.Error)
	}

	if dispatched.SessionID != sessionID {
		t.Fatalf("dispatcher session id = %q, want %q", dispatched.SessionID, sessionID)
	}
	if dispatched.Name != "filesystem_read" {
		t.Fatalf("dispatcher tool name = %q, want filesystem_read", dispatched.Name)
	}
	if got := dispatched.Arguments["path"]; got != "/tmp/demo.txt" {
		t.Fatalf("dispatcher path = %v, want /tmp/demo.txt", got)
	}
}

func TestStreamableHTTPRejectsUnknownSession(t *testing.T) {
	t.Parallel()

	server := NewServer(ServerConfig{
		ServerName:    "Executor",
		ServerVersion: "dev",
	})

	request := rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "ping",
	}

	response := performHTTPRequest(t, server, "missing-session", request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	var body rpcResponse
	decodeJSON(t, response.Body.Bytes(), &body)
	if body.Error == nil {
		t.Fatal("expected error for unknown session")
	}
	if body.Error.Code != errCodeInvalidSession {
		t.Fatalf("error code = %d, want %d", body.Error.Code, errCodeInvalidSession)
	}
}

func TestStdioHandlerUsesMCPFramingAndRetainsSession(t *testing.T) {
	t.Parallel()

	handler := NewStdioHandler(NewServer(ServerConfig{
		ServerName:    "Executor",
		ServerVersion: "dev",
	}))

	var input bytes.Buffer
	writeFrameForTest(t, &input, rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "initialize",
	})
	writeFrameForTest(t, &input, rpcRequest{
		JSONRPC: "2.0",
		ID:      "2",
		Method:  "tools/list",
	})

	var output bytes.Buffer
	if err := handler.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	reader := bufio.NewReader(&output)
	first := readFrameForTest(t, reader)
	second := readFrameForTest(t, reader)

	var initializeResponse rpcResponse
	decodeJSON(t, first, &initializeResponse)
	initResult := initializeResponse.Result.(map[string]any)
	if initResult["sessionId"] == "" {
		t.Fatal("stdio initialize response missing sessionId")
	}

	var toolsResponse rpcResponse
	decodeJSON(t, second, &toolsResponse)
	result := toolsResponse.Result.(map[string]any)
	if len(result["tools"].([]any)) != len(BuiltinTools()) {
		t.Fatalf("tools/list returned %d tools, want %d", len(result["tools"].([]any)), len(BuiltinTools()))
	}
}

func performHTTPRequest(t *testing.T, server *Server, sessionID string, request rpcRequest) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	httpRequest := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(payload))
	httpRequest.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		httpRequest.Header.Set(SessionHeader, sessionID)
	}

	recorder := httptest.NewRecorder()
	server.HandleStreamableHTTP(recorder, httpRequest)
	return recorder
}

func decodeJSON(t *testing.T, payload []byte, out *rpcResponse) {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		t.Fatalf("json decode error: %v\npayload=%s", err, payload)
	}
}

func writeFrameForTest(t *testing.T, buffer *bytes.Buffer, request rpcRequest) {
	t.Helper()

	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	if err := WriteFrame(buffer, payload); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
}

func readFrameForTest(t *testing.T, reader *bufio.Reader) []byte {
	t.Helper()

	frame, err := ReadFrame(reader)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if strings.TrimSpace(string(frame)) == "" {
		t.Fatal("ReadFrame() returned empty payload")
	}
	return frame
}
