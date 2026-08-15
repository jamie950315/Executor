package control

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/oauth"
	"github.com/jamie950315/executor/internal/secrets"
)

type recordingServices struct {
	steps  []string
	fail   map[string]error
	events *[]string
}

func (s *recordingServices) Stop(_ context.Context, service Service) error {
	s.steps = append(s.steps, "stop:"+string(service))
	if s.events != nil {
		*s.events = append(*s.events, "stop:"+string(service))
	}
	return s.fail["stop:"+string(service)]
}

func (s *recordingServices) Start(_ context.Context, service Service) error {
	s.steps = append(s.steps, "start:"+string(service))
	if s.events != nil {
		*s.events = append(*s.events, "start:"+string(service))
	}
	return s.fail["start:"+string(service)]
}

type recordingCaller struct {
	steps  []string
	fail   map[string]error
	events *[]string
}

func (c *recordingCaller) KillAll(_ context.Context, component Service) error {
	c.steps = append(c.steps, "kill:"+string(component))
	if c.events != nil {
		*c.events = append(*c.events, "kill:"+string(component))
	}
	return c.fail["kill:"+string(component)]
}

func TestKillRunsEveryStepAndReturnsRotatedRecoveryMaterial(t *testing.T) {
	cfg, values := controlFixture(t)
	var events []string
	services := &recordingServices{events: &events}
	caller := &recordingCaller{events: &events}
	controller := NewController(cfg, values, services, caller)

	result, err := controller.Kill(context.Background())
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if result.RecoveryKey == "" || result.URLSecret == "" {
		t.Fatal("Kill did not return the replacement one-time material")
	}
	if values.VerifyRecoveryKey(result.RecoveryKey) || values.VerifyURLSecret(result.URLSecret) {
		t.Fatal("Kill returned unchanged secret material")
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, disabledMarkerName)); err != nil {
		t.Fatalf("disabled marker: %v", err)
	}
	if got, want := events, []string{"stop:cloudflared", "kill:broker", "kill:desktop", "stop:desktop", "stop:broker", "stop:agent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("side effects = %#v, want %#v", got, want)
	}
}

func TestKillContinuesAfterFailuresAndRevokesPersistedOAuth(t *testing.T) {
	cfg, values := controlFixture(t)
	accessToken := writeOAuthState(t, cfg, values)
	services := &recordingServices{fail: map[string]error{"stop:cloudflared": errors.New("tunnel unavailable"), "stop:agent": errors.New("agent unavailable")}}
	caller := &recordingCaller{fail: map[string]error{"kill:broker": errors.New("broker unavailable")}}
	controller := NewController(cfg, values, services, caller)

	result, err := controller.Kill(context.Background())
	if err == nil || result.RecoveryKey == "" || result.URLSecret == "" {
		t.Fatalf("Kill result = %#v, %v; want rotated material plus joined errors", result, err)
	}
	if got, want := services.steps, []string{"stop:cloudflared", "stop:desktop", "stop:broker", "stop:agent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %#v, want %#v", got, want)
	}
	if got, want := caller.steps, []string{"kill:broker", "kill:desktop"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IPC = %#v, want %#v", got, want)
	}
	if err := oauthStateRevoked(cfg, values.OAuthKey, accessToken); err != nil {
		t.Fatal(err)
	}
}

func TestResumeStopsAtFirstFailureAndKeepsDisabledMarker(t *testing.T) {
	cfg, values := controlFixture(t)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, disabledMarkerName), []byte("quiesced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	services := &recordingServices{fail: map[string]error{"start:desktop": errors.New("desktop unavailable")}}
	controller := NewController(cfg, values, services, &recordingCaller{})

	if err := controller.Resume(context.Background()); err == nil {
		t.Fatal("Resume unexpectedly succeeded")
	}
	if got, want := services.steps, []string{"start:broker", "start:desktop"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, disabledMarkerName)); err != nil {
		t.Fatalf("Resume removed marker after failure: %v", err)
	}
}

func TestResumeRemovesMarkerOnlyAfterAllServicesStart(t *testing.T) {
	cfg, values := controlFixture(t)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, disabledMarkerName), []byte("quiesced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	services := &recordingServices{}
	controller := NewController(cfg, values, services, &recordingCaller{})

	if err := controller.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got, want := services.steps, []string{"start:broker", "start:desktop", "start:agent", "start:cloudflared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, disabledMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled marker exists after successful Resume: %v", err)
	}
}

func controlFixture(t *testing.T) (config.Config, secrets.Values) {
	t.Helper()
	stateDir := t.TempDir()
	values, err := secrets.Create(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(stateDir)
	cfg.Domain = "executor.example.test"
	return cfg, values
}

func writeOAuthState(t *testing.T, cfg config.Config, values secrets.Values) string {
	t.Helper()
	core := oauthCore(t, cfg, values.OAuthKey)
	client, err := core.RegisterClient(oauth.DynamicClientRegistrationRequest{
		ClientName:   "Kill test client",
		RedirectURIs: []string{"https://client.example.test/callback"},
		Scopes:       []string{"executor.full"},
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier := "kill-test-verifier"
	sum := sha256.Sum256([]byte(verifier))
	grant, err := core.Authorize(oauth.AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: client.RedirectURIs[0], Scopes: client.Scopes,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]), CodeChallengeMethod: "S256", OwnerSubject: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := core.ExchangeCode(oauth.TokenRequest{ClientID: client.ClientID, Code: grant.Code, RedirectURI: client.RedirectURIs[0], CodeVerifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.SaveState(filepath.Join(cfg.StateDir, oauthStateFilename)); err != nil {
		t.Fatal(err)
	}
	return tokens.AccessToken
}

func oauthStateRevoked(cfg config.Config, key, accessToken string) error {
	core, err := oauth.NewCore(oauth.Config{
		Issuer: "https://" + cfg.Domain, Resource: "https://" + cfg.Domain, Audience: "https://" + cfg.Domain,
		AuthorizationPath: "/oauth/authorize", TokenPath: "/oauth/token", RegistrationPath: "/oauth/register",
		AccessTokenTTL: time.Hour, AuthorizationCodeTTL: 5 * time.Minute, RefreshTokenTTL: 30 * 24 * time.Hour, SigningKey: []byte(key),
	})
	if err != nil {
		return err
	}
	if err := core.LoadState(filepath.Join(cfg.StateDir, oauthStateFilename)); err != nil {
		return err
	}
	if _, err := core.VerifyAccessToken(accessToken, oauth.VerifyOptions{}); err == nil {
		return errors.New("persisted OAuth state still accepted the pre-Kill access token")
	}
	return nil
}

func oauthCore(t *testing.T, cfg config.Config, key string) *oauth.Core {
	t.Helper()
	core, err := oauth.NewCore(oauth.Config{
		Issuer: "https://" + cfg.Domain, Resource: "https://" + cfg.Domain, Audience: "https://" + cfg.Domain,
		AuthorizationPath: "/oauth/authorize", TokenPath: "/oauth/token", RegistrationPath: "/oauth/register",
		AccessTokenTTL: time.Hour, AuthorizationCodeTTL: 5 * time.Minute, RefreshTokenTTL: 30 * 24 * time.Hour, SigningKey: []byte(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	return core
}
