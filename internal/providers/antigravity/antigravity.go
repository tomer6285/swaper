package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"swaper/internal/accounts"
	"swaper/internal/provider"
)

const (
	QuotaEndpoint = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	TierEndpoint  = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	UserAgent     = "antigravity-cli"
)

// QuotaResponse reflects the JSON payload returned by retrieveUserQuotaSummary
type QuotaResponse struct {
	Groups []struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Buckets     []struct {
			BucketID          string  `json:"bucketId"`
			DisplayName       string  `json:"displayName"`
			Window            string  `json:"window"`
			ResetTime         string  `json:"resetTime"`
			Description       string  `json:"description"`
			RemainingFraction float64 `json:"remainingFraction"`
		} `json:"buckets"`
	} `json:"groups"`
	Description string `json:"description"`
}

// TierResponse reflects the JSON payload returned by loadCodeAssist
type TierResponse struct {
	CurrentTier struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"currentTier"`
}

// Provider implements provider.Provider for Antigravity (agy).
type Provider struct {
	store      *accounts.ProfileStore
	httpClient *http.Client

	// Cache quota requests to prevent rate limits
	cacheMu sync.RWMutex
	cache   map[string]cacheEntry
}

type cacheEntry struct {
	status    provider.AccountStatus
	fetchedAt time.Time
}

func NewProvider(storageDir string) (*Provider, error) {
	antigravityDir := filepath.Join(storageDir, "antigravity")
	store, err := accounts.NewProfileStore(antigravityDir)
	if err != nil {
		return nil, err
	}
	return &Provider{
		store: store,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		cache: make(map[string]cacheEntry),
	}, nil
}

func (p *Provider) ID() string {
	return "antigravity"
}

func (p *Provider) DisplayName() string {
	return "Antigravity (Gemini pool)"
}

func (p *Provider) CLIBinary() string {
	return "agy"
}

func (p *Provider) InvalidateCache() {
	p.cacheMu.Lock()
	p.cache = make(map[string]cacheEntry)
	p.cacheMu.Unlock()
}

func (p *Provider) ListAccounts() ([]provider.StoredAccount, error) {
	return p.store.ListProfiles()
}

func (p *Provider) ActiveAccount() (*provider.StoredAccount, error) {
	list, err := p.ListAccounts()
	if err != nil {
		return nil, err
	}
	for _, acc := range list {
		if acc.IsActive {
			return &acc, nil
		}
	}
	return nil, nil
}

// ImportCurrent saves the currently active keyring session into a named profile.
func (p *Provider) ImportCurrent(id, email string) (*provider.StoredAccount, error) {
	auth, err := accounts.ReadCurrentKeyring()
	if err != nil {
		return nil, fmt.Errorf("could not read current keyring: %w", err)
	}

	extractedEmail := accounts.ExtractEmailFromIDToken(auth.IDToken)
	if email == "" {
		email = extractedEmail
	}

	// Stop duplicates: same account already saved under another profile name.
	// Match by email first, then by refresh_token (covers empty/unparseable ID tokens).
	existingID := p.store.FindProfileByEmail(email)
	if existingID == "" {
		existingID = p.store.FindDuplicate(auth)
	}

	if id == "" {
		if existingID != "" {
			id = existingID
		} else {
			id = "default"
		}
	}
	if err := accounts.ValidateProfileName(id); err != nil {
		return nil, err
	}

	if existingID != "" && existingID != id {
		return nil, fmt.Errorf("account for %s already exists under profile '%s' — refusing to create duplicate '%s' (use 'swaper rename %s <new-name>' to rename it)", email, existingID, id, existingID)
	}

	if existingID == id {
		// Same name + same account: refresh stored token, don't duplicate.
		if err := p.store.SaveProfile(id, auth); err != nil {
			return nil, fmt.Errorf("failed to save profile: %w", err)
		}
		return nil, fmt.Errorf("account for %s is already saved as '%s' (token refreshed)", email, id)
	}

	// Target name taken by a *different* account: never overwrite.
	if _, err := p.store.LoadProfile(id); err == nil {
		return nil, fmt.Errorf("profile '%s' already exists for a different account — choose another name or 'swaper rename %s <new-name>' first", id, id)
	}

	if err := p.store.SaveProfile(id, auth); err != nil {
		return nil, fmt.Errorf("failed to save profile: %w", err)
	}

	if p.store.GetActiveProfileName() == "" {
		_ = p.store.SetActiveProfileName(id)
	}

	return &provider.StoredAccount{
		ID:        id,
		Email:     email,
		IsActive:  p.store.GetActiveProfileName() == id,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

// RenameAccount renames a stored profile.
func (p *Provider) RenameAccount(oldID, newID string) error {
	if oldID == "" || newID == "" {
		return fmt.Errorf("usage: rename <old-name> <new-name>")
	}
	if oldID == newID {
		return fmt.Errorf("profile '%s' already has that name", oldID)
	}
	if err := p.store.RenameProfile(oldID, newID); err != nil {
		return err
	}
	p.cacheMu.Lock()
	delete(p.cache, oldID)
	delete(p.cache, newID)
	p.cacheMu.Unlock()
	return nil
}

// AddNew starts the flow for adding a new profile: marks an empty/pending profile or prepares login.
func (p *Provider) AddNew(id, email string) (*provider.StoredAccount, error) {
	if err := accounts.ValidateProfileName(id); err != nil {
		return nil, err
	}
	// Check if already exists
	_, err := p.store.LoadProfile(id)
	if err == nil {
		return nil, fmt.Errorf("account profile '%s' already exists", id)
	}

	// Create profile with placeholder auth or copy current if requested
	auth := &accounts.StoredAuthToken{}
	if err := p.store.SaveProfile(id, auth); err != nil {
		return nil, err
	}

	return &provider.StoredAccount{
		ID:        id,
		Email:     email,
		IsActive:  false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

func (p *Provider) RemoveAccount(id string) error {
	return p.store.DeleteProfile(id)
}

// SwitchTo changes the active profile atomically:
// 1. Saves current active profile's keyring state back to disk (to persist any refreshed token).
// 2. Loads target profile's token.
// 3. Writes target token to keyring.
// 4. Updates active_profile.txt.
func (p *Provider) SwitchTo(account provider.StoredAccount) error {
	// First check if CLI is running and warn
	running, _ := p.IsCLIRunning()
	if running {
		// Note: caller can prompt, but provider proceeds safely
	}

	// Step 1: Save current keyring back to active profile if active profile is set
	currActive := p.store.GetActiveProfileName()
	if currActive != "" {
		if currAuth, err := accounts.ReadCurrentKeyring(); err == nil {
			_ = p.store.SaveProfile(currActive, currAuth)
		}
	}

	// Step 2: Load target profile auth
	targetAuth, err := p.store.LoadProfile(account.ID)
	if err != nil {
		return fmt.Errorf("failed to load target profile '%s': %w", account.ID, err)
	}

	// Step 3: Write target auth into keyring
	if err := accounts.WriteCurrentKeyring(targetAuth); err != nil {
		return fmt.Errorf("failed to update keyring for '%s': %w", account.ID, err)
	}

	// Step 4: Update active marker
	if err := p.store.SetActiveProfileName(account.ID); err != nil {
		return fmt.Errorf("failed to mark profile as active: %w", err)
	}

	return nil
}

// FetchStatus queries the quota and plan tier for the account without mutating system keyring.
func (p *Provider) FetchStatus(ctx context.Context, account provider.StoredAccount) (provider.AccountStatus, error) {
	// Check in-memory cache (60s TTL)
	p.cacheMu.RLock()
	entry, found := p.cache[account.ID]
	p.cacheMu.RUnlock()
	if found && time.Since(entry.fetchedAt) < 60*time.Second {
		return entry.status, nil
	}

	auth, err := p.store.LoadProfile(account.ID)
	if err != nil {
		return provider.AccountStatus{
			ID:          account.ID,
			Email:       account.Email,
			Healthy:     false,
			LastChecked: time.Now(),
			Error:       fmt.Sprintf("load profile error: %v", err),
		}, nil
	}

	// Ensure token is fresh
	token, err := accounts.EnsureValidAccessToken(auth)
	if err != nil {
		return provider.AccountStatus{
			ID:          account.ID,
			Email:       account.Email,
			Healthy:     false,
			LastChecked: time.Now(),
			Error:       fmt.Sprintf("auth token invalid: %v", err),
		}, nil
	}
	// Persist updated token if refreshed
	_ = p.store.SaveProfile(account.ID, auth)

	email := account.Email
	if email == "" {
		email = accounts.ExtractEmailFromIDToken(auth.IDToken)
	}

	// Fetch Quotas
	quotas, err := p.fetchQuotas(ctx, token)
	if err != nil {
		return provider.AccountStatus{
			ID:          account.ID,
			Email:       email,
			Healthy:     false,
			LastChecked: time.Now(),
			Error:       fmt.Sprintf("quota fetch error: %v", err),
		}, nil
	}

	// Fetch Plan Tier
	plan := p.fetchPlanTier(ctx, token)

	status := provider.AccountStatus{
		ID:          account.ID,
		Email:       email,
		Plan:        plan,
		Quotas:      quotas,
		Healthy:     true,
		LastChecked: time.Now(),
	}

	p.cacheMu.Lock()
	p.cache[account.ID] = cacheEntry{
		status:    status,
		fetchedAt: time.Now(),
	}
	p.cacheMu.Unlock()

	return status, nil
}

func (p *Provider) fetchQuotas(ctx context.Context, accessToken string) ([]provider.Quota, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", QuotaEndpoint, bytes.NewBuffer([]byte("{}")))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var qr QuotaResponse
	if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
		return nil, err
	}

	var quotas []provider.Quota
	now := time.Now()

	// Prioritize Gemini Models group, fallback to others
	for _, grp := range qr.Groups {
		if strings.Contains(strings.ToLower(grp.DisplayName), "gemini") {
			for _, b := range grp.Buckets {
				resetTime, _ := time.Parse(time.RFC3339, b.ResetTime)
				resetIn := formatResetIn(resetTime.Sub(now))
				label := b.Window
				if b.BucketID == "gemini-5h" || b.Window == "5h" {
					label = "5-hour"
				} else if b.BucketID == "gemini-weekly" || b.Window == "weekly" {
					label = "Weekly"
				}

				quotas = append(quotas, provider.Quota{
					Label:       label,
					PercentLeft: b.RemainingFraction * 100.0,
					ResetAt:     resetTime,
					ResetIn:     resetIn,
					Description: b.Description,
				})
			}
			break
		}
	}

	// Fallback to first group if no gemini group found
	if len(quotas) == 0 && len(qr.Groups) > 0 {
		for _, b := range qr.Groups[0].Buckets {
			resetTime, _ := time.Parse(time.RFC3339, b.ResetTime)
			quotas = append(quotas, provider.Quota{
				Label:       b.Window,
				PercentLeft: b.RemainingFraction * 100.0,
				ResetAt:     resetTime,
				ResetIn:     formatResetIn(resetTime.Sub(now)),
				Description: b.Description,
			})
		}
	}

	return quotas, nil
}

func (p *Provider) fetchPlanTier(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, "POST", TierEndpoint, bytes.NewBuffer([]byte("{}")))
	if err != nil {
		return "Standard"
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "Standard"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "Standard"
	}

	var tr TierResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "Standard"
	}

	tierID := strings.ToLower(tr.CurrentTier.ID)
	if strings.Contains(tierID, "free") {
		return "Free"
	} else if strings.Contains(tierID, "pro") {
		return "Pro"
	} else if strings.Contains(tierID, "ultra") {
		return "Ultra"
	} else if tr.CurrentTier.Name != "" {
		return tr.CurrentTier.Name
	}
	return "Standard"
}

func formatResetIn(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// IsCLIRunning checks if any process named "agy" is running.
func (p *Provider) IsCLIRunning() (bool, error) {
	out, err := exec.Command("pgrep", "-x", "agy").Output()
	if err != nil {
		return false, nil
	}
	pids := strings.Fields(string(out))
	myPid := os.Getpid()
	for _, pidStr := range pids {
		pid, _ := strconv.Atoi(pidStr)
		if pid != myPid && pid > 0 {
			return true, nil
		}
	}
	return false, nil
}

// LaunchCLI replaces current process with `agy` or executes it.
func (p *Provider) LaunchCLI(account provider.StoredAccount) error {
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		return fmt.Errorf("agy binary not found in PATH: %w", err)
	}
	// Replace process
	return syscall.Exec(agyPath, []string{"agy"}, os.Environ())
}
