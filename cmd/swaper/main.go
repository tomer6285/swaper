package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"swaper/internal/config"
	"swaper/internal/provider"
	"swaper/internal/providers/antigravity"
	"swaper/internal/tui"
)

func printUsage() {
	fmt.Println(`swaper — Multi-account switcher and quota monitor

Usage:
  swaper                 Launch interactive TUI
  swaper list            List all stored accounts and their status
  swaper status [--json] Show quota limits across all accounts
  swaper switch <name>   Switch active account
  swaper import [name]   Save currently logged-in CLI session as a named profile
  swaper add <name>      Create new profile entry
  swaper rename <old> <new> Rename a profile
  swaper remove <name>   Remove a profile
  swaper open            Launch provider CLI (agy) using active account

Flags:
  -v, --version          Show version
  -h, --help             Show help`)
}

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	p, err := antigravity.NewProvider(cfg.StorageDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing provider: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		// Default: launch TUI
		if err := tui.Run(p, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "help", "-h", "--help":
		printUsage()

	case "version", "-v", "--version":
		fmt.Println("swaper v0.1.0 (2026-09)")

	case "list":
		accs, err := p.ListAccounts()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list accounts: %v\n", err)
			os.Exit(1)
		}
		if len(accs) == 0 {
			fmt.Println("No accounts saved. Run 'swaper import' to import current session.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
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
		if len(os.Args) > 2 {
			name = os.Args[2]
		}
		acc, err := p.ImportCurrent(name, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Import failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Successfully imported profile '%s' (%s)\n", acc.ID, acc.Email)

	case "add":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: swaper add <name>\n")
			os.Exit(1)
		}
		name := os.Args[2]
		acc, err := p.AddNew(name, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Add failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Created profile placeholder '%s'.\nRun 'agy auth login' then 'swaper import %s' to save its credentials.\n", acc.ID, name)

	case "rename":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: swaper rename <old-name> <new-name>\n")
			os.Exit(1)
		}
		oldName, newName := os.Args[2], os.Args[3]
		if err := p.RenameAccount(oldName, newName); err != nil {
			fmt.Fprintf(os.Stderr, "Rename failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Renamed profile '%s' → '%s'\n", oldName, newName)

	case "remove":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: swaper remove <name>\n")
			os.Exit(1)
		}
		name := os.Args[2]
		if err := p.RemoveAccount(name); err != nil {
			fmt.Fprintf(os.Stderr, "Remove failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Removed profile '%s'\n", name)

	case "switch":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: swaper switch <name>\n")
			os.Exit(1)
		}
		name := os.Args[2]
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
			fmt.Fprintf(os.Stderr, "Account '%s' not found. Use 'swaper list' to see saved accounts.\n", name)
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
		_ = statusFlags.Parse(os.Args[2:])

		accs, err := p.ListAccounts()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(accs) == 0 {
			// Auto import current session if none exist
			if imported, err := p.ImportCurrent("default", ""); err == nil && imported != nil {
				accs = append(accs, *imported)
			}
		}

		var statuses []provider.AccountStatus
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		for _, a := range accs {
			st, err := p.FetchStatus(ctx, a)
			if err != nil {
				statuses = append(statuses, provider.AccountStatus{
					ID:      a.ID,
					Email:   a.Email,
					Healthy: false,
					Error:   err.Error(),
				})
			} else {
				statuses = append(statuses, st)
			}
		}

		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(statuses)
			return
		}

		fmt.Printf("Provider: %s\n\n", p.DisplayName())
		for _, st := range statuses {
			active := " "
			curr, _ := p.ActiveAccount()
			if curr != nil && curr.ID == st.ID {
				active = "● active"
			}
			fmt.Printf("[%s] Profile: %s  (%s, %s)\n", active, st.ID, st.Plan, st.Email)
			if !st.Healthy {
				fmt.Printf("   Error: %s\n\n", st.Error)
				continue
			}
			for _, q := range st.Quotas {
				fmt.Printf("   %-8s: %5.1f%% remaining  (resets in %s)\n", q.Label, q.PercentLeft, q.ResetIn)
			}
			fmt.Println()
		}

	case "open":
		acc, err := p.ActiveAccount()
		if err != nil || acc == nil {
			fmt.Fprintf(os.Stderr, "No active account found. Run 'swaper import' first.\n")
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
