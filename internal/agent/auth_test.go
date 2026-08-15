package agent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/oauth"
)

type fakeTokenVerifier struct {
	valid string
}

func (f fakeTokenVerifier) VerifyAccessToken(token string, options oauth.VerifyOptions) (oauth.AccessTokenClaims, error) {
	if token != f.valid || options.Scope != "executor.full" {
		return oauth.AccessTokenClaims{}, errors.New("invalid")
	}
	return oauth.AccessTokenClaims{Subject: "owner", ClientID: "chatgpt"}, nil
}

func TestProtectMCPAcceptsOAuthBearerAndSetsActor(t *testing.T) {
	called := false
	h := ProtectMCP(fakeTokenVerifier{valid: "good-token"}, "https://executor.example.com", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		actor, ok := ActorFromContext(r.Context())
		if !ok || actor.Subject != "owner" || actor.Method != "oauth" {
			t.Fatalf("actor = %#v ok=%v", actor, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent || !called {
		t.Fatalf("status=%d called=%v", res.Code, called)
	}
}

func TestProtectMCPChallengesInvalidBearer(t *testing.T) {
	h := ProtectMCP(fakeTokenVerifier{valid: "good-token"}, "https://executor.example.com", nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler was called")
	}))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", res.Code)
	}
	want := `resource_metadata="https://executor.example.com/.well-known/oauth-protected-resource"`
	if got := res.Header().Get("WWW-Authenticate"); !strings.Contains(got, want) {
		t.Fatalf("challenge=%q, want it to contain %q", got, want)
	}
}

func TestProtectMCPAcceptsEnabledURLSecretWithoutLeakingFailures(t *testing.T) {
	verifier := func(candidate string) bool { return candidate == "url-secret" }
	h := ProtectMCP(fakeTokenVerifier{}, "https://executor.example.com", verifier, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("rewritten path=%q", r.URL.Path)
		}
		actor, _ := ActorFromContext(r.Context())
		if actor.Method != "url-secret" {
			t.Fatalf("actor=%#v", actor)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	valid := httptest.NewRecorder()
	h.ServeHTTP(valid, httptest.NewRequest(http.MethodPost, "/url-secret/mcp", nil))
	if valid.Code != http.StatusNoContent {
		t.Fatalf("valid status=%d", valid.Code)
	}
	invalid := httptest.NewRecorder()
	h.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/wrong/mcp", nil))
	if invalid.Code != http.StatusNotFound || invalid.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("invalid response status=%d challenge=%q", invalid.Code, invalid.Header().Get("WWW-Authenticate"))
	}
}
