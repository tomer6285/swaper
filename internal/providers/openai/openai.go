package openai

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"swaper/internal/accounts"
	"swaper/internal/provider"
)

// CodexAuth represents the credentials stored in ~/.codex/auth.json.
type CodexAuth struct {
	AuthMode     string      `json:"auth_mode"`
	OpenAIAPIKey *string     `json:"OPENAI_API_KEY"`
	Tokens       CodexTokens `json:"tokens"`
	LastRefresh  string      `json:"last_refresh,omitempty"`
}

type CodexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

type AppServerRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type AppServerRateLimitsResponse struct {
	ID     int `json:"id"`
	Result struct {
		RateLimits struct {
			PlanType string `json:"planType"`
			Primary  *struct {
				UsedPercent        int   `json:"usedPercent"`
				WindowDurationMins int   `json:"windowDurationMins"`
				ResetsAt           int64 `json:"resetsAt"`
			} `json:"primary"`
			Secondary *struct {
				UsedPercent        int   `json:"usedPercent"`
				WindowDurationMins int   `json:"windowDurationMins"`
				ResetsAt           int64 `json:"resetsAt"`
			} `json:"secondary"`
		} `json:"rateLimits"`
		Account *struct {
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type cacheEntry struct {
	Status    provider.AccountStatus `json:"status"`
	FetchedAt time.Time              `json:"fetched_at"`
}

type Provider struct {
	storageDir  string
	profilesDir string
	activeFile  string
	codexHome   string
	codexBin    string

	cacheMu   sync.RWMutex
	cache     map[string]cacheEntry
	cacheFile string
}

func NewProvider(storageDir string) (*Provider, error) {
	openaiDir := filepath.Join(storageDir, "openai")
	profilesDir := filepath.Join(openaiDir, "profiles")
	if err := os.MkdirAll(profilesDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create openai storage: %w", err)
	}

	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}

	p := &Provider{
		storageDir:  openaiDir,
		profilesDir: profilesDir,
		activeFile:  filepath.Join(openaiDir, "active_profile.txt"),
		codexHome:   codexHome,
		codexBin:    FindCodexBinary(),
		cache:       make(map[string]cacheEntry),
		cacheFile:   filepath.Join(openaiDir, "cache.json"),
	}
	p.loadCacheFromDisk()
	return p, nil
}

func (p *Provider) ID() string {
	return "openai"
}

func (p *Provider) DisplayName() string {
	return "openai"
}

func (p *Provider) CLIBinary() string {
	return "codex"
}

func (p *Provider) InvalidateCache() {
	p.cacheMu.Lock()
	p.cache = make(map[string]cacheEntry)
	p.cacheMu.Unlock()
	if p.cacheFile != "" {
		_ = os.Remove(p.cacheFile)
	}
}

func (p *Provider) CachedStatus(id string) (provider.AccountStatus, bool) {
	p.cacheMu.RLock()
	defer p.cacheMu.RUnlock()
	entry, found := p.cache[id]
	return entry.Status, found
}

func (p *Provider) loadCacheFromDisk() {
	if p.cacheFile == "" {
		return
	}
	data, err := os.ReadFile(p.cacheFile)
	if err != nil {
		return
	}
	var diskCache map[string]cacheEntry
	if err := json.Unmarshal(data, &diskCache); err == nil && diskCache != nil {
		p.cacheMu.Lock()
		p.cache = diskCache
		p.cacheMu.Unlock()
	}
}

func (p *Provider) saveCacheToDisk() {
	if p.cacheFile == "" {
		return
	}
	p.cacheMu.RLock()
	data, err := json.MarshalIndent(p.cache, "", "  ")
	p.cacheMu.RUnlock()
	if err != nil {
		return
	}
	tmpFile := p.cacheFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err == nil {
		_ = os.Rename(tmpFile, p.cacheFile)
	}
}

func (p *Provider) invalidate(ids ...string) {
	p.cacheMu.Lock()
	for _, id := range ids {
		delete(p.cache, id)
	}
	p.cacheMu.Unlock()
	p.saveCacheToDisk()
}

func (p *Provider) getActiveProfileName() string {
	data, err := os.ReadFile(p.activeFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (p *Provider) setActiveProfileName(name string) error {
	tmpFile := p.activeFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(name), 0600); err != nil {
		return err
	}
	return os.Rename(tmpFile, p.activeFile)
}

func (p *Provider) profileAuthPath(id string) string {
	return filepath.Join(p.profilesDir, id, "auth.json")
}

func (p *Provider) loadProfile(id string) (*CodexAuth, error) {
	path := p.profileAuthPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var auth CodexAuth
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

func (p *Provider) saveProfile(id string, auth *CodexAuth) error {
	dir := filepath.Join(p.profilesDir, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.profileAuthPath(id), data, 0600)
}

func (p *Provider) readLiveAuth() (*CodexAuth, error) {
	path := filepath.Join(p.codexHome, "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var auth CodexAuth
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

func (p *Provider) writeLiveAuth(auth *CodexAuth) error {
	if err := os.MkdirAll(p.codexHome, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(p.codexHome, "auth.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hasCodexCredentials(auth *CodexAuth) bool {
	if auth == nil {
		return false
	}
	return auth.Tokens.IDToken != "" || auth.Tokens.AccessToken != "" || auth.Tokens.RefreshToken != ""
}

func sameCodexAccount(a, b *CodexAuth) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Tokens.AccountID != "" && a.Tokens.AccountID == b.Tokens.AccountID {
		return true
	}
	emailA := strings.ToLower(strings.TrimSpace(extractEmailFromCodexAuth(a)))
	emailB := strings.ToLower(strings.TrimSpace(extractEmailFromCodexAuth(b)))
	if emailA != "" && emailA == emailB {
		return true
	}
	if a.Tokens.RefreshToken != "" && a.Tokens.RefreshToken == b.Tokens.RefreshToken {
		return true
	}
	return false
}

func extractEmailFromCodexAuth(auth *CodexAuth) string {
	if auth == nil {
		return ""
	}
	if email := extractEmailFromJWT(auth.Tokens.IDToken); email != "" {
		return email
	}
	return extractEmailFromJWT(auth.Tokens.AccessToken)
}

func extractPlanFromCodexAuth(auth *CodexAuth) string {
	if auth == nil {
		return ""
	}
	claims := extractClaimsFromJWT(auth.Tokens.IDToken)
	if len(claims) == 0 {
		claims = extractClaimsFromJWT(auth.Tokens.AccessToken)
	}
	if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if pt, ok := authClaim["chatgpt_plan_type"].(string); ok && pt != "" {
			return formatPlanType(pt)
		}
	}
	return "Standard"
}

func extractClaimsFromJWT(token string) map[string]interface{} {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := parts[1]
	pad := 4 - (len(seg) % 4)
	if pad != 4 {
		seg += strings.Repeat("=", pad)
	}
	data, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]interface{}
	_ = json.Unmarshal(data, &claims)
	return claims
}

func extractEmailFromJWT(token string) string {
	claims := extractClaimsFromJWT(token)
	if claims == nil {
		return ""
	}
	if email, ok := claims["email"].(string); ok {
		return email
	}
	if prof, ok := claims["https://api.openai.com/profile"].(map[string]interface{}); ok {
		if email, ok := prof["email"].(string); ok {
			return email
		}
	}
	return ""
}

func formatPlanType(pt string) string {
	pt = strings.TrimSpace(strings.ToLower(pt))
	switch pt {
	case "free":
		return "Free"
	case "plus":
		return "Plus"
	case "pro":
		return "Pro"
	case "team":
		return "Team"
	case "business":
		return "Business"
	case "enterprise":
		return "Enterprise"
	case "edu", "edu_plus", "edu_pro":
		return "Edu"
	default:
		if len(pt) > 0 {
			return strings.ToUpper(pt[:1]) + pt[1:]
		}
		return "Standard"
	}
}

func (p *Provider) ListAccounts() ([]provider.StoredAccount, error) {
	entries, err := os.ReadDir(p.profilesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	activeName := p.getActiveProfileName()
	var accountsList []provider.StoredAccount

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		auth, err := p.loadProfile(name)
		if err != nil {
			continue
		}
		email := extractEmailFromCodexAuth(auth)
		info, err := entry.Info()
		modTime := time.Now()
		if err == nil {
			modTime = info.ModTime()
		}

		accountsList = append(accountsList, provider.StoredAccount{
			ID:        name,
			Email:     email,
			IsActive:  name == activeName,
			CreatedAt: modTime,
			UpdatedAt: modTime,
		})
	}

	sort.SliceStable(accountsList, func(i, j int) bool {
		if accountsList[i].IsActive != accountsList[j].IsActive {
			return accountsList[i].IsActive
		}
		return accountsList[i].ID < accountsList[j].ID
	})

	return accountsList, nil
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

func (p *Provider) ImportCurrent(id, email string) (*provider.StoredAccount, error) {
	auth, err := p.readLiveAuth()
	if err != nil {
		return nil, fmt.Errorf("no active Codex session found at %s: %w — run 'codex login' first", filepath.Join(p.codexHome, "auth.json"), err)
	}
	if !hasCodexCredentials(auth) {
		return nil, fmt.Errorf("active session at %s has no credentials — run 'codex login' first", p.codexHome)
	}

	extractedEmail := extractEmailFromCodexAuth(auth)
	if email == "" {
		email = extractedEmail
	}

	existingID := p.findProfileByEmail(email)
	if existingID == "" {
		existingID = p.findDuplicate(auth)
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
		_ = p.setActiveProfileName(existingID)
		return nil, fmt.Errorf("account for %s already exists under profile '%s' — refusing to create duplicate '%s' (use 'swaper rename %s <new-name>')", email, existingID, id, existingID)
	}

	if existingID == id {
		if err := p.saveProfile(id, auth); err != nil {
			return nil, fmt.Errorf("failed to save profile: %w", err)
		}
		p.invalidate(id)
		_ = p.setActiveProfileName(id)
		return nil, fmt.Errorf("account for %s is already saved as '%s' (token refreshed)", email, id)
	}

	if _, err := p.loadProfile(id); err == nil {
		return nil, fmt.Errorf("profile '%s' already exists for a different account — choose another name or rename first", id)
	}

	if err := p.saveProfile(id, auth); err != nil {
		return nil, fmt.Errorf("failed to save profile: %w", err)
	}
	p.invalidate(id)
	_ = p.setActiveProfileName(id)

	return &provider.StoredAccount{
		ID:        id,
		Email:     email,
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

func (p *Provider) findProfileByEmail(email string) string {
	if email == "" {
		return ""
	}
	lower := strings.ToLower(email)
	entries, _ := os.ReadDir(p.profilesDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		auth, err := p.loadProfile(entry.Name())
		if err != nil {
			continue
		}
		if strings.ToLower(extractEmailFromCodexAuth(auth)) == lower {
			return entry.Name()
		}
	}
	return ""
}

func (p *Provider) findDuplicate(auth *CodexAuth) string {
	entries, _ := os.ReadDir(p.profilesDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stored, err := p.loadProfile(entry.Name())
		if err != nil {
			continue
		}
		if sameCodexAccount(stored, auth) {
			return entry.Name()
		}
	}
	return ""
}

func (p *Provider) AddNew(id, email string) (*provider.StoredAccount, error) {
	if err := accounts.ValidateProfileName(id); err != nil {
		return nil, err
	}
	if _, err := p.loadProfile(id); err == nil {
		return nil, fmt.Errorf("profile '%s' already exists", id)
	}
	auth := &CodexAuth{
		AuthMode: "chatgpt",
	}
	if err := p.saveProfile(id, auth); err != nil {
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
	dir := filepath.Join(p.profilesDir, id)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if p.getActiveProfileName() == id {
		_ = os.Remove(p.activeFile)
	}
	p.invalidate(id)
	return nil
}

func (p *Provider) RenameAccount(oldID, newID string) error {
	if oldID == "" || newID == "" {
		return fmt.Errorf("usage: rename <old-name> <new-name>")
	}
	if oldID == newID {
		return fmt.Errorf("profile '%s' already has that name", oldID)
	}
	if err := accounts.ValidateProfileName(newID); err != nil {
		return err
	}
	oldPath := filepath.Join(p.profilesDir, oldID)
	newPath := filepath.Join(p.profilesDir, newID)
	if _, err := os.Stat(oldPath); err != nil {
		return fmt.Errorf("profile '%s' does not exist", oldID)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("profile '%s' already exists", newID)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	if p.getActiveProfileName() == oldID {
		_ = p.setActiveProfileName(newID)
	}
	p.invalidate(oldID, newID)
	return nil
}

func (p *Provider) SwitchTo(account provider.StoredAccount) error {
	targetAuth, err := p.loadProfile(account.ID)
	if err != nil {
		return fmt.Errorf("failed to load target profile '%s': %w", account.ID, err)
	}
	if !hasCodexCredentials(targetAuth) {
		return fmt.Errorf("profile '%s' has no saved credentials — run 'codex login' then 'swaper import %s' first", account.ID, account.ID)
	}

	// Reconcile live session before overwriting
	if currAuth, err := p.readLiveAuth(); err == nil && hasCodexCredentials(currAuth) {
		if sameCodexAccount(currAuth, targetAuth) {
			_ = p.saveProfile(account.ID, currAuth)
			p.invalidate(account.ID)
			return p.setActiveProfileName(account.ID)
		}
		if owner := p.findDuplicate(currAuth); owner != "" {
			if owner != account.ID {
				_ = p.saveProfile(owner, currAuth)
				p.invalidate(owner)
			}
		} else {
			_ = p.preserveUnknownSession(currAuth)
		}
	}

	if err := p.writeLiveAuth(targetAuth); err != nil {
		return fmt.Errorf("failed to write live session for '%s': %w", account.ID, err)
	}

	if err := p.setActiveProfileName(account.ID); err != nil {
		return fmt.Errorf("failed to mark profile as active: %w", err)
	}
	p.invalidate(account.ID)
	return nil
}

func (p *Provider) preserveUnknownSession(live *CodexAuth) error {
	email := extractEmailFromCodexAuth(live)
	base := "external"
	if email != "" {
		if at := strings.Index(email, "@"); at > 0 {
			base = email[:at]
		}
	}
	base = strings.ToLower(strings.TrimSpace(base))
	name := base
	for i := 2; ; i++ {
		existing, err := p.loadProfile(name)
		if err != nil {
			break
		}
		if sameCodexAccount(existing, live) {
			_ = p.saveProfile(name, live)
			return nil
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return p.saveProfile(name, live)
}

func (p *Provider) FetchStatus(ctx context.Context, account provider.StoredAccount) (provider.AccountStatus, error) {
	p.cacheMu.RLock()
	entry, found := p.cache[account.ID]
	p.cacheMu.RUnlock()
	if found && time.Since(entry.FetchedAt) < 60*time.Second {
		return entry.Status, nil
	}

	auth, err := p.loadProfile(account.ID)
	if err != nil {
		return provider.AccountStatus{
			ID:          account.ID,
			Email:       account.Email,
			Healthy:     false,
			LastChecked: time.Now(),
			Error:       fmt.Sprintf("load profile error: %v", err),
		}, nil
	}

	if !hasCodexCredentials(auth) {
		return provider.AccountStatus{
			ID:          account.ID,
			Email:       account.Email,
			Healthy:     false,
			LastChecked: time.Now(),
			Error:       "no saved credentials (empty placeholder)",
		}, nil
	}

	email := account.Email
	if email == "" {
		email = extractEmailFromCodexAuth(auth)
	}
	plan := extractPlanFromCodexAuth(auth)

	// Fetch live rate limits via codex app-server using the profile's CODEX_HOME
	profileDir := filepath.Join(p.profilesDir, account.ID)
	quotas, rpcPlan, rpcEmail, err := p.fetchRateLimits(ctx, profileDir)
	if err == nil {
		if rpcPlan != "" {
			plan = rpcPlan
		}
		if rpcEmail != "" {
			email = rpcEmail
		}
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
			Status:    status,
			FetchedAt: time.Now(),
		}
		p.cacheMu.Unlock()
		p.saveCacheToDisk()
		return status, nil
	}

	// Fallback when app-server fails
	status := provider.AccountStatus{
		ID:          account.ID,
		Email:       email,
		Plan:        plan,
		Quotas:      nil,
		Healthy:     false,
		LastChecked: time.Now(),
		Error:       fmt.Sprintf("quota fetch error: %v", err),
	}
	return status, nil
}

func (p *Provider) fetchRateLimits(ctx context.Context, codexHome string) ([]provider.Quota, string, string, error) {
	if p.codexBin == "" {
		p.codexBin = FindCodexBinary()
	}
	if p.codexBin == "" {
		return nil, "", "", fmt.Errorf("codex CLI binary not found")
	}

	perReqCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	cmd := exec.CommandContext(perReqCtx, p.codexBin, "app-server", "--stdio")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", "", err
	}

	if err := cmd.Start(); err != nil {
		return nil, "", "", fmt.Errorf("failed to start codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	initReq := AppServerRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]interface{}{
			"clientInfo": map[string]string{
				"name":    "swaper",
				"version": "1.0.0",
			},
		},
	}
	rlReq := AppServerRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "account/rateLimits/read",
		Params:  map[string]interface{}{},
	}

	enc := json.NewEncoder(stdin)
	if err := enc.Encode(initReq); err != nil {
		return nil, "", "", err
	}
	if err := enc.Encode(rlReq); err != nil {
		return nil, "", "", err
	}

	scanner := bufio.NewScanner(stdout)
	now := time.Now()
	for scanner.Scan() {
		line := scanner.Bytes()
		var base struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(line, &base); err != nil {
			continue
		}
		if base.ID == 2 {
			var resp AppServerRateLimitsResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				return nil, "", "", err
			}
			if resp.Error != nil {
				return nil, "", "", fmt.Errorf("app-server error %d: %s", resp.Error.Code, resp.Error.Message)
			}

			var quotas []provider.Quota
			rl := resp.Result.RateLimits
			if p := rl.Primary; p != nil {
				resetTime, resetIn := parseUnixReset(p.ResetsAt, now)
				label := "5-hour"
				if p.WindowDurationMins > 0 && p.WindowDurationMins != 300 {
					label = fmt.Sprintf("%dm", p.WindowDurationMins)
				}
				quotas = append(quotas, provider.Quota{
					Label:       label,
					PercentLeft: float64(100 - p.UsedPercent),
					ResetAt:     resetTime,
					ResetIn:     resetIn,
				})
			}
			if s := rl.Secondary; s != nil {
				resetTime, resetIn := parseUnixReset(s.ResetsAt, now)
				label := "Weekly"
				if s.WindowDurationMins > 0 && s.WindowDurationMins != 10080 {
					label = fmt.Sprintf("%dm", s.WindowDurationMins)
				}
				quotas = append(quotas, provider.Quota{
					Label:       label,
					PercentLeft: float64(100 - s.UsedPercent),
					ResetAt:     resetTime,
					ResetIn:     resetIn,
				})
			}

			plan := formatPlanType(rl.PlanType)
			email := ""
			if resp.Result.Account != nil {
				email = resp.Result.Account.Email
				if resp.Result.Account.PlanType != "" {
					plan = formatPlanType(resp.Result.Account.PlanType)
				}
			}
			return quotas, plan, email, nil
		}
	}
	return nil, "", "", fmt.Errorf("no rate-limit response received from codex app-server")
}

func parseUnixReset(unixSec int64, now time.Time) (time.Time, string) {
	if unixSec <= 0 {
		return time.Time{}, "—"
	}
	t := time.Unix(unixSec, 0)
	if t.Before(now) {
		return t, "now"
	}
	return t, formatResetIn(t.Sub(now))
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

func (p *Provider) IsCLIRunning() (bool, error) {
	out, err := exec.Command("pgrep", "-x", "codex").Output()
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

func (p *Provider) LaunchCLI(account provider.StoredAccount) error {
	bin := p.codexBin
	if bin == "" {
		bin = FindCodexBinary()
	}
	fullPath, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("codex executable not found: %w", err)
	}
	return syscall.Exec(fullPath, []string{bin}, os.Environ())
}

// FindCodexBinary searches known system and application paths for the codex binary.
func FindCodexBinary() string {
	if bin, err := exec.LookPath("codex"); err == nil {
		return bin
	}
	home, _ := os.UserHomeDir()
	cfgData, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err == nil {
		for _, line := range strings.Split(string(cfgData), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "CODEX_CLI_PATH") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					path := strings.Trim(strings.TrimSpace(parts[1]), "\"'\t ")
					if _, err := os.Stat(path); err == nil {
						return path
					}
				}
			}
		}
	}
	candidates := []string{
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
		"/usr/local/bin/codex",
		filepath.Join(home, ".local", "bin", "codex"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "codex"
}
