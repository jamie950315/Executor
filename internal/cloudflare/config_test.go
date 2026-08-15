package cloudflare

import "testing"

func TestBuildCloudflaredRunCommandUsesTokenFile(t *testing.T) {
	t.Parallel()

	cmd, err := BuildCloudflaredRunCommand("/usr/local/bin/cloudflared", "/etc/cloudflared/executor.token")
	if err != nil {
		t.Fatalf("BuildCloudflaredRunCommand: %v", err)
	}
	want := "/usr/local/bin/cloudflared tunnel run --token-file /etc/cloudflared/executor.token"
	if cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
}
