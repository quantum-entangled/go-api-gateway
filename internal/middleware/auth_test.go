package middleware_test

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"go-api-gateway/internal/middleware"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKey is an RSA key pair generated once for all auth tests.
var testKey *rsa.PrivateKey

func init() {
	var err error
	testKey, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("failed to generate test RSA key: " + err.Error())
	}
}

// signToken is a test helper that creates a signed JWT with the given claims.
func signToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	s, err := token.SignedString(testKey)
	require.NoError(t, err)
	return s
}

func TestJWTAuth_RejectsHS256(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "attacker",
		"roles": []string{"admin"},
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	// Sign with the public key bytes as HMAC secret (the attack vector)
	pubKeyBytes := testKey.PublicKey.N.Bytes()
	tokenStr, err := token.SignedString(pubKeyBytes)
	require.NoError(t, err)

	handler := middleware.JWTAuth(&testKey.PublicKey)(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestJWTAuth_RejectsExpired(t *testing.T) {
	tokenStr := signToken(t, jwt.MapClaims{
		"sub":   "user1",
		"roles": []string{"reader"},
		"exp":   time.Now().Add(-time.Hour).Unix(), // expired 1 hour ago
	})

	handler := middleware.JWTAuth(&testKey.PublicKey)(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestJWTAuth_ValidToken(t *testing.T) {
	tokenStr := signToken(t, jwt.MapClaims{
		"sub":   "user1",
		"roles": []string{"admin"},
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := middleware.ClaimsFromContext(r.Context())
		require.True(t, ok)
		assert.Equal(t, "user1", claims.Subject)
		assert.True(t, slices.Contains(claims.Roles, "admin"))
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware.JWTAuth(&testKey.PublicKey)(inner)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestJWTAuth_MissingHeader(t *testing.T) {
	handler := middleware.JWTAuth(&testKey.PublicKey)(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, body, "missing token")
}

func TestJWTAuth_MalformedHeader(t *testing.T) {
	handler := middleware.JWTAuth(&testKey.PublicKey)(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Token abc")
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, body, "missing token")
}

func TestRequireRole_Allowed(t *testing.T) {
	tokenStr := signToken(t, jwt.MapClaims{
		"sub":   "user1",
		"roles": []string{"admin"},
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	handler := middleware.JWTAuth(&testKey.PublicKey)(middleware.RequireRole("admin")(okHandler()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireRole_Forbidden(t *testing.T) {
	tokenStr := signToken(t, jwt.MapClaims{
		"sub":   "user1",
		"roles": []string{"reader"},
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	handler := middleware.JWTAuth(&testKey.PublicKey)(middleware.RequireRole("admin")(okHandler()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, body, "forbidden")
}

// okHandler returns a simple handler that writes 200 OK.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}
