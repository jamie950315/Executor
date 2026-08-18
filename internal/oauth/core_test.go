package oauth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNewCoreRejectsShortSigningKey(t *testing.T) {
	t.Parallel()

	_, err := NewCore(Config{
		Issuer:               "https://executor.example.com",
		Resource:             "https://executor.example.com",
		Audience:             "executor-cli",
		AuthorizationPath:    "/oauth/authorize",
		TokenPath:            "/oauth/token",
		RegistrationPath:     "/oauth/register",
		AccessTokenTTL:       2 * time.Minute,
		AuthorizationCodeTTL: time.Minute,
		RefreshTokenTTL:      10 * time.Minute,
		SigningKey:           []byte("short-key"),
	})
	if err == nil {
		t.Fatal("NewCore() succeeded with short signing key, want error")
	}
}

func TestMetadataAndDynamicClientRegistration(t *testing.T) {
	t.Parallel()

	core := newTestCore(t)

	authz := core.AuthorizationServerMetadata()
	if authz.Issuer != "https://executor.example.com" {
		t.Fatalf("issuer = %q, want https://executor.example.com", authz.Issuer)
	}
	if got := authz.CodeChallengeMethodsSupported; len(got) != 1 || got[0] != "S256" {
		t.Fatalf("code challenge methods = %#v, want [S256]", got)
	}
	if got := authz.ScopesSupported; len(got) != 1 || got[0] != "executor.full" {
		t.Fatalf("authorization scopes = %#v, want [executor.full]", got)
	}
	if authz.ClientIDMetadataDocumentSupported {
		t.Fatal("authorization metadata advertises CIMD despite the ChatGPT callback compatibility fallback")
	}
	if authz.RegistrationEndpoint == "" {
		t.Fatal("authorization metadata must advertise DCR registration endpoint")
	}

	resource := core.ProtectedResourceMetadata()
	if resource.Resource != "https://executor.example.com" {
		t.Fatalf("resource = %q, want https://executor.example.com", resource.Resource)
	}
	if len(resource.AuthorizationServers) != 1 || resource.AuthorizationServers[0] != authz.Issuer {
		t.Fatalf("authorization servers = %#v, want [%q]", resource.AuthorizationServers, authz.Issuer)
	}
	if got := resource.ScopesSupported; len(got) != 1 || got[0] != "executor.full" {
		t.Fatalf("resource scopes = %#v, want [executor.full]", got)
	}

	client, err := core.RegisterClient(DynamicClientRegistrationRequest{
		ClientName:   "Executor CLI",
		RedirectURIs: []string{"http://127.0.0.1/callback"},
		Scopes:       []string{"executor.tools", "executor.filesystem"},
	})
	if err != nil {
		t.Fatalf("RegisterClient() error = %v", err)
	}

	if client.TokenEndpointAuthMethod != "none" {
		t.Fatalf("token endpoint auth method = %q, want none", client.TokenEndpointAuthMethod)
	}
	if len(client.GrantTypes) != 2 {
		t.Fatalf("grant types = %#v, want authorization_code and refresh_token", client.GrantTypes)
	}
}

func TestClientIDURLRegistrationAndValidation(t *testing.T) {
	t.Parallel()

	core := newTestCore(t)

	client, err := core.RegisterClientIDURL(ClientIDURLRegistrationRequest{
		ClientIDURL:  "https://client.example.com/executor-cli",
		ClientName:   "Executor CLI",
		RedirectURIs: []string{"https://client.example.com/callback"},
		Scopes:       []string{"executor.tools"},
	})
	if err != nil {
		t.Fatalf("RegisterClientIDURL() error = %v", err)
	}
	if client.ClientID != "https://client.example.com/executor-cli" {
		t.Fatalf("client_id = %q, want client_id URL", client.ClientID)
	}

	if err := core.ValidateClient(ClientValidationRequest{
		ClientID:    client.ClientID,
		RedirectURI: "https://client.example.com/callback",
	}); err != nil {
		t.Fatalf("ValidateClient() error = %v", err)
	}

	if _, err := core.RegisterClientIDURL(ClientIDURLRegistrationRequest{
		ClientIDURL:  "client.example.com/executor-cli",
		ClientName:   "Executor CLI",
		RedirectURIs: []string{"https://client.example.com/callback"},
	}); err == nil {
		t.Fatal("RegisterClientIDURL() succeeded with non-URL client_id, want error")
	}
}

