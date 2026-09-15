package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestHubCLIInitAndPublicRegistration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	args := []string{"hub", "init", "--state-dir", dir, "--hub-id", "pi5", "--domain", "hub.example.test", "--dashboard-url", "https://dashboard.example.test"}
	var out, errout bytes.Buffer
	handled, code := runHubCommand(context.Background(), args, &out, &errout)
	if !handled || code != 0 || !strings.Contains(out.String(), "Recovery key (shown once):") {
		t.Fatalf("Hub init status=%d error=%s", code, errout.String())
	}
	out.Reset()
	errout.Reset()
	_, code = runHubCommand(context.Background(), args, &out, &errout)
	if code != 0 || strings.Contains(out.String(), "Recovery key (shown once):") {
		t.Fatal("idempotent init reprinted/replaced recovery key")
	}
	out.Reset()
	_, code = runHubCommand(context.Background(), []string{"hub", "registration", "--state-dir", dir}, &out, &errout)
	if code != 0 || !strings.Contains(out.String(), `"token_hash"`) || strings.Contains(out.String(), "Recovery") {
		t.Fatal("public registration output invalid")
	}
}
