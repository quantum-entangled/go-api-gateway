// gentoken prints a signed JWT to stdout for use in load tests.
// Usage: go run ./cmd/gentoken -key ../../dev.key
package main

import (
	"flag"
	"fmt"
	"os"

	"go-api-gateway/loadtest"
)

func main() {
	keyPath := flag.String("key", "../../dev.key", "path to RSA private key")
	sub := flag.String("sub", "550e8400-e29b-41d4-a716-446655440000", "JWT subject")
	flag.Parse()

	key, err := loadtest.LoadPrivateKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load key: %v\n", err)
		os.Exit(1)
	}

	token, err := loadtest.SignToken(key, *sub, []string{"admin"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to sign token: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(token)
}
