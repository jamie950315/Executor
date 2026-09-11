package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestFetchToolAdvertisesStringInputAndWriteEffects(t *testing.T) {
	for _, tool := range BuiltinTools() {
		if tool.Name != "fetch" {
			continue
		}
		if tool.Annotations.ReadOnlyHint || !tool.Annotations.DestructiveHint {
			t.Fatalf("fetch must expose write effects: %+v", tool.Annotations)
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		request := properties["request"].(map[string]any)
		if request["type"] != "string" || request["maxLength"] != 8<<20 || !reflect.DeepEqual(tool.InputSchema["required"], []string{"request"}) {
			t.Fatalf("unexpected fetch schema: %#v", tool.InputSchema)
		}
		if !strings.Contains(tool.Description, "modify") || !strings.Contains(tool.Description, "executor://call?request=") {
			t.Fatalf("fetch description omits its contract: %s", tool.Description)
		}
		return
	}
	t.Fatal("fetch tool is missing")
}

type fetchContextKey struct{}

func TestFetchForwardsDecodedCallWithOriginalContextAndSession(t *testing.T) {
	arguments := map[string]any{
		"action": "write_file", "privilege": "admin", "path": "/tmp/fetch fixture.txt",
		"content": "中文 + & % # ? \"quoted\"\nsecond line", "encoding": "utf8",
	}
	encoded, err := json.Marshal(map[string]any{"name": "filesystem_write", "arguments": arguments})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []string{string(encoded), "executor://call?request=" + url.QueryEscape(string(encoded))} {
		called := 0
		ctx := context.WithValue(context.Background(), fetchContextKey{}, "original-identity")
		server := NewServer(ServerConfig{NewSessionID: func() string { return "fetch-session" }, Dispatcher: func(got context.Context, call ToolCall) (any, error) {
			called++
			if got != ctx || got.Value(fetchContextKey{}) != "original-identity" || call.SessionID != "fetch-session" || call.Name != "filesystem_write" || !reflect.DeepEqual(call.Arguments, arguments) {
				t.Fatalf("fetch altered the original call context: %#v", call)
			}
			return map[string]any{"ok": true, "writtenBytes": 42}, nil
		}})
		response, status, _ := server.handleRPC(ctx, server.newSession(), rpcRequest{
			JSONRPC: "2.0", ID: "fetch-call", Method: "tools/call",
			Params: map[string]any{"name": "fetch", "arguments": map[string]any{"request": request}},
		})
		result := fetchResultForTest(t, response, status)
		if called != 1 || result["isError"] != false || result["toolName"] != "fetch" {
			t.Fatalf("fetch result = %#v, dispatch count = %d", result, called)
		}
		structured := result["structuredContent"].(map[string]any)
		if structured["ok"] != true || structured["writtenBytes"] != float64(42) {
			t.Fatalf("fetch changed the target result: %#v", result)
		}
	}
}

func TestFetchRejectsAmbiguousOrUnsafeEnvelopesBeforeDispatch(t *testing.T) {
	valid := `{"name":"filesystem_write","arguments":{"action":"write_file","path":"/tmp/fixture","content":"must-not-echo-fixture"}}`
	uri := "executor://call?request=" + url.QueryEscape(valid)
	cases := map[string]map[string]any{
		"missing input":          {},
		"non string input":       {"request": map[string]any{}},
		"empty input":            {"request": " \n "},
		"extra outer argument":   {"request": valid, "privilege": "admin"},
		"malformed json":         {"request": `{"name":"must-not-echo-fixture"`},
		"null":                   {"request": "null"},
		"array":                  {"request": "[" + valid + "]"},
		"missing name":           {"request": `{"arguments":{}}`},
		"non string name":        {"request": `{"name":7,"arguments":{}}`},
		"unknown name":           {"request": `{"name":"must-not-echo-fixture","arguments":{}}`},
		"private tool":           {"request": `{"name":"desktop_live","arguments":{}}`},
		"recursive fetch":        {"request": `{"name":"fetch","arguments":{}}`},
		"missing arguments":      {"request": `{"name":"device_status"}`},
		"null arguments":         {"request": `{"name":"device_status","arguments":null}`},
		"array arguments":        {"request": `{"name":"device_status","arguments":[]}`},
		"scalar arguments":       {"request": `{"name":"device_status","arguments":true}`},
		"unknown envelope field": {"request": `{"name":"device_status","arguments":{},"sessionId":"forged"}`},
		"case variant field":     {"request": `{"Name":"device_status","arguments":{}}`},
		"duplicate name":         {"request": `{"name":"device_status","name":"filesystem_write","arguments":{}}`},
		"duplicate arguments":    {"request": `{"name":"device_status","arguments":{},"arguments":{}}`},
		"trailing json":          {"request": valid + "{}"},
		"oversized":              {"request": strings.Repeat(" ", (8<<20)+1)},
		"invalid utf8":           {"request": string([]byte{0xff})},
		"web url":                {"request": "https://example.invalid/?request=" + url.QueryEscape(valid)},
		"wrong uri target":       {"request": "executor://other?request=" + url.QueryEscape(valid)},
		"uri path":               {"request": "executor://call/extra?request=" + url.QueryEscape(valid)},
		"uri userinfo":           {"request": "executor://secret@call?request=" + url.QueryEscape(valid)},
		"uri fragment":           {"request": uri + "#ignored"},
		"empty uri fragment":     {"request": uri + "#"},
		"uri invalid utf8":       {"request": "executor://call?request=%FF"},
		"extra query":            {"request": uri + "&token=must-not-echo-fixture"},
		"duplicate query":        {"request": uri + "&request=" + url.QueryEscape(valid)},
		"bad escape":             {"request": "executor://call?request=%XX"},
		"missing query":          {"request": "executor://call"},
		"nested uri":             {"request": "executor://call?request=" + url.QueryEscape(uri)},
		"double encoded json":    {"request": "executor://call?request=" + url.QueryEscape(url.QueryEscape(valid))},
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			server := NewServer(ServerConfig{Dispatcher: func(context.Context, ToolCall) (any, error) {
				t.Fatal("invalid fetch envelope reached the dispatcher")
				return nil, nil
			}})
			response, status, _ := server.handleRPC(context.Background(), server.newSession(), rpcRequest{
				JSONRPC: "2.0", ID: "invalid", Method: "tools/call",
				Params: map[string]any{"name": "fetch", "arguments": arguments},
			})
			result := fetchResultForTest(t, response, status)
			if result["isError"] != true {
				t.Fatalf("invalid fetch returned success: %#v", result)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "must-not-echo-fixture") {
				t.Fatal("fetch parser reflected untrusted request content")
			}
		})
	}
}

