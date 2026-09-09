# swaper

Monitor quota and switch accounts for the Antigravity (`agy`) CLI. No re-login.

## Install

```bash
go build -o swaper ./cmd/swaper
```

Requires Go 1.27+ and the `agy` binary in `PATH`.

## Quick start

```bash
./swaper import default  # save current agy login
./swaper                 # open TUI
```

## CLI

```bash
swaper list               # saved profiles
swaper status [--json]    # quota across accounts
swaper switch <name>      # set active account
swaper import [name]      # save current login (default: default)
swaper add <name>         # placeholder, then: agy auth login + swaper import <name>
swaper rename <old> <new> # rename a profile
swaper remove <name>      # delete a profile
swaper open               # launch agy with active account
```

## TUI keys

| Key | Action |
|---|---|
| `↑`/`k`, `↓`/`j` | Move |
| `Enter` | Switch to selected account |
| `r` | Refresh quota |
| `i` | Import current session |
| `R` | Rename selected account |
| `o` | Open `agy` |
| `q` | Quit |

## Storage

- Config: `~/.swaper/config.json`
- Profiles: `~/.swaper/antigravity/profiles/<name>/auth.json`
- Active pointer: `~/.swaper/antigravity/active_profile.txt`
- Live token: system keyring (`gemini` / `antigravity`)

## OAuth credentials

No client secrets are stored in this repo. Each user supplies their own
Google OAuth client (the same one their `agy` login uses):

```bash
export SWAPER_GOOGLE_CLIENT_ID="....apps.googleusercontent.com"
export SWAPER_GOOGLE_CLIENT_SECRET="...."
```

Alternatively put `google_client_id` / `google_client_secret` in
`~/.swaper/config.json`. Token refresh fails with a clear error until set.
