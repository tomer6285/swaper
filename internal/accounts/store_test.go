package accounts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadCurrentKeyring_FileFallback(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "antigravity-oauth-token")

	origOverride := AntigravityOAuthTokenFileOverride
	AntigravityOAuthTokenFileOverride = tokenPath
	defer func() {
		AntigravityOAuthTokenFileOverride = origOverride
	}()

	sampleAuth := StoredAuthToken{
		AuthMethod: "consumer",
		IDToken:    "sample.id.token",
	}
	sampleAuth.Token.AccessToken = "ya29.test-access-token"
	sampleAuth.Token.RefreshToken = "1//test-refresh-token"
	sampleAuth.Token.TokenType = "Bearer"
	sampleAuth.Token.Expiry = time.Now().Add(24 * time.Hour)

	data, err := json.MarshalIndent(sampleAuth, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal sample auth: %v", err)
	}
	if err := os.WriteFile(tokenPath, data, 0600); err != nil {
		t.Fatalf("failed to write token file: %v", err)
	}

	auth, err := ReadCurrentKeyring()
	if err != nil {
		t.Fatalf("expected ReadCurrentKeyring to succeed using file fallback, got: %v", err)
	}
	if auth.Token.AccessToken != sampleAuth.Token.AccessToken {
		t.Errorf("expected access_token %q, got %q", sampleAuth.Token.AccessToken, auth.Token.AccessToken)
	}
	if auth.Token.RefreshToken != sampleAuth.Token.RefreshToken {
		t.Errorf("expected refresh_token %q, got %q", sampleAuth.Token.RefreshToken, auth.Token.RefreshToken)
	}
	if auth.AuthMethod != sampleAuth.AuthMethod {
		t.Errorf("expected auth_method %q, got %q", sampleAuth.AuthMethod, auth.AuthMethod)
	}
}

func TestWriteCurrentKeyring_FileFallback(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "sub", "antigravity-oauth-token")

	origOverride := AntigravityOAuthTokenFileOverride
	AntigravityOAuthTokenFileOverride = tokenPath
	defer func() {
		AntigravityOAuthTokenFileOverride = origOverride
	}()

	sampleAuth := &StoredAuthToken{
		AuthMethod: "consumer",
		IDToken:    "sample.id.token",
	}
	sampleAuth.Token.AccessToken = "ya29.write-test"
	sampleAuth.Token.RefreshToken = "1//write-test-refresh"
	sampleAuth.Token.TokenType = "Bearer"
	sampleAuth.Token.Expiry = time.Now().Add(2 * time.Hour)

	if err := WriteCurrentKeyring(sampleAuth); err != nil {
		t.Fatalf("expected WriteCurrentKeyring to succeed, got: %v", err)
	}

	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("expected token file to exist at %s: %v", tokenPath, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected file permissions 0600, got %o", perm)
	}

	readBack, err := ReadCurrentKeyring()
	if err != nil {
		t.Fatalf("expected ReadCurrentKeyring to read back written auth: %v", err)
	}
	if readBack.Token.AccessToken != sampleAuth.Token.AccessToken {
		t.Errorf("expected access token %q, got %q", sampleAuth.Token.AccessToken, readBack.Token.AccessToken)
	}
}
