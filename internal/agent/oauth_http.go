package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/oauth"
)

type RecoveryVerifier func(string) bool

type oauthHandler struct {
	core           *oauth.Core
	resource       string
	verifyRecovery RecoveryVerifier
	cimdClient     *http.Client
	statePath      string
	recordOAuth    OAuthEventRecorder
}

type OAuthEvent struct {
	Stage      string
	AuthMethod string
	GrantType  string
	Outcome    string
	Code       string
	Reason     string
}

type OAuthEventRecorder func(OAuthEvent)

type OAuthOption func(*oauthHandler)

func WithCIMDHTTPClient(client *http.Client) OAuthOption {
	return func(handler *oauthHandler) {
		if client != nil {
			handler.cimdClient = client
		}
	}
}

func WithOAuthStatePath(path string) OAuthOption {
	return func(handler *oauthHandler) {
		handler.statePath = path
	}
}

func WithOAuthEventRecorder(recorder OAuthEventRecorder) OAuthOption {
	return func(handler *oauthHandler) {
		handler.recordOAuth = recorder
	}
}

func NewOAuthHandler(core *oauth.Core, resource string, verifyRecovery RecoveryVerifier, options ...OAuthOption) http.Handler {
	handler := &oauthHandler{
		core:           core,
		resource:       strings.TrimRight(resource, "/"),
		verifyRecovery: verifyRecovery,
		cimdClient:     &http.Client{Timeout: 5 * time.Second},
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

func (h *oauthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/.well-known/oauth-protected-resource":
		writeJSON(w, http.StatusOK, h.core.ProtectedResourceMetadata())
	case r.Method == http.MethodGet && r.URL.Path == "/.well-known/oauth-authorization-server":
		writeJSON(w, http.StatusOK, h.core.AuthorizationServerMetadata())
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/register":
		h.register(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/oauth/authorize":
		h.authorizePage(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/authorize":
		h.authorize(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		h.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *oauthHandler) register(w http.ResponseWriter, r *http.Request) {
	var request oauth.DynamicClientRegistrationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := decoder.Decode(&request); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "request must contain exactly one JSON object within the size limit")
		return
	}
	for _, redirectURI := range request.RedirectURIs {
		if !trustedDCRRedirect(redirectURI) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect must use chatgpt.com or a loopback host")
			return
		}
	}
	client, err := h.core.RegisterClient(request)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	if err := h.persist(); err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "persist OAuth state")
		return
	}
	writeJSON(w, http.StatusCreated, client)
}

func (h *oauthHandler) authorizePage(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	if err := h.validateAuthorize(r.Context(), values); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	client, ok := h.core.Client(values.Get("client_id"))
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "unknown client")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'sha256-"+authorizeScriptHash+"'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	_ = authorizeTemplate.Execute(w, struct {
		ClientName          string
		ClientID            string
		RedirectURI         string
		Scope               string
		State               string
		CodeChallenge       string
		CodeChallengeMethod string
		Resource            string
		Script              template.JS
	}{
		ClientName:          client.ClientName,
		ClientID:            values.Get("client_id"),
		RedirectURI:         values.Get("redirect_uri"),
		Scope:               values.Get("scope"),
		State:               values.Get("state"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
		Resource:            h.resource,
		Script:              template.JS(authorizeScript),
	})
}

func trustedDCRRedirect(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if trustedCIMDHost(host) {
		return true
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *oauthHandler) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if h.verifyRecovery == nil || !h.verifyRecovery(r.Form.Get("recovery_key")) {
		http.Error(w, "authorization denied", http.StatusForbidden)
		return
	}
	if err := h.validateAuthorize(r.Context(), r.Form); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	grant, err := h.core.Authorize(oauth.AuthorizeRequest{
		ClientID:            r.Form.Get("client_id"),
		RedirectURI:         r.Form.Get("redirect_uri"),
		Scopes:              strings.Fields(r.Form.Get("scope")),
		CodeChallenge:       r.Form.Get("code_challenge"),
		CodeChallengeMethod: r.Form.Get("code_challenge_method"),
		OwnerSubject:        "owner",
	})
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", err.Error())
		return
	}
	if err := h.persist(); err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "persist OAuth state")
		return
	}
	redirect, _ := url.Parse(r.Form.Get("redirect_uri"))
	query := redirect.Query()
	query.Set("code", grant.Code)
	if state := r.Form.Get("state"); state != "" {
		query.Set("state", state)
	}
	redirect.RawQuery = query.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
}

