package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type arrayJSONMarshaler struct{}

func (arrayJSONMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`[1,2]`), nil
}

type scalarJSONMarshaler struct{}

func (scalarJSONMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`"custom"`), nil
}

type statefulJSONMarshaler struct {
	calls int
}

func (value *statefulJSONMarshaler) MarshalJSON() ([]byte, error) {
	value.calls++
	if value.calls == 1 {
		return []byte(`[1]`), nil
	}
	return []byte(`"changed"`), nil
}

type statefulObjectJSONMarshaler struct {
	calls int
}

func (value *statefulObjectJSONMarshaler) MarshalJSON() ([]byte, error) {
	value.calls++
	if value.calls == 1 {
		return []byte(`{"width":3024,"height":1964}`), nil
	}
	return []byte(`{"unexpected":true}`), nil
}

func TestBuiltinToolsExposeExpectedAnnotations(t *testing.T) {
	t.Parallel()

	want := map[string]ToolAnnotations{
		"terminal":          {DestructiveHint: true},
		"terminal_output":   {ReadOnlyHint: true},
		"terminal_sessions": {ReadOnlyHint: true},
		"filesystem_read":   {ReadOnlyHint: true},
		"filesystem_write":  {DestructiveHint: true},
		"desktop_observe":   {ReadOnlyHint: true},
		"desktop_control":   {DestructiveHint: true},
		"device_status":     {ReadOnlyHint: true},
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
		assertConcreteToolSchema(t, tool)
	}
}

func TestPrivilegedToolsExposeOwnerAndAdminSelection(t *testing.T) {
	t.Parallel()

	for _, tool := range BuiltinTools() {
		switch tool.Name {
		case "terminal", "terminal_output", "terminal_sessions", "filesystem_read", "filesystem_write":
			properties := tool.InputSchema["properties"].(map[string]any)
			privilege, ok := properties["privilege"].(map[string]any)
			if !ok {
				t.Fatalf("tool %q missing privilege selector", tool.Name)
			}
			if got := privilege["enum"]; !reflect.DeepEqual(got, []string{"owner", "admin"}) {
				t.Fatalf("tool %q privilege enum = %#v", tool.Name, got)
			}
		}
	}
}

func TestDesktopToolSchemasMatchExecutableArguments(t *testing.T) {
	t.Parallel()
	for _, tool := range BuiltinTools() {
		properties := tool.InputSchema["properties"].(map[string]any)
		switch tool.Name {
		case "desktop_observe":
			if _, ok := properties["path"]; !ok {
				t.Fatal("desktop_observe screenshot path is missing")
			}
		case "desktop_control":
			for _, name := range []string{"keyCode", "modifiers", "name"} {
				if _, ok := properties[name]; !ok {
					t.Fatalf("desktop_control property %q is missing", name)
				}
			}
		}
	}
}

func TestDesktopToolSchemasExposeComputerUseLoop(t *testing.T) {
	t.Parallel()

	for _, tool := range BuiltinTools() {
		if tool.Name != "desktop_control" {
			continue
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		actions := properties["action"].(map[string]any)["enum"].([]string)
		if !slices.Contains(actions, "batch") {
			t.Fatalf("desktop_control actions = %#v, missing batch", actions)
		}
		if _, ok := properties["captureId"].(map[string]any); !ok {
			t.Fatal("desktop_control captureId schema is missing")
		}
		batchActions, ok := properties["actions"].(map[string]any)
		if !ok {
			t.Fatal("desktop_control actions schema is missing")
		}
		items, ok := batchActions["items"].(map[string]any)
		if !ok || items["type"] != "object" {
			t.Fatalf("desktop_control action item schema = %#v", batchActions["items"])
		}
		itemProperties := items["properties"].(map[string]any)
		if _, ok := itemProperties["keyCode"]; ok {
			t.Fatal("Computer Use keypress schema advertises unsupported keyCode instead of keys")
		}
		types := itemProperties["type"].(map[string]any)["enum"].([]string)
		for _, actionType := range []string{"click", "double_click", "move", "drag", "scroll", "type", "keypress", "wait", "screenshot"} {
			if !slices.Contains(types, actionType) {
				t.Fatalf("computer action types = %#v, missing %q", types, actionType)
			}
		}
		return
	}
	t.Fatal("desktop_control tool is missing")
}

func TestTerminalToolSchemaExposesResizeDimensions(t *testing.T) {
	t.Parallel()
	var terminal Tool
	for _, tool := range BuiltinTools() {
		if tool.Name == "terminal" {
			terminal = tool
			break
		}
	}
	properties := terminal.InputSchema["properties"].(map[string]any)
	actions := properties["action"].(map[string]any)["enum"].([]string)
	if !slices.Contains(actions, "resize") {
		t.Fatalf("terminal actions = %#v, missing resize", actions)
	}
	for _, name := range []string{"columns", "rows"} {
		property, ok := properties[name].(map[string]any)
		if !ok || property["minimum"] != 1 || property["maximum"] != 32767 {
			t.Fatalf("terminal %s schema = %#v", name, properties[name])
		}
	}
}

func TestInitializeNegotiatesSupportedProtocolVersion(t *testing.T) {
	t.Parallel()

	server := NewServer(ServerConfig{
		ServerName:    "Executor",
		ServerVersion: "dev",
	})

	response := performHTTPRequest(t, server, "", rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2025-06-18",
			"clientInfo":      map[string]any{"name": "tester"},
		},
	})

	var body rpcResponse
	decodeJSON(t, response.Body.Bytes(), &body)
	if body.Error != nil {
		t.Fatalf("initialize returned error: %#v", body.Error)
	}

	result := body.Result.(map[string]any)
	if got := result["protocolVersion"]; got != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want 2025-06-18", got)
	}
}

