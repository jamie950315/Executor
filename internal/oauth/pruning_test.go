package oauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatePersistencePrunesUnusableGrants(t *testing.T) {
	for _, operation := range []string{"save", "load"} {
		t.Run(operation, func(t *testing.T) {
			core := newTestCore(t)
			client := registerClientForTest(t, core)
			now := core.now()
			core.generation = 2
			fixture := persistenceState{
				Generation: 2, Clients: map[string]ClientRegistration{client.ClientID: client},
				Codes: make(map[string]persistedCodeRecord), RefreshTokens: make(map[string]persistedRefreshRecord),
			}
			for _, entry := range []struct {
				name       string
				expires    time.Time
				generation uint64
			}{
				{"valid", now.Add(time.Minute), 2},
				{"expired", now.Add(-time.Second), 2},
				{"boundary", now, 2},
				{"revoked", now.Add(time.Minute), 1},
			} {
				core.codes[entry.name] = authorizationCodeRecord{
					ClientID: client.ClientID, RedirectURI: client.RedirectURIs[0],
					Scopes: client.Scopes, CodeChallenge: s256Challenge("fixture-verifier"),
					ChallengeMethod: "S256", Subject: "fixture-owner",
					ExpiresAt: entry.expires, Generation: entry.generation,
				}
				core.refreshTokens[hashRefreshToken(entry.name)] = refreshTokenRecord{
					ClientID: client.ClientID, Scopes: client.Scopes, Subject: "fixture-owner",
					ExpiresAt: entry.expires, Generation: entry.generation,
				}
				fixture.Codes[entry.name] = persistedCodeRecord{
					ClientID: client.ClientID, RedirectURI: client.RedirectURIs[0], Scopes: client.Scopes,
					CodeChallenge: s256Challenge("fixture-verifier"), ChallengeMethod: "S256", Subject: "fixture-owner",
					ExpiresAt: entry.expires, Generation: entry.generation,
				}
				fixture.RefreshTokens[hashRefreshToken(entry.name)] = persistedRefreshRecord{
					ClientID: client.ClientID, Scopes: client.Scopes, Subject: "fixture-owner",
					ExpiresAt: entry.expires, Generation: entry.generation,
				}
			}
			path := filepath.Join(t.TempDir(), "oauth-state.json")
			if operation == "save" {
				if err := core.SaveState(path); err != nil {
					t.Fatal(err)
				}
				payload, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var state persistenceState
				if err := json.Unmarshal(payload, &state); err != nil {
					t.Fatal(err)
				}
				if len(state.Codes) != 1 || len(state.RefreshTokens) != 1 {
					t.Errorf("persisted grants: codes=%d refresh=%d, want one usable grant each", len(state.Codes), len(state.RefreshTokens))
				}
			} else {
				payload, err := json.Marshal(fixture)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, payload, 0600); err != nil {
					t.Fatal(err)
				}
				core = newTestCore(t)
				if err := core.LoadState(path); err != nil {
					t.Fatal(err)
				}
			}
			if len(core.codes) != 1 || len(core.refreshTokens) != 1 {
				t.Fatalf("in-memory grants: codes=%d refresh=%d, want one usable grant each", len(core.codes), len(core.refreshTokens))
			}
			if len(core.clients) != 1 || core.generation != 2 {
				t.Fatal("client registration or revocation generation changed")
			}
			if _, err := core.ExchangeCode(TokenRequest{ClientID: client.ClientID, Code: "valid", RedirectURI: client.RedirectURIs[0], CodeVerifier: "fixture-verifier"}); err != nil {
				t.Fatalf("valid authorization code was not preserved: %v", err)
			}
			if _, err := core.Refresh(TokenRefreshRequest{ClientID: client.ClientID, RefreshToken: "valid"}); err != nil {
				t.Fatalf("valid refresh grant was not preserved: %v", err)
			}
		})
	}
}
