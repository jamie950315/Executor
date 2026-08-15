package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/oauth"
)

func TestOAuthHTTPMetadataRegistrationAuthorizationAndToken(t *testing.T) {
	core := testOAuthCore(t)
	h := NewOAuthHandler(core, "https://executor.example.com", func(key string) bool { return key == "recovery-key" })

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
		res := httptest.NewRecorder()
		h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("metadata %s status=%d content-type=%q body=%q", path, res.Code, res.Header().Get("Content-Type"), res.Body.String())
		}
	}

	registrationBody := `{"client_name":"ChatGPT","redirect_uris":["https://chatgpt.com/connector/oauth/test"],"scopes":["executor.full"]}`
	registrationRes := httptest.NewRecorder()
	registrationReq := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(registrationBody))
	registrationReq.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(registrationRes, registrationReq)
	if registrationRes.Code != http.StatusCreated {
		t.Fatalf("registration status=%d body=%q", registrationRes.Code, registrationRes.Body.String())
	}
	var client oauth.ClientRegistration
	if err := json.Unmarshal(registrationRes.Body.Bytes(), &client); err != nil || client.ClientID == "" {
		t.Fatalf("registration=%#v err=%v", client, err)
	}

	verifier := strings.Repeat("v", 48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {client.RedirectURIs[0]},
		"scope":                 {"executor.full"},
		"state":                 {"chatgpt-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	authorizePage := httptest.NewRecorder()
	h.ServeHTTP(authorizePage, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+values.Encode(), nil))
	if authorizePage.Code != http.StatusOK || !strings.Contains(authorizePage.Body.String(), "Executor") {
		t.Fatalf("authorize page status=%d body=%q", authorizePage.Code, authorizePage.Body.String())
	}

	values.Set("recovery_key", "wrong")
	deniedReq := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(values.Encode()))
	deniedReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	denied := httptest.NewRecorder()
	h.ServeHTTP(denied, deniedReq)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("wrong recovery status=%d", denied.Code)
	}

	values.Set("recovery_key", "recovery-key")
	authorizeReq := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(values.Encode()))
	authorizeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	authorized := httptest.NewRecorder()
	h.ServeHTTP(authorized, authorizeReq)
	if authorized.Code != http.StatusSeeOther {
		t.Fatalf("authorize status=%d body=%q", authorized.Code, authorized.Body.String())
	}
	redirect, err := url.Parse(authorized.Header().Get("Location"))
	if err != nil || redirect.Query().Get("state") != "chatgpt-state" || redirect.Query().Get("code") == "" {
		t.Fatalf("redirect=%q err=%v", authorized.Header().Get("Location"), err)
	}

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {client.ClientID},
		"code":          {redirect.Query().Get("code")},
		"redirect_uri":  {client.RedirectURIs[0]},
		"code_verifier": {verifier},
	}
	tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenForm.Encode()))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRes := httptest.NewRecorder()
	h.ServeHTTP(tokenRes, tokenReq)
	if tokenRes.Code != http.StatusOK {
		t.Fatalf("token status=%d body=%q", tokenRes.Code, tokenRes.Body.String())
	}
	var tokens oauth.TokenSet
	if err := json.Unmarshal(tokenRes.Body.Bytes(), &tokens); err != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("tokens=%#v err=%v", tokens, err)
	}
	if _, err := core.VerifyAccessToken(tokens.AccessToken, oauth.VerifyOptions{Audience: "https://executor.example.com", Scope: "executor.full"}); err != nil {
		t.Fatalf("VerifyAccessToken: %v", err)
	}
}

func testOAuthCore(t *testing.T) *oauth.Core {
	t.Helper()
	core, err := oauth.NewCore(oauth.Config{
		Issuer:               "https://executor.example.com",
		Resource:             "https://executor.example.com",
		Audience:             "https://executor.example.com",
		AuthorizationPath:    "/oauth/authorize",
		TokenPath:            "/oauth/token",
		RegistrationPath:     "/oauth/register",
		AccessTokenTTL:       5 * time.Minute,
		AuthorizationCodeTTL: time.Minute,
		RefreshTokenTTL:      24 * time.Hour,
		SigningKey:           []byte("01234567890123456789012345678901"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return core
}