func TestInitializeRespondsWithServerVersionWhenClientRequestsNewerVersion(t *testing.T) {
	t.Parallel()
	server := NewServer(ServerConfig{ProtocolVersion: "2025-06-18"})
	response := performHTTPRequest(t, server, "", rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "initialize",
		Params:  map[string]any{"protocolVersion": "2025-11-25"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200", response.Code)
	}
	var body rpcResponse
	decodeJSON(t, response.Body.Bytes(), &body)
	if body.Error != nil {
		t.Fatalf("initialize returned error: %#v", body.Error)
	}
	if got := body.Result.(map[string]any)["protocolVersion"]; got != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want server-supported version", got)
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
	sessionID := initializeResponse.Result().Header.Get(SessionHeader)
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
	if notificationResponse.Code != http.StatusAccepted {
		t.Fatalf("notifications/initialized status = %d, want %d", notificationResponse.Code, http.StatusAccepted)
	}

	ping := rpcRequest{
		JSONRPC: "2.0",
		ID:      "2",
		Method:  "ping",
	}
	pingResponse := performHTTPRequest(t, server, sessionID, ping)
	if got := pingResponse.Result().Header.Get(SessionHeader); got != sessionID {
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

func TestToolCallWrapsArrayResultInObjectStructuredContent(t *testing.T) {
	t.Parallel()

	server := NewServer(ServerConfig{
		Dispatcher: func(_ context.Context, _ ToolCall) (any, error) {
			return []any{
				map[string]any{"app": "Finder", "title": "Desktop"},
			}, nil
		},
	})

	initialize := performHTTPRequest(t, server, "", rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "initialize",
	})
	sessionID := initialize.Header().Get(SessionHeader)
	if sessionID == "" {
		t.Fatal("initialize response missing session id header")
	}

	response := performHTTPRequest(t, server, sessionID, rpcRequest{
		JSONRPC: "2.0",
		ID:      "2",
		Method:  "tools/call",
		Params: map[string]any{
			"name":      "desktop_observe",
			"arguments": map[string]any{"action": "windows"},
		},
	})

	var body rpcResponse
	decodeJSON(t, response.Body.Bytes(), &body)
	if body.Error != nil {
		t.Fatalf("tools/call returned error: %#v", body.Error)
	}

	result := body.Result.(map[string]any)
	structuredContent, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent = %#v, want object", result["structuredContent"])
	}
	want := map[string]any{
		"items": []any{
			map[string]any{"app": "Finder", "title": "Desktop"},
		},
	}
	if !reflect.DeepEqual(structuredContent, want) {
		t.Fatalf("structuredContent = %#v, want %#v", structuredContent, want)
	}
}

func TestToolCallReturnsExplicitMultimodalResult(t *testing.T) {
	t.Parallel()

	structuredContent := &statefulObjectJSONMarshaler{}
	server := NewServer(ServerConfig{
		Dispatcher: func(_ context.Context, _ ToolCall) (any, error) {
			return ToolResult{
				StructuredContent: structuredContent,
				Content: []any{
					map[string]any{
						"type":     "image",
						"data":     "iVBORw0KGgo=",
						"mimeType": "image/png",
						"_meta": map[string]any{
							"codex/imageDetail": "original",
						},
					},
				},
				IsError: true,
			}, nil
		},
	})

	initialize := performHTTPRequest(t, server, "", rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "initialize",
	})
	sessionID := initialize.Header().Get(SessionHeader)
	if sessionID == "" {
		t.Fatal("initialize response missing session id header")
	}

	response := performHTTPRequest(t, server, sessionID, rpcRequest{
		JSONRPC: "2.0",
		ID:      "2",
		Method:  "tools/call",
		Params: map[string]any{
			"name":      "desktop_observe",
			"arguments": map[string]any{"action": "screenshot"},
		},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, want 200", response.Code)
	}

	var body rpcResponse
	decodeJSON(t, response.Body.Bytes(), &body)
	if body.Error != nil {
		t.Fatalf("tools/call returned error: %#v", body.Error)
	}
	result := body.Result.(map[string]any)

	wantStructuredContent := map[string]any{
		"width":  json.Number("3024"),
		"height": json.Number("1964"),
	}
	if got := result["structuredContent"]; !reflect.DeepEqual(got, wantStructuredContent) {
		t.Fatalf("structuredContent = %#v, want %#v", got, wantStructuredContent)
	}
	wantContent := []any{
		map[string]any{
			"type":     "image",
			"data":     "iVBORw0KGgo=",
			"mimeType": "image/png",
			"_meta": map[string]any{
				"codex/imageDetail": "original",
			},
		},
	}
	if got := result["content"]; !reflect.DeepEqual(got, wantContent) {
		t.Fatalf("content = %#v, want %#v", got, wantContent)
	}
	if got := result["isError"]; got != true {
		t.Fatalf("isError = %#v, want true", got)
	}
	if structuredContent.calls != 1 {
		t.Fatalf("structured content MarshalJSON() calls = %d, want 1", structuredContent.calls)
	}
}

func TestNormalizeStructuredContentUsesSerializedJSONShape(t *testing.T) {
	t.Parallel()

	type namedSlice []string
	var nilMap map[string]any
	var nilSlice []any
	nilMapPointer := &nilMap
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{name: "object remains unchanged", input: map[string]any{"ok": true}, want: `{"ok":true}`},
		{name: "array uses items", input: []any{"one"}, want: `{"items":["one"]}`},
		{name: "named slice uses items", input: namedSlice{"one"}, want: `{"items":["one"]}`},
		{name: "scalar uses value", input: "one", want: `{"value":"one"}`},
		{name: "nil uses empty object", input: nil, want: `{}`},
		{name: "typed nil map uses empty object", input: nilMap, want: `{}`},
		{name: "typed nil slice uses empty object", input: nilSlice, want: `{}`},
		{name: "pointer to typed nil map uses empty object", input: nilMapPointer, want: `{}`},
		{name: "custom array JSON uses items", input: arrayJSONMarshaler{}, want: `{"items":[1,2]}`},
		{name: "custom scalar JSON uses value", input: scalarJSONMarshaler{}, want: `{"value":"custom"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizeStructuredContent(test.input)
			if err != nil {
				t.Fatalf("normalizeStructuredContent() error = %v", err)
			}
			encoded, err := json.Marshal(normalized)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if got := string(encoded); got != test.want {
				t.Fatalf("normalizeStructuredContent() JSON = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNormalizeStructuredContentReusesValidatedJSON(t *testing.T) {
	t.Parallel()

	input := &statefulJSONMarshaler{}
	normalized, err := normalizeStructuredContent(input)
	if err != nil {
		t.Fatalf("normalizeStructuredContent() error = %v", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if got, want := string(encoded), `{"items":[1]}`; got != want {
		t.Fatalf("normalized JSON = %s, want %s", got, want)
	}
	if input.calls != 1 {
		t.Fatalf("MarshalJSON() calls = %d, want 1", input.calls)
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
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
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

func TestStreamableHTTPRejectsMissingSessionAndMismatchedProtocolHeader(t *testing.T) {
	t.Parallel()
	server := NewServer(ServerConfig{})
	missing := performHTTPRequest(t, server, "", rpcRequest{JSONRPC: "2.0", ID: "1", Method: "ping"})
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing session status = %d, want 400", missing.Code)
	}

	initialize := performHTTPRequest(t, server, "", rpcRequest{JSONRPC: "2.0", ID: "2", Method: "initialize"})
	sessionID := initialize.Header().Get(SessionHeader)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"3","method":"ping"}`))
	request.Header.Set(SessionHeader, sessionID)
	request.Header.Set("MCP-Protocol-Version", "1900-01-01")
	response := httptest.NewRecorder()
	server.HandleStreamableHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("mismatched protocol header status = %d, want 400", response.Code)
	}
}

