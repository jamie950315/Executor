package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestProxySurfacePublishesOnlyReadAndFetchAndRejectsDirectCalls(t *testing.T) {
	server := NewServer(ServerConfig{Dispatcher: func(context.Context, ToolCall) (any, error) {
		t.Fatal("a direct operation reached the dispatcher")
		return nil, nil
	}})
	session := server.newSession()
	response, status, _ := server.handleRPC(context.Background(), session, rpcRequest{
		JSONRPC: "2.0", ID: "list", Method: "tools/list",
	})
	if status != http.StatusOK || response.Error != nil {
		t.Fatalf("tools/list failed: status=%d", status)
	}
	tools := response.Result.(map[string]any)["tools"].([]Tool)
	if len(tools) != 2 || tools[0].Name != "read" || tools[1].Name != "fetch" {
		t.Fatalf("unexpected public tools: %+v", tools)
	}
	if !tools[0].Annotations.ReadOnlyHint || tools[0].Annotations.DestructiveHint || tools[1].Annotations.ReadOnlyHint || !tools[1].Annotations.DestructiveHint {
		t.Fatal("public tools must accurately declare their side effects")
	}
	for _, tool := range tools {
		assertConcreteToolSchema(t, tool)
	}
	for _, name := range []string{"filesystem_write", "terminal", "desktop_control", "desktop_observe", "device_permissions", "filesystem_read", "terminal_output", "terminal_sessions", "device_status"} {
		response, status, _ := server.handleRPC(context.Background(), session, rpcRequest{
			JSONRPC: "2.0", ID: name, Method: "tools/call",
			Params: map[string]any{"name": name, "arguments": map[string]any{}},
		})
		if status != http.StatusBadRequest || response.Error == nil || response.Error.Code != -32602 {
			t.Errorf("direct %s was accepted: status=%d", name, status)
		}
	}
}

func TestProxyReadDiscoversInternalSchemasWithoutExecutingOperations(t *testing.T) {
	server := NewServer(ServerConfig{Dispatcher: func(context.Context, ToolCall) (any, error) {
		t.Fatal("schema discovery reached the dispatcher")
		return nil, nil
	}})
	response, status, _ := server.handleRPC(context.Background(), server.newSession(), rpcRequest{
		JSONRPC: "2.0", ID: "catalog", Method: "tools/call",
		Params: map[string]any{"name": "read", "arguments": map[string]any{"action": "tools"}},
	})
	result := fetchResultForTest(t, response, status)
	if result["isError"] != false {
		t.Fatalf("catalog failed: %#v", result)
	}
	catalog := result["structuredContent"].(map[string]any)
	operations := catalog["operations"].([]any)
	readOperations := catalog["readOperations"].([]any)
	if len(operations) != 9 || len(readOperations) != 6 {
		t.Fatalf("catalog sizes: operations=%d readOperations=%d", len(operations), len(readOperations))
	}
	for _, raw := range operations {
		op := raw.(map[string]any)
		if op["name"] == "read" || op["name"] == "fetch" || op["inputSchema"] == nil {
			t.Fatalf("invalid internal schema: %#v", op)
		}
		if op["name"] == "desktop_observe" {
			properties := op["inputSchema"].(map[string]any)["properties"].(map[string]any)
			if properties["path"] == nil || op["annotations"].(map[string]any)["readOnlyHint"] != false {
				t.Fatal("read catalog corrupted the full screenshot export contract")
			}
		}
	}
	for _, raw := range readOperations {
		op := raw.(map[string]any)
		if op["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Fatalf("read operation has inaccurate annotations: %#v", op)
		}
		props := op["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if op["name"] == "desktop_observe" && props["path"] != nil {
			t.Fatal("read screenshot schema permits file export")
		}
		if op["name"] == "device_permissions" && !reflect.DeepEqual(props["action"].(map[string]any)["enum"], []any{"status"}) {
			t.Fatal("read permission schema permits a permission request")
		}
	}
}

func TestProxyCatalogWorksWithoutDispatcherAndHonorsExplicitRestrictions(t *testing.T) {
	for _, config := range []ServerConfig{
		{},
		{Operations: []Tool{}},
		{Tools: BuiltinTools()},
		{Operations: []Tool{builtinOperations()[1]}},
	} {
		server := NewServer(config)
		response, status, _ := server.handleRPC(context.Background(), server.newSession(), rpcRequest{
			JSONRPC: "2.0", ID: "catalog", Method: "tools/call",
			Params: map[string]any{"name": "read", "arguments": map[string]any{"action": "tools"}},
		})
		result := fetchResultForTest(t, response, status)
		if result["isError"] != false {
			t.Fatal("schema discovery requires a live dispatcher")
		}
		count := len(result["structuredContent"].(map[string]any)["operations"].([]any))
		want := 9
		if config.Operations != nil {
			want = len(config.Operations)
		} else if config.Tools != nil {
			want = 0
		}
		if count != want {
			t.Fatalf("catalog widened configured operations: got %d want %d", count, want)
		}
		if want != 9 {
			if _, err := server.resolveFetchCall(ToolCall{Arguments: map[string]any{"request": `{"name":"filesystem_write","arguments":{}}`}}); err == nil {
				t.Fatal("explicit restriction was bypassed by fetch")
			}
		}
	}
}

