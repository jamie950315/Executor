package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

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

	resource := core.ProtectedResourceMetadata()
	if resource.Resource != "https://executor.example.com" {
		t.Fatalf("resource = %q, want https://executor.example.com", resource.Resource)
	}
	if len(resource.AuthorizationServers) != 1 || resource.AuthorizationServers[0] != authz.Issuer {
		t.Fatalf("authorization servers = %#v, want [%q]", resource.AuthorizationServers, authz.Issuer)
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

func newTestCore(t *testing.T) *Core {
	t.Helper()

	core, err := NewCore(Config{
		Issuer:              "https://executor.example.com",
		Resource:            "https://executor.example.com",
		Audience:            "executor-cli",
		AuthorizationPath:   "/oauth/authorize",
		TokenPath:           "/oauth/token",
		RegistrationPath:    "/oauth/register",
		AccessTokenTTL:      2 * time.Minute,
		AuthorizationCodeTTL: time.Minute,
		RefreshTokenTTL:     10 * time.Minute,
		SigningKey:          []byte("test-signing-key-please-change"),
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

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return strings.TrimRight(base64.URLEncoding.EncodeToString(sum[:]), "=")
}
