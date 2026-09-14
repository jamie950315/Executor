package agent

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOAuthResourceAliasesAreExplicitAndExact(t *testing.T) {
	const canonical = "https://executor.example.com"
	const alias = "https://tunnel.example.test/v1/mcp/tunnel_test"
	for _, enabled := range []bool{false, true} {
		core := testOAuthCore(t)
		var options []OAuthOption
		if enabled {
			options = append(options, WithOAuthResourceAliases([]string{alias}))
		}
		h := NewOAuthHandler(core, canonical, nil, options...).(*oauthHandler)
		for _, resource := range []string{"", canonical, alias, alias + "/", alias + "?x=1", "https://other.example.test/v1/mcp/tunnel_test"} {
			want := resource == "" || resource == canonical || enabled && resource == alias
			if got := h.acceptsResource(url.Values{"resource": {resource}}); got != want {
				t.Errorf("enabled=%v resource=%q accepted=%v want=%v", enabled, resource, got, want)
			}
		}
		if h.acceptsResource(url.Values{"resource": {canonical, alias}}) {
			t.Fatal("duplicate resource accepted")
		}
	}
}

func TestOAuthAliasStillRequiresOwnerAndPKCE(t *testing.T) {
	const alias = "https://tunnel.example.test/v1/mcp/tunnel_test"
	h := NewOAuthHandler(testOAuthCore(t), "https://executor.example.com", func(string) bool { return false }, WithOAuthResourceAliases([]string{alias}))
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(url.Values{"resource": {alias}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("owner authentication status=%d", res.Code)
	}
	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth/authorize?response_type=code&resource="+url.QueryEscape(alias), nil))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "PKCE") {
		t.Fatalf("PKCE enforcement: %d %s", res.Code, res.Body.String())
	}
}
