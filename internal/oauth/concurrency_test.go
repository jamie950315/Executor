package oauth

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func pendingGrant(t *testing.T, core *Core) (ClientRegistration, TokenRequest) {
	t.Helper()
	client := registerClientForTest(t, core)
	verifier := strings.Repeat("v", 43)
	grant, err := core.Authorize(AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: client.RedirectURIs[0],
		CodeChallenge: s256Challenge(verifier), CodeChallengeMethod: "S256", OwnerSubject: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, TokenRequest{ClientID: client.ClientID, RedirectURI: client.RedirectURIs[0], Code: grant.Code, CodeVerifier: verifier}
}

func TestRevocationDuringTokenIssuance(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		name := "authorization_code"
		if refresh {
			name = "refresh_token"
		}
		t.Run(name, func(t *testing.T) {
			core := newTestCore(t)
			client, request := pendingGrant(t, core)
			issue := func() (TokenSet, error) { return core.ExchangeCode(request) }
			if refresh {
				tokens, err := issue()
				if err != nil {
					t.Fatal(err)
				}
				issue = func() (TokenSet, error) {
					return core.Refresh(TokenRefreshRequest{ClientID: client.ClientID, RefreshToken: tokens.RefreshToken})
				}
			}
			now := core.now()
			calls := 0
			core.now = func() time.Time {
				calls++
				// The first read validates the grant, the second begins issuance.
				if calls == 2 {
					core.RevokeAll()
				}
				return now
			}
			if _, err := issue(); err == nil {
				t.Fatal("revoked grant issued fresh tokens")
			}
			if len(core.refreshTokens) != 0 {
				t.Fatal("revoked grant left an active refresh token")
			}
		})
	}
}

func TestGrantsExpireAtDeadline(t *testing.T) {
	t.Run("authorization_code", func(t *testing.T) {
		core := newTestCore(t)
		_, request := pendingGrant(t, core)
		deadline := core.codes[request.Code].ExpiresAt
		core.now = func() time.Time { return deadline }
		if _, err := core.ExchangeCode(request); err == nil {
			t.Fatal("authorization code accepted at expiry")
		}
	})
	t.Run("refresh_token", func(t *testing.T) {
		core := newTestCore(t)
		client, request := pendingGrant(t, core)
		tokens, err := core.ExchangeCode(request)
		if err != nil {
			t.Fatal(err)
		}
		deadline := core.refreshTokens[hashRefreshToken(tokens.RefreshToken)].ExpiresAt
		core.now = func() time.Time { return deadline }
		if _, err := core.Refresh(TokenRefreshRequest{ClientID: client.ClientID, RefreshToken: tokens.RefreshToken}); err == nil {
			t.Fatal("refresh token accepted at expiry")
		}
	})
}

func TestConcurrentSaveState(t *testing.T) {
	t.Run("same_core", func(t *testing.T) { testConcurrentSaveState(t, false) })
	t.Run("distinct_cores", func(t *testing.T) { testConcurrentSaveState(t, true) })
}

func TestConcurrentStateReadersAndWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth-state.json")
	if err := newTestCore(t).SaveState(path); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range 32 {
		core := newTestCore(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 5 {
				var err error
				if index%2 == 0 {
					err = core.LoadState(path)
				} else {
					err = core.SaveState(path)
				}
				if err != nil {
					t.Errorf("concurrent state access failed: %v", err)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}

func testConcurrentSaveState(t *testing.T, distinct bool) {
	sharedCore := newTestCore(t)
	path := filepath.Join(t.TempDir(), "oauth-state.json")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 32 {
		core := sharedCore
		if distinct {
			core = newTestCore(t)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := core.SaveState(path); err != nil {
				t.Errorf("concurrent SaveState failed: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if err := newTestCore(t).LoadState(path); err != nil {
		t.Fatalf("saved state invalid: %v", err)
	}
}