func TestFetchCannotReachToolsExcludedFromServerCatalog(t *testing.T) {
	var operations []Tool
	for _, tool := range builtinOperations() {
		if tool.Name == "filesystem_read" {
			operations = append(operations, tool)
		}
	}
	server := NewServer(ServerConfig{Operations: operations, Dispatcher: func(context.Context, ToolCall) (any, error) {
		t.Fatal("excluded operation reached the dispatcher")
		return nil, nil
	}})
	response, status, _ := server.handleRPC(context.Background(), server.newSession(), rpcRequest{
		JSONRPC: "2.0", ID: "hidden", Method: "tools/call",
		Params: map[string]any{"name": "fetch", "arguments": map[string]any{
			"request": `{"name":"filesystem_write","arguments":{"action":"delete","path":"/tmp/fixture"}}`,
		}},
	})
	if result := fetchResultForTest(t, response, status); result["isError"] != true {
		t.Fatalf("excluded tool returned success: %#v", result)
	}
}

func TestFetchPreservesToolErrorsAndImageBlocks(t *testing.T) {
	for _, failure := range []bool{false, true} {
		server := NewServer(ServerConfig{Dispatcher: func(context.Context, ToolCall) (any, error) {
			if failure {
				return nil, errors.New("fixture helper unavailable")
			}
			return ToolResult{
				StructuredContent: map[string]any{"captureId": "fixture-capture"},
				Content:           []any{map[string]any{"type": "image", "mimeType": "image/png", "data": "aW1hZ2U="}},
			}, nil
		}})
		response, status, _ := server.handleRPC(context.Background(), server.newSession(), rpcRequest{
			JSONRPC: "2.0", ID: "result", Method: "tools/call",
			Params: map[string]any{"name": "fetch", "arguments": map[string]any{
				"request": `{"name":"desktop_observe","arguments":{"action":"screenshot"}}`,
			}},
		})
		result := fetchResultForTest(t, response, status)
		if result["isError"] != failure {
			t.Fatalf("tool error flag changed: %#v", result)
		}
		block := result["content"].([]any)[0].(map[string]any)
		if failure {
			if block["text"] != "fixture helper unavailable" {
				t.Fatalf("tool error changed: %#v", result)
			}
		} else if block["type"] != "image" || block["data"] != "aW1hZ2U=" {
			t.Fatalf("image block changed: %#v", result)
		}
	}
}