func TestStreamableHTTPRejectsCrossOriginBrowserRequest(t *testing.T) {
	t.Parallel()
	server := NewServer(ServerConfig{})
	request := httptest.NewRequest(http.MethodPost, "https://executor.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"initialize"}`))
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	server.HandleStreamableHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", response.Code)
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

func TestStdioFramesAreNewlineDelimitedJSON(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	var output bytes.Buffer
	if err := WriteFrame(&output, payload); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), string(payload)+"\n"; got != want {
		t.Fatalf("stdio frame = %q, want newline-delimited JSON", got)
	}
	frame, err := ReadFrame(bufio.NewReader(strings.NewReader(output.String())))
	if err != nil || !bytes.Equal(frame, payload) {
		t.Fatalf("ReadFrame = %q, %v", frame, err)
	}
}

func assertConcreteToolSchema(t *testing.T, tool Tool) {
	t.Helper()

	if tool.InputSchema["type"] != "object" {
		t.Fatalf("tool %q schema type = %v, want object", tool.Name, tool.InputSchema["type"])
	}

	properties, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		t.Fatalf("tool %q missing concrete schema properties", tool.Name)
	}

	required, ok := tool.InputSchema["required"].([]string)
	if !ok || len(required) == 0 {
		t.Fatalf("tool %q missing required fields", tool.Name)
	}

	if action, hasAction := properties["action"]; hasAction {
		actionSchema, ok := action.(map[string]any)
		if !ok {
			t.Fatalf("tool %q action schema has unexpected type %T", tool.Name, action)
		}
		enumValues, ok := actionSchema["enum"].([]string)
		if !ok || len(enumValues) == 0 {
			t.Fatalf("tool %q action schema missing enum values", tool.Name)
		}
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
