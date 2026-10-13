package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"swaper/internal/config"
	"swaper/internal/provider"
)

// Styling constants
var (
	subtleColor        = lipgloss.AdaptiveColor{Light: "#888888", Dark: "#777777"}
	titleStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	activeStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00FFA3"))
	inactiveStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#CCCCCC"))
	barFullStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676"))
	barMidStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD600"))
	barLowStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF1744"))
	statusStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E5FF")).Italic(true)
	summaryHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E5FF")).Bold(true)
	dimStyle           = lipgloss.NewStyle().Foreground(subtleColor)
	boxStyle           = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#444466")).Padding(0, 1)
	selectedBox        = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7D56F4")).Padding(0, 1)
)

// Messages
type accountsLoadedMsg struct {
	accounts []provider.StoredAccount
	err      error
}

type statusFetchedMsg struct {
	id     string
	status provider.AccountStatus
}

type tickMsg time.Time

type switchDoneMsg struct {
	account provider.StoredAccount
	err     error
}

type loginDoneMsg struct {
	name string
	err  error
}

type clearStatusMsg struct {
	id int
}

type Model struct {
	provider    provider.Provider
	providers   []provider.Provider
	providerIdx int
	cfg         config.Config

	accounts []provider.StoredAccount
	statuses map[string]provider.AccountStatus
	loading  map[string]bool

	cursor      int
	statusMsg   string
	statusMsgID int
	quitting    bool
	launching   bool

	confirmSwitch bool
	pendingSwitch *provider.StoredAccount

	confirmDelete bool
	deleteTarget  *provider.StoredAccount

	showHelp bool

	searchMode  bool
	filterQuery string

	inputMode    bool
	inputPrompt  string
	inputValue   string
	inputAction  string
	renameTarget string

	width  int
	height int
}

func NewModel(cfg config.Config, providers ...provider.Provider) Model {
	if len(providers) == 0 {
		panic("at least one provider required")
	}
	return Model{
		provider:    providers[0],
		providers:   providers,
		providerIdx: 0,
		cfg:         cfg,
		statuses:    make(map[string]provider.AccountStatus),
		loading:     make(map[string]bool),
		cursor:      0,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadAccountsCmd(),
		m.tickCmd(),
	)
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(m.cfg.RefreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) loadAccountsCmd() tea.Cmd {
	return func() tea.Msg {
		accs, err := m.provider.ListAccounts()
		return accountsLoadedMsg{accounts: accs, err: err}
	}
}

func (m Model) fetchStatusCmd(acc provider.StoredAccount) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		st, _ := m.provider.FetchStatus(ctx, acc)
		return statusFetchedMsg{id: acc.ID, status: st}
	}
}

func (m Model) switchAccountCmd(acc provider.StoredAccount) tea.Cmd {
	return func() tea.Msg {
		err := m.provider.SwitchTo(acc)
		return switchDoneMsg{account: acc, err: err}
	}
}

