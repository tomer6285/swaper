package accounts

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"swaper/internal/config"
	"swaper/internal/provider"
)

const (
	KeyringService = "gemini"
	KeyringUser    = "antigravity"

	TokenEndpoint = "https://oauth2.googleapis.com/token"
)

// OAuthClientCredentials returns the Google OAuth client ID/secret from the
// environment (SWAPER_GOOGLE_CLIENT_ID / SWAPER_GOOGLE_CLIENT_SECRET,
// falling back to GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET) so no secret
// lives in the codebase and any user can supply their own.
func OAuthClientCredentials() (id, secret string) {
	if v := os.Getenv("SWAPER_GOOGLE_CLIENT_ID"); v != "" {
		id = v
	} else {
		id = os.Getenv("GOOGLE_CLIENT_ID")
	}
	if v := os.Getenv("SWAPER_GOOGLE_CLIENT_SECRET"); v != "" {
		secret = v
	} else {
		secret = os.Getenv("GOOGLE_CLIENT_SECRET")
	}
	return id, secret
}

// StoredAuthToken represents the payload stored in the keyring/profile.
type StoredAuthToken struct {
	Token struct {
		AccessToken  string    `json:"access_token"`
		TokenType    string    `json:"token_type"`
		RefreshToken string    `json:"refresh_token"`
		Expiry       time.Time `json:"expiry"`
	} `json:"token"`
	AuthMethod string `json:"auth_method"`
	IDToken    string `json:"id_token"`
}

// ExtractEmailFromIDToken decodes the JWT payload to extract user email.
func ExtractEmailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	seg := parts[1]
	pad := 4 - (len(seg) % 4)
	if pad != 4 {
		seg += strings.Repeat("=", pad)
	}
	payloadBytes, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(payloadBytes, &claims)
	return claims.Email
}

// ProfileStore manages accounts directory and active profile pointer.
type ProfileStore struct {
	baseDir string
	mu      sync.Mutex
}

func NewProfileStore(baseDir string) (*ProfileStore, error) {
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, err
	}
	profilesDir := filepath.Join(baseDir, "profiles")
	if err := os.MkdirAll(profilesDir, 0700); err != nil {
		return nil, err
	}
	return &ProfileStore{baseDir: baseDir}, nil
}

func (ps *ProfileStore) ProfilesDir() string {
	return filepath.Join(ps.baseDir, "profiles")
}

func (ps *ProfileStore) ProfilePath(name string) string {
	return filepath.Join(ps.ProfilesDir(), name)
}

func (ps *ProfileStore) ActiveProfileFile() string {
	return filepath.Join(ps.baseDir, "active_profile.txt")
}

