package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var (
	clientIDPattern     = regexp.MustCompile(`[0-9]{6,}-[A-Za-z0-9_-]+\.apps\.googleusercontent\.com`)
	clientSecretPattern = regexp.MustCompile(`GOCSPX-[A-Za-z0-9_-]{28}`)
	validIDPattern      = regexp.MustCompile(`^[0-9]{6,}-[A-Za-z0-9_-]+\.apps\.googleusercontent\.com$`)
	validSecretPattern  = regexp.MustCompile(`^GOCSPX-[A-Za-z0-9_-]{28}$`)
)

type Config struct {
	RefreshInterval time.Duration `json:"refresh_interval"` // e.g. 60s
	GreenThreshold  float64       `json:"green_threshold"`  // default: 30%
	YellowThreshold float64       `json:"yellow_threshold"` // default: 10%
	CLIBinary       string        `json:"cli_binary"`       // default: agy
	StorageDir      string        `json:"storage_dir"`      // default: ~/.swaper
	// OAuth client for token refresh. Never hardcoded — set via env
	// (SWAPER_GOOGLE_CLIENT_ID / SWAPER_GOOGLE_CLIENT_SECRET) or config.json.
	GoogleClientID     string `json:"google_client_id,omitempty"`
	GoogleClientSecret string `json:"google_client_secret,omitempty"`
}

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		RefreshInterval: 60 * time.Second,
		GreenThreshold:  30.0,
		YellowThreshold: 10.0,
		CLIBinary:       "agy",
		StorageDir:      filepath.Join(home, ".swaper"),
		GoogleClientID:     envFirst("SWAPER_GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_ID"),
		GoogleClientSecret: envFirst("SWAPER_GOOGLE_CLIENT_SECRET", "GOOGLE_CLIENT_SECRET"),
	}
}

func LoadConfig() (Config, error) {
	cfg := DefaultConfig()
	configFile := filepath.Join(cfg.StorageDir, "config.json")
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			_ = SaveConfig(cfg)
		} else {
			return cfg, err
		}
	} else {
		_ = json.Unmarshal(data, &cfg)
		if cfg.GoogleClientID == "" {
			cfg.GoogleClientID = envFirst("SWAPER_GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_ID")
		}
		if cfg.GoogleClientSecret == "" {
			cfg.GoogleClientSecret = envFirst("SWAPER_GOOGLE_CLIENT_SECRET", "GOOGLE_CLIENT_SECRET")
		}
	}
	// Drop cached values with impossible shapes (e.g. from an earlier greedy
	// scan that swallowed trailing bytes) so they get re-extracted below.
	if cfg.GoogleClientID != "" && !validIDPattern.MatchString(cfg.GoogleClientID) {
		cfg.GoogleClientID = ""
	}
	if cfg.GoogleClientSecret != "" && !validSecretPattern.MatchString(cfg.GoogleClientSecret) {
		cfg.GoogleClientSecret = ""
	}
	if cfg.GoogleClientID == "" || cfg.GoogleClientSecret == "" {
		if id, secret := extractFromCLIBinary(cfg.CLIBinary); id != "" && secret != "" {
			if cfg.GoogleClientID == "" {
				cfg.GoogleClientID = id
			}
			if cfg.GoogleClientSecret == "" {
				cfg.GoogleClientSecret = secret
			}
			_ = SaveConfig(cfg)
		}
	}
	return cfg, nil
}

// RememberCredentials persists the working OAuth pair for future boots.
func RememberCredentials(id, secret string) {
	cfg, err := LoadConfig()
	if err != nil {
		return
	}
	if cfg.GoogleClientID == id && cfg.GoogleClientSecret == secret {
		return
	}
	cfg.GoogleClientID = id
	cfg.GoogleClientSecret = secret
	_ = SaveConfig(cfg)
}

var candCacheMu sync.Mutex
var candCache = map[string][][2]string{}

// CandidatePairs returns every deduped (clientID, clientSecret) combination
// found in the provider CLI binary. The binary can embed several (old + new,
// feature-specific), and only the right pairing is accepted by Google, so
// callers should try each in order until one works.
func CandidatePairs(cliBinary string) [][2]string {
	if cliBinary == "" {
		cliBinary = "agy"
	}
	bin, err := exec.LookPath(cliBinary)
	if err != nil {
		if filepath.Base(cliBinary) != cliBinary {
			bin = cliBinary
		} else {
			return nil
		}
	}
	candCacheMu.Lock()
	if cached, ok := candCache[bin]; ok {
		candCacheMu.Unlock()
		return cached
	}
	candCacheMu.Unlock()

	data, err := os.ReadFile(bin)
	if err != nil || len(data) == 0 {
		return nil
	}
	var ids, secrets []string
	seen := map[string]bool{}
	for _, m := range clientIDPattern.FindAll(data, -1) {
		s := string(m)
		if !seen["id:"+s] {
			seen["id:"+s] = true
			ids = append(ids, s)
		}
	}
	for _, m := range clientSecretPattern.FindAll(data, -1) {
		s := string(m)
		if !seen["sec:"+s] {
			seen["sec:"+s] = true
			secrets = append(secrets, s)
		}
	}
	var pairs [][2]string
	for _, id := range ids {
		for _, secret := range secrets {
			pairs = append(pairs, [2]string{id, secret})
		}
	}
	candCacheMu.Lock()
	candCache[bin] = pairs
	candCacheMu.Unlock()
	return pairs
}

// extractFromCLIBinary scans the provider CLI binary for its embedded Google
// OAuth client credentials (same for every user) so users never configure them.
func extractFromCLIBinary(cliBinary string) (id, secret string) {
	if cliBinary == "" {
		cliBinary = "agy"
	}
	bin, err := exec.LookPath(cliBinary)
	if err != nil {
		if filepath.Base(cliBinary) != cliBinary {
			bin = cliBinary
		} else {
			return "", ""
		}
	}
	data, err := os.ReadFile(bin)
	if err != nil || len(data) == 0 {
		return "", ""
	}
	return clientIDPattern.FindString(string(data)), clientSecretPattern.FindString(string(data))
}

func SaveConfig(cfg Config) error {
	if err := os.MkdirAll(cfg.StorageDir, 0700); err != nil {
		return err
	}
	configFile := filepath.Join(cfg.StorageDir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, data, 0600)
}
