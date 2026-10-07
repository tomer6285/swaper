# swaper

Monitor quota and switch accounts for AI developer CLIs:
- **`antigravity`** (`agy`): Gemini and OpenAI (Claude & GPT) model pools.
- **`openai`** (`codex`): Codex / ChatGPT account-based auth, 5-hour and weekly limits.

No re-login needed.

## Install

```bash
go build -o swaper ./cmd/swaper
```

Requires Go 1.27+.

## Quick start

```bash
./swaper import default  # save current login
./swaper                 # open interactive TUI (press Tab to switch providers)
```

## CLI

```bash
swaper list               # saved profiles for active provider
swaper status [--all]     # quota across accounts (--all for both providers)
swaper switch <name>      # set active account
swaper import [name]      # save current CLI login (default: default)
swaper add <name>         # placeholder profile
swaper rename <old> <new> # rename a profile
swaper remove <name>      # delete a profile
swaper open               # launch provider CLI (agy / codex) with active account
swaper provider [name]    # show or switch default provider (antigravity, openai)
```

Target a specific provider with `-p, --provider <name>`:
```bash
swaper -p openai status
swaper -p antigravity status
```

## TUI keys

| Key | Action |
|---|---|
| `Tab` | Switch provider (`antigravity` ↔ `openai`) |
| `↑`/`k`, `↓`/`j` | Move selection |
| `Enter` | Switch to selected account |
| `r` | Refresh quota |
| `i` | Import current session |
| `R` | Rename selected account |
| `o` | Open active CLI (`agy` / `codex`) |
| `q` | Quit |

## Storage

- Config: `~/.swaper/config.json`
- `antigravity` profiles: `~/.swaper/antigravity/profiles/<name>/auth.json`
- `antigravity` active pointer: `~/.swaper/antigravity/active_profile.txt`
- `antigravity` live session: system keyring (`gemini` / `antigravity`)
- `openai` profiles: `~/.swaper/openai/profiles/<name>/auth.json`
- `openai` active pointer: `~/.swaper/openai/active_profile.txt`
- `openai` live session: `~/.codex/auth.json`
