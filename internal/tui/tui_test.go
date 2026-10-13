package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	id             string
	name           string
	removedAccount string
	importedName   string
	importedEmail  string
	noLoginCmd     bool
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
func (d *dummyProvider) ImportCurrent(id, email string) (*provider.StoredAccount, error) {
	d.importedName = id
	d.importedEmail = email
	return &provider.StoredAccount{ID: id, Email: email, IsActive: true}, nil
}
func (d *dummyProvider) AddNew(id, email string) (*provider.StoredAccount, error)                 { return nil, nil }
func (d *dummyProvider) RemoveAccount(id string) error {
	d.removedAccount = id
	return nil
}
func (d *dummyProvider) RenameAccount(oldID, newID string) error                                  { return nil }
func (d *dummyProvider) ActiveAccount() (*provider.StoredAccount, error)                          { return nil, nil }
func (d *dummyProvider) IsCLIRunning() (bool, error)                                              { return false, nil }
func (d *dummyProvider) LaunchCLI(acc provider.StoredAccount) error                               { return nil }
func (d *dummyProvider) LoginCommand() []string {
	if d.noLoginCmd {
		return nil
	}
	return []string{"true"}
}
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

type dummyLauncherProvider struct {
	dummyProvider
	launchErr error
	launched  bool
}

func (d *dummyLauncherProvider) LaunchApp() error {
	d.launched = true
	return d.launchErr
}

func (d *dummyLauncherProvider) AppDisplayName() string {
	return "Dummy App"
}

func TestShiftOLaunchesDesktopApp(t *testing.T) {
	lp := &dummyLauncherProvider{
		dummyProvider: dummyProvider{id: "p1", name: "Provider 1"},
	}
	m := NewModel(config.Config{}, lp)

	// Verify hint mentions "O: app"
	view := m.View()
	if !strings.Contains(view, "O: app") {
		t.Errorf("expected View() to show 'O: app' hint, got:\n%s", view)
	}

	// Press 'O' (Shift+O)
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	updated := newM.(Model)

	if !lp.launched {
		t.Errorf("expected LaunchApp to be called")
	}
	if !strings.Contains(updated.statusMsg, "Launched Dummy App ✓") {
		t.Errorf("expected status message to contain 'Launched Dummy App ✓', got %q", updated.statusMsg)
	}
}

func TestShiftONoDesktopApp(t *testing.T) {
	p := &dummyProvider{id: "p1", name: "Provider 1"}
	m := NewModel(config.Config{}, p)

	// Verify hint mentions "o: open" without "O: app"
	view := m.View()
	if strings.Contains(view, "O: app") {
		t.Errorf("expected View() not to show 'O: app' hint, got:\n%s", view)
	}

	// Press 'O'
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	updated := newM.(Model)

	if !strings.Contains(updated.statusMsg, "No desktop app available") {
		t.Errorf("expected 'No desktop app available', got %q", updated.statusMsg)
	}
}

type dummyDetailedLauncherProvider struct {
	dummyProvider
	launchMsg string
	launchErr error
	called    bool
}

func (d *dummyDetailedLauncherProvider) LaunchApp() error {
	d.called = true
	return d.launchErr
}

func (d *dummyDetailedLauncherProvider) LaunchAppDetailed() (string, error) {
	d.called = true
	return d.launchMsg, d.launchErr
}

func (d *dummyDetailedLauncherProvider) AppDisplayName() string {
	return "Smart App"
}

func TestShiftODetailedAppLauncherSuccess(t *testing.T) {
	dlp := &dummyDetailedLauncherProvider{
		dummyProvider: dummyProvider{id: "p1", name: "Provider 1"},
		launchMsg:     "Restarted Smart App with active account (work) ✓",
	}
	m := NewModel(config.Config{}, dlp)

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	updated := newM.(Model)

	if !dlp.called {
		t.Errorf("expected LaunchAppDetailed to be called")
	}
	if updated.statusMsg != "Restarted Smart App with active account (work) ✓" {
		t.Errorf("expected exact status message, got %q", updated.statusMsg)
	}
}

func TestShiftODetailedAppLauncherError(t *testing.T) {
	dlp := &dummyDetailedLauncherProvider{
		dummyProvider: dummyProvider{id: "p1", name: "Provider 1"},
		launchErr:     fmt.Errorf("cannot restart: tool is executing"),
	}
	m := NewModel(config.Config{}, dlp)

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	updated := newM.(Model)

	if !dlp.called {
		t.Errorf("expected LaunchAppDetailed to be called")
	}
	if !strings.Contains(updated.statusMsg, "Failed to launch Smart App: cannot restart: tool is executing") {
		t.Errorf("expected error in status message, got %q", updated.statusMsg)
	}
}