func TestClientRegistrationRejectsInsecureRedirectURIs(t *testing.T) {
	t.Parallel()

	core := newTestCore(t)

	if _, err := core.RegisterClient(DynamicClientRegistrationRequest{
		ClientName:   "Executor CLI",
		RedirectURIs: []string{"javascript:alert(1)"},
	}); err == nil {
		t.Fatal("RegisterClient() succeeded with javascript redirect URI, want error")
	}

	if _, err := core.RegisterClient(DynamicClientRegistrationRequest{
		ClientName:   "Executor CLI",
		RedirectURIs: []string{"http://example.com/callback"},
	}); err == nil {
		t.Fatal("RegisterClient() succeeded with insecure non-loopback http redirect URI, want error")
	}
}

func TestAuthorizationCodeExchangeRequiresPKCES256AndStoresHashedRefreshTokens(t *testing.T) {
	t.Parallel()

	core := newTestCore(t)
	client := registerClientForTest(t, core)
	verifier := "verifier-value-1234567890"

	code, err := core.Authorize(AuthorizeRequest{
		ClientID:            client.ClientID,
		RedirectURI:         client.RedirectURIs[0],
		Scopes:              []string{"executor.tools", "executor.filesystem"},
		CodeChallenge:       s256Challenge(verifier),
		CodeChallengeMethod: "S256",
		OwnerSubject:        "owner@example.com",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}

	if _, err := core.ExchangeCode(TokenRequest{
		ClientID:     client.ClientID,
		Code:         code.Code,
		RedirectURI:  client.RedirectURIs[0],
		CodeVerifier: "wrong-verifier",
	}); err == nil {
		t.Fatal("ExchangeCode() with wrong verifier succeeded, want error")
	}

	code, err = core.Authorize(AuthorizeRequest{
		ClientID:            client.ClientID,
		RedirectURI:         client.RedirectURIs[0],
		Scopes:              []string{"executor.tools", "executor.filesystem"},
		CodeChallenge:       s256Challenge(verifier),
		CodeChallengeMethod: "S256",
		OwnerSubject:        "owner@example.com",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}

	tokens, err := core.ExchangeCode(TokenRequest{
		ClientID:     client.ClientID,
		Code:         code.Code,
		RedirectURI:  client.RedirectURIs[0],
		CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}

	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("tokens = %#v, want non-empty access and refresh tokens", tokens)
	}

	for key := range core.refreshTokens {
		if key == tokens.RefreshToken {
			t.Fatal("refresh token stored in plaintext")
		}
	}

	hash := hashRefreshToken(tokens.RefreshToken)
	if _, ok := core.refreshTokens[hash]; !ok {
		t.Fatal("hashed refresh token record missing")
	}
}

func TestVerifyAccessTokenEnforcesIssuerAudienceScopeAndExpiry(t *testing.T) {
	t.Parallel()

	core := newTestCore(t)
	client := registerClientForTest(t, core)
	verifier := "verifier-value-1234567890"
	code, err := core.Authorize(AuthorizeRequest{
		ClientID:            client.ClientID,
		RedirectURI:         client.RedirectURIs[0],
		Scopes:              []string{"executor.tools", "executor.desktop"},
		CodeChallenge:       s256Challenge(verifier),
		CodeChallengeMethod: "S256",
		OwnerSubject:        "owner@example.com",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}

	tokens, err := core.ExchangeCode(TokenRequest{
		ClientID:     client.ClientID,
		Code:         code.Code,
		RedirectURI:  client.RedirectURIs[0],
		CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}

	claims, err := core.VerifyAccessToken(tokens.AccessToken, VerifyOptions{
		Audience: "executor-cli",
		Scope:    "executor.tools",
	})
	if err != nil {
		t.Fatalf("VerifyAccessToken() error = %v", err)
	}
	if claims.Subject != "owner@example.com" {
		t.Fatalf("subject = %q, want owner@example.com", claims.Subject)
	}

	if _, err := core.VerifyAccessToken(tokens.AccessToken, VerifyOptions{
		Audience: "wrong-audience",
		Scope:    "executor.tools",
	}); err == nil {
		t.Fatal("VerifyAccessToken() with wrong audience succeeded, want error")
	}

	if _, err := core.VerifyAccessToken(tokens.AccessToken, VerifyOptions{
		Audience: "executor-cli",
		Scope:    "executor.admin",
	}); err == nil {
		t.Fatal("VerifyAccessToken() with missing scope succeeded, want error")
	}

	core.now = func() time.Time {
		return time.Unix(claims.ExpiresAt+1, 0)
	}
	if _, err := core.VerifyAccessToken(tokens.AccessToken, VerifyOptions{
		Audience: "executor-cli",
		Scope:    "executor.tools",
	}); err == nil {
		t.Fatal("VerifyAccessToken() succeeded for expired token, want error")
	}
}

func TestRefreshTokensHonorGenerationAndRevokeAll(t *testing.T) {
	t.Parallel()

	core := newTestCore(t)
	client := registerClientForTest(t, core)
	verifier := "verifier-value-1234567890"
	code, err := core.Authorize(AuthorizeRequest{
		ClientID:            client.ClientID,
		RedirectURI:         client.RedirectURIs[0],
		Scopes:              []string{"executor.tools"},
		CodeChallenge:       s256Challenge(verifier),
		CodeChallengeMethod: "S256",
		OwnerSubject:        "owner@example.com",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}

	tokens, err := core.ExchangeCode(TokenRequest{
		ClientID:     client.ClientID,
		Code:         code.Code,
		RedirectURI:  client.RedirectURIs[0],
		CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}

	refreshed, err := core.Refresh(TokenRefreshRequest{
		ClientID:     client.ClientID,
		RefreshToken: tokens.RefreshToken,
		Scopes:       []string{"executor.tools"},
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed.AccessToken == tokens.AccessToken {
		t.Fatal("Refresh() returned identical access token, want a newly signed token")
	}

	core.RevokeAll()

	if _, err := core.Refresh(TokenRefreshRequest{
		ClientID:     client.ClientID,
		RefreshToken: refreshed.RefreshToken,
		Scopes:       []string{"executor.tools"},
	}); err == nil {
		t.Fatal("Refresh() succeeded after RevokeAll(), want error")
	}

	if _, err := core.VerifyAccessToken(refreshed.AccessToken, VerifyOptions{
		Audience: "executor-cli",
		Scope:    "executor.tools",
	}); err == nil {
		t.Fatal("VerifyAccessToken() succeeded for revoked generation, want error")
	}
}

func TestPersistAndRestoreStateAtomicallyWithPrivatePermissions(t *testing.T) {
	t.Parallel()

	core := newTestCore(t)
	client := registerClientForTest(t, core)
	verifier := "verifier-value-1234567890"
	code, err := core.Authorize(AuthorizeRequest{
		ClientID:            client.ClientID,
		RedirectURI:         client.RedirectURIs[0],
		Scopes:              []string{"executor.tools"},
		CodeChallenge:       s256Challenge(verifier),
		CodeChallengeMethod: "S256",
		OwnerSubject:        "owner@example.com",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	tokens, err := core.ExchangeCode(TokenRequest{
		ClientID:     client.ClientID,
		Code:         code.Code,
		RedirectURI:  client.RedirectURIs[0],
		CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	core.RevokeAll()

	statePath := filepath.Join(t.TempDir(), "oauth-state.json")
	if err := core.SaveState(statePath); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("file mode = %#o, want 0600", got)
	}
	if _, err := os.Stat(statePath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file state = %v, want cleaned up temporary file", err)
	}

	restored := newTestCore(t)
	if err := restored.LoadState(statePath); err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	if _, err := restored.VerifyAccessToken(tokens.AccessToken, VerifyOptions{
		Audience: "executor-cli",
		Scope:    "executor.tools",
	}); err == nil {
		t.Fatal("VerifyAccessToken() succeeded for token from revoked generation after restore, want error")
	}

	if _, err := restored.Refresh(TokenRefreshRequest{
		ClientID:     client.ClientID,
		RefreshToken: tokens.RefreshToken,
		Scopes:       []string{"executor.tools"},
	}); err == nil {
		t.Fatal("Refresh() succeeded for plaintext or revoked refresh token after restore, want error")
	}

	if err := restored.ValidateClient(ClientValidationRequest{
		ClientID:    client.ClientID,
		RedirectURI: client.RedirectURIs[0],
	}); err != nil {
		t.Fatalf("ValidateClient() after restore error = %v", err)
	}
}

func TestPersistAndRestoreActiveCodesAndRefreshTokens(t *testing.T) {
	t.Parallel()

	core := newTestCore(t)
	client := registerClientForTest(t, core)
	verifier := "verifier-value-1234567890"

	pendingCode, err := core.Authorize(AuthorizeRequest{
		ClientID:            client.ClientID,
		RedirectURI:         client.RedirectURIs[0],
		Scopes:              []string{"executor.tools"},
		CodeChallenge:       s256Challenge(verifier),
		CodeChallengeMethod: "S256",
		OwnerSubject:        "owner@example.com",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}

	exchangedCode, err := core.Authorize(AuthorizeRequest{
		ClientID:            client.ClientID,
		RedirectURI:         client.RedirectURIs[0],
		Scopes:              []string{"executor.tools"},
		CodeChallenge:       s256Challenge(verifier),
		CodeChallengeMethod: "S256",
		OwnerSubject:        "owner@example.com",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	tokens, err := core.ExchangeCode(TokenRequest{
		ClientID:     client.ClientID,
		Code:         exchangedCode.Code,
		RedirectURI:  client.RedirectURIs[0],
		CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "oauth-active-state.json")
	if err := core.SaveState(statePath); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	restored := newTestCore(t)
	if err := restored.LoadState(statePath); err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	if _, err := restored.ExchangeCode(TokenRequest{
		ClientID:     client.ClientID,
		Code:         pendingCode.Code,
		RedirectURI:  client.RedirectURIs[0],
		CodeVerifier: verifier,
	}); err != nil {
		t.Fatalf("ExchangeCode() after restore error = %v", err)
	}

	if _, err := restored.Refresh(TokenRefreshRequest{
		ClientID:     client.ClientID,
		RefreshToken: tokens.RefreshToken,
		Scopes:       []string{"executor.tools"},
	}); err != nil {
		t.Fatalf("Refresh() after restore error = %v", err)
	}
}

func newTestCore(t *testing.T) *Core {
	t.Helper()

	core, err := NewCore(Config{
		Issuer:               "https://executor.example.com",
		Resource:             "https://executor.example.com",
		Audience:             "executor-cli",
		AuthorizationPath:    "/oauth/authorize",
		TokenPath:            "/oauth/token",
		RegistrationPath:     "/oauth/register",
		AccessTokenTTL:       2 * time.Minute,
		AuthorizationCodeTTL: time.Minute,
		RefreshTokenTTL:      10 * time.Minute,
		SigningKey:           []byte("test-signing-key-please-change-123"),
		Now: func() time.Time {
			return time.Unix(1_760_000_000, 0)
		},
	})
	if err != nil {
		t.Fatalf("NewCore() error = %v", err)
	}
	return core
}

func registerClientForTest(t *testing.T, core *Core) ClientRegistration {
	t.Helper()

	client, err := core.RegisterClient(DynamicClientRegistrationRequest{
		ClientName:   "Executor CLI",
		RedirectURIs: []string{"http://127.0.0.1/callback"},
		Scopes:       []string{"executor.tools", "executor.filesystem", "executor.desktop"},
	})
	if err != nil {
		t.Fatalf("RegisterClient() error = %v", err)
	}
	return client
}
