package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/mcp"
)

type diagnosticCallerFunc func(context.Context, string, any, any) error

func (f diagnosticCallerFunc) Call(ctx context.Context, method string, args, result any) error {
	return f(ctx, method, args, result)
}

func TestDeviceStatusPartialSurvivesMCPTransports(t *testing.T) {
	for _, action := range []string{"summary", "terminals"} {
		for _, transport := range []string{"http", "stdio"} {
			t.Run(action+"/"+transport, func(t *testing.T) {
				broker := &recordingCaller{responses: map[string]any{
					desktop.RPCMethodDeviceStatus: map[string]any{"available": true}, "terminal.list": []any{},
				}}
				d := NewMCP(broker, &recordingCaller{err: errors.New("private diagnostic fixture")})
				s := mcp.NewServer(mcp.ServerConfig{Dispatcher: d.Dispatch})
				var call func([]byte) []byte
				if transport == "stdio" {
					handler := mcp.NewStdioHandler(s)
					call = func(payload []byte) []byte {
						data, err := handler.HandleFrame(context.Background(), payload)
						if err != nil {
							t.Fatal(err)
						}
						return data
					}
				} else {
					sessionID := ""
					call = func(payload []byte) []byte {
						req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(payload))
						req.Header.Set(mcp.SessionHeader, sessionID)
						response := httptest.NewRecorder()
						s.HandleStreamableHTTP(response, req)
						if response.Code != 200 {
							t.Fatalf("HTTP status=%d", response.Code)
						}
						if next := response.Header().Get(mcp.SessionHeader); next != "" {
							sessionID = next
						}
						return response.Body.Bytes()
					}
				}
				call([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
				request, err := json.Marshal(map[string]any{
					"jsonrpc": "2.0", "id": 2, "method": "tools/call",
					"params": map[string]any{"name": "read", "arguments": map[string]any{
						"action": "call", "request": `{"name":"device_status","arguments":{"action":"` + action + `"}}`,
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				encoded := call(request)
				var envelope struct {
					Result map[string]any `json:"result"`
				}
				if err := json.Unmarshal(encoded, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Result["isError"] == true {
					t.Fatal("one helper failure hid the healthy helper")
				}
				result, ok := envelope.Result["structuredContent"].(map[string]any)
				if !ok || result["partial"] != true {
					t.Fatalf("missing partial diagnostic: %s", encoded)
				}
				healthy, failed := "broker", "desktop"
				if action == "terminals" {
					healthy, failed = "admin", "owner"
				}
				if result[healthy] == nil || result[failed] != nil {
					t.Fatalf("healthy value lost: %#v", result)
				}
				components := result["components"].(map[string]any)
				if components[healthy].(map[string]any)["state"] != "ok" || components[failed].(map[string]any)["state"] != "error" {
					t.Fatalf("incorrect component states: %#v", components)
				}
				if strings.Contains(string(encoded), "private diagnostic fixture") {
					t.Fatal("unfiltered helper error leaked into diagnostic metadata")
				}
			})
		}
	}
}

func TestDeviceStatusProbesAreIndependent(t *testing.T) {
	reached := make(chan struct{})
	broker := diagnosticCallerFunc(func(ctx context.Context, _ string, _, _ any) error {
		select {
		case <-reached:
			return errors.New("broker failed")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	owner := diagnosticCallerFunc(func(_ context.Context, _ string, _, result any) error {
		close(reached)
		return json.Unmarshal([]byte(`{"available":true}`), result)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	result, err := NewMCP(broker, owner).Dispatch(ctx, mcp.ToolCall{Name: "device_status", Arguments: map[string]any{"action": "summary"}})
	if err != nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("independent probe was blocked: duration=%v error=%v", time.Since(start), err)
	}
	if result.(map[string]any)["desktop"] == nil {
		t.Fatal("healthy desktop result missing")
	}
}

func TestDeviceStatusCancellationIsBounded(t *testing.T) {
	blocked := diagnosticCallerFunc(func(ctx context.Context, _ string, _, _ any) error { <-ctx.Done(); return ctx.Err() })
	owner := &recordingCaller{responses: map[string]any{desktop.RPCMethodDeviceStatus: map[string]any{"available": true}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := NewMCP(blocked, owner).Dispatch(ctx, mcp.ToolCall{Name: "device_status", Arguments: map[string]any{"action": "summary"}})
	if err != nil {
		t.Fatal(err)
	}
	value := result.(map[string]any)
	if value["desktop"] == nil || value["components"].(map[string]any)["broker"].(map[string]any)["state"] != "timeout" {
		t.Fatalf("canceled probe lost partial evidence: %#v", value)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	caller := &recordingCaller{}
	if _, err := NewMCP(caller, caller).Dispatch(ctx2, mcp.ToolCall{Name: "device_status", Arguments: map[string]any{"action": "summary"}}); !errors.Is(err, context.Canceled) || len(caller.calls) != 0 {
		t.Fatalf("pre-canceled diagnostic reached helpers: %v", err)
	}
}
