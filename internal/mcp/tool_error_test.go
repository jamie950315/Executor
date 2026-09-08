package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestDispatcherFailureRemainsReadableMCPToolResult(t *testing.T) {
	for _, transport := range []string{"http", "stdio"} {
		t.Run(transport, func(t *testing.T) {
			server := NewServer(ServerConfig{Dispatcher: func(context.Context, ToolCall) (any, error) {
				return nil, errors.New("content must be a string")
			}})
			call := rpcRequest{JSONRPC: "2.0", ID: "call", Method: "tools/call", Params: map[string]any{
				"name": "filesystem_write", "arguments": map[string]any{"action": "write_file", "path": "/isolated-fixture"},
			}}
			var encoded []byte
			if transport == "http" {
				init := performHTTPRequest(t, server, "", rpcRequest{JSONRPC: "2.0", ID: "init", Method: "initialize"})
				response := performHTTPRequest(t, server, init.Header().Get(SessionHeader), call)
				if response.Code != http.StatusOK {
					t.Errorf("tool execution failure used transport status %d; want 200", response.Code)
				}
				encoded = response.Body.Bytes()
			} else {
				handler := NewStdioHandler(server)
				if _, err := handler.HandleFrame(context.Background(), []byte(`{"jsonrpc":"2.0","id":"init","method":"initialize"}`)); err != nil {
					t.Fatal(err)
				}
				request, err := json.Marshal(call)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err = handler.HandleFrame(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
			}
			var response struct {
				ID     string `json:"id"`
				Error  any    `json:"error"`
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					StructuredContent json.RawMessage `json:"structuredContent"`
				} `json:"result"`
			}
			if err := json.Unmarshal(encoded, &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != nil || response.ID != "call" || !response.Result.IsError {
				t.Fatalf("tool failure is not an MCP error result: %s", encoded)
			}
			if len(response.Result.Content) != 1 || response.Result.Content[0].Type != "text" || response.Result.Content[0].Text != "content must be a string" {
				t.Fatalf("actionable error text was lost: %s", encoded)
			}
			if len(response.Result.StructuredContent) != 0 {
				t.Fatal("failed tool fabricated structured output which may violate its success schema")
			}
		})
	}
}