func (m Model) clearStatusTimerCmd(id int, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return clearStatusMsg{id: id}
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		var cmds []tea.Cmd
		for _, acc := range m.accounts {
			m.loading[acc.ID] = true
			cmds = append(cmds, m.fetchStatusCmd(acc))
		}
		cmds = append(cmds, m.tickCmd())
		return m, tea.Batch(cmds...)

	case accountsLoadedMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Error listing accounts: %v", msg.err)
			return m, nil
		}
		m.accounts = msg.accounts
		if len(m.accounts) == 0 {
			// Auto import current if no profiles exist
			imported, err := m.provider.ImportCurrent("default", "")
			if err == nil && imported != nil {
				m.accounts = []provider.StoredAccount{*imported}
				m.statusMsg = "Auto-imported active session as 'default'"
			}
		}
		filtered := m.FilteredAccounts()
		if m.cursor >= len(filtered) {
			m.cursor = max(0, len(filtered)-1)
		}

		var cmds []tea.Cmd
		for _, acc := range m.accounts {
			if st, ok := m.provider.CachedStatus(acc.ID); ok {
				m.statuses[acc.ID] = st
			}
			m.loading[acc.ID] = true
			cmds = append(cmds, m.fetchStatusCmd(acc))
		}
		return m, tea.Batch(cmds...)

	case clearStatusMsg:
		if m.statusMsgID == msg.id {
			m.statusMsg = ""
		}
		return m, nil

	case statusFetchedMsg:
		m.loading[msg.id] = false
		m.statuses[msg.id] = msg.status

		// Check if any accounts are still loading
		anyLoading := false
		for _, l := range m.loading {
			if l {
				anyLoading = true
				break
			}
		}
		if !anyLoading && strings.HasPrefix(m.statusMsg, "Refreshing") {
			m.statusMsg = "Quota refreshed ✓"
			m.statusMsgID++
			return m, m.clearStatusTimerCmd(m.statusMsgID, 3*time.Second)
		}
		return m, nil

	case switchDoneMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Switch failed: %v", msg.err)
		} else {
			m.statusMsg = fmt.Sprintf("Switched to %s ✓ (%s is now active)", msg.account.ID, m.provider.CLIBinary())
			m.statusMsgID++
			// reload accounts state
			return m, tea.Batch(m.loadAccountsCmd(), m.clearStatusTimerCmd(m.statusMsgID, 4*time.Second))
		}
		return m, nil

	case loginDoneMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Login cancelled or failed: %v", msg.err)
			m.statusMsgID++
			return m, m.clearStatusTimerCmd(m.statusMsgID, 4*time.Second)
		}
		imported, err := m.provider.ImportCurrent(msg.name, "")
		if err != nil {
			m.statusMsg = fmt.Sprintf("Import error: %v", err)
			m.statusMsgID++
			return m, m.clearStatusTimerCmd(m.statusMsgID, 5*time.Second)
		}
		m.statusMsg = fmt.Sprintf("Added and logged in as '%s' ✓", imported.ID)
		m.statusMsgID++
		return m, tea.Batch(m.loadAccountsCmd(), m.clearStatusTimerCmd(m.statusMsgID, 4*time.Second))

	case tea.KeyMsg:
		if m.showHelp {
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			default:
				m.showHelp = false
				return m, nil
			}
		}

		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y":
				m.confirmDelete = false
				if m.deleteTarget != nil {
					target := *m.deleteTarget
					m.deleteTarget = nil
					if err := m.provider.RemoveAccount(target.ID); err != nil {
						m.statusMsg = fmt.Sprintf("Delete failed: %v", err)
					} else {
						m.statusMsg = fmt.Sprintf("Removed profile '%s' ✓", target.ID)
						delete(m.statuses, target.ID)
						delete(m.loading, target.ID)
						filtered := m.FilteredAccounts()
						if m.cursor >= len(filtered) {
							m.cursor = max(0, len(filtered)-1)
						}
					}
					m.statusMsgID++
					return m, tea.Batch(m.loadAccountsCmd(), m.clearStatusTimerCmd(m.statusMsgID, 4*time.Second))
				}
				return m, nil
			case "n", "N", "esc":
				m.confirmDelete = false
				m.deleteTarget = nil
				m.statusMsg = "Account deletion cancelled."
				m.statusMsgID++
				return m, m.clearStatusTimerCmd(m.statusMsgID, 2*time.Second)
			default:
				m.confirmDelete = false
				m.deleteTarget = nil
				m.statusMsg = "Account deletion cancelled."
				m.statusMsgID++
				return m, m.clearStatusTimerCmd(m.statusMsgID, 2*time.Second)
			}
		}

		if m.confirmSwitch {
			switch msg.String() {
			case "y", "Y":
				m.confirmSwitch = false
				if m.pendingSwitch != nil {
					acc := *m.pendingSwitch
					m.pendingSwitch = nil
					m.statusMsg = fmt.Sprintf("Switching to %s...", acc.ID)
					return m, m.switchAccountCmd(acc)
				}
			default:
				m.confirmSwitch = false
				m.pendingSwitch = nil
				m.statusMsg = "Switch cancelled."
			}
			return m, nil
		}

		if m.inputMode {
			switch msg.String() {
			case "enter":
				val := strings.TrimSpace(m.inputValue)
				action := m.inputAction
				target := m.renameTarget
				m.inputMode = false
				m.inputValue = ""
				m.inputAction = ""
				m.renameTarget = ""
				switch action {
				case "rename":
					if val != "" {
						if err := m.provider.RenameAccount(target, val); err != nil {
							m.statusMsg = fmt.Sprintf("Rename error: %v", err)
						} else {
							m.statusMsg = fmt.Sprintf("Renamed '%s' → '%s' ✓", target, val)
							return m, m.loadAccountsCmd()
						}
					}
				case "add_login":
					cmdArgs := m.provider.LoginCommand()
					if len(cmdArgs) == 0 {
						m.statusMsg = "No login command configured for this provider."
						m.statusMsgID++
						return m, m.clearStatusTimerCmd(m.statusMsgID, 3*time.Second)
					}
					c := exec.Command(cmdArgs[0], cmdArgs[1:]...)
					return m, tea.ExecProcess(c, func(err error) tea.Msg {
						return loginDoneMsg{name: val, err: err}
					})
				default:
					if val != "" {
						imported, err := m.provider.ImportCurrent(val, "")
						if err != nil {
							m.statusMsg = fmt.Sprintf("Import stopped: %v", err)
						} else {
							m.statusMsg = fmt.Sprintf("Imported profile '%s'", imported.ID)
							return m, m.loadAccountsCmd()
						}
					}
				}
				return m, nil
			case "esc":
				m.inputMode = false
				m.inputValue = ""
				return m, nil
			case "backspace":
				if len(m.inputValue) > 0 {
					m.inputValue = m.inputValue[:len(m.inputValue)-1]
				}
				return m, nil
			default:
				if len(msg.String()) == 1 {
					m.inputValue += msg.String()
				}
				return m, nil
			}
		}

		if m.searchMode {
			switch msg.String() {
			case "enter":
				m.searchMode = false
				return m, nil
			case "esc":
				m.searchMode = false
				m.filterQuery = ""
				filtered := m.FilteredAccounts()
				if m.cursor >= len(filtered) {
					m.cursor = max(0, len(filtered)-1)
				}
				return m, nil
			case "backspace":
				runes := []rune(m.filterQuery)
				if len(runes) > 0 {
					m.filterQuery = string(runes[:len(runes)-1])
					filtered := m.FilteredAccounts()
					if m.cursor >= len(filtered) {
						m.cursor = max(0, len(filtered)-1)
					}
				}
				return m, nil
			case "up":
				if m.cursor > 0 {
					m.cursor--
				}
				return m, nil
			case "down":
				filtered := m.FilteredAccounts()
				if m.cursor < len(filtered)-1 {
					m.cursor++
				}
				return m, nil
			default:
				if len(msg.String()) == 1 {
					m.filterQuery += msg.String()
					filtered := m.FilteredAccounts()
					if m.cursor >= len(filtered) {
						m.cursor = max(0, len(filtered)-1)
					}
					return m, nil
				}
			}
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "?":
			m.showHelp = !m.showHelp
			return m, nil

		case "/":
			m.searchMode = true
			return m, nil

		case "esc":
			if m.filterQuery != "" {
				m.filterQuery = ""
				filtered := m.FilteredAccounts()
				if m.cursor >= len(filtered) {
					m.cursor = max(0, len(filtered)-1)
				}
				return m, nil
			}

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			filtered := m.FilteredAccounts()
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}

		case "enter":
			filtered := m.FilteredAccounts()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				target := filtered[m.cursor]
				if target.IsActive {
					m.statusMsg = fmt.Sprintf("Account %s is already active.", target.ID)
					return m, nil
				}

				// Check if agy is running
				running, _ := m.provider.IsCLIRunning()
				if running {
					m.confirmSwitch = true
					m.pendingSwitch = &target
					m.statusMsg = fmt.Sprintf("Warning: %s is currently running! Switch anyway? (y/n)", m.provider.CLIBinary())
					return m, nil
				}

				m.statusMsg = fmt.Sprintf("Switching to %s...", target.ID)
				return m, m.switchAccountCmd(target)
			}

		case "d", "x":
			filtered := m.FilteredAccounts()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				target := filtered[m.cursor]
				m.confirmDelete = true
				m.deleteTarget = &target
			} else {
				m.statusMsg = "No account selected to delete."
				m.statusMsgID++
				return m, m.clearStatusTimerCmd(m.statusMsgID, 3*time.Second)
			}
			return m, nil

		case "a":
			m.inputMode = true
			m.inputAction = "add_login"
			m.inputPrompt = "Enter profile name for login (leave blank to auto-detect): "
			m.inputValue = ""
			return m, nil

		case "r":
			m.provider.InvalidateCache()
			m.statusMsg = "Refreshing quota status..."
			m.statusMsgID++
			var cmds []tea.Cmd
			for _, acc := range m.accounts {
				m.loading[acc.ID] = true
				cmds = append(cmds, m.fetchStatusCmd(acc))
			}
			return m, tea.Batch(cmds...)

		case "i":
			m.inputMode = true
			m.inputAction = "import"
			m.inputPrompt = "Enter profile name to save current session as: "
			m.inputValue = ""
			return m, nil

		case "R", "e":
			filtered := m.FilteredAccounts()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				target := filtered[m.cursor]
				m.inputMode = true
				m.inputAction = "rename"
				m.renameTarget = target.ID
				m.inputPrompt = fmt.Sprintf("Rename '%s' to: ", m.renameTarget)
				m.inputValue = m.renameTarget
			} else {
				m.statusMsg = "No account selected to rename."
			}
			return m, nil

		case "tab":
			if len(m.providers) > 1 {
				m.providerIdx = (m.providerIdx + 1) % len(m.providers)
				m.provider = m.providers[m.providerIdx]
				m.cfg.ActiveProvider = m.provider.ID()
				_ = config.SaveConfig(m.cfg)
				m.cursor = 0
				m.accounts = nil
				m.statuses = make(map[string]provider.AccountStatus)
				m.loading = make(map[string]bool)
				m.statusMsg = ""
				m.statusMsgID++
				m.filterQuery = ""
				m.searchMode = false
				m.confirmDelete = false
				m.deleteTarget = nil
				m.showHelp = false
				return m, m.loadAccountsCmd()
			}
			return m, nil

		case "o":
			filtered := m.FilteredAccounts()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				m.launching = true
				return m, tea.Quit
			}

		case "O":
			if dl, ok := m.provider.(provider.DetailedAppLauncher); ok {
				msg, err := dl.LaunchAppDetailed()
				if err != nil {
					m.statusMsg = fmt.Sprintf("Failed to launch %s: %v", dl.AppDisplayName(), err)
				} else if msg != "" {
					m.statusMsg = msg
				} else {
					m.statusMsg = fmt.Sprintf("Launched %s ✓", dl.AppDisplayName())
				}
				m.statusMsgID++
				return m, m.clearStatusTimerCmd(m.statusMsgID, 4*time.Second)
			}
			if launcher, ok := m.provider.(provider.AppLauncher); ok {
				if err := launcher.LaunchApp(); err != nil {
					m.statusMsg = fmt.Sprintf("Failed to launch %s: %v", launcher.AppDisplayName(), err)
				} else {
					m.statusMsg = fmt.Sprintf("Launched %s ✓", launcher.AppDisplayName())
				}
				m.statusMsgID++
				return m, m.clearStatusTimerCmd(m.statusMsgID, 4*time.Second)
			}
			m.statusMsg = "No desktop app available for this provider."
			m.statusMsgID++
			return m, m.clearStatusTimerCmd(m.statusMsgID, 3*time.Second)
		}
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return "Exiting swaper.\n"
	}
	if m.launching {
		return fmt.Sprintf("Launching %s...\n", m.provider.CLIBinary())
	}
	if m.showHelp {
		modal := m.renderHelpModal()
		if m.width >= 40 && m.height >= 10 {
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
		}
		return modal + "\n"
	}
	if m.confirmDelete {
		modal := m.renderDeleteModal()
		if m.width >= 40 && m.height >= 10 {
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
		}
		return modal + "\n"
	}

	var sb strings.Builder

	// Header
	headerLeft := titleStyle.Render(fmt.Sprintf("swaper — %s", m.provider.DisplayName()))
	openHint := "o: open"
	if _, ok := m.provider.(provider.AppLauncher); ok {
		openHint = "o: cli  O: app"
	}
	hints := fmt.Sprintf("q: quit  r: refresh  /: filter  a: add  d: del  enter: switch  %s  ?: help", openHint)
	if len(m.providers) > 1 {
		hints = "Tab: provider  " + hints
	}
	headerRight := dimStyle.Render(hints)
	gap := max(2, m.width-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight))
	sb.WriteString(headerLeft + strings.Repeat(" ", gap) + headerRight + "\n\n")

	// Aggregated Quota Summary Header
	if summary := m.AggregateQuotaSummary(); summary != "" {
		sb.WriteString(summaryHeaderStyle.Render(summary) + "\n\n")
	}

	// Search bar or active filter indicator
	if m.searchMode {
		searchBar := fmt.Sprintf("/ %s█", m.filterQuery)
		sb.WriteString(titleStyle.Render(searchBar) + dimStyle.Render("  (enter: done, esc: clear)") + "\n\n")
	} else if m.filterQuery != "" {
		filterBar := fmt.Sprintf("Filter: %q", m.filterQuery)
		sb.WriteString(statusStyle.Render(filterBar) + dimStyle.Render("  (press / to edit, esc to clear)") + "\n\n")
	}

	filtered := m.FilteredAccounts()
	if len(filtered) == 0 {
		if m.filterQuery != "" {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("No accounts found matching %q. Press Esc to clear filter.\n", m.filterQuery)))
		} else {
			sb.WriteString("No accounts configured yet. Press 'a' to add & login, or 'i' to import current session.\n")
		}
	} else {
		for i, acc := range filtered {
			isSelected := i == m.cursor
			card := m.renderAccountCard(acc, isSelected)
			sb.WriteString(card + "\n")
		}
	}

	if m.inputMode {
		sb.WriteString("\n" + titleStyle.Render(m.inputPrompt) + m.inputValue + "█\n" + dimStyle.Render("(press enter to confirm, esc to cancel)\n"))
	} else if m.statusMsg != "" {
		sb.WriteString("\n" + statusStyle.Render(m.statusMsg) + "\n")
	}

	return sb.String()
}

