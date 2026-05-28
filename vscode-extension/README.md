# Pastebin for VS Code

Pastebin connects VS Code to `pb`, a GitHub-backed personal scratchpad for notes and snippets. It keeps GitHub authentication in the GitHub CLI and uses the `pb` binary for storage, sync, conflict handling, and version history.

## Features

- Browse all pastebin notes from a native VS Code sidebar.
- Create a note from VS Code and push it with the editor title upload icon.
- Sync notes with the configured GitHub-backed `pb` store.
- See the active GitHub account and storage repository in the sidebar.
- Build a local `pb` binary from this repository for extension development.

## Requirements

- `gh` installed and authenticated with `gh auth login`.
- `pb` installed and initialized with `pb init`.
- For `Push Current Note`, `pb` must support `pb save <path> --stdin`. If your installed release is older, run `Pastebin: Build Local pb` from this repository workspace.

## Getting Started

1. Install and authenticate GitHub CLI: `gh auth login`.
2. Initialize storage: `pb init`.
3. Open the Pastebin sidebar in VS Code.
4. Use `New Note`, write content, then click the editor title upload icon.
5. Enter a pastebin path such as `notes/today.md`.

## Commands

- `Pastebin: New Note`
- `Pastebin: Initialize`
- `Pastebin: Push Current Note`
- `Pastebin: Build Local pb`
- `Pastebin: Open Note`
- `Pastebin: Delete Note`
- `Pastebin: Sync`
- `Pastebin: Refresh`
- `Pastebin: Show Status`
- `Pastebin: Configure pb Path`
- `Pastebin: Filter`

## Troubleshooting

- If push says your `pb` CLI is too old, run `Pastebin: Build Local pb` or update `pb`.
- If the extension cannot find `pb`, run `Pastebin: Configure pb Path`.
- If GitHub access fails, run `gh auth status` and refresh the Pastebin sidebar.
- The extension does not store GitHub tokens; authentication stays delegated to `gh`.

## Development

```bash
cd vscode-extension
npm install
npm test
npx @vscode/vsce package
```
