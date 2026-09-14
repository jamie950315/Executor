package config

import (
	"path/filepath"
	"testing"
)

func TestOAuthResourceAliasConfig(t *testing.T) {
	for _, alias := range []string{"https://tunnel.example.test/v1/mcp/tunnel_test", "http://unsafe.test/mcp", "https://user:secret@example.test/mcp", "https://example.test/mcp?q=x", "https://example.test/mcp#fragment", "https://*.example.test/mcp", ""} {
		cfg := Default(t.TempDir())
		cfg.OAuthResourceAliases = []string{alias}
		path := filepath.Join(cfg.StateDir, "config.json")
		err := Save(path, cfg)
		valid := alias == "https://tunnel.example.test/v1/mcp/tunnel_test"
		if valid && err != nil {
			t.Fatal(err)
		}
		if !valid && err == nil {
			t.Fatalf("accepted invalid alias %q", alias)
		}
		if valid {
			loaded, err := Load(path)
			if err != nil || len(loaded.OAuthResourceAliases) != 1 || loaded.OAuthResourceAliases[0] != alias {
				t.Fatal("alias round trip failed")
			}
		}
	}
}
