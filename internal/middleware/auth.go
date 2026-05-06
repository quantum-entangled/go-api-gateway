package middleware

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type claimsKeyType struct{}

// Claims holds the authenticated user's identity extracted from a JWT.
type Claims struct {
	Subject string
	Roles   []string
}

var claimsKey claimsKeyType

func newClaimsContext(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// ClaimsFromContext extracts the authenticated claims from the context.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(*Claims)
	return claims, ok
}

// JWTAuth validates the Bearer token in the Authorization header using RS256.
// On success, it stores the extracted Claims in the request context.
func JWTAuth(publicKey *rsa.PublicKey) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			tokenStr, ok := strings.CutPrefix(authHeader, "Bearer ")
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
				return
			}

			token, err := parseAndValidateToken(tokenStr, publicKey)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
				return
			}

			claims, err := extractClaims(token)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
				return
			}

			ctx := newClaimsContext(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// parseAndValidateToken parses the JWT string and verifies its RS256 signature.
func parseAndValidateToken(tokenStr string, publicKey *rsa.PublicKey) (*jwt.Token, error) {
	token, err := jwt.ParseWithClaims(tokenStr, jwt.MapClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected method: %v", t.Header["alg"])
		}
		return publicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	return token, err
}

// extractClaims reads sub and roles from the validated token's MapClaims.
func extractClaims(token *jwt.Token) (*Claims, error) {
	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		sub, subOk := claims["sub"].(string)
		if !subOk {
			return nil, fmt.Errorf("missing sub claim")
		}

		rawRoles, rolesOk := claims["roles"].([]any)
		if !rolesOk {
			return nil, fmt.Errorf("missing roles claim")
		}

		roles := make([]string, 0, len(rawRoles))
		for _, rawRole := range rawRoles {
			role, ok := rawRole.(string)
			if !ok {
				return nil, fmt.Errorf("roles claim is corrupted")
			}
			roles = append(roles, role)
		}

		return &Claims{Subject: sub, Roles: roles}, nil
	}
	return nil, fmt.Errorf("cannot extract claims from token")
}

// RequireAnyRole admits the request if the authenticated user holds at
// least one of the given roles. Must be used after JWTAuth.
func RequireAnyRole(roles []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
				return
			}
			for _, want := range roles {
				if slices.Contains(claims.Roles, want) {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		})
	}
}

// writeJSON writes a JSON error response with the given status code.
func writeJSON(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
