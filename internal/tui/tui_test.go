package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"swaper/internal/config"
	"swaper/internal/provider"
)

func TestRenderAccountCardSpacing(t *testing.T) {
	m := Model{
		statuses: map[string]provider.AccountStatus{
			"main": {
				ID:      "main",
				Email:   "tomer@gmail.com",
				Plan:    "Free",
				Healthy: true,
				Quotas: []provider.Quota{
					{Label: "Gemini 5h", PercentLeft: 75, ResetIn: "1h 18m"},
					{Label: "Gemini Weekly", PercentLeft: 88, ResetIn: "4d 20h"},
					{Label: "Other 5h", PercentLeft: 100, ResetIn: "4h 59m"},
					{Label: "Other Weekly", PercentLeft: 100, ResetIn: "6d 23h"},
				},
			},
		},
		width: 120,
	}
	acc := provider.StoredAccount{ID: "main", IsActive: true}
	card := m.renderAccountCard(acc, true)

	if !strings.Contains(card, "Other 5h") {
		t.Errorf("expected card to contain 'Other 5h', got:\n%s", card)
	}
	if !strings.Contains(card, "Other Weekly") {
		t.Errorf("expected card to contain 'Other Weekly', got:\n%s", card)
	}

	// Verify that the quota lines are separated by a space (blank line)
	lines := strings.Split(card, "\n")
	var geminiLineIdx, otherLineIdx int
	for idx, line := range lines {
		if strings.Contains(line, "Gemini 5h") {
			geminiLineIdx = idx
		}
		if strings.Contains(line, "Other 5h") {
			otherLineIdx = idx
		}
	}
	if otherLineIdx <= geminiLineIdx+1 {
		t.Errorf("expected blank line between quota rows (gemini line %d, other line %d), got:\n%s",
			geminiLineIdx, otherLineIdx, card)
	}
}

type dummyProvider struct {
	id   string
	name string
}

func (d *dummyProvider) ID() string                                                               { return d.id }
func (d *dummyProvider) DisplayName() string                                                      { return d.name }
func (d *dummyProvider) CLIBinary() string                                                        { return d.id }
func (d *dummyProvider) ListAccounts() ([]provider.StoredAccount, error)                           { return nil, nil }
func (d *dummyProvider) FetchStatus(ctx context.Context, acc provider.StoredAccount) (provider.AccountStatus, error) {
	return provider.AccountStatus{}, nil
}
func (d *dummyProvider) CachedStatus(id string) (provider.AccountStatus, bool)                    { return provider.AccountStatus{}, false }
func (d *dummyProvider) SwitchTo(acc provider.StoredAccount) error                                { return nil }
func (d *dummyProvider) ImportCurrent(id, email string) (*provider.StoredAccount, error)          { return nil, nil }
func (d *dummyProvider) AddNew(id, email string) (*provider.StoredAccount, error)                 { return nil, nil }
func (d *dummyProvider) RemoveAccount(id string) error                                            { return nil }
func (d *dummyProvider) RenameAccount(oldID, newID string) error                                  { return nil }
func (d *dummyProvider) ActiveAccount() (*provider.StoredAccount, error)                          { return nil, nil }
func (d *dummyProvider) IsCLIRunning() (bool, error)                                              { return false, nil }
func (d *dummyProvider) LaunchCLI(acc provider.StoredAccount) error                               { return nil }
func (d *dummyProvider) InvalidateCache()                                                         {}

func TestTabSwitchesProviderWithoutStatusText(t *testing.T) {
	p1 := &dummyProvider{id: "p1", name: "Provider 1"}
	p2 := &dummyProvider{id: "p2", name: "Provider 2"}

	m := NewModel(config.Config{}, p1, p2)
	m.statusMsg = "some prior message"

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := newM.(Model)

	if updated.provider.ID() != "p2" {
		t.Fatalf("expected active provider to be 'p2', got '%s'", updated.provider.ID())
	}
	if updated.statusMsg != "" {
		t.Errorf("expected statusMsg to be empty when switching provider pages, got: %q", updated.statusMsg)
	}

	view := updated.View()
	if strings.Contains(view, "Switched provider") {
		t.Errorf("expected View() not to contain 'Switched provider', got:\n%s", view)
	}
}