func TestDeleteAccountModalAndConfirm(t *testing.T) {
	dp := &dummyProvider{id: "p1", name: "Provider 1"}
	m := NewModel(config.Config{}, dp)
	m.accounts = []provider.StoredAccount{
		{ID: "personal", Email: "personal@example.com"},
		{ID: "work", Email: "work@example.com"},
	}
	m.cursor = 1 // select "work"

	// Press 'd'
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	updated := newM.(Model)

	if !updated.confirmDelete {
		t.Fatalf("expected confirmDelete to be true")
	}
	if updated.deleteTarget == nil || updated.deleteTarget.ID != "work" {
		t.Fatalf("expected deleteTarget to be 'work', got: %v", updated.deleteTarget)
	}

	view := updated.View()
	if !strings.Contains(view, "Delete Account Confirmation") {
		t.Errorf("expected view to contain 'Delete Account Confirmation', got:\n%s", view)
	}
	if !strings.Contains(view, "work") {
		t.Errorf("expected view to contain target profile 'work', got:\n%s", view)
	}
	if !strings.Contains(view, "[y] Confirm Delete") {
		t.Errorf("expected view to contain confirm hints, got:\n%s", view)
	}

	// Confirm with 'y'
	confirmedM, _ := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	postDelete := confirmedM.(Model)

	if postDelete.confirmDelete {
		t.Errorf("expected confirmDelete to be false after confirmation")
	}
	if dp.removedAccount != "work" {
		t.Errorf("expected provider.RemoveAccount to be called with 'work', got: %q", dp.removedAccount)
	}
	if !strings.Contains(postDelete.statusMsg, "Removed profile 'work' ✓") {
		t.Errorf("expected statusMsg to show removal, got: %q", postDelete.statusMsg)
	}
}

func TestDeleteAccountCancel(t *testing.T) {
	dp := &dummyProvider{id: "p1", name: "Provider 1"}
	m := NewModel(config.Config{}, dp)
	m.accounts = []provider.StoredAccount{
		{ID: "personal", Email: "personal@example.com"},
	}
	m.cursor = 0

	// Press 'x'
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	updated := newM.(Model)

	if !updated.confirmDelete {
		t.Fatalf("expected confirmDelete to be true")
	}

	// Cancel with 'n'
	cancelM, _ := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	postCancel := cancelM.(Model)

	if postCancel.confirmDelete {
		t.Errorf("expected confirmDelete to be false")
	}
	if dp.removedAccount != "" {
		t.Errorf("expected provider.RemoveAccount not to be called, got %q", dp.removedAccount)
	}
	if !strings.Contains(postCancel.statusMsg, "cancelled") {
		t.Errorf("expected statusMsg to indicate cancelled, got %q", postCancel.statusMsg)
	}
}

func TestHelpModalToggle(t *testing.T) {
	dp := &dummyProvider{id: "p1", name: "Provider 1"}
	m := NewModel(config.Config{}, dp)

	// Press '?'
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	updated := newM.(Model)

	if !updated.showHelp {
		t.Fatalf("expected showHelp to be true")
	}

	view := updated.View()
	if !strings.Contains(view, "swaper — Keybindings & Help") {
		t.Errorf("expected help modal title, got:\n%s", view)
	}
	if !strings.Contains(view, "Navigation & Selection") {
		t.Errorf("expected Navigation section, got:\n%s", view)
	}
	if !strings.Contains(view, "Account Management") {
		t.Errorf("expected Account Management section, got:\n%s", view)
	}
	if !strings.Contains(view, "Delete selected profile") {
		t.Errorf("expected delete shortcut description, got:\n%s", view)
	}

	// Press '?' again to dismiss
	closedM, _ := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	postClose := closedM.(Model)
	if postClose.showHelp {
		t.Errorf("expected showHelp to be false after toggle")
	}

	// Open and close with 'esc'
	reopenedM, _ := postClose.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	withEscClose, _ := reopenedM.(Model).Update(tea.KeyMsg{Type: tea.KeyEscape})
	if withEscClose.(Model).showHelp {
		t.Errorf("expected showHelp to be false after esc")
	}
}

