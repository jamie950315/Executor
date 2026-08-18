package oauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Issuer               string
	Resource             string
	Audience             string
	AuthorizationPath    string
	TokenPath            string
	RegistrationPath     string
	AccessTokenTTL       time.Duration
	AuthorizationCodeTTL time.Duration
	RefreshTokenTTL      time.Duration
	SigningKey           []byte
	Now                  func() time.Time
}

type Core struct {
	issuer               string
	resource             string
	audience             string
	authorizationURL     string
	tokenURL             string
	registrationURL      string
	accessTokenTTL       time.Duration
	authorizationCodeTTL time.Duration
	refreshTokenTTL      time.Duration
	signingKey           []byte

	now func() time.Time

	mu            sync.Mutex
	clients       map[string]ClientRegistration
	codes         map[string]authorizationCodeRecord
	refreshTokens map[string]refreshTokenRecord
	generation    uint64
}

type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethods          []string `json:"token_endpoint_auth_methods_supported"`
	TokenEndpointAuthSigningAlgs      []string `json:"token_endpoint_auth_signing_alg_values_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
}

type ProtectedResourceMetadata struct {
	Resource                 string   `json:"resource"`
	AuthorizationServers     []string `json:"authorization_servers"`
	BearerMethodsSupported   []string `json:"bearer_methods_supported"`
	ScopesSupported          []string `json:"scopes_supported"`
	ResourceDocumentationURL string   `json:"resource_documentation,omitempty"`
}

type DynamicClientRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	Scope                   string   `json:"scope,omitempty"`
	Scopes                  []string `json:"scopes,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
}

type ClientIDURLRegistrationRequest struct {
	ClientIDURL  string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes,omitempty"`
}

type ClientValidationRequest struct {
	ClientID    string
	RedirectURI string
}

