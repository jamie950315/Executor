package agent

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/jamie950315/executor/internal/oauth"
)

type RecoveryVerifier func(string) bool

type oauthHandler struct {
	core           *oauth.Core
	resource       string
	verifyRecovery RecoveryVerifier
}

func NewOAuthHandler(core *oauth.Core, resource string, verifyRecovery RecoveryVerifier) http.Handler {
	return &oauthHandler{core: core, resource: strings.TrimRight(resource, "/"), verifyRecovery: verifyRecovery}
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
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	client, err := h.core.RegisterClient(request)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, client)
}

func (h *oauthHandler) authorizePage(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	if err := h.validateAuthorize(values); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	_ = authorizeTemplate.Execute(w, map[string]string{
		"ClientID":            values.Get("client_id"),
		"RedirectURI":         values.Get("redirect_uri"),
		"Scope":               values.Get("scope"),
		"State":               values.Get("state"),
		"CodeChallenge":       values.Get("code_challenge"),
		"CodeChallengeMethod": values.Get("code_challenge_method"),
	})
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
	if err := h.validateAuthorize(r.Form); err != nil {
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
	redirect, _ := url.Parse(r.Form.Get("redirect_uri"))
	query := redirect.Query()
	query.Set("code", grant.Code)
	if state := r.Form.Get("state"); state != "" {
		query.Set("state", state)
	}
	redirect.RawQuery = query.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
}

func (h *oauthHandler) validateAuthorize(values url.Values) error {
	if values.Get("response_type") != "code" {
		return &oauthRequestError{"response_type must be code"}
	}
	if values.Get("code_challenge_method") != "S256" || values.Get("code_challenge") == "" {
		return &oauthRequestError{"PKCE S256 is required"}
	}
	return h.core.ValidateClient(oauth.ClientValidationRequest{ClientID: values.Get("client_id"), RedirectURI: values.Get("redirect_uri")})
}

func (h *oauthHandler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var (
		tokens oauth.TokenSet
		err    error
	)
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		tokens, err = h.core.ExchangeCode(oauth.TokenRequest{
			ClientID:     r.Form.Get("client_id"),
			Code:         r.Form.Get("code"),
			RedirectURI:  r.Form.Get("redirect_uri"),
			CodeVerifier: r.Form.Get("code_verifier"),
		})
	case "refresh_token":
		tokens, err = h.core.Refresh(oauth.TokenRefreshRequest{
			ClientID:     r.Form.Get("client_id"),
			RefreshToken: r.Form.Get("refresh_token"),
			Scopes:       strings.Fields(r.Form.Get("scope")),
		})
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant type is not supported")
		return
	}
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tokens)
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

var authorizeTemplate = template.Must(template.New("authorize").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize Executor</title><style>body{margin:0;background:#11120f;color:#e8e2d5;font-family:ui-monospace,monospace;display:grid;place-items:center;min-height:100vh}.card{width:min(560px,calc(100% - 40px));border:1px solid #d7ff45;padding:32px}.eyebrow{color:#d7ff45;text-transform:uppercase;letter-spacing:.16em;font-size:12px}h1{font:900 52px/1 sans-serif;margin:14px 0}p{color:#b3b0a7;line-height:1.6}input{width:100%;box-sizing:border-box;padding:14px;background:#1d1e19;border:1px solid #55564e;color:#fff;font:inherit}button{margin-top:14px;width:100%;padding:15px;border:0;background:#d7ff45;color:#11120f;font:900 13px ui-monospace,monospace;text-transform:uppercase;cursor:pointer}.warning{border-left:3px solid #ff4d2e;padding-left:14px}</style></head><body><form class="card" method="post" action="/oauth/authorize"><div class="eyebrow">Sovereign machine control</div><h1>Executor</h1><p class="warning">This grants ChatGPT unrestricted terminal, filesystem, administrator, and active-desktop control of this machine.</p><p>Enter the recovery key shown locally during setup to approve this connection.</p><input type="password" name="recovery_key" autocomplete="off" required autofocus><input type="hidden" name="response_type" value="code"><input type="hidden" name="client_id" value="{{.ClientID}}"><input type="hidden" name="redirect_uri" value="{{.RedirectURI}}"><input type="hidden" name="scope" value="{{.Scope}}"><input type="hidden" name="state" value="{{.State}}"><input type="hidden" name="code_challenge" value="{{.CodeChallenge}}"><input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}"><button type="submit">Authorize full control</button></form></body></html>`))