// FilteredAccounts returns accounts filtered by real-time search query.
func (m Model) FilteredAccounts() []provider.StoredAccount {
	if strings.TrimSpace(m.filterQuery) == "" {
		return m.accounts
	}
	q := strings.ToLower(strings.TrimSpace(m.filterQuery))
	var res []provider.StoredAccount
	for _, acc := range m.accounts {
		if strings.Contains(strings.ToLower(acc.ID), q) || strings.Contains(strings.ToLower(acc.Email), q) {
			res = append(res, acc)
			continue
		}
		if st, ok := m.statuses[acc.ID]; ok {
			if strings.Contains(strings.ToLower(st.Plan), q) || strings.Contains(strings.ToLower(st.Email), q) {
				res = append(res, acc)
			}
		}
	}
	return res
}

// AggregateQuotaSummary calculates an aggregate pool metric across all configured accounts for the active provider.
func (m Model) AggregateQuotaSummary() string {
	if len(m.accounts) == 0 {
		return ""
	}

	count := len(m.accounts)
	profileWord := "profiles"
	if count == 1 {
		profileWord = "profile"
	}

	var totalHours float64
	hasAnyQuota := false
	anyLoading := false

	for _, acc := range m.accounts {
		if m.loading[acc.ID] {
			anyLoading = true
		}
		st, ok := m.statuses[acc.ID]
		if !ok || !st.Healthy || len(st.Quotas) == 0 {
			continue
		}

		var primaryQuota *provider.Quota
		for i, q := range st.Quotas {
			lbl := strings.ToLower(q.Label)
			if strings.Contains(lbl, "5h") || strings.Contains(lbl, "5-hour") || strings.Contains(lbl, "5 hour") {
				if strings.Contains(lbl, "gemini") {
					primaryQuota = &st.Quotas[i]
					break
				}
				if primaryQuota == nil {
					primaryQuota = &st.Quotas[i]
				}
			}
		}

		if primaryQuota == nil && len(st.Quotas) > 0 {
			primaryQuota = &st.Quotas[0]
		}

		if primaryQuota != nil {
			hasAnyQuota = true
			windowHours := 5.0
			lbl := strings.ToLower(primaryQuota.Label)
			if strings.HasSuffix(lbl, "m") {
				var mins int
				if n, _ := fmt.Sscanf(lbl, "%dm", &mins); n == 1 && mins > 0 {
					windowHours = float64(mins) / 60.0
				}
			} else if strings.Contains(lbl, "h") {
				var hrs float64
				if n, _ := fmt.Sscanf(lbl, "%fh", &hrs); n == 1 && hrs > 0 {
					windowHours = hrs
				}
			}
			totalHours += (primaryQuota.PercentLeft / 100.0) * windowHours
		}
	}

	if !hasAnyQuota && anyLoading {
		return fmt.Sprintf("%d active %s • Calculating aggregate quota...", count, profileWord)
	}

	return fmt.Sprintf("%d active %s • Combined: ~%.1f hours of continuous coding quota remaining", count, profileWord, totalHours)
}