type ClientRegistration struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	Scope                   string   `json:"scope,omitempty"`
	Scopes                  []string `json:"scopes,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

type AuthorizeRequest struct {
	ClientID            string
	RedirectURI         string
	Scopes              []string
	CodeChallenge       string
	CodeChallengeMethod string
	OwnerSubject        string
}

type AuthorizationCodeGrant struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

type TokenRequest struct {
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
}

type TokenRefreshRequest struct {
	ClientID     string
	RefreshToken string
	Scopes       []string
}

type TokenSet struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

type VerifyOptions struct {
	Audience string
	Scope    string
}

type AccessTokenClaims struct {
	Issuer     string
	Subject    string
	Audience   string
	ClientID   string
	Scopes     []string
	IssuedAt   int64
	ExpiresAt  int64
	Generation uint64
	TokenID    string
}

type authorizationCodeRecord struct {
	ClientID        string
	RedirectURI     string
	Scopes          []string
	CodeChallenge   string
	ChallengeMethod string
	Subject         string
	ExpiresAt       time.Time
	Generation      uint64
}

type refreshTokenRecord struct {
	ClientID   string
	Scopes     []string
	Subject    string
	ExpiresAt  time.Time
	Generation uint64
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

type jwtClaims struct {
	Issuer     string `json:"iss"`
	Subject    string `json:"sub"`
	Audience   string `json:"aud"`
	ClientID   string `json:"client_id"`
	Scope      string `json:"scope"`
	IssuedAt   int64  `json:"iat"`
	ExpiresAt  int64  `json:"exp"`
	Generation uint64 `json:"gen"`
	TokenID    string `json:"jti"`
}

type persistenceState struct {
	Generation    uint64                            `json:"generation"`
	Clients       map[string]ClientRegistration     `json:"clients"`
	Codes         map[string]persistedCodeRecord    `json:"codes"`
	RefreshTokens map[string]persistedRefreshRecord `json:"refresh_tokens"`
}

type persistedCodeRecord struct {
	ClientID        string    `json:"client_id"`
	RedirectURI     string    `json:"redirect_uri"`
	Scopes          []string  `json:"scopes"`
	CodeChallenge   string    `json:"code_challenge"`
	ChallengeMethod string    `json:"challenge_method"`
	Subject         string    `json:"subject"`
	ExpiresAt       time.Time `json:"expires_at"`
	Generation      uint64    `json:"generation"`
}

type persistedRefreshRecord struct {
	ClientID   string    `json:"client_id"`
	Scopes     []string  `json:"scopes"`
	Subject    string    `json:"subject"`
	ExpiresAt  time.Time `json:"expires_at"`
	Generation uint64    `json:"generation"`
}

func NewCore(config Config) (*Core, error) {
	if config.Issuer == "" {
		return nil, errors.New("issuer is required")
	}
	if config.Resource == "" {
		return nil, errors.New("resource is required")
	}
	if config.Audience == "" {
		return nil, errors.New("audience is required")
	}
	if len(config.SigningKey) < 32 {
		return nil, errors.New("signing key must be at least 32 bytes")
	}
	if config.AccessTokenTTL <= 0 || config.AuthorizationCodeTTL <= 0 || config.RefreshTokenTTL <= 0 {
		return nil, errors.New("token TTLs must be positive")
	}

	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &Core{
		issuer:               strings.TrimRight(config.Issuer, "/"),
		resource:             config.Resource,
		audience:             config.Audience,
		authorizationURL:     joinURL(config.Issuer, config.AuthorizationPath),
		tokenURL:             joinURL(config.Issuer, config.TokenPath),
		registrationURL:      joinURL(config.Issuer, config.RegistrationPath),
		accessTokenTTL:       config.AccessTokenTTL,
		authorizationCodeTTL: config.AuthorizationCodeTTL,
		refreshTokenTTL:      config.RefreshTokenTTL,
		signingKey:           append([]byte(nil), config.SigningKey...),
		now:                  now,
		clients:              make(map[string]ClientRegistration),
		codes:                make(map[string]authorizationCodeRecord),
		refreshTokens:        make(map[string]refreshTokenRecord),
	}, nil
}

func (c *Core) AuthorizationServerMetadata() AuthorizationServerMetadata {
	return AuthorizationServerMetadata{
		Issuer:                            c.issuer,
		AuthorizationEndpoint:             c.authorizationURL,
		TokenEndpoint:                     c.tokenURL,
		RegistrationEndpoint:              c.registrationURL,
		ClientIDMetadataDocumentSupported: false,
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethods:          []string{"none", "private_key_jwt"},
		TokenEndpointAuthSigningAlgs:      []string{"RS256"},
		ScopesSupported:                   []string{"executor.full"},
	}
}

func (c *Core) ProtectedResourceMetadata() ProtectedResourceMetadata {
	return ProtectedResourceMetadata{
		Resource:               c.resource,
		AuthorizationServers:   []string{c.issuer},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        []string{"executor.full"},
	}
}

func (c *Core) RegisterClient(request DynamicClientRegistrationRequest) (ClientRegistration, error) {
	if err := validateRedirectURIs(request.RedirectURIs); err != nil {
		return ClientRegistration{}, err
	}
	if request.TokenEndpointAuthMethod != "" && request.TokenEndpointAuthMethod != "none" {
		return ClientRegistration{}, errors.New("only token_endpoint_auth_method none is supported")
	}
	if !supportedValues(request.GrantTypes, "authorization_code", "refresh_token") {
		return ClientRegistration{}, errors.New("unsupported grant_types")
	}
	if !supportedValues(request.ResponseTypes, "code") {
		return ClientRegistration{}, errors.New("unsupported response_types")
	}
	scopes := append([]string(nil), request.Scopes...)
	scopes = append(scopes, strings.Fields(request.Scope)...)
	scopes = dedupeScopes(scopes)

	client := ClientRegistration{
		ClientID:                randomID("client"),
		ClientName:              fallback(request.ClientName, "Executor Client"),
		RedirectURIs:            append([]string(nil), request.RedirectURIs...),
		Scope:                   strings.Join(scopes, " "),
		Scopes:                  scopes,
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
	}

	c.mu.Lock()
	c.clients[client.ClientID] = client
	c.mu.Unlock()

	return client, nil
}

func supportedValues(values []string, supported ...string) bool {
	for _, value := range values {
		if !slices.Contains(supported, value) {
			return false
		}
	}
	return true
}

func (c *Core) RegisterClientIDURL(request ClientIDURLRegistrationRequest) (ClientRegistration, error) {
	if err := validateClientIDURL(request.ClientIDURL); err != nil {
		return ClientRegistration{}, err
	}
	if err := validateRedirectURIs(request.RedirectURIs); err != nil {
		return ClientRegistration{}, err
	}

	scopes := dedupeScopes(request.Scopes)
	client := ClientRegistration{
		ClientID:                request.ClientIDURL,
		ClientName:              fallback(request.ClientName, "Executor Client"),
		RedirectURIs:            append([]string(nil), request.RedirectURIs...),
		Scope:                   strings.Join(scopes, " "),
		Scopes:                  scopes,
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
	}

	c.mu.Lock()
	c.clients[client.ClientID] = client
	c.mu.Unlock()

	return client, nil
}

func (c *Core) ValidateClient(request ClientValidationRequest) error {
	client, ok := c.lookupClient(request.ClientID)
	if !ok {
		return errors.New("unknown client")
	}
	if request.RedirectURI == "" {
		return nil
	}
	if err := validateRedirectURI(request.RedirectURI); err != nil {
		return err
	}
	if !slices.Contains(client.RedirectURIs, request.RedirectURI) {
		return errors.New("redirect URI mismatch")
	}
	return nil
}

func (c *Core) Client(clientID string) (ClientRegistration, bool) {
	return c.lookupClient(clientID)
}

func (c *Core) Authorize(request AuthorizeRequest) (AuthorizationCodeGrant, error) {
	client, ok := c.lookupClient(request.ClientID)
	if !ok {
		return AuthorizationCodeGrant{}, errors.New("unknown client")
	}
	if !slices.Contains(client.RedirectURIs, request.RedirectURI) {
		return AuthorizationCodeGrant{}, errors.New("redirect URI mismatch")
	}
	if request.CodeChallengeMethod != "S256" || request.CodeChallenge == "" {
		return AuthorizationCodeGrant{}, errors.New("PKCE S256 is required")
	}
	if request.OwnerSubject == "" {
		return AuthorizationCodeGrant{}, errors.New("owner subject is required")
	}

	scopes := request.Scopes
	if len(scopes) == 0 {
		scopes = client.Scopes
	}
	if len(client.Scopes) > 0 && !scopesSubset(scopes, client.Scopes) {
		return AuthorizationCodeGrant{}, errors.New("requested scope exceeds client scope")
	}

	now := c.now()
	grant := AuthorizationCodeGrant{
		Code:      randomID("code"),
		ExpiresAt: now.Add(c.authorizationCodeTTL).Unix(),
	}

	c.mu.Lock()
	c.codes[grant.Code] = authorizationCodeRecord{
		ClientID:        client.ClientID,
		RedirectURI:     request.RedirectURI,
		Scopes:          dedupeScopes(scopes),
		CodeChallenge:   request.CodeChallenge,
		ChallengeMethod: request.CodeChallengeMethod,
		Subject:         request.OwnerSubject,
		ExpiresAt:       now.Add(c.authorizationCodeTTL),
		Generation:      c.generation,
	}
	c.mu.Unlock()

	return grant, nil
}

func (c *Core) ExchangeCode(request TokenRequest) (TokenSet, error) {
	c.mu.Lock()
	record, ok := c.codes[request.Code]
	if !ok {
		c.mu.Unlock()
		return TokenSet{}, errors.New("unknown authorization code")
	}
	if record.ExpiresAt.Before(c.now()) {
		delete(c.codes, request.Code)
		c.mu.Unlock()
		return TokenSet{}, errors.New("authorization code expired")
	}
	if record.Generation != c.generation {
		delete(c.codes, request.Code)
		c.mu.Unlock()
		return TokenSet{}, errors.New("authorization code revoked")
	}
	if record.ClientID != request.ClientID || record.RedirectURI != request.RedirectURI {
		c.mu.Unlock()
		return TokenSet{}, errors.New("authorization code client mismatch")
	}
	if s256Challenge(request.CodeVerifier) != record.CodeChallenge {
		c.mu.Unlock()
		return TokenSet{}, errors.New("PKCE verification failed")
	}
	delete(c.codes, request.Code)
	c.mu.Unlock()

	return c.issueTokens(record.ClientID, record.Subject, record.Scopes)
}

func (c *Core) Refresh(request TokenRefreshRequest) (TokenSet, error) {
	hash := hashRefreshToken(request.RefreshToken)

	c.mu.Lock()
	record, ok := c.refreshTokens[hash]
	if !ok {
		c.mu.Unlock()
		return TokenSet{}, errors.New("unknown refresh token")
	}
	if record.ClientID != request.ClientID {
		c.mu.Unlock()
		return TokenSet{}, errors.New("refresh token client mismatch")
	}
	if record.ExpiresAt.Before(c.now()) {
		delete(c.refreshTokens, hash)
		c.mu.Unlock()
		return TokenSet{}, errors.New("refresh token expired")
	}
	if record.Generation != c.generation {
		delete(c.refreshTokens, hash)
		c.mu.Unlock()
		return TokenSet{}, errors.New("refresh token revoked")
	}

	scopes := record.Scopes
	if len(request.Scopes) > 0 {
		if !scopesSubset(request.Scopes, record.Scopes) {
			c.mu.Unlock()
			return TokenSet{}, errors.New("requested scope exceeds refresh token scope")
		}
		scopes = dedupeScopes(request.Scopes)
	}
	delete(c.refreshTokens, hash)
	c.mu.Unlock()

	return c.issueTokens(record.ClientID, record.Subject, scopes)
}

func (c *Core) VerifyAccessToken(token string, options VerifyOptions) (AccessTokenClaims, error) {
	claims, err := c.parseAndVerifyJWT(token)
	if err != nil {
		return AccessTokenClaims{}, err
	}
	if claims.Issuer != c.issuer {
		return AccessTokenClaims{}, errors.New("issuer mismatch")
	}

	audience := fallback(options.Audience, c.audience)
	if claims.Audience != audience {
		return AccessTokenClaims{}, errors.New("audience mismatch")
	}

	now := c.now().Unix()
	if claims.ExpiresAt <= now {
		return AccessTokenClaims{}, errors.New("access token expired")
	}

	c.mu.Lock()
	currentGeneration := c.generation
	c.mu.Unlock()
	if claims.Generation != currentGeneration {
		return AccessTokenClaims{}, errors.New("access token revoked")
	}

	scopes := strings.Fields(claims.Scope)
	if options.Scope != "" && !slices.Contains(scopes, options.Scope) {
		return AccessTokenClaims{}, errors.New("scope missing")
	}

	return AccessTokenClaims{
		Issuer:     claims.Issuer,
		Subject:    claims.Subject,
		Audience:   claims.Audience,
		ClientID:   claims.ClientID,
		Scopes:     scopes,
		IssuedAt:   claims.IssuedAt,
		ExpiresAt:  claims.ExpiresAt,
		Generation: claims.Generation,
		TokenID:    claims.TokenID,
	}, nil
}

func (c *Core) RevokeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.generation++
	c.codes = make(map[string]authorizationCodeRecord)
	c.refreshTokens = make(map[string]refreshTokenRecord)
}

func (c *Core) SaveState(path string) error {
	if path == "" {
		return errors.New("state path is required")
	}

	state := c.snapshotState()
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}

	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if existing != nil {
		if err := preserveFileOwnership(file, existing); err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}

	writeErr := func() error {
		if _, err := file.Write(payload); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		return file.Close()
	}()
	if writeErr != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return writeErr
	}

	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (c *Core) LoadState(path string) error {
	if path == "" {
		return errors.New("state path is required")
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var state persistenceState
	if err := json.Unmarshal(payload, &state); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.generation = state.Generation
	c.clients = make(map[string]ClientRegistration, len(state.Clients))
	for key, value := range state.Clients {
		c.clients[key] = value
	}
	c.codes = make(map[string]authorizationCodeRecord, len(state.Codes))
	for key, value := range state.Codes {
		c.codes[key] = authorizationCodeRecord{
			ClientID:        value.ClientID,
			RedirectURI:     value.RedirectURI,
			Scopes:          append([]string(nil), value.Scopes...),
			CodeChallenge:   value.CodeChallenge,
			ChallengeMethod: value.ChallengeMethod,
			Subject:         value.Subject,
			ExpiresAt:       value.ExpiresAt,
			Generation:      value.Generation,
		}
	}
	c.refreshTokens = make(map[string]refreshTokenRecord, len(state.RefreshTokens))
	for key, value := range state.RefreshTokens {
		c.refreshTokens[key] = refreshTokenRecord{
			ClientID:   value.ClientID,
			Scopes:     append([]string(nil), value.Scopes...),
			Subject:    value.Subject,
			ExpiresAt:  value.ExpiresAt,
			Generation: value.Generation,
		}
	}
	return nil
}

func (c *Core) issueTokens(clientID string, subject string, scopes []string) (TokenSet, error) {
	now := c.now()
	accessToken, err := c.signJWT(jwtClaims{
		Issuer:     c.issuer,
		Subject:    subject,
		Audience:   c.audience,
		ClientID:   clientID,
		Scope:      strings.Join(dedupeScopes(scopes), " "),
		IssuedAt:   now.Unix(),
		ExpiresAt:  now.Add(c.accessTokenTTL).Unix(),
		Generation: c.currentGeneration(),
		TokenID:    randomID("atk"),
	})
	if err != nil {
		return TokenSet{}, err
	}

	refreshToken := randomID("rtk")
	refreshRecord := refreshTokenRecord{
		ClientID:   clientID,
		Scopes:     dedupeScopes(scopes),
		Subject:    subject,
		ExpiresAt:  now.Add(c.refreshTokenTTL),
		Generation: c.currentGeneration(),
	}

	c.mu.Lock()
	c.refreshTokens[hashRefreshToken(refreshToken)] = refreshRecord
	c.mu.Unlock()

	return TokenSet{
		TokenType:    "Bearer",
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Scope:        strings.Join(refreshRecord.Scopes, " "),
		ExpiresIn:    int64(c.accessTokenTTL / time.Second),
	}, nil
}

func (c *Core) signJWT(claims jwtClaims) (string, error) {
	headerPayload, err := marshalJWTPart(jwtHeader{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	claimsPayload, err := marshalJWTPart(claims)
	if err != nil {
		return "", err
	}

	message := headerPayload + "." + claimsPayload
	mac := hmac.New(sha256.New, c.signingKey)
	if _, err := mac.Write([]byte(message)); err != nil {
		return "", err
	}

	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return message + "." + signature, nil
}

func (c *Core) parseAndVerifyJWT(token string) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, errors.New("invalid token format")
	}

	mac := hmac.New(sha256.New, c.signingKey)
	if _, err := mac.Write([]byte(parts[0] + "." + parts[1])); err != nil {
		return jwtClaims{}, err
	}
	expectedSignature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expectedSignature), []byte(parts[2])) {
		return jwtClaims{}, errors.New("invalid token signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, err
	}

	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return jwtClaims{}, err
	}
	return claims, nil
}

func (c *Core) lookupClient(clientID string) (ClientRegistration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	client, ok := c.clients[clientID]
	return client, ok
}

func (c *Core) currentGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

func hashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func joinURL(base string, path string) string {
	if path == "" {
		return strings.TrimRight(base, "/")
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func marshalJWTPart(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func dedupeScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func scopesSubset(requested []string, allowed []string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, scope := range allowed {
		allowedSet[scope] = struct{}{}
	}
	for _, scope := range requested {
		if _, ok := allowedSet[scope]; !ok {
			return false
		}
	}
	return true
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomID(prefix string) string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	if prefix == "" {
		return hex.EncodeToString(data[:])
	}
	return prefix + "_" + hex.EncodeToString(data[:])
}

func fallback(value string, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}

func validateRedirectURIs(redirectURIs []string) error {
	if len(redirectURIs) == 0 {
		return errors.New("redirect URIs are required")
	}
	for _, redirectURI := range redirectURIs {
		if err := validateRedirectURI(redirectURI); err != nil {
			return err
		}
	}
	return nil
}

func validateRedirectURI(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid redirect URI: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return errors.New("redirect URI must be absolute")
	}
	if parsed.Fragment != "" {
		return errors.New("redirect URI fragment is not allowed")
	}
	if parsed.User != nil {
		return errors.New("redirect URI userinfo is not allowed")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return errors.New("redirect URI must use https unless it is a loopback callback")
	default:
		return errors.New("redirect URI scheme is not allowed")
	}
}

func validateClientIDURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid client_id URL: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return errors.New("client_id URL must be absolute")
	}
	if parsed.Scheme != "https" {
		return errors.New("client_id URL must use https")
	}
	if parsed.Fragment != "" {
		return errors.New("client_id URL fragment is not allowed")
	}
	if parsed.User != nil {
		return errors.New("client_id URL userinfo is not allowed")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func (c *Core) snapshotState() persistenceState {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := persistenceState{
		Generation:    c.generation,
		Clients:       make(map[string]ClientRegistration, len(c.clients)),
		Codes:         make(map[string]persistedCodeRecord, len(c.codes)),
		RefreshTokens: make(map[string]persistedRefreshRecord, len(c.refreshTokens)),
	}
	for key, value := range c.clients {
		value.RedirectURIs = append([]string(nil), value.RedirectURIs...)
		value.Scopes = append([]string(nil), value.Scopes...)
		value.GrantTypes = append([]string(nil), value.GrantTypes...)
		value.ResponseTypes = append([]string(nil), value.ResponseTypes...)
		state.Clients[key] = value
	}
	for key, value := range c.codes {
		state.Codes[key] = persistedCodeRecord{
			ClientID:        value.ClientID,
			RedirectURI:     value.RedirectURI,
			Scopes:          append([]string(nil), value.Scopes...),
			CodeChallenge:   value.CodeChallenge,
			ChallengeMethod: value.ChallengeMethod,
			Subject:         value.Subject,
			ExpiresAt:       value.ExpiresAt,
			Generation:      value.Generation,
		}
	}
	for key, value := range c.refreshTokens {
		state.RefreshTokens[key] = persistedRefreshRecord{
			ClientID:   value.ClientID,
			Scopes:     append([]string(nil), value.Scopes...),
			Subject:    value.Subject,
			ExpiresAt:  value.ExpiresAt,
			Generation: value.Generation,
		}
	}
	return state
}
