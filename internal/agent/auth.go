package agent

import (
	"context"
	"net/http"
	"strings"

	"github.com/jamie950315/executor/internal/oauth"
)

type TokenVerifier interface {
	VerifyAccessToken(string, oauth.VerifyOptions) (oauth.AccessTokenClaims, error)
}

type URLSecretVerifier func(candidate string) bool

type Actor struct {
	Subject  string
	ClientID string
	Method   string
}

type actorContextKey struct{}

func ActorFromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	return actor, ok
}

func ProtectMCP(verifier TokenVerifier, resource string, urlSecret URLSecretVerifier, next http.Handler) http.Handler {
	resource = strings.TrimRight(resource, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			token := bearerToken(r.Header.Get("Authorization"))
			if token != "" && verifier != nil {
				claims, err := verifier.VerifyAccessToken(token, oauth.VerifyOptions{Audience: resource, Scope: "executor.full"})
				if err == nil {
					ctx := context.WithValue(r.Context(), actorContextKey{}, Actor{Subject: claims.Subject, ClientID: claims.ClientID, Method: "oauth"})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resource+`/.well-known/oauth-protected-resource", scope="executor.full"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 2 && parts[1] == "mcp" && urlSecret != nil && urlSecret(parts[0]) {
			clone := r.Clone(context.WithValue(r.Context(), actorContextKey{}, Actor{Subject: "owner", ClientID: "url-secret", Method: "url-secret"}))
			clone.URL.Path = "/mcp"
			next.ServeHTTP(w, clone)
			return
		}
		http.NotFound(w, r)
	})
}

func bearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