func (m Model) renderHelpModal() string {
	modalStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(1, 2).
		Width(66)

	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("swaper — Keybindings & Help")

	secHeader := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00E5FF"))
	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00FFA3"))
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#DDDDDD"))

	row := func(key, desc string) string {
		return fmt.Sprintf("  %-16s %s", keyStyle.Render(key), descStyle.Render(desc))
	}

	var lines []string
	lines = append(lines, title)
	lines = append(lines, "")

	lines = append(lines, secHeader.Render("Navigation & Selection"))
	lines = append(lines, row("↑ / k, ↓ / j", "Navigate profiles"))
	lines = append(lines, row("Enter", "Switch active account"))
	lines = append(lines, row("Tab", "Switch provider (antigravity ⇄ openai)"))
	lines = append(lines, row("/", "Search & filter profiles in real-time"))
	lines = append(lines, "")

	lines = append(lines, secHeader.Render("Account Management"))
	lines = append(lines, row("a", "Add new account & interactive CLI login"))
	lines = append(lines, row("d / x", "Delete selected profile (with confirmation)"))
	lines = append(lines, row("R / e", "Rename selected profile"))
	lines = append(lines, row("i", "Import current CLI session"))
	lines = append(lines, "")

	lines = append(lines, secHeader.Render("Actions & Launcher"))
	lines = append(lines, row("r", "Force refresh quota metrics"))
	lines = append(lines, row("o", "Launch provider CLI in-place"))
	lines = append(lines, row("O", "Launch provider desktop application"))
	lines = append(lines, row("?", "Toggle this help modal"))
	lines = append(lines, row("q / Ctrl+C", "Quit swaper"))
	lines = append(lines, "")

	footer := lipgloss.NewStyle().Foreground(subtleColor).Render("Press '?' or 'esc' to close")
	lines = append(lines, footer)

	return modalStyle.Render(strings.Join(lines, "\n"))
}

