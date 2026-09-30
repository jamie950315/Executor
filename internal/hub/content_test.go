package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

type contentRelay struct{ result any }

func (f contentRelay) Devices(context.Context) ([]Device, error) {
	return []Device{{ID: "mac", Online: true, Authorized: true}}, nil
}

func (f contentRelay) Call(context.Context, string, mcp.ToolCall) (any, error) {
	// The machine HTTP relay decodes a device ToolResult into untyped JSON.
	encoded, err := json.Marshal(f.result)
	if err != nil {
		return nil, err
	}
	var decoded any
	err = json.Unmarshal(encoded, &decoded)
	return decoded, err
}

func TestRelayedDesktopResultsRemainNativeMCPContent(t *testing.T) {
	for _, name := range []string{"desktop_observe", "desktop_control"} {
		t.Run(name, func(t *testing.T) {
			want := mcp.ToolResult{
				StructuredContent: map[string]any{"captureId": "fixture-capture", "width": float64(1), "height": float64(1)},
				Content: []any{
					map[string]any{"type": "text", "text": "Fixture screenshot"},
					map[string]any{"type": "image", "data": "iVBORw0KGgo=", "mimeType": "image/png"},
				},
				IsError: true,
			}
			router := New(contentRelay{result: want})
			server := mcp.NewServer(mcp.ServerConfig{Tools: Tools(), Dispatcher: router.Dispatch})
			request := func(session, method string, params any) *httptest.ResponseRecorder {
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
				req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if session != "" {
					req.Header.Set(mcp.SessionHeader, session)
				}
				res := httptest.NewRecorder()
				server.HandleStreamableHTTP(res, req)
				return res
			}
			init := request("", "initialize", map[string]any{})
			response := request(init.Header().Get(mcp.SessionHeader), "tools/call", map[string]any{"name": name, "arguments": map[string]any{"deviceId": "mac", "action": "screenshot"}})
			var body struct {
				Result struct {
					Content           []any `json:"content"`
					StructuredContent any   `json:"structuredContent"`
					IsError           bool  `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || !reflect.DeepEqual(body.Result.Content, want.Content) || !reflect.DeepEqual(body.Result.StructuredContent, want.StructuredContent) || !body.Result.IsError {
				t.Fatalf("relayed image became ordinary JSON text: status=%d result=%+v", response.Code, body.Result)
			}
		})
	}
}

func TestRestoreDesktopContentRejectsMalformedEnvelope(t *testing.T) {
	for _, result := range []map[string]any{
		{"Content": "invalid", "StructuredContent": map[string]any{}, "IsError": false},
		{"Content": []any{}, "StructuredContent": map[string]any{}, "IsError": "false"},
		{"Content": []any{}, "IsError": false},
		{"Content": []any{}, "StructuredContent": map[string]any{}, "IsError": false, "extra": true},
	} {
		if _, err := restoreDesktopContent(result); err != ErrUnconfirmed {
			t.Fatalf("malformed desktop envelope accepted: %v", err)
		}
	}
	ordinary := map[string]any{"windows": []any{}}
	got, err := restoreDesktopContent(ordinary)
	if err != nil || !reflect.DeepEqual(got, ordinary) {
		t.Fatal("ordinary desktop result changed")
	}
}
