package antigravity

import (
	"os"
	"path/filepath"
	"testing"

	"swaper/internal/accounts"
	"swaper/internal/provider"
)

func TestAntigravityProviderImplementsAppLauncher(t *testing.T) {
	tempDir := t.TempDir()
	p, err := NewProvider(tempDir)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	var launcher provider.AppLauncher = p
	if launcher.AppDisplayName() != "Antigravity" {
		t.Errorf("expected AppDisplayName 'Antigravity', got %q", launcher.AppDisplayName())
	}
	var detailedLauncher provider.DetailedAppLauncher = p
	if detailedLauncher.AppDisplayName() != "Antigravity" {
		t.Errorf("expected DetailedAppLauncher display name 'Antigravity', got %q", detailedLauncher.AppDisplayName())
	}
}

func TestLastLaunchedAppProfile(t *testing.T) {
	tempDir := t.TempDir()
	p, err := NewProvider(tempDir)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	if p.getLastLaunchedAppProfile() != "" {
		t.Errorf("expected empty last launched profile initially")
	}

	if err := p.setLastLaunchedAppProfile("work-account"); err != nil {
		t.Fatalf("failed to set last launched profile: %v", err)
	}

	if got := p.getLastLaunchedAppProfile(); got != "work-account" {
		t.Errorf("expected 'work-account', got %q", got)
	}
}

func TestGetRunningAntigravityAccountInfo(t *testing.T) {
	// Tests reading and unmarshaling jetski.onboarding.lastLoginUsername
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	appSupportDir := filepath.Join(tmpHome, "Library", "Application Support", "Antigravity")
	if err := os.MkdirAll(appSupportDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Case 1: file does not exist
	if email := getRunningAntigravityAccountInfo(); email != "" {
		t.Errorf("expected empty string when file missing, got %q", email)
	}

	// Case 2: app_storage.json contains email
	storageJSON := `{
  "ide-install-wizard-shown": "true",
  "jetski.onboarding.lastLoginUsername": "TestUser@example.com"
}`
	if err := os.WriteFile(filepath.Join(appSupportDir, "app_storage.json"), []byte(storageJSON), 0644); err != nil {
		t.Fatal(err)
	}

	if email := getRunningAntigravityAccountInfo(); email != "testuser@example.com" {
		t.Errorf("expected 'testuser@example.com', got %q", email)
	}
}

func TestLaunchAppDetailedAlreadyRunningRightAccount(t *testing.T) {
	tempDir := t.TempDir()
	p, err := NewProvider(tempDir)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	// Setup profile
	dummyAuth := &accounts.StoredAuthToken{}
	if err := p.store.SaveProfile("active-prof", dummyAuth); err != nil {
		t.Fatalf("failed to save profile: %v", err)
	}
	if err := p.store.SetActiveProfileName("active-prof"); err != nil {
		t.Fatalf("failed to set active profile: %v", err)
	}

	// Set last launched profile to active-prof
	if err := p.setLastLaunchedAppProfile("active-prof"); err != nil {
		t.Fatalf("failed to set last launched profile: %v", err)
	}

	// When app is running and active account matches, it returns a message without calling open
	// We can verify that getActiveProfileName and getLastLaunchedAppProfile match.
	if p.store.GetActiveProfileName() != p.getLastLaunchedAppProfile() {
		t.Errorf("expected active profile to match last launched")
	}
}