func TestSearchAndFilterRealTime(t *testing.T) {
	dp := &dummyProvider{id: "p1", name: "Provider 1"}
	m := NewModel(config.Config{}, dp)
	m.accounts = []provider.StoredAccount{
		{ID: "alpha", Email: "alice@work.com"},
		{ID: "beta", Email: "bob@personal.com"},
		{ID: "gamma", Email: "charlie@work.com"},
	}

	// Press '/' to enter search mode
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	mSearch := newM.(Model)
	if !mSearch.searchMode {
		t.Fatalf("expected searchMode to be true")
	}

	// Type 'b' then 'e' then 't'
	for _, ch := range "bet" {
		nm, _ := mSearch.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		mSearch = nm.(Model)
	}

	if mSearch.filterQuery != "bet" {
		t.Errorf("expected filterQuery to be 'bet', got %q", mSearch.filterQuery)
	}

	filtered := mSearch.FilteredAccounts()
	if len(filtered) != 1 || filtered[0].ID != "beta" {
		t.Fatalf("expected only 'beta' to match, got: %v", filtered)
	}

	view := mSearch.View()
	if !strings.Contains(view, "beta") {
		t.Errorf("expected view to contain 'beta', got:\n%s", view)
	}
	if strings.Contains(view, "alpha") {
		t.Errorf("expected view not to contain 'alpha' while filtered, got:\n%s", view)
	}

	// Press Enter to confirm search query
	confirmedM, _ := mSearch.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mConfirmed := confirmedM.(Model)
	if mConfirmed.searchMode {
		t.Errorf("expected searchMode to be false after Enter")
	}
	if mConfirmed.filterQuery != "bet" {
		t.Errorf("expected filterQuery to remain 'bet', got %q", mConfirmed.filterQuery)
	}

	// Press Esc to clear filter
	clearedM, _ := mConfirmed.Update(tea.KeyMsg{Type: tea.KeyEscape})
	mCleared := clearedM.(Model)
	if mCleared.filterQuery != "" {
		t.Errorf("expected filterQuery to be empty after Esc, got %q", mCleared.filterQuery)
	}
	if len(mCleared.FilteredAccounts()) != 3 {
		t.Errorf("expected all 3 accounts to be present, got %d", len(mCleared.FilteredAccounts()))
	}
}

func TestAddAndLoginPromptAndCompletion(t *testing.T) {
	dp := &dummyProvider{id: "p1", name: "Provider 1"}
	m := NewModel(config.Config{}, dp)

	// Press 'a'
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	updated := newM.(Model)

	if !updated.inputMode || updated.inputAction != "add_login" {
		t.Fatalf("expected inputMode=true and action='add_login', got mode=%v action=%q", updated.inputMode, updated.inputAction)
	}

	view := updated.View()
	if !strings.Contains(view, "Enter profile name") {
		t.Errorf("expected prompt text in view, got:\n%s", view)
	}

	// Simulate login completion
	doneM, _ := updated.Update(loginDoneMsg{name: "dev-account", err: nil})
	postLogin := doneM.(Model)

	if dp.importedName != "dev-account" {
		t.Errorf("expected provider.ImportCurrent to be called with 'dev-account', got %q", dp.importedName)
	}
	if !strings.Contains(postLogin.statusMsg, "Added and logged in as 'dev-account' ✓") {
		t.Errorf("expected success status message, got %q", postLogin.statusMsg)
	}
}

func TestAddWhenNoLoginCommand(t *testing.T) {
	dp := &dummyProvider{id: "agy", name: "antigravity", noLoginCmd: true}
	m := NewModel(config.Config{}, dp)

	// Press 'a'
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	updated := newM.(Model)

	if updated.inputMode {
		t.Fatalf("expected inputMode=false when no login command, got true")
	}
	if !strings.Contains(updated.statusMsg, "has no CLI login command") {
		t.Errorf("expected statusMsg to explain missing login command, got %q", updated.statusMsg)
	}
}

func TestAggregateQuotaSummaryHeader(t *testing.T) {
	dp := &dummyProvider{id: "p1", name: "Provider 1"}
	m := NewModel(config.Config{}, dp)
	m.accounts = []provider.StoredAccount{
		{ID: "acc1", Email: "acc1@gmail.com"},
		{ID: "acc2", Email: "acc2@gmail.com"},
		{ID: "acc3", Email: "acc3@gmail.com"},
	}
	m.statuses = map[string]provider.AccountStatus{
		"acc1": {
			ID:      "acc1",
			Healthy: true,
			Quotas: []provider.Quota{
				{Label: "Gemini 5h", PercentLeft: 100}, // 5.0h
				{Label: "Gemini Weekly", PercentLeft: 90},
			},
		},
		"acc2": {
			ID:      "acc2",
			Healthy: true,
			Quotas: []provider.Quota{
				{Label: "Gemini 5h", PercentLeft: 100}, // 5.0h
			},
		},
		"acc3": {
			ID:      "acc3",
			Healthy: true,
			Quotas: []provider.Quota{
				{Label: "Gemini 5h", PercentLeft: 90}, // 4.5h
			},
		},
	}

	summary := m.AggregateQuotaSummary()
	expected := "3 active profiles • Combined: ~14.5 hours of continuous coding quota remaining"
	if summary != expected {
		t.Errorf("expected %q, got %q", expected, summary)
	}

	view := m.View()
	if !strings.Contains(view, expected) {
		t.Errorf("expected View() to contain summary %q, got:\n%s", expected, view)
	}
}