func TestFetchRunsThroughHTTPPostAndStdioWithReadMethodsInert(t *testing.T) {
	calls := 0
	server := NewServer(ServerConfig{Dispatcher: func(context.Context, ToolCall) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	}})
	sessionID := server.newSession()
	payloadBytes, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0", ID: 2, Method: "tools/call",
		Params: map[string]any{"name": "fetch", "arguments": map[string]any{
			"request": `{"name":"filesystem_write","arguments":{"action":"write_file","path":"/tmp/fixture","content":"ok"}}`,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := string(payloadBytes)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req := httptest.NewRequest(method, "/mcp?request="+url.QueryEscape(payload), strings.NewReader(payload))
		req.Header.Set(SessionHeader, sessionID)
		response := httptest.NewRecorder()
		server.HandleStreamableHTTP(response, req)
		if response.Code != http.StatusMethodNotAllowed || calls != 0 {
			t.Fatalf("%s executed a mutation: status=%d calls=%d", method, response.Code, calls)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	req.Header.Set(SessionHeader, sessionID)
	response := httptest.NewRecorder()
	server.HandleStreamableHTTP(response, req)
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("HTTP fetch failed: status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	handler := NewStdioHandler(server)
	if _, err := handler.HandleFrame(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.HandleFrame(context.Background(), []byte(payload)); err != nil || calls != 2 {
		t.Fatalf("stdio fetch failed: calls=%d err=%v", calls, err)
	}
}

func fetchResultForTest(t *testing.T, response *rpcResponse, status int) map[string]any {
	t.Helper()
	if status != http.StatusOK || response == nil || response.Error != nil {
		t.Fatalf("expected an MCP tool result: status=%d response=%+v", status, response)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func FuzzFetchEnvelope(f *testing.F) {
	for _, seed := range []string{
		`{"name":"filesystem_write","arguments":{"action":"write_file","content":"中文"}}`,
		`executor://call?request=%7B%22name%22%3A%22device_status%22%2C%22arguments%22%3A%7B%7D%7D`,
		`{"name":"fetch","arguments":{}}`,
		`{"name":"device_status","name":"filesystem_write","arguments":{}}`,
		`executor://call?request=%FF`,
		`null`,
	} {
		f.Add(seed)
	}
	server := NewServer(ServerConfig{})
	f.Fuzz(func(t *testing.T, request string) {
		resolved, err := server.resolveFetchCall(ToolCall{
			SessionID: "original-session", Name: "fetch", Arguments: map[string]any{"request": request},
		})
		if err == nil && (resolved.SessionID != "original-session" || !server.operationExists(resolved.Name) || resolved.Arguments == nil) {
			t.Fatal("fetch accepted an invalid target or changed session scope")
		}
	})
}
