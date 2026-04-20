# pb

`pb` is a GitHub-backed personal pastebin CLI for small text files.

It stores plaintext files in a dedicated private GitHub repository, keeps a local cache for offline edits, autosaves while you work, and gives you an explicit `sync` command for reconciling changes across devices.

## Features

- GitHub-backed text storage with local-first caching and recovery
- built-in terminal editor with autosave
- explicit `sync` flow for reconciling changes across devices
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

## Quick Start

```bash
./pb init
./pb new notes/today.txt
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
pb delete <path> [--yes]
pb list [prefix] [--refresh]
pb sync
pb status
pb logout
```

Global flags:

- `--repo <name>`: override the default GitHub storage repo
- `--json`: emit JSON for `read`, `list`, and `status`

## Local Data Layout

`pb` stores app-owned local state under your user config directory:

- `config.json`: repo/login/device settings
- `state/index.json`: tracked file metadata
- `state/journal.json`: resumable pending operations
- `state/recovery/`: autosave recovery snapshots
- `cache/files/`: cached text content

## Project Files

- [Shell installer](scripts/install.sh)
- [PowerShell installer](scripts/install.ps1)
- [Release workflow](.github/workflows/release.yml)
- [CI workflow](.github/workflows/ci.yml)