// GetActiveProfileName reads the currently marked active profile.
func (ps *ProfileStore) GetActiveProfileName() string {
	data, err := os.ReadFile(ps.ActiveProfileFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SetActiveProfileName updates the active profile file atomically.
func (ps *ProfileStore) SetActiveProfileName(name string) error {
	tmpFile := ps.ActiveProfileFile() + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(name), 0600); err != nil {
		return err
	}
	return os.Rename(tmpFile, ps.ActiveProfileFile())
}

// SameAuthAccount reports whether two stored tokens belong to the same
// Google account. It matches by email (case-insensitive) first, then falls
// back to refresh_token equality so rotated access tokens and unparseable
// ID tokens still match. Empty identities never match.
func SameAuthAccount(a, b *StoredAuthToken) bool {
	if a == nil || b == nil {
		return false
	}
	emailA := strings.ToLower(strings.TrimSpace(ExtractEmailFromIDToken(a.IDToken)))
	emailB := strings.ToLower(strings.TrimSpace(ExtractEmailFromIDToken(b.IDToken)))
	if emailA != "" && emailA == emailB {
		return true
	}
	if a.Token.RefreshToken != "" && a.Token.RefreshToken == b.Token.RefreshToken {
		return true
	}
	return false
}

// HasCredentials reports whether a stored token can actually authenticate
// (i.e. it is not an empty placeholder created by `swaper add`).
func HasCredentials(auth *StoredAuthToken) bool {
	if auth == nil {
		return false
	}
	return auth.Token.RefreshToken != "" || auth.Token.AccessToken != ""
}

// LiveOwnerName identifies which stored profile (if any) owns the given live
// keyring session. Returns "" when the session matches nothing stored.
func (ps *ProfileStore) LiveOwnerName(live *StoredAuthToken) string {
	if !HasCredentials(live) {
		return ""
	}
	return ps.FindDuplicate(live)
}

// ListProfiles returns all saved profile names and their active status.
//
// IsActive reflects the LIVE agy session (system keyring), not just the
// active_profile.txt pointer:
//   - keyring readable + matches a stored profile -> that profile is active
//     (pointer may be stale; live truth wins)
//   - keyring readable + matches nothing -> external/unknown session, none active
//   - keyring unreadable (logged out) -> fall back to the pointer file
func (ps *ProfileStore) ListProfiles() ([]provider.StoredAccount, error) {
	// Best-effort live read done outside the lock (keychain I/O can block).
	liveAuth, liveErr := ReadCurrentKeyring()
	liveOwner := ""
	liveReadable := liveErr == nil && HasCredentials(liveAuth)
	if liveReadable {
		liveOwner = ps.FindDuplicate(liveAuth)
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	fileActive := ps.GetActiveProfileName()
	entries, err := os.ReadDir(ps.ProfilesDir())
	if err != nil {
		return nil, err
	}

	var accounts []provider.StoredAccount
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		profDir := ps.ProfilePath(name)
		info, err := entry.Info()
		if err != nil {
			continue
		}

		email := ""
		tokenPath := filepath.Join(profDir, "auth.json")
		if data, err := os.ReadFile(tokenPath); err == nil {
			var auth StoredAuthToken
			if err := json.Unmarshal(data, &auth); err == nil {
				email = ExtractEmailFromIDToken(auth.IDToken)
			}
		}

		accounts = append(accounts, provider.StoredAccount{
			ID:        name,
			Email:     email,
			IsActive:  ps.isLiveActive(name, fileActive, liveReadable, liveOwner),
			CreatedAt: info.ModTime(),
			UpdatedAt: info.ModTime(),
		})
	}

	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].IsActive != accounts[j].IsActive {
			return accounts[i].IsActive
		}
		return accounts[i].ID < accounts[j].ID
	})

	return accounts, nil
}

// isLiveActive resolves the active flag for one profile given the live
// keyring state. Pure function to keep ListProfiles testable.
func (ps *ProfileStore) isLiveActive(name, fileActive string, liveReadable bool, liveOwner string) bool {
	if liveReadable {
		if liveOwner != "" {
			return name == liveOwner
		}
		// Live session exists but matches nothing stored: external login.
		return false
	}
	return name == fileActive
}

// SaveProfile saves the auth token data and optional files for an account.
func (ps *ProfileStore) SaveProfile(name string, auth *StoredAuthToken) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	profDir := ps.ProfilePath(name)
	if err := os.MkdirAll(profDir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}

	tokenPath := filepath.Join(profDir, "auth.json")
	return os.WriteFile(tokenPath, data, 0600)
}

// LoadProfile reads the auth token for a specific profile.
func (ps *ProfileStore) LoadProfile(name string) (*StoredAuthToken, error) {
	tokenPath := filepath.Join(ps.ProfilePath(name), "auth.json")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, err
	}
	var auth StoredAuthToken
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

