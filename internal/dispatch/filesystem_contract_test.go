package dispatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

func fileRangeResponse(data []byte, offset int64, truncated bool) map[string]any {
	return map[string]any{
		"data": data, "offset_bytes": offset, "next_offset_bytes": offset + int64(len(data)),
		"truncated": truncated, "eof": !truncated,
	}
}

func TestFilesystemRangeReadForwardsByteBoundsAndStableMetadata(t *testing.T) {
	t.Parallel()
	for _, privilege := range []string{"owner", "admin"} {
		t.Run(privilege, func(t *testing.T) {
			caller := &recordingCaller{responses: map[string]any{
				"filesystem.read":       []byte("0123456789abcdef"),
				"filesystem.read-range": fileRangeResponse([]byte("4567"), 4, true),
			}}
			other := &recordingCaller{}
			d := NewMCP(other, caller)
			if privilege == "admin" {
				d = NewMCP(caller, other)
			}
			result, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: map[string]any{
				"action": "read_file", "path": "/fixture", "offset": float64(4), "limit": float64(4), "privilege": privilege,
			}})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"content": "4567", "encoding": "utf8", "size": 4,
				"offsetBytes": int64(4), "returnedBytes": 4, "nextOffsetBytes": int64(8), "truncated": true, "eof": false,
			}
			if !reflect.DeepEqual(result, want) {
				t.Fatalf("range result = %#v, want %#v", result, want)
			}
			if len(other.calls) != 0 || len(caller.calls) != 1 || caller.calls[0].method != "filesystem.read-range" {
				t.Fatalf("range routing = %#v; other=%#v", caller.calls, other.calls)
			}
			params := caller.calls[0].params
			if params["offset_bytes"] != float64(4) || params["limit_bytes"] != float64(4) {
				t.Fatalf("range parameters = %#v", params)
			}
		})
	}
}

func TestFilesystemRangeReadDefaultsAreBoundedAndEncodingIndependent(t *testing.T) {
	t.Parallel()
	var previous any
	for _, encoding := range []string{"", "utf8"} {
		caller := &recordingCaller{responses: map[string]any{
			"filesystem.read":       []byte("text"),
			"filesystem.read-range": fileRangeResponse([]byte("text"), 0, false),
		}}
		arguments := map[string]any{"action": "read_file", "path": "/fixture"}
		if encoding != "" {
			arguments["encoding"] = encoding
		}
		result, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		if len(caller.calls) != 1 || caller.calls[0].params["limit_bytes"] != float64(65536) || caller.calls[0].params["offset_bytes"] != float64(0) {
			t.Fatalf("default byte bounds = %#v", caller.calls)
		}
		if previous != nil && !reflect.DeepEqual(result, previous) {
			t.Fatalf("default and explicit utf8 differ: %#v != %#v", result, previous)
		}
		previous = result
	}
}

func TestFilesystemRangeReadRejectsInvalidBoundsBeforeIPC(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"offset", "limit"} {
		values := []any{nil, true, "4", -1, 1.5, math.NaN(), math.Inf(1), int64(math.MaxInt64), uint64(math.MaxUint64), float64(1 << 53), json.Number("1.5"), json.Number("9007199254740992")}
		if field == "limit" {
			values = append(values, 0, 1048577)
		}
		for _, value := range values {
			caller := &recordingCaller{responses: map[string]any{"filesystem.read": []byte("fixture")}}
			arguments := map[string]any{"action": "read_file", "path": "/fixture", field: value}
			_, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: arguments})
			if err == nil || len(caller.calls) != 0 {
				t.Errorf("%s=%v (%T): error=%v calls=%d", field, value, value, err, len(caller.calls))
			}
		}
	}
}

func TestFilesystemRangeReadUTF8BoundaryRequiresBase64ForExactBytes(t *testing.T) {
	t.Parallel()
	data := []byte("中")[1:]
	caller := &recordingCaller{responses: map[string]any{
		"filesystem.read":       data,
		"filesystem.read-range": fileRangeResponse(data, 1, false),
	}}
	for _, encoding := range []string{"", "utf8", "base64"} {
		arguments := map[string]any{"action": "read_file", "path": "/fixture", "offset": 1, "limit": 2}
		if encoding != "" {
			arguments["encoding"] = encoding
		}
		result, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: arguments})
		if encoding == "base64" {
			if err != nil || result.(map[string]any)["content"] != base64.StdEncoding.EncodeToString(data) || result.(map[string]any)["size"] != 2 {
				t.Fatalf("binary byte range = %#v, %v", result, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "base64") {
			t.Errorf("encoding %q returned lossy text or omitted guidance: %#v, %v", encoding, result, err)
		}
	}
}

