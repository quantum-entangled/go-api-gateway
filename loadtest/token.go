// Package loadtest provides shared helpers for load testing the gateway.
package loadtest

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// LoadPrivateKey reads an RSA private key from a PEM file (PKCS#8 format).
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not RSA")
	}
	return rsaKey, nil
}

// SignToken creates a signed JWT with the given subject and roles, valid for 1 hour.
func SignToken(key *rsa.PrivateKey, subject string, roles []string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":   subject,
		"roles": roles,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	return token.SignedString(key)
}