func TestAggregateQuotaSummarySingleAndLoading(t *testing.T) {
	dp := &dummyProvider{id: "p1", name: "Provider 1"}
	m := NewModel(config.Config{}, dp)
	m.accounts = []provider.StoredAccount{
		{ID: "single", Email: "single@gmail.com"},
	}

	// While loading
	m.loading["single"] = true
	loadingSummary := m.AggregateQuotaSummary()
	if !strings.Contains(loadingSummary, "Calculating aggregate quota...") {
		t.Errorf("expected loading message, got %q", loadingSummary)
	}

	// Once status arrives with 50% on 5-hour
	m.loading["single"] = false
	m.statuses["single"] = provider.AccountStatus{
		ID:      "single",
		Healthy: true,
		Quotas: []provider.Quota{
			{Label: "5-hour", PercentLeft: 50}, // 2.5h
		},
	}

	singleSummary := m.AggregateQuotaSummary()
	expected := "1 active profile • Combined: ~2.5 hours of continuous coding quota remaining"
	if singleSummary != expected {
		t.Errorf("expected %q, got %q", expected, singleSummary)
	}
}

func TestHeaderProviderTabsAndHintsAntigravityAndOpenAI(t *testing.T) {
	pAgy := &dummyDetailedLauncherProvider{
		dummyProvider: dummyProvider{id: "antigravity", name: "antigravity"},
	}
	pOpenAI := &dummyDetailedLauncherProvider{
		dummyProvider: dummyProvider{id: "openai", name: "openai"},
	}

	m := NewModel(config.Config{}, pAgy, pOpenAI)
	m.width = 100

	view := m.View()
	firstLine := strings.Split(view, "\n")[0]

	// Verify Antigravity is active tab and OpenAI is inactive
	if !strings.Contains(firstLine, "[● Antigravity]") {
		t.Errorf("expected active tab '[● Antigravity]', got:\n%s", firstLine)
	}
	if !strings.Contains(firstLine, "○ OpenAI") {
		t.Errorf("expected inactive tab '○ OpenAI', got:\n%s", firstLine)
	}
	if !strings.Contains(firstLine, "O: app") {
		t.Errorf("expected 'O: app' hint, got:\n%s", firstLine)
	}
	if !strings.Contains(firstLine, "o: cli") {
		t.Errorf("expected 'o: cli' hint, got:\n%s", firstLine)
	}
	if !strings.Contains(firstLine, "?: help") {
		t.Errorf("expected '?: help' hint, got:\n%s", firstLine)
	}

	// Press Tab to switch to OpenAI
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	mOpenAI := newM.(Model)
	viewOpenAI := mOpenAI.View()
	firstLineOpenAI := strings.Split(viewOpenAI, "\n")[0]

	// Verify OpenAI is active tab and Antigravity is inactive
	if !strings.Contains(firstLineOpenAI, "[● OpenAI]") {
		t.Errorf("expected active tab '[● OpenAI]', got:\n%s", firstLineOpenAI)
	}
	if !strings.Contains(firstLineOpenAI, "○ Antigravity") {
		t.Errorf("expected inactive tab '○ Antigravity', got:\n%s", firstLineOpenAI)
	}
	if !strings.Contains(firstLineOpenAI, "O: app") {
		t.Errorf("expected 'O: app' hint on OpenAI page, got:\n%s", firstLineOpenAI)
	}
	if !strings.Contains(firstLineOpenAI, "o: cli") {
		t.Errorf("expected 'o: cli' hint on OpenAI page, got:\n%s", firstLineOpenAI)
	}
}

func TestHeaderWidthResponsiveDoesNotOverflow(t *testing.T) {
	pAgy := &dummyDetailedLauncherProvider{
		dummyProvider: dummyProvider{id: "antigravity", name: "antigravity"},
	}
	pOpenAI := &dummyDetailedLauncherProvider{
		dummyProvider: dummyProvider{id: "openai", name: "openai"},
	}

	for _, width := range []int{80, 90, 100, 120} {
		m := NewModel(config.Config{}, pAgy, pOpenAI)
		m.width = width
		view := m.View()
		firstLine := strings.Split(view, "\n")[0]

		lineWidth := lipgloss.Width(firstLine)
		if lineWidth > width {
			t.Errorf("at width %d, header line width is %d which overflows: %q", width, lineWidth, firstLine)
		}

		if !strings.Contains(firstLine, "O: app") {
			t.Errorf("at width %d, expected 'O: app' to remain visible, got: %q", width, firstLine)
		}
	}
}



