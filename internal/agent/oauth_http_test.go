package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/oauth"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthHTTPMetadataRegistrationAuthorizationAndToken(t *testing.T) {
	core := testOAuthCore(t)
	statePath := filepath.Join(t.TempDir(), "oauth-state.json")
	h := NewOAuthHandler(core, "https://executor.example.com", func(key string) bool { return key == "recovery-key" }, WithOAuthStatePath(statePath))

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
	if authorizePage.Code != http.StatusOK || !strings.Contains(authorizePage.Body.String(), "ChatGPT") || !strings.Contains(authorizePage.Body.String(), client.RedirectURIs[0]) {
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
	info, err := os.Stat(statePath)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("OAuth state mode = %v, err=%v", info, err)
	}
	restarted := testOAuthCore(t)
	if err := restarted.LoadState(statePath); err != nil {
		t.Fatalf("LoadState after HTTP mutations: %v", err)
	}
	if _, err := restarted.Refresh(oauth.TokenRefreshRequest{
		ClientID: client.ClientID, RefreshToken: tokens.RefreshToken, Scopes: []string{"executor.full"},
	}); err != nil {
		t.Fatalf("refresh after restart: %v", err)
	}
}

func TestOAuthHTTPRejectsDCRRedirectOutsideChatGPTOrLoopback(t *testing.T) {
	core := testOAuthCore(t)
	h := NewOAuthHandler(core, "https://executor.example.com", func(string) bool { return true })
	request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"client_name":"ChatGPT","redirect_uris":["https://attacker.example/callback"]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("untrusted redirect status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestOAuthHTTPAcceptsStandardDynamicClientRegistrationMetadata(t *testing.T) {
	core := testOAuthCore(t)
	h := NewOAuthHandler(core, "https://executor.example.com", func(string) bool { return true })
	body := `{
		"client_name":"ChatGPT",
		"redirect_uris":["https://chatgpt.com/connector/oauth/executor"],
		"scope":"executor.full executor.desktop",
		"token_endpoint_auth_method":"none",
		"grant_types":["authorization_code","refresh_token"],
		"response_types":["code"],
		"software_id":"chatgpt"
	}`
	request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("standard DCR status=%d body=%q", response.Code, response.Body.String())
	}
	var registration oauth.ClientRegistration
	if err := json.Unmarshal(response.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(registration.Scopes, " "), "executor.full executor.desktop"; got != want {
		t.Fatalf("registered scopes = %q, want %q", got, want)
	}
}

func TestOAuthHTTPResolvesChatGPTClientMetadataDocument(t *testing.T) {
	core := testOAuthCore(t)
	clientID := "https://chatgpt.com/oauth/executor/client.json"
	callback := "https://chatgpt.com/connector/oauth/executor"
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != clientID {
			t.Fatalf("CIMD request URL = %q", r.URL.String())
		}
		body := `{"client_name":"ChatGPT","redirect_uris":["` + callback + `"],"scope":"executor.full","token_endpoint_auth_methods_supported":["none","private_key_jwt"]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	h := NewOAuthHandler(core, "https://executor.example.com", func(string) bool { return true }, WithCIMDHTTPClient(httpClient))
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {callback},
		"scope":                 {"executor.full"},
		"code_challenge":        {strings.Repeat("a", 43)},
		"code_challenge_method": {"S256"},
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+values.Encode(), nil))
	if res.Code != http.StatusOK {
		t.Fatalf("authorize status=%d body=%q", res.Code, res.Body.String())
	}
	if err := core.ValidateClient(oauth.ClientValidationRequest{ClientID: clientID, RedirectURI: callback}); err != nil {
		t.Fatalf("CIMD client was not registered: %v", err)
	}
}

func TestOAuthHTTPRejectsUntrustedClientMetadataHost(t *testing.T) {
	core := testOAuthCore(t)
	h := NewOAuthHandler(core, "https://executor.example.com", func(string) bool { return true })
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {"https://127.0.0.1/client.json"},
		"redirect_uri":          {"https://chatgpt.com/connector/oauth/test"},
		"code_challenge":        {strings.Repeat("a", 43)},
		"code_challenge_method": {"S256"},
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+values.Encode(), nil))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", res.Code)
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