// FindProfileByEmail looks for an existing profile with the given email address.
func (ps *ProfileStore) FindProfileByEmail(email string) string {
	if email == "" {
		return ""
	}
	entries, err := os.ReadDir(ps.ProfilesDir())
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		tokenPath := filepath.Join(ps.ProfilePath(entry.Name()), "auth.json")
		data, err := os.ReadFile(tokenPath)
		if err != nil {
			continue
		}
		var auth StoredAuthToken
		if err := json.Unmarshal(data, &auth); err == nil {
			if strings.EqualFold(ExtractEmailFromIDToken(auth.IDToken), email) {
				return entry.Name()
			}
		}
	}
	return ""
}

// ValidateProfileName ensures a profile name is safe to use as a directory name.
func ValidateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name cannot be empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid profile name %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("profile name %q cannot contain slashes", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("profile name %q cannot start with '.'", name)
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return fmt.Errorf("profile name %q contains invalid characters", name)
		}
	}
	return nil
}

// RenameProfile renames a profile directory and updates the active pointer.
func (ps *ProfileStore) RenameProfile(oldName, newName string) error {
	if err := ValidateProfileName(newName); err != nil {
		return err
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()

	oldPath := ps.ProfilePath(oldName)
	newPath := ps.ProfilePath(newName)

	if _, err := os.Stat(oldPath); err != nil {
		return fmt.Errorf("profile '%s' not found", oldName)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("profile '%s' already exists", newName)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("failed to rename profile: %w", err)
	}
	if ps.GetActiveProfileName() == oldName {
		_ = ps.SetActiveProfileName(newName)
	}
	return nil
}

// FindDuplicate looks for an existing profile holding the same account.
// It matches by email (case-insensitive) first, then falls back to
// refresh_token equality for accounts where email extraction fails.
// Returns the matching profile name, or "" if none.
func (ps *ProfileStore) FindDuplicate(auth *StoredAuthToken) string {
	if auth == nil {
		return ""
	}
	email := strings.ToLower(strings.TrimSpace(ExtractEmailFromIDToken(auth.IDToken)))
	refresh := auth.Token.RefreshToken

	entries, err := os.ReadDir(ps.ProfilesDir())
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		tokenPath := filepath.Join(ps.ProfilePath(entry.Name()), "auth.json")
		data, err := os.ReadFile(tokenPath)
		if err != nil {
			continue
		}
		var existing StoredAuthToken
		if err := json.Unmarshal(data, &existing); err != nil {
			continue
		}
		if email != "" && strings.EqualFold(ExtractEmailFromIDToken(existing.IDToken), email) {
			return entry.Name()
		}
		if refresh != "" && existing.Token.RefreshToken != "" && existing.Token.RefreshToken == refresh {
			return entry.Name()
		}
	}
	return ""
}

// DeleteProfile removes the profile folder.
func (ps *ProfileStore) DeleteProfile(name string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	profDir := ps.ProfilePath(name)
	if err := os.RemoveAll(profDir); err != nil {
		return err
	}
	if ps.GetActiveProfileName() == name {
		_ = os.Remove(ps.ActiveProfileFile())
	}
	return nil
}

// ReadCurrentKeyring reads the active Antigravity auth token from system keyring.
func ReadCurrentKeyring() (*StoredAuthToken, error) {
	raw, err := keyring.Get(KeyringService, KeyringUser)
	if err != nil {
		return nil, fmt.Errorf("failed to read keyring (%s/%s): %w", KeyringService, KeyringUser, err)
	}

	// go-keyring on macOS may return raw JSON or base64
	var jsonBytes []byte
	if strings.HasPrefix(raw, "go-keyring-base64:") {
		trimmed := strings.TrimPrefix(raw, "go-keyring-base64:")
		decoded, err := base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 keyring token: %w", err)
		}
		jsonBytes = decoded
	} else {
		jsonBytes = []byte(raw)
	}

	var auth StoredAuthToken
	if err := json.Unmarshal(jsonBytes, &auth); err != nil {
		return nil, fmt.Errorf("failed to unmarshal keyring auth json: %w", err)
	}
	return &auth, nil
}

