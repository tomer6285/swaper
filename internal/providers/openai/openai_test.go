package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swaper/internal/provider"
)

func createTestJWT(claims map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".sig"
}

func TestExtractClaimsAndPlan(t *testing.T) {
	jwt := createTestJWT(map[string]interface{}{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_plan_type": "plus",
		},
	})

	auth := &CodexAuth{
		AuthMode: "chatgpt",
		Tokens: CodexTokens{
			IDToken: jwt,
		},
	}

	email := extractEmailFromCodexAuth(auth)
	if email != "user@example.com" {
		t.Errorf("got email %q, want user@example.com", email)
	}

	plan := extractPlanFromCodexAuth(auth)
	if plan != "Plus" {
		t.Errorf("got plan %q, want Plus", plan)
	}
}

func TestOpenAIProviderLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	p, err := NewProvider(tmpDir)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	// 1. Initial list should be empty
	accs, err := p.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if len(accs) != 0 {
		t.Fatalf("expected 0 accounts, got %d", len(accs))
	}

	// 2. Add placeholder account
	placeholder, err := p.AddNew("work", "work@example.com")
	if err != nil {
		t.Fatalf("AddNew failed: %v", err)
	}
	if placeholder.ID != "work" {
		t.Errorf("expected ID 'work', got %q", placeholder.ID)
	}

	// 3. Setup mock live auth in custom codexHome
	mockCodexHome := filepath.Join(tmpDir, "mock_codex")
	p.codexHome = mockCodexHome
	_ = os.MkdirAll(mockCodexHome, 0700)

	liveAuth := &CodexAuth{
		AuthMode: "chatgpt",
		Tokens: CodexTokens{
			AccountID:    "acc-123",
			RefreshToken: "ref-123",
			IDToken: createTestJWT(map[string]interface{}{
				"email": "live@example.com",
				"https://api.openai.com/auth": map[string]interface{}{
					"chatgpt_plan_type": "pro",
				},
			}),
		},
	}
	liveData, _ := json.Marshal(liveAuth)
	_ = os.WriteFile(filepath.Join(mockCodexHome, "auth.json"), liveData, 0600)

	// 4. Import current session
	imported, err := p.ImportCurrent("personal", "")
	if err != nil {
		t.Fatalf("ImportCurrent failed: %v", err)
	}
	if imported.ID != "personal" || imported.Email != "live@example.com" {
		t.Errorf("unexpected imported account: %+v", imported)
	}

	// 5. Verify active account
	active, err := p.ActiveAccount()
	if err != nil {
		t.Fatalf("ActiveAccount failed: %v", err)
	}
	if active == nil || active.ID != "personal" {
		t.Errorf("expected active account personal, got %+v", active)
	}

	// 6. Rename placeholder
	if err := p.RenameAccount("work", "office"); err != nil {
		t.Fatalf("RenameAccount failed: %v", err)
	}
	accs, _ = p.ListAccounts()
	foundOffice := false
	for _, a := range accs {
		if a.ID == "office" {
			foundOffice = true
		}
		if a.ID == "work" {
			t.Errorf("old profile 'work' still exists")
		}
	}
	if !foundOffice {
		t.Errorf("renamed profile 'office' not found")
	}

	// 7. Remove account
	if err := p.RemoveAccount("office"); err != nil {
		t.Fatalf("RemoveAccount failed: %v", err)
	}
	accs, _ = p.ListAccounts()
	if len(accs) != 1 || accs[0].ID != "personal" {
		t.Errorf("expected only 'personal' remaining, got %+v", accs)
	}

	// 8. Switching to empty placeholder fails
	_, _ = p.AddNew("empty", "")
	err = p.SwitchTo(provider.StoredAccount{ID: "empty"})
	if err == nil {
		t.Errorf("expected error switching to empty placeholder")
	}

	// 9. FetchStatus fallback when app-server is unavailable
	p.codexBin = "nonexistent_binary_xyz"
	st, err := p.FetchStatus(context.Background(), provider.StoredAccount{ID: "personal"})
	if err != nil {
		t.Fatalf("FetchStatus returned unexpected hard error: %v", err)
	}
	if st.Email != "live@example.com" {
		t.Errorf("got email %q, want live@example.com", st.Email)
	}
	if st.Plan != "Pro" {
		t.Errorf("got plan %q, want Pro", st.Plan)
	}
}

func TestFormatPlanType(t *testing.T) {
	cases := map[string]string{
		"free":       "Free",
		"plus":       "Plus",
		"pro":        "Pro",
		"team":       "Team",
		"business":   "Business",
		"enterprise": "Enterprise",
		"edu":        "Edu",
		"custom":     "Custom",
		"":           "Standard",
	}
	for in, want := range cases {
		if got := formatPlanType(in); got != want {
			t.Errorf("formatPlanType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUnixReset(t *testing.T) {
	now := time.Now()
	_, s1 := parseUnixReset(0, now)
	if s1 != "—" {
		t.Errorf("expected —, got %q", s1)
	}

	future := now.Add(2 * time.Hour).Unix()
	_, s2 := parseUnixReset(future, now)
	if s2 != "2h 0m" && s2 != "1h 59m" && s2 != "2h" {
		t.Errorf("unexpected reset time string: %q", s2)
	}
}
