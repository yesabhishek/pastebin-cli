# pb

`pb` is a GitHub-backed personal pastebin CLI for small text files.

It stores plaintext files in a dedicated private GitHub repository, keeps a local cache for offline edits, autosaves drafts locally while you work, and gives you explicit save/sync commands plus per-file version history.

## Features

- GitHub-backed text storage with local-first caching and recovery
- built-in terminal editor with local draft autosave
- explicit `sync` flow for reconciling changes across devices
- durable per-file version history with show and restore commands
- conflict copies instead of risky remote overwrites
- installable without sudo or admin rights

## Requirements

- Go 1.25+ if you want to build from source
- [GitHub CLI](https://cli.github.com/) installed and authenticated with `gh auth login`

## Install From Releases

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/yesabhishek/pastebin-cli/main/scripts/install.sh | sh
```

Windows PowerShell:

```powershell
iwr https://raw.githubusercontent.com/yesabhishek/pastebin-cli/main/scripts/install.ps1 -useb | iex
```

These installers place the binary in a user-owned bin directory, so no sudo or admin rights are required.

## Build From Source

```bash
git clone https://github.com/yesabhishek/pastebin-cli.git
cd pastebin-cli
go build -o pb ./cmd/pb
```

## Local Checks

Install the local sanity-check tools first:

```bash
brew install actionlint shellcheck
git config core.hooksPath .githooks
```

Then run the same checks locally that CI uses:

```bash
./scripts/check.sh
```

## Quick Start

```bash
./pb init
./pb new notes/today.txt
./pb versions notes/today.txt
./pb list
./pb sync
```

`pb init` uses your current `gh` login and creates a dedicated private storage repository, `pastebin-cli-store`, by default.

## Commands

```text
pb init
pb new <path>
pb edit <path>
pb read <path>
pb versions <path>
pb show <path> <version-id>
pb restore <path> <version-id>
pb delete <path> [--yes]
pb list [prefix] [--refresh]
pb sync
pb status
pb logout
```

Global flags:

- `--repo <name>`: override the default GitHub storage repo
- `--json`: emit JSON for `read`, `list`, and `status`

## Version History

- `pb versions <path>` lists durable synced versions for a file, newest first
- `pb show <path> <version-id>` prints the content of a historical version
- `pb restore <path> <version-id>` restores a historical version as the latest one

Durable versions are created on explicit save, save-on-exit, restore, and sync. The editor's background autosave only protects local drafts and does not create remote version spam.

## Local Data Layout

`pb` stores app-owned local state under your user config directory:

- `config.json`: repo/login/device settings
- `state/index.json`: tracked file metadata
- `state/journal.json`: resumable pending operations
- `state/recovery/`: autosave recovery snapshots
- `cache/files/`: cached text content

## Project Files

- [Repo check script](scripts/check.sh)
- [Shell installer](scripts/install.sh)
- [PowerShell installer](scripts/install.ps1)
- [Release workflow](.github/workflows/release.yml)
- [CI workflow](.github/workflows/ci.yml)