func (m Model) renderDeleteModal() string {
	if m.deleteTarget == nil {
		return ""
	}
	target := m.deleteTarget
	modalStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FF1744")).
		Padding(1, 2).
		Width(56)

	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF1744")).Render("Delete Account Confirmation")

	emailStr := target.Email
	if emailStr == "" {
		if st, ok := m.statuses[target.ID]; ok && st.Email != "" {
			emailStr = st.Email
		}
	}
	targetDesc := target.ID
	if emailStr != "" {
		targetDesc = fmt.Sprintf("%s (%s)", target.ID, emailStr)
	}

	body := fmt.Sprintf("Are you sure you want to delete profile:\n  %s\n\nAll stored credentials for this account will be removed.",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Render(targetDesc))

	footer := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#555555", Dark: "#AAAAAA"}).
		Render("[y] Confirm Delete     [n / esc] Cancel")

	content := fmt.Sprintf("%s\n\n%s\n\n%s", title, body, footer)
	return modalStyle.Render(content)
}

func (m Model) renderAccountCard(acc provider.StoredAccount, isSelected bool) string {
	st, hasStatus := m.statuses[acc.ID]
	isLoading := m.loading[acc.ID]

	// Title line
	bullet := "○"
	statusText := inactiveStyle.Render(acc.ID)
	if acc.IsActive {
		bullet = activeStyle.Render("●")
		statusText = activeStyle.Render(fmt.Sprintf("active ● %s", acc.ID))
	} else if isSelected {
		bullet = titleStyle.Render("▶")
		statusText = titleStyle.Render(acc.ID)
	}

	email := acc.Email
	if email == "" && hasStatus {
		email = st.Email
	}
	plan := "Standard"
	if hasStatus && st.Plan != "" {
		plan = st.Plan
	}

	meta := dimStyle.Render(fmt.Sprintf("(%s, %s)", plan, email))
	header := fmt.Sprintf("%s %s %s", bullet, statusText, meta)

	// Quota lines
	var quotaContent string
	if isLoading && !hasStatus {
		quotaContent = dimStyle.Render("  Fetching quota...")
	} else if !hasStatus || !st.Healthy {
		errMsg := "error fetching quota"
		if hasStatus && st.Error != "" {
			errMsg = st.Error
		}
		quotaContent = dimStyle.Render("  " + errMsg)
	} else if len(st.Quotas) == 0 {
		quotaContent = dimStyle.Render("  No quota metrics reported.")
	} else if len(st.Quotas) > 2 {
		var lines []string
		for i := 0; i < len(st.Quotas); i += 2 {
			var rowParts []string
			for j := i; j < i+2 && j < len(st.Quotas); j++ {
				q := st.Quotas[j]
				bar := renderBar(q.PercentLeft)
				pct := fmt.Sprintf("%3.0f%%", q.PercentLeft)
				reset := dimStyle.Render(fmt.Sprintf("reset %s", q.ResetIn))
				rowParts = append(rowParts, fmt.Sprintf("%-13s %s %s  %s", q.Label, bar, pct, reset))
			}
			lines = append(lines, "  "+strings.Join(rowParts, "   │   "))
		}
		quotaContent = strings.Join(lines, "\n\n")
	} else {
		var parts []string
		for _, q := range st.Quotas {
			bar := renderBar(q.PercentLeft)
			pct := fmt.Sprintf("%3.0f%%", q.PercentLeft)
			reset := dimStyle.Render(fmt.Sprintf("reset %s", q.ResetIn))
			parts = append(parts, fmt.Sprintf("%s %s %s  %s", q.Label, bar, pct, reset))
		}
		quotaContent = "  " + strings.Join(parts, "   │   ")
	}

	content := header + "\n" + quotaContent

	if isSelected {
		return selectedBox.Width(max(60, m.width-4)).Render(content)
	}
	return boxStyle.Width(max(60, m.width-4)).Render(content)
}

func renderBar(pct float64) string {
	totalBlocks := 10
	filled := int((pct / 100.0) * float64(totalBlocks))
	if filled > totalBlocks {
		filled = totalBlocks
	}
	if filled < 0 {
		filled = 0
	}
	empty := totalBlocks - filled

	filledStr := strings.Repeat("█", filled)
	emptyStr := strings.Repeat("░", empty)

	if pct > 30.0 {
		return barFullStyle.Render(filledStr) + dimStyle.Render(emptyStr)
	} else if pct > 10.0 {
		return barMidStyle.Render(filledStr) + dimStyle.Render(emptyStr)
	}
	return barLowStyle.Render(filledStr) + dimStyle.Render(emptyStr)
}

func Run(cfg config.Config, providers ...provider.Provider) error {
	m := NewModel(cfg, providers...)
	pProg := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := pProg.Run()
	if err != nil {
		return err
	}
	if mFinal, ok := finalModel.(Model); ok && mFinal.launching {
		acc, _ := mFinal.provider.ActiveAccount()
		if acc != nil {
			return mFinal.provider.LaunchCLI(*acc)
		}
	}
	return nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
