package agent

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/oauth"
)

const privateKeyJWTAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

type clientAssertionHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

type clientAssertionClaims struct {
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	Audience  json.RawMessage `json:"aud"`
	IssuedAt  int64           `json:"iat"`
	ExpiresAt int64           `json:"exp"`
	TokenID   string          `json:"jti"`
}

type JSONWebKeySet struct {
	Keys []JSONWebKey `json:"keys"`
}

type JSONWebKey struct {
	KeyType   string `json:"kty"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

func (h *oauthHandler) authenticateTokenClient(ctx context.Context, values url.Values) (string, error) {
	clientID := values.Get("client_id")
	assertion := values.Get("client_assertion")
	if assertion == "" {
		if clientID == "" {
			return "", errors.New("client_id is required")
		}
		if err := h.core.ValidateClient(oauth.ClientValidationRequest{ClientID: clientID}); err != nil {
			return "", err
		}
		return clientID, nil
	}
	if values.Get("client_assertion_type") != privateKeyJWTAssertionType {
		return "", errors.New("unsupported client_assertion_type")
	}
	assertionClientID, err := h.verifyChatGPTClientAssertion(ctx, assertion)
	if err != nil {
		return "", err
	}
	if clientID != "" && clientID != assertionClientID {
		return "", errors.New("client assertion identity mismatch")
	}
	if err := h.core.ValidateClient(oauth.ClientValidationRequest{ClientID: assertionClientID}); err != nil {
		return "", err
	}
	return assertionClientID, nil
}

func (h *oauthHandler) verifyChatGPTClientAssertion(ctx context.Context, assertion string) (string, error) {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid client assertion format")
	}
	var header clientAssertionHeader
	if err := decodeJWTPart(parts[0], &header); err != nil {
		return "", fmt.Errorf("decode client assertion header: %w", err)
	}
	if header.Algorithm != "RS256" || header.KeyID == "" {
		return "", errors.New("client assertion must use RS256 with kid")
	}
	var claims clientAssertionClaims
	if err := decodeJWTPart(parts[1], &claims); err != nil {
		return "", fmt.Errorf("decode client assertion claims: %w", err)
	}
	if claims.Issuer == "" || claims.Issuer != claims.Subject {
		return "", errors.New("client assertion issuer and subject must match")
	}
	if !trustedChatGPTURL(claims.Subject) {
		return "", errors.New("untrusted client assertion issuer")
	}

	var metadata clientMetadataDocument
	if err := h.fetchTrustedChatGPTJSON(ctx, claims.Subject, &metadata); err != nil {
		return "", fmt.Errorf("fetch assertion client metadata: %w", err)
	}
	if metadata.ClientID != "" && metadata.ClientID != claims.Subject {
		return "", errors.New("client assertion metadata identifier mismatch")
	}
	if metadata.TokenEndpointAuthMethod != "private_key_jwt" && !containsOAuthValue(metadata.TokenEndpointAuthMethods, "private_key_jwt") {
		return "", errors.New("client metadata does not permit private_key_jwt")
	}
	if metadata.TokenEndpointAuthSigningAlg != "" && metadata.TokenEndpointAuthSigningAlg != "RS256" {
		return "", errors.New("client metadata does not permit RS256")
	}
	if !sameTrustedChatGPTOrigin(claims.Subject, metadata.JWKSURI) {
		return "", errors.New("untrusted client JWKS URL")
	}
	var keySet JSONWebKeySet
	if err := h.fetchTrustedChatGPTJSON(ctx, metadata.JWKSURI, &keySet); err != nil {
		return "", fmt.Errorf("fetch client JWKS: %w", err)
	}
	publicKey, err := rsaKeyForAssertion(keySet, header.KeyID)
	if err != nil {
		return "", err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("invalid client assertion signature encoding")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		return "", errors.New("invalid client assertion signature")
	}
	if err := h.validateClientAssertionClaims(claims); err != nil {
		return "", err
	}
	return claims.Subject, nil
}

func (h *oauthHandler) validateClientAssertionClaims(claims clientAssertionClaims) error {
	if !assertionAudienceContains(claims.Audience, h.resource+"/oauth/token") {
		return errors.New("client assertion audience mismatch")
	}
	now := time.Now().Unix()
	if claims.ExpiresAt <= now || claims.ExpiresAt > now+300 {
		return errors.New("client assertion expiration is invalid")
	}
	if claims.IssuedAt == 0 || claims.IssuedAt > now+60 || claims.IssuedAt < now-300 {
		return errors.New("client assertion issued-at time is invalid")
	}
	if claims.TokenID == "" {
		return errors.New("client assertion jti is required")
	}
	return nil
}

func (h *oauthHandler) fetchTrustedChatGPTJSON(ctx context.Context, rawURL string, output any) error {
	if !trustedChatGPTURL(rawURL) {
		return errors.New("untrusted ChatGPT URL")
	}
	client := *h.cimdClient
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !trustedChatGPTURL(request.URL.String()) {
			return errors.New("untrusted ChatGPT redirect")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("remote metadata returned %s", response.Status)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(output); err != nil {
		return err
	}
	return nil
}

func trustedChatGPTURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && trustedCIMDHost(parsed.Hostname())
}

func sameTrustedChatGPTOrigin(clientID, jwksURI string) bool {
	clientURL, clientErr := url.Parse(clientID)
	jwksURL, jwksErr := url.Parse(jwksURI)
	return clientErr == nil && jwksErr == nil && trustedChatGPTURL(jwksURI) && strings.EqualFold(clientURL.Scheme, jwksURL.Scheme) && strings.EqualFold(clientURL.Host, jwksURL.Host)
}

func decodeJWTPart(encoded string, output any) error {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, output)
}

func rsaKeyForAssertion(keySet JSONWebKeySet, keyID string) (*rsa.PublicKey, error) {
	for _, key := range keySet.Keys {
		if key.KeyID != keyID || key.KeyType != "RSA" || key.Algorithm != "RS256" || key.Use != "sig" {
			continue
		}
		modulusBytes, err := base64.RawURLEncoding.DecodeString(key.Modulus)
		if err != nil {
			return nil, errors.New("invalid RSA modulus")
		}
		exponentBytes, err := base64.RawURLEncoding.DecodeString(key.Exponent)
		if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
			return nil, errors.New("invalid RSA exponent")
		}
		exponent := 0
		for _, value := range exponentBytes {
			exponent = exponent<<8 | int(value)
		}
		modulus := new(big.Int).SetBytes(modulusBytes)
		if modulus.BitLen() < 2048 || exponent < 3 || exponent%2 == 0 {
			return nil, errors.New("unsafe RSA assertion key")
		}
		return &rsa.PublicKey{N: modulus, E: exponent}, nil
	}
	return nil, errors.New("client assertion signing key not found")
}

func assertionAudienceContains(raw json.RawMessage, target string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == target
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil {
		return false
	}
	return containsOAuthValue(multiple, target)
}

func containsOAuthValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