func (h *oauthHandler) validateAuthorize(ctx context.Context, values url.Values) error {
	if values.Get("response_type") != "code" {
		return &oauthRequestError{"response_type must be code"}
	}
	if values.Get("code_challenge_method") != "S256" || values.Get("code_challenge") == "" {
		return &oauthRequestError{"PKCE S256 is required"}
	}
	if resource := values.Get("resource"); resource != "" && resource != h.resource {
		return &oauthRequestError{"resource does not match this Executor"}
	}
	clientID := values.Get("client_id")
	if err := h.core.ValidateClient(oauth.ClientValidationRequest{ClientID: clientID}); err != nil {
		if err := h.resolveClientMetadata(ctx, clientID); err != nil {
			return err
		}
	}
	return h.core.ValidateClient(oauth.ClientValidationRequest{ClientID: clientID, RedirectURI: values.Get("redirect_uri")})
}

type clientMetadataDocument struct {
	ClientID                    string   `json:"client_id"`
	ClientName                  string   `json:"client_name"`
	RedirectURIs                []string `json:"redirect_uris"`
	Scope                       string   `json:"scope"`
	TokenEndpointAuthMethod     string   `json:"token_endpoint_auth_method"`
	TokenEndpointAuthMethods    []string `json:"token_endpoint_auth_methods_supported"`
	TokenEndpointAuthSigningAlg string   `json:"token_endpoint_auth_signing_alg"`
	JWKSURI                     string   `json:"jwks_uri"`
}

func (h *oauthHandler) resolveClientMetadata(ctx context.Context, clientID string) error {
	var document clientMetadataDocument
	if err := h.fetchTrustedChatGPTJSON(ctx, clientID, &document); err != nil {
		return fmt.Errorf("fetch client metadata: %w", err)
	}
	if document.ClientID != "" && document.ClientID != clientID {
		return fmt.Errorf("client metadata identifier mismatch")
	}
	_, err := h.core.RegisterClientIDURL(oauth.ClientIDURLRegistrationRequest{
		ClientIDURL:  clientID,
		ClientName:   document.ClientName,
		RedirectURIs: document.RedirectURIs,
		Scopes:       strings.Fields(document.Scope),
	})
	if err != nil {
		return err
	}
	return h.persist()
}

func trustedCIMDHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com")
}

func (h *oauthHandler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.recordOAuthEvent(OAuthEvent{Stage: "token", AuthMethod: "unknown", GrantType: "unknown", Outcome: "failed", Code: "invalid_request", Reason: "invalid form"})
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	event := OAuthEvent{
		Stage:      "token",
		AuthMethod: tokenAuthMethod(r.Form),
		GrantType:  tokenGrantType(r.Form.Get("grant_type")),
		Outcome:    "attempted",
	}
	h.recordOAuthEvent(event)
	clientID, err := h.authenticateTokenClient(r.Context(), r.Form)
	if err != nil {
		h.recordOAuthFailure(event, "invalid_client", err)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	if resource := r.Form.Get("resource"); resource != "" && resource != h.resource {
		h.recordOAuthFailure(event, "invalid_target", errors.New("resource mismatch"))
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource does not match this Executor")
		return
	}
	var (
		tokens   oauth.TokenSet
		tokenErr error
	)
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		tokens, tokenErr = h.core.ExchangeCode(oauth.TokenRequest{
			ClientID:     clientID,
			Code:         r.Form.Get("code"),
			RedirectURI:  r.Form.Get("redirect_uri"),
			CodeVerifier: r.Form.Get("code_verifier"),
		})
	case "refresh_token":
		tokens, tokenErr = h.core.Refresh(oauth.TokenRefreshRequest{
			ClientID:     clientID,
			RefreshToken: r.Form.Get("refresh_token"),
			Scopes:       strings.Fields(r.Form.Get("scope")),
		})
	default:
		h.recordOAuthFailure(event, "unsupported_grant_type", errors.New("unsupported grant type"))
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant type is not supported")
		return
	}
	if tokenErr != nil {
		h.recordOAuthFailure(event, "invalid_grant", tokenErr)
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", tokenErr.Error())
		return
	}
	if err := h.persist(); err != nil {
		h.recordOAuthFailure(event, "server_error", errors.New("persist OAuth state"))
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "persist OAuth state")
		return
	}
	event.Outcome = "succeeded"
	event.Code = "ok"
	h.recordOAuthEvent(event)
	writeJSON(w, http.StatusOK, tokens)
}