func TestFilesystemWriteResultsAreStableAcrossEncodingAndAction(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"write_file", "append_file"} {
		for _, encoding := range []string{"", "utf8", "base64"} {
			caller := &recordingCaller{responses: map[string]any{"filesystem.write": nil, "filesystem.append": nil}}
			arguments := map[string]any{"action": action, "path": "/fixture", "content": "中文"}
			wantEncoding := "utf8"
			if encoding != "" {
				arguments["encoding"] = encoding
				wantEncoding = encoding
			}
			if encoding == "base64" {
				arguments["content"] = base64.StdEncoding.EncodeToString([]byte("中文"))
			}
			result, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: arguments})
			want := map[string]any{"ok": true, "action": action, "encoding": wantEncoding, "size": 6, "writtenBytes": 6}
			if err != nil || !reflect.DeepEqual(result, want) {
				t.Errorf("%s encoding %q: %#v, %v; want %#v", action, encoding, result, err, want)
			}
		}
	}
	for _, action := range []string{"mkdir", "move", "delete"} {
		caller := &recordingCaller{responses: map[string]any{"filesystem." + action: nil}}
		result, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
			"action": action, "path": "/fixture", "destination": "/destination",
		}})
		if err != nil || !reflect.DeepEqual(result, map[string]any{"ok": true, "action": action}) {
			t.Errorf("%s metadata: %#v, %v", action, result, err)
		}
	}
}

func TestFilesystemWriteErrorsRemainErrorsAndUTF8IsValidatedBeforeIPC(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{err: errors.New("fixture write error")}
	result, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
		"action": "write_file", "path": "/fixture", "content": "text",
	}})
	if err == nil || result != nil {
		t.Fatalf("write failure became success: %#v, %v", result, err)
	}
	for _, encoding := range []string{"", "utf8"} {
		caller := &recordingCaller{responses: map[string]any{"filesystem.write": nil}}
		arguments := map[string]any{"action": "write_file", "path": "/fixture", "content": string([]byte{0xff})}
		if encoding != "" {
			arguments["encoding"] = encoding
		}
		_, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: arguments})
		if err == nil || len(caller.calls) != 0 {
			t.Errorf("invalid UTF-8 encoding %q reached IPC: %v calls=%d", encoding, err, len(caller.calls))
		}
	}
}

func TestFilesystemRangeReadRejectsUnsafeNextOffsetBeforeIPC(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{responses: map[string]any{
		"filesystem.read-range": fileRangeResponse([]byte("x"), 1<<53-1, false),
	}}
	_, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: map[string]any{
		"action": "read_file", "path": "/fixture", "offset": int64(1<<53 - 1), "limit": 1,
	}})
	if err == nil || len(caller.calls) != 0 {
		t.Fatalf("unsafe next offset accepted: error=%v calls=%d", err, len(caller.calls))
	}
}

func TestFilesystemRangeReadRejectsMalformedHelperMetadata(t *testing.T) {
	t.Parallel()
	for _, response := range []any{
		nil,
		map[string]any{},
		fileRangeResponse([]byte("abcde"), 0, true),
		fileRangeResponse([]byte("abcd"), 1, true),
		fileRangeResponse([]byte(""), 0, true),
		fileRangeResponse([]byte("ab"), 0, true),
		map[string]any{"data": []byte("abcd"), "offset_bytes": 0, "next_offset_bytes": 5, "truncated": true, "eof": false},
		map[string]any{"data": []byte("abcd"), "offset_bytes": 0, "next_offset_bytes": 4, "truncated": true, "eof": true},
	} {
		caller := &recordingCaller{responses: map[string]any{"filesystem.read-range": response}}
		result, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: map[string]any{
			"action": "read_file", "path": "/fixture", "offset": 0, "limit": 4,
		}})
		if err == nil || result != nil {
			t.Errorf("malformed response accepted: %#v -> %#v, %v", response, result, err)
		}
	}
}