func TestProxyReadForwardsOnlyReadOperationsAndPreservesContext(t *testing.T) {
	for _, operation := range []string{
		`{"name":"filesystem_read","arguments":{"action":"read_file","path":"/fixture"}}`,
		`{"name":"terminal_output","arguments":{"sessionId":"fixture"}}`,
		`{"name":"terminal_sessions","arguments":{"action":"list"}}`,
		`{"name":"device_status","arguments":{"action":"summary"}}`,
		`{"name":"desktop_observe","arguments":{"action":"screenshot"}}`,
		`{"name":"desktop_observe","arguments":{"action":"windows"}}`,
		`{"name":"device_permissions","arguments":{"action":"status"}}`,
	} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), fetchContextKey{}, "original")
			var want struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal([]byte(operation), &want); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := NewServer(ServerConfig{NewSessionID: func() string { return "read-session" }, Dispatcher: func(got context.Context, call ToolCall) (any, error) {
				calls++
				if got != ctx || call.SessionID != "read-session" || call.Name != want.Name || !reflect.DeepEqual(call.Arguments, want.Arguments) {
					t.Fatalf("read changed the call: %#v", call)
				}
				return ToolResult{StructuredContent: map[string]any{"ok": true}, Content: []any{map[string]any{"type": "image", "mimeType": "image/png", "data": "aW1hZ2U="}}}, nil
			}})
			response, status, _ := server.handleRPC(ctx, server.newSession(), rpcRequest{
				JSONRPC: "2.0", ID: "query", Method: "tools/call",
				Params: map[string]any{"name": "read", "arguments": map[string]any{"action": "call", "request": operation}},
			})
			result := fetchResultForTest(t, response, status)
			if calls != 1 || result["isError"] != false || result["toolName"] != "read" || result["content"].([]any)[0].(map[string]any)["type"] != "image" {
				t.Fatalf("read lost the result: %#v calls=%d", result, calls)
			}
		})
	}
}

func TestProxyReadRejectsMutationsAndMalformedInputsBeforeDispatch(t *testing.T) {
	server := NewServer(ServerConfig{Dispatcher: func(context.Context, ToolCall) (any, error) {
		t.Fatal("an invalid read reached the dispatcher")
		return nil, nil
	}})
	cases := []map[string]any{
		{}, {"action": "unknown"}, {"action": "tools", "request": "unexpected"},
		{"action": "call"}, {"action": "call", "request": 7},
		{"action": "call", "request": "{}", "privilege": "admin"},
	}
	for _, operation := range []string{
		`{"name":"filesystem_write","arguments":{"action":"write_file","path":"/fixture","content":"PRIVATE_READ_FIXTURE"}}`,
		`{"name":"terminal","arguments":{"action":"create","command":"PRIVATE_READ_FIXTURE"}}`,
		`{"name":"desktop_control","arguments":{"action":"type_text","text":"PRIVATE_READ_FIXTURE"}}`,
		`{"name":"device_permissions","arguments":{"action":"request_all"}}`,
		`{"name":"device_permissions","arguments":{}}`,
		`{"name":"desktop_observe","arguments":{"action":"screenshot","path":"/fixture"}}`,
		`{"name":"desktop_observe","arguments":{"action":"screenshot","path":""}}`,
		`{"name":"desktop_observe","arguments":{"action":"screenshot","path":null}}`,
		`{"name":"desktop_observe","arguments":{"action":"type_text"}}`,
		`{"name":"read","arguments":{"action":"tools"}}`,
		`{"name":"fetch","arguments":{}}`, `null`, `{"name":`,
	} {
		cases = append(cases, map[string]any{"action": "call", "request": operation})
	}
	for _, arguments := range cases {
		response, status, _ := server.handleRPC(context.Background(), server.newSession(), rpcRequest{
			JSONRPC: "2.0", ID: "invalid", Method: "tools/call",
			Params: map[string]any{"name": "read", "arguments": arguments},
		})
		result := fetchResultForTest(t, response, status)
		encoded, _ := json.Marshal(result)
		if result["isError"] != true || strings.Contains(string(encoded), "PRIVATE_READ_FIXTURE") {
			t.Errorf("invalid read succeeded or echoed input: %#v", result)
		}
	}
}