func (h *oauthHandler) recordOAuthFailure(event OAuthEvent, code string, err error) {
	event.Outcome = "failed"
	event.Code = code
	if err != nil {
		event.Reason = err.Error()
	}
	h.recordOAuthEvent(event)
}

func (h *oauthHandler) recordOAuthEvent(event OAuthEvent) {
	if h.recordOAuth != nil {
		h.recordOAuth(event)
	}
}

func tokenAuthMethod(values url.Values) string {
	if values.Get("client_assertion") != "" {
		return "private_key_jwt"
	}
	return "none"
}

func tokenGrantType(value string) string {
	switch value {
	case "authorization_code", "refresh_token":
		return value
	case "":
		return "missing"
	default:
		return "other"
	}
}

func (h *oauthHandler) persist() error {
	if h.statePath == "" {
		return nil
	}
	return h.core.SaveState(h.statePath)
}

type oauthRequestError struct{ message string }

func (e *oauthRequestError) Error() string { return e.message }

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

const authorizeScript = `(()=>{const form=document.getElementById("authorize-form"),submit=document.getElementById("authorize-submit");const activate=event=>{if(event.pointerType!=="touch"&&event.button!==0)return;event.preventDefault();if(!form.reportValidity())return;if(typeof form.requestSubmit==="function")form.requestSubmit(submit);else form.submit()};if(window.PointerEvent)submit.addEventListener("pointerup",activate);else submit.addEventListener("touchend",activate,{passive:false})})();`

var authorizeScriptHash = func() string {
	sum := sha256.Sum256([]byte(authorizeScript))
	return base64.StdEncoding.EncodeToString(sum[:])
}()

var authorizeTemplate = template.Must(template.New("authorize").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize Executor</title><style>body{margin:0;background:#11120f;color:#e8e2d5;font-family:ui-monospace,monospace;display:grid;place-items:center;min-height:100vh}.card{width:min(560px,calc(100% - 40px));border:1px solid #d7ff45;padding:32px}.eyebrow{color:#d7ff45;text-transform:uppercase;letter-spacing:.16em;font-size:12px}h1{font:900 52px/1 sans-serif;margin:14px 0}p{color:#b3b0a7;line-height:1.6}code,strong{color:#fff;overflow-wrap:anywhere}input{width:100%;box-sizing:border-box;padding:14px;background:#1d1e19;border:1px solid #55564e;color:#fff;font:inherit}.authorize-submit{margin-top:14px;width:100%;padding:15px;border:0;background:#d7ff45;color:#11120f;font:900 13px ui-monospace,monospace;text-transform:uppercase;cursor:pointer;touch-action:manipulation;-webkit-appearance:none}.warning{border-left:3px solid #ff4d2e;padding-left:14px}</style></head><body><form id="authorize-form" class="card" method="post" action="/oauth/authorize"><div class="eyebrow">Sovereign machine control</div><h1>Executor</h1><p class="warning">This grants unrestricted terminal, filesystem, administrator, and active-desktop control of this machine. Approve only if you initiated this connection.</p><p>Requesting client: <strong>{{.ClientName}}</strong><br>Redirect destination: <code>{{.RedirectURI}}</code></p><p>Enter the recovery key shown locally during setup to approve this connection.</p><input type="password" name="recovery_key" autocomplete="off" required autofocus><input type="hidden" name="response_type" value="code"><input type="hidden" name="client_id" value="{{.ClientID}}"><input type="hidden" name="redirect_uri" value="{{.RedirectURI}}"><input type="hidden" name="scope" value="{{.Scope}}"><input type="hidden" name="state" value="{{.State}}"><input type="hidden" name="code_challenge" value="{{.CodeChallenge}}"><input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}"><input type="hidden" name="resource" value="{{.Resource}}"><input id="authorize-submit" class="authorize-submit" type="submit" value="Authorize full control"></form><script>{{.Script}}</script></body></html>`))