// WriteCurrentKeyring stores the given auth token into the system keyring.
func WriteCurrentKeyring(auth *StoredAuthToken) error {
	jsonBytes, err := json.Marshal(auth)
	if err != nil {
		return fmt.Errorf("failed to marshal auth for keyring: %w", err)
	}
	// Note: on macOS go-keyring handles base64 encoding transparently
	if err := keyring.Set(KeyringService, KeyringUser, string(jsonBytes)); err != nil {
		return fmt.Errorf("failed to set keyring (%s/%s): %w", KeyringService, KeyringUser, err)
	}
	return nil
}

// EnsureValidAccessToken checks if access token is expired; if so, uses refresh_token to obtain a new one.
func EnsureValidAccessToken(auth *StoredAuthToken) (string, error) {
	// If still valid for at least 60 seconds, return existing
	if auth.Token.AccessToken != "" && auth.Token.Expiry.After(time.Now().Add(60*time.Second)) {
		return auth.Token.AccessToken, nil
	}

	if auth.Token.RefreshToken == "" {
		if auth.Token.AccessToken != "" {
			return auth.Token.AccessToken, nil
		}
		return "", fmt.Errorf("no refresh_token or access_token available")
	}

	// Ordered credential pairs: explicit config first, then every combination
	// embedded in the CLI binary. The binary can hold several (old + new),
	// and only the right pairing is accepted, so try each in turn.
	var pairs [][2]string
	seen := map[[2]string]bool{}
	push := func(id, secret string) {
		if id == "" || secret == "" {
			return
		}
		p := [2]string{id, secret}
		if !seen[p] {
			seen[p] = true
			pairs = append(pairs, p)
		}
	}
	if id, secret := OAuthClientCredentials(); id != "" && secret != "" {
		push(id, secret)
	}
	if cfg, err := config.LoadConfig(); err == nil {
		push(cfg.GoogleClientID, cfg.GoogleClientSecret)
	}
	for _, p := range config.CandidatePairs("agy") {
		push(p[0], p[1])
	}
	if len(pairs) == 0 {
		return "", fmt.Errorf("missing OAuth client credentials: set SWAPER_GOOGLE_CLIENT_ID and SWAPER_GOOGLE_CLIENT_SECRET (or google_client_id/secret in ~/.swaper/config.json)")
	}

	var lastErr error
	for i, p := range pairs {
		token, retryPair, err := tryRefreshPair(auth, p[0], p[1])
		if err == nil {
			if i > 0 {
				config.RememberCredentials(p[0], p[1])
			}
			return token, nil
		}
		if !retryPair {
			return "", err
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("token refresh failed: no working OAuth client pair found")
}

// tryRefreshPair attempts one token refresh. The second return value reports
// whether another credential pair is worth trying (wrong client) as opposed
// to a hard failure (bad grant, network).
func tryRefreshPair(auth *StoredAuthToken, clientID, clientSecret string) (string, bool, error) {
	data := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {auth.Token.RefreshToken},
	}

	resp, err := http.PostForm(TokenEndpoint, data)
	if err != nil {
		return "", false, fmt.Errorf("token refresh network request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, fmt.Errorf("failed to read token refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &errBody)
		if errBody.Error == "invalid_client" || errBody.Error == "unauthorized_client" {
			return "", true, fmt.Errorf("token refresh failed with code %d: %s", resp.StatusCode, string(body))
		}
		return "", false, fmt.Errorf("token refresh failed with code %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", false, fmt.Errorf("failed to parse token refresh json: %w", err)
	}

	auth.Token.AccessToken = tokenResp.AccessToken
	if tokenResp.ExpiresIn > 0 {
		auth.Token.Expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	} else {
		auth.Token.Expiry = time.Now().Add(3600 * time.Second)
	}
	if tokenResp.TokenType != "" {
		auth.Token.TokenType = tokenResp.TokenType
	}

	return auth.Token.AccessToken, false, nil
}
