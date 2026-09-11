package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/audit"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/oauth"
)

func TestFetchRuntimeAuthenticatedHTTPAndStdio(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	core, err := newOAuthCore("https://"+cfg.Domain, values)
	if err != nil {
		t.Fatal(err)
	}
	client, err := core.RegisterClient(oauth.DynamicClientRegistrationRequest{
		ClientName: "Fetch integration fixture", RedirectURIs: []string{"http://127.0.0.1/callback"}, Scopes: []string{"executor.full"},
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("f", 64)
	challenge := sha256.Sum256([]byte(verifier))
	grant, err := core.Authorize(oauth.AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: client.RedirectURIs[0], Scopes: []string{"executor.full"},
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]), CodeChallengeMethod: "S256", OwnerSubject: "fetch-fixture-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := core.ExchangeCode(oauth.TokenRequest{ClientID: client.ClientID, Code: grant.Code, RedirectURI: client.RedirectURIs[0], CodeVerifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.SaveState(filepath.Join(cfg.StateDir, oauthStateFilename)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := []<-chan error{
		startDaemon(t, func() error { return RunBroker(ctx, configPath) }),
		startDaemon(t, func() error { return RunDesktop(ctx, configPath) }),
		startDaemon(t, func() error { return RunAgent(ctx, configPath) }),
	}
	t.Cleanup(func() {
		cancel()
		for _, ch := range done {
			assertDaemonStopped(t, ch)
		}
	})
	for _, helper := range []struct{ endpoint, key string }{{cfg.BrokerEndpoint, values.BrokerIPCKey}, {cfg.DesktopEndpoint, values.DesktopIPCKey}} {
		waitFor(t, func() error {
			return ipc.NewRPCClient(helper.endpoint, []byte(helper.key)).Call(ctx, desktop.RPCMethodTerminalList, struct{}{}, nil)
		})
	}
	baseURL := "http://" + cfg.AgentAddress
	ready := waitForHTTP(t, http.MethodGet, baseURL+"/.well-known/oauth-protected-resource", nil, nil)
	ready.Body.Close()
	httpClient := &http.Client{Timeout: 5 * time.Second}
	send := func(method, token, session, origin string, payload map[string]any) *http.Response {
		t.Helper()
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, method, baseURL+"/mcp", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set(mcp.SessionHeader, session)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal("fixture HTTP request failed")
		}
		return response
	}
	initialized := send(http.MethodPost, tokens.AccessToken, "", "", initializeRequest("init"))
	session := initialized.Header.Get(mcp.SessionHeader)
	initialized.Body.Close()
	if initialized.StatusCode != http.StatusOK || session == "" {
		t.Fatalf("initialize status=%d missingSession=%t", initialized.StatusCode, session == "")
	}
	call := func(name string, args map[string]any, uri, wantError bool) map[string]any {
		t.Helper()
		response := send(http.MethodPost, tokens.AccessToken, session, "", fetchRuntimeRequest(t, name, args, uri))
		var decoded struct {
			Result struct {
				IsError           bool           `json:"isError"`
				StructuredContent map[string]any `json:"structuredContent"`
			} `json:"result"`
			Error any `json:"error"`
		}
		decodeHTTPJSON(t, response, &decoded)
		if decoded.Error != nil || decoded.Result.IsError != wantError {
			t.Fatalf("%s returned error=%v toolError=%t, want %t", name, decoded.Error, decoded.Result.IsError, wantError)
		}
		return decoded.Result.StructuredContent
	}

	t.Run("authentication origin and session boundaries", func(t *testing.T) {
		path := filepath.Join(cfg.StateDir, "rejected.txt")
		payload := fetchRuntimeRequest(t, "filesystem_write", map[string]any{"action": "write_file", "path": path, "content": "REJECTED_FIXTURE"}, false)
		for _, test := range []struct {
			method, token, session, origin string
			status                         int
		}{
			{http.MethodPost, "", session, "", http.StatusUnauthorized},
			{http.MethodPost, "invalid-fixture-token", session, "", http.StatusUnauthorized},
			{http.MethodPost, tokens.AccessToken, "", "", http.StatusBadRequest},
			{http.MethodPost, tokens.AccessToken, session, "https://other.example.test", http.StatusForbidden},
			{http.MethodGet, tokens.AccessToken, session, "", http.StatusMethodNotAllowed},
			{http.MethodHead, tokens.AccessToken, session, "", http.StatusMethodNotAllowed},
		} {
			response := send(test.method, test.token, test.session, test.origin, payload)
			response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("boundary status=%d want=%d", response.StatusCode, test.status)
			}
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("a rejected request created a file")
		}
	})

	content := "FETCH_PAYLOAD_PRIVATE 中文 + & % #\n"
	for _, privilege := range []string{"owner", "admin"} {
		t.Run(privilege+" file and terminal operations", func(t *testing.T) {
			path := filepath.Join(cfg.StateDir, privilege+"-fetch.txt")
			call("filesystem_write", map[string]any{"action": "write_file", "privilege": privilege, "path": path, "content": content}, false, false)
			call("filesystem_write", map[string]any{"action": "append_file", "privilege": privilege, "path": path, "content": "tail\n"}, true, false)
			read := call("filesystem_read", map[string]any{"action": "read_file", "privilege": privilege, "path": path}, true, false)
			if read["content"] != content+"tail\n" {
				t.Fatal("fetch read did not preserve Unicode content")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != content+"tail\n" {
				t.Fatal("fetch did not actually modify the fixture file")
			}
			moved := path + ".moved"
			call("filesystem_write", map[string]any{"action": "move", "privilege": privilege, "path": path, "destination": moved}, true, false)
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("fetch move left the original file")
			}
			call("filesystem_write", map[string]any{"action": "delete", "privilege": privilege, "path": moved, "recursive": false}, false, false)
			if _, err := os.Stat(moved); !os.IsNotExist(err) {
				t.Fatal("fetch delete left the fixture file")
			}

			argv := []string{"/bin/sh", "-c", "printf FETCH_TERMINAL_OK; exit 7"}
			if runtime.GOOS == "windows" {
				argv = []string{"cmd.exe", "/d", "/c", "echo FETCH_TERMINAL_OK&exit /b 7"}
			}
			created := call("terminal", map[string]any{"action": "create", "privilege": privilege, "argv": argv, "cwd": cfg.StateDir}, true, false)
			id, ok := created["ID"].(string)
			if !ok || id == "" {
				t.Fatal("fetch terminal did not return a session ID")
			}
			var output []byte
			cursor := float64(0)
			deadline := time.Now().Add(5 * time.Second)
			for {
				page := call("terminal_output", map[string]any{"sessionId": id, "privilege": privilege, "cursor": cursor, "limit": 4}, false, false)
				chunk, err := base64.StdEncoding.DecodeString(page["Data"].(string))
				if err != nil || len(chunk) > 4 {
					t.Fatal("fetch terminal output violated its byte-page contract")
				}
				output = append(output, chunk...)
				cursor = page["NextCursor"].(float64)
				if page["sessionRunning"] == false && page["hasMore"] == false {
					if page["exitCode"] != float64(7) || !strings.Contains(string(output), "FETCH_TERMINAL_OK") {
						t.Fatalf("fetch terminal completion mismatch: exit=%v", page["exitCode"])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("fetch terminal did not complete")
				}
				time.Sleep(10 * time.Millisecond)
			}
			call("terminal", map[string]any{"action": "close", "privilege": privilege, "sessionId": id}, false, false)
		})
	}

	t.Run("target validation", func(t *testing.T) {
		path := filepath.Join(cfg.StateDir, "invalid.txt")
		call("filesystem_write", map[string]any{"action": "write_file", "path": path, "content": "VALIDATION_PRIVATE", "privilege": "root"}, false, true)
		call("filesystem_write", map[string]any{"action": "write_file", "path": path, "content": "invalid_base64", "encoding": "base64"}, true, true)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid target arguments created a file")
		}
	})

	t.Run("stdio file write", func(t *testing.T) {
		path := filepath.Join(cfg.StateDir, "stdio.txt")
		var input, output bytes.Buffer
		writeMCPFrame(t, &input, initializeRequest("init"))
		writeMCPFrame(t, &input, fetchRuntimeRequest(t, "filesystem_write", map[string]any{"action": "write_file", "privilege": "owner", "path": path, "content": content}, true))
		if err := RunStdio(ctx, configPath, &input, &output); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(&output)
		for i := 0; i < 2; i++ {
			var response struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
				Error any `json:"error"`
			}
			decodeMCPFrame(t, reader, &response)
			if response.Error != nil || response.Result.IsError {
				t.Fatal("stdio fetch failed")
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != content {
			t.Fatal("stdio fetch did not actually write the file")
		}
	})

	t.Run("metadata only audit", func(t *testing.T) {
		store, err := audit.Open(filepath.Join(cfg.StateDir, "audit.jsonl"), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		events, err := store.List(0)
		if err != nil || len(events) < 20 {
			t.Fatalf("missing fetch audit events: count=%d err=%v", len(events), err)
		}
		seen := map[string]bool{}
		for _, event := range events {
			if event.Tool == "fetch" || event.Detail != "" || event.SessionID == "" {
				t.Fatal("fetch audit lost the underlying operation or retained request content")
			}
			if event.Outcome == "succeeded" {
				seen[event.Actor+":"+event.Identity+":"+event.Tool] = true
			}
		}
		for _, key := range []string{"fetch-fixture-owner:owner:filesystem_write", "fetch-fixture-owner:admin:filesystem_write", "fetch-fixture-owner:owner:terminal", "fetch-fixture-owner:admin:terminal", "local-stdio:owner:filesystem_write"} {
			if !seen[key] {
				t.Errorf("missing successful audit identity %s", key)
			}
		}
		raw, err := os.ReadFile(filepath.Join(cfg.StateDir, "audit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{tokens.AccessToken, "FETCH_PAYLOAD_PRIVATE", "FETCH_TERMINAL_OK", "VALIDATION_PRIVATE", "executor://", `"request"`, "invalid_base64"} {
			if bytes.Contains(raw, []byte(private)) {
				t.Fatal("audit retained request, credential, or output content")
			}
		}
	})

	t.Run("disabled marker", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(cfg.StateDir, "disabled"), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
		response := send(http.MethodPost, tokens.AccessToken, session, "", fetchRuntimeRequest(t, "device_status", map[string]any{"action": "summary"}, false))
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("disabled fetch status=%d", response.StatusCode)
		}
	})
}

func fetchRuntimeRequest(t *testing.T, name string, args map[string]any, uri bool) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	request := string(encoded)
	if uri {
		request = "executor://call?request=" + url.QueryEscape(request)
	}
	return toolCallRequest("fetch-test", "fetch", map[string]any{"request": request})
}
