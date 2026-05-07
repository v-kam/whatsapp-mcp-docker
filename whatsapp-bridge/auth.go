// auth.go — bearer-token storage and regeneration for the MCP HTTP transports.
//
// The token lives in a small file under store/ (default: store/auth_token,
// override via MCP_AUTH_TOKEN_PATH). On first start a fresh 32-byte hex token
// is generated and written with mode 0600. The Python MCP server (auth.py)
// reads the same file on every request, so regenerating the token via the
// pairing UI takes effect immediately — no Python restart, no redeploy.
//
// stdio transport never needs a token (the MCP runs as a subprocess of the
// AI client). The UI server therefore only includes the token in the
// connection JSON when MCP_TRANSPORT is sse or streamable-http.

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	tokenFileEnv     = "MCP_AUTH_TOKEN_PATH"
	tokenFileDefault = "store/auth_token"
	tokenLenBytes    = 32
)

// tokenMu serialises generate/regenerate; reads can race with writes safely
// because os.WriteFile is atomic-ish on local filesystems.
var tokenMu sync.Mutex

func tokenPath() string {
	if p := os.Getenv(tokenFileEnv); p != "" {
		return p
	}
	return tokenFileDefault
}

// EnsureToken returns the persisted token, generating one on first call.
// Idempotent: calling it repeatedly never overwrites an existing token.
func EnsureToken() (string, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if tok, err := readTokenLocked(); err == nil && tok != "" {
		return tok, nil
	}
	return generateLocked()
}

// RegenerateToken always writes a new token and returns it.
// The Python middleware re-reads the file on every request, so the new
// token starts being required as soon as this function returns.
func RegenerateToken() (string, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	return generateLocked()
}

func readTokenLocked() (string, error) {
	b, err := os.ReadFile(tokenPath())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func generateLocked() (string, error) {
	b := make([]byte, tokenLenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	tok := hex.EncodeToString(b)

	p := tokenPath()
	if dir := filepath.Dir(p); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", p, err)
	}
	return tok, nil
}
