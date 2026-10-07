package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"swaper/internal/config"
	"swaper/internal/provider"
	"swaper/internal/providers/antigravity"
	"swaper/internal/providers/openai"
	"swaper/internal/tui"
)

func printUsage() {
	fmt.Println(`swaper — Multi-account switcher and quota monitor

Usage:
  swaper                 Launch interactive TUI (press Tab to switch providers)
  swaper list            List all stored accounts and their status
  swaper status [--json] Show quota limits across accounts (--all for all providers)
  swaper switch <name>   Switch active account
  swaper import [name]   Save currently logged-in CLI session as a named profile
  swaper add <name>      Create new profile entry
  swaper rename <old> <new> Rename a profile
  swaper remove <name>   Remove a profile
  swaper open            Launch provider CLI using active account
  swaper provider [name] Show or set default provider (antigravity, openai)

Options:
  -p, --provider <name>  Select provider for command (antigravity, openai)
  -v, --version          Show version
  -h, --help             Show help`)
}

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	agyProv, err := antigravity.NewProvider(cfg.StorageDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing antigravity provider: %v\n", err)
		os.Exit(1)
	}
	openaiProv, err := openai.NewProvider(cfg.StorageDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing openai provider: %v\n", err)
		os.Exit(1)
	}

	providersMap := map[string]provider.Provider{
		"antigravity": agyProv,
		"openai":      openaiProv,
	}

	// Filter out -p/--provider flags from os.Args
	var cleanArgs []string
	cleanArgs = append(cleanArgs, os.Args[0])
	providerOverride := ""

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "-p" || arg == "--provider" {
			if i+1 < len(os.Args) {
				providerOverride = os.Args[i+1]
				i++
				continue
			}
		} else if strings.HasPrefix(arg, "--provider=") {
			providerOverride = strings.TrimPrefix(arg, "--provider=")
			continue
		} else if strings.HasPrefix(arg, "-p=") {
			providerOverride = strings.TrimPrefix(arg, "-p=")
			continue
		}
		cleanArgs = append(cleanArgs, arg)
	}

	selectedProviderName := strings.ToLower(strings.TrimSpace(cfg.ActiveProvider))
	if selectedProviderName == "" {
		selectedProviderName = "antigravity"
	}
	if providerOverride != "" {
		selectedProviderName = strings.ToLower(strings.TrimSpace(providerOverride))
	}

	p, ok := providersMap[selectedProviderName]
	if !ok {
		// Try aliases
		switch selectedProviderName {
		case "codex", "chatgpt":
			p = openaiProv
			selectedProviderName = "openai"
		case "gemini", "agy":
			p = agyProv
			selectedProviderName = "antigravity"
		default:
			fmt.Fprintf(os.Stderr, "Unknown provider '%s' (available: antigravity, openai)\n", selectedProviderName)
			os.Exit(1)
		}
	}

	if len(cleanArgs) < 2 {
		// Default: launch interactive TUI
		var tuiProviders []provider.Provider
		if selectedProviderName == "openai" {
			tuiProviders = []provider.Provider{openaiProv, agyProv}
		} else {
			tuiProviders = []provider.Provider{agyProv, openaiProv}
		}
		if err := tui.Run(cfg, tuiProviders...); err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cmd := cleanArgs[1]
	switch cmd {
	case "help", "-h", "--help":
		printUsage()

	case "version", "-v", "--version":
		fmt.Println("swaper v0.2.0 (2026-10)")

	case "provider":
		if len(cleanArgs) < 3 {
			fmt.Printf("Default provider: %s\n\nAvailable providers:\n", cfg.ActiveProvider)
			for _, id := range []string{"antigravity", "openai"} {
				marker := "  "
				if id == cfg.ActiveProvider {
					marker = "● "
				}
				fmt.Printf("%s%s\n", marker, id)
			}
			return
		}
		target := strings.ToLower(cleanArgs[2])
		switch target {
		case "codex", "chatgpt":
			target = "openai"
		case "gemini", "agy":
			target = "antigravity"
		}
		if _, exists := providersMap[target]; !exists {
			fmt.Fprintf(os.Stderr, "Unknown provider '%s'. Available: antigravity, openai\n", target)
			os.Exit(1)
		}
		cfg.ActiveProvider = target
		if err := config.SaveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Switched default provider to '%s'\n", target)

	case "list":
		accs, err := p.ListAccounts()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list accounts: %v\n", err)
			os.Exit(1)
		}
		if len(accs) == 0 {
			fmt.Printf("[%s] No accounts saved. Run 'swaper import' to import current session.\n", p.DisplayName())
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintf(w, "Provider: %s\n", p.DisplayName())
		fmt.Fprintln(w, "ACTIVE\tPROFILE\tEMAIL\tUPDATED")
		for _, a := range accs {
			activeMark := " "
			if a.IsActive {
				activeMark = "●"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", activeMark, a.ID, a.Email, a.UpdatedAt.Format(time.DateOnly))
		}
		w.Flush()

	case "import":
		name := "default"
		if len(cleanArgs) > 2 {
			name = cleanArgs[2]
		}
		acc, err := p.ImportCurrent(name, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Import failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Successfully imported profile '%s' (%s) for %s\n", acc.ID, acc.Email, p.DisplayName())

	case "add":
		if len(cleanArgs) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: swaper add <name>\n")
			os.Exit(1)
		}
		name := cleanArgs[2]
		acc, err := p.AddNew(name, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Add failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Created profile placeholder '%s' for %s.\nRun '%s login' then 'swaper import %s' to save its credentials.\n", acc.ID, p.DisplayName(), p.CLIBinary(), name)

	case "rename":
		if len(cleanArgs) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: swaper rename <old-name> <new-name>\n")
			os.Exit(1)
		}
		oldName, newName := cleanArgs[2], cleanArgs[3]
		if err := p.RenameAccount(oldName, newName); err != nil {
			fmt.Fprintf(os.Stderr, "Rename failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Renamed profile '%s' → '%s' (%s)\n", oldName, newName, p.DisplayName())

	case "remove":
		if len(cleanArgs) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: swaper remove <name>\n")
			os.Exit(1)
		}
		name := cleanArgs[2]
		if err := p.RemoveAccount(name); err != nil {
			fmt.Fprintf(os.Stderr, "Remove failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Removed profile '%s' (%s)\n", name, p.DisplayName())

	case "switch":
		if len(cleanArgs) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: swaper switch <name>\n")
			os.Exit(1)
		}
		name := cleanArgs[2]
		accs, err := p.ListAccounts()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing accounts: %v\n", err)
			os.Exit(1)
		}
		var target *provider.StoredAccount
		for _, a := range accs {
			if a.ID == name {
				target = &a
				break
			}
		}
		if target == nil {
			fmt.Fprintf(os.Stderr, "Account '%s' not found for %s. Use 'swaper list' to see saved accounts.\n", name, p.DisplayName())
			os.Exit(1)
		}

		running, _ := p.IsCLIRunning()
		if running {
			fmt.Printf("Warning: %s process is currently running.\n", p.CLIBinary())
		}

		if err := p.SwitchTo(*target); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to switch: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Switched active account to '%s' (%s is now using this account)\n", target.ID, p.CLIBinary())

	case "status":
		statusFlags := flag.NewFlagSet("status", flag.ExitOnError)
		jsonFlag := statusFlags.Bool("json", false, "Output in JSON format")
		allFlag := statusFlags.Bool("all", false, "Show quota limits across all providers")
		_ = statusFlags.Parse(cleanArgs[2:])

		var targetProviders []provider.Provider
		if *allFlag {
			targetProviders = []provider.Provider{agyProv, openaiProv}
		} else {
			targetProviders = []provider.Provider{p}
		}

		type ProviderStatusReport struct {
			Provider string                   `json:"provider"`
			Statuses []provider.AccountStatus `json:"statuses"`
		}
		var allReports []ProviderStatusReport

		for _, prov := range targetProviders {
			accs, err := prov.ListAccounts()
			if err != nil {
				continue
			}
			if len(accs) == 0 {
				if imported, err := prov.ImportCurrent("default", ""); err == nil && imported != nil {
					accs = append(accs, *imported)
				}
			}

			statuses := make([]provider.AccountStatus, len(accs))
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)

			var wg sync.WaitGroup
			for i, a := range accs {
				wg.Add(1)
				go func(idx int, acc provider.StoredAccount) {
					defer wg.Done()
					st, err := prov.FetchStatus(ctx, acc)
					if err != nil {
						statuses[idx] = provider.AccountStatus{
							ID:      acc.ID,
							Email:   acc.Email,
							Healthy: false,
							Error:   err.Error(),
						}
					} else {
						statuses[idx] = st
					}
				}(i, a)
			}
			wg.Wait()
			cancel()

			allReports = append(allReports, ProviderStatusReport{
				Provider: prov.DisplayName(),
				Statuses: statuses,
			})
		}

		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if *allFlag {
				_ = enc.Encode(allReports)
			} else if len(allReports) > 0 {
				_ = enc.Encode(allReports[0].Statuses)
			}
			return
		}

		for idx, report := range allReports {
			if idx > 0 {
				fmt.Println(strings.Repeat("─", 50))
				fmt.Println()
			}
			prov := targetProviders[idx]
			fmt.Printf("Provider: %s\n\n", report.Provider)
			if len(report.Statuses) == 0 {
				fmt.Println("   No accounts configured. Run 'swaper import' to import current session.")
				fmt.Println()
				continue
			}
			for _, st := range report.Statuses {
				active := " "
				curr, _ := prov.ActiveAccount()
				if curr != nil && curr.ID == st.ID {
					active = "● active"
				}
				fmt.Printf("[%s] Profile: %s  (%s, %s)\n", active, st.ID, st.Plan, st.Email)
				if !st.Healthy {
					fmt.Printf("   Error: %s\n\n", st.Error)
					continue
				}
				for _, q := range st.Quotas {
					fmt.Printf("   %-13s: %5.1f%% remaining  (resets in %s)\n", q.Label, q.PercentLeft, q.ResetIn)
				}
				fmt.Println()
			}
		}

	case "open":
		acc, err := p.ActiveAccount()
		if err != nil || acc == nil {
			fmt.Fprintf(os.Stderr, "No active account found for %s. Run 'swaper import' first.\n", p.DisplayName())
			os.Exit(1)
		}
		if err := p.LaunchCLI(*acc); err != nil {
			fmt.Fprintf(os.Stderr, "Error launching CLI: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}
