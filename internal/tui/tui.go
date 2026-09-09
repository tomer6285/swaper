package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"swaper/internal/config"
	"swaper/internal/provider"
)

// Styling constants
var (
	subtleColor   = lipgloss.AdaptiveColor{Light: "#888888", Dark: "#777777"}
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	activeStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00FFA3"))
	inactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#CCCCCC"))
	barFullStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676"))
	barMidStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD600"))
	barLowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF1744"))
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E5FF")).Italic(true)
	dimStyle      = lipgloss.NewStyle().Foreground(subtleColor)
	boxStyle      = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#444466")).Padding(0, 1)
	selectedBox   = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7D56F4")).Padding(0, 1)
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

type clearStatusMsg struct {
	id int
}

type Model struct {
	provider provider.Provider
	cfg      config.Config

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

	inputMode   bool
	inputPrompt string
	inputValue  string
	inputAction string
	renameTarget string

	width  int
	height int
}

func NewModel(p provider.Provider, cfg config.Config) Model {
	return Model{
		provider: p,
		cfg:      cfg,
		statuses: make(map[string]provider.AccountStatus),
		loading:  make(map[string]bool),
		cursor:   0,
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
		if m.cursor >= len(m.accounts) {
			m.cursor = max(0, len(m.accounts)-1)
		}

		var cmds []tea.Cmd
		for _, acc := range m.accounts {
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

	case tea.KeyMsg:
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
				if val != "" {
					switch action {
					case "rename":
						if err := m.provider.RenameAccount(target, val); err != nil {
							m.statusMsg = fmt.Sprintf("Rename error: %v", err)
						} else {
							m.statusMsg = fmt.Sprintf("Renamed '%s' → '%s' ✓", target, val)
							return m, m.loadAccountsCmd()
						}
					default:
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

		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.cursor < len(m.accounts)-1 {
				m.cursor++
			}

		case "enter":
			if len(m.accounts) > 0 && m.cursor < len(m.accounts) {
				target := m.accounts[m.cursor]
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
			if len(m.accounts) > 0 && m.cursor < len(m.accounts) {
				m.inputMode = true
				m.inputAction = "rename"
				m.renameTarget = m.accounts[m.cursor].ID
				m.inputPrompt = fmt.Sprintf("Rename '%s' to: ", m.renameTarget)
				m.inputValue = m.renameTarget
			} else {
				m.statusMsg = "No account selected to rename."
			}
			return m, nil

		case "o":
			if len(m.accounts) > 0 && m.cursor < len(m.accounts) {
				m.launching = true
				return m, tea.Quit
			}
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

	var sb strings.Builder

	// Header
	headerLeft := titleStyle.Render(fmt.Sprintf("swaper — %s", m.provider.DisplayName()))
	headerRight := dimStyle.Render("q: quit  r: refresh  i: import  R: rename  enter: switch  o: open agy")
	gap := max(2, m.width-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight))
	sb.WriteString(headerLeft + strings.Repeat(" ", gap) + headerRight + "\n\n")

	if len(m.accounts) == 0 {
		sb.WriteString("No accounts configured yet. Press 'i' to import current session.\n")
	}

	// Account cards
	for i, acc := range m.accounts {
		isSelected := i == m.cursor
		card := m.renderAccountCard(acc, isSelected)
		sb.WriteString(card + "\n")
	}

	if m.inputMode {
		sb.WriteString("\n" + titleStyle.Render(m.inputPrompt) + m.inputValue + "█\n" + dimStyle.Render("(press enter to confirm, esc to cancel)\n"))
	} else if m.statusMsg != "" {
		sb.WriteString("\n" + statusStyle.Render(m.statusMsg) + "\n")
	}

	return sb.String()
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
	if isLoading {
		quotaContent = dimStyle.Render("  Fetching quota...")
	} else if !hasStatus || !st.Healthy {
		errMsg := "error fetching quota"
		if hasStatus && st.Error != "" {
			errMsg = st.Error
		}
		quotaContent = dimStyle.Render("  " + errMsg)
	} else if len(st.Quotas) == 0 {
		quotaContent = dimStyle.Render("  No quota metrics reported.")
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

func Run(p provider.Provider, cfg config.Config) error {
	m := NewModel(p, cfg)
	pProg := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := pProg.Run()
	if err != nil {
		return err
	}
	if mFinal, ok := finalModel.(Model); ok && mFinal.launching {
		acc, _ := p.ActiveAccount()
		if acc != nil {
			return p.LaunchCLI(*acc)
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
