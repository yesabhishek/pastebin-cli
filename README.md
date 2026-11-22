<p align="center">
  <img src="docs/icon.png" width="96" height="96" alt="Pastebin icon">
</p>

<h1 align="center">pb — encrypted notes that sync through your own GitHub</h1>

<p align="center">
  A fast CLI and a Notion-style VS Code extension for private notes, snippets and screenshots.<br>
  Everything is <b>end-to-end encrypted on your machine</b> before it is uploaded to a private GitHub repo you own.
</p>

<p align="center">
  <img src="docs/screenshots/quick-note-dark.png" alt="Pastebin in VS Code: Notion-style sidebar and the quick-note page" width="860">
</p>

---

## Why pb

- **Private by design.** Notes, note names and the index are encrypted with AES-256-GCM before they leave your device. GitHub only ever stores ciphertext and random-looking object IDs.
- **Works on every machine you use.** Unlock once per device with your passphrase. The key then lives in your OS keychain (macOS Keychain, Windows Credential Manager, or Secret Service on Linux).
- **Your storage, your account.** Data lives in a private repo in *your* GitHub account. There is no server, no subscription, and no third party.
- **Quick capture.** Press <kbd>⌥⌘P</kbd> (<kbd>Ctrl+Alt+P</kbd>) in VS Code, or run `pb note "…"` in a terminal.
- **Full version history.** Every save is a version you can browse and restore, fetched and decrypted on demand.
- **Multiple accounts.** See which GitHub account you're syncing to and switch with one click or `pb account switch`.
- **Offline-first.** Edits are cached locally and sync when you're back online. Conflicts never overwrite: they create a conflict copy instead.

## Install

### CLI

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/yesabhishek/pastebin-cli/main/scripts/install.sh | sh
```

Windows (PowerShell):

```powershell
iwr https://raw.githubusercontent.com/yesabhishek/pastebin-cli/main/scripts/install.ps1 -useb | iex
```

The installers verify the release's `SHA256SUMS` before installing, and need no sudo or admin rights. Requires the [GitHub CLI](https://cli.github.com/) (`gh auth login`).

From source (Go 1.26+):

```bash
git clone https://github.com/yesabhishek/pastebin-cli.git
cd pastebin-cli && go build -o pb ./cmd/pb
```

### VS Code extension

Search **"Pastebin — Encrypted Notes"** in the Extensions view, or install it from the [Marketplace](https://marketplace.visualstudio.com/items?itemName=AbhishekChoudhury.pastebin-vscode). It uses the `pb` CLI under the hood.

## Quick start

```bash
pb init                         # connects your GitHub account, creates a PRIVATE repo, sets your passphrase
pb note "call the bank at 3pm"  # quick note -> notes/2026-11-22-1405-call-the-bank-at-3pm.md
pb list
pb sync
```

On another machine:

```bash
pb init                         # same account -> asks for your passphrase once, then syncs
```

### Upgrading from pb ≤ 0.2 (unencrypted repos)

```bash
pb encrypt
```

This sets a passphrase, encrypts every existing note, and deletes the plaintext copies. Older pb releases are locked out on purpose so they can't upload plaintext again: upgrade pb on all your machines.

> **Git history:** encryption can't rewrite history you already pushed, so old plaintext versions remain in that repo's commit history. To get rid of them, start a fresh repo with `pb --repo pastebin-vault init`, then delete the old repo on GitHub.

## VS Code extension

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/history-light.png" alt="Version history with decrypted preview and restore"><br><sub><b>Version history</b>: browse decrypted past versions and restore with one click</sub></td>
    <td width="50%"><img src="docs/screenshots/account-switcher-light.png" alt="Account switcher menu in the sidebar"><br><sub><b>Account switcher</b>: see which GitHub account you sync to and switch any time</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/quick-note-dark.png" alt="Quick note page in the dark theme"><br><sub><b>Quick note</b> (<kbd>⌥⌘P</kbd>): type, press <kbd>⌘↵</kbd>, and it's encrypted and synced</sub></td>
    <td width="50%"><img src="docs/screenshots/locked-vault-dark.png" alt="Locked vault banner on a new device"><br><sub><b>New device?</b> Unlock once with your passphrase and everything syncs</sub></td>
  </tr>
</table>

| | |
|---|---|
| **Sidebar** | Notion-style page tree with search (<kbd>/</kbd>), recent pages, folders, and sync status |
| **Quick note** | <kbd>⌥⌘P</kbd> / <kbd>Ctrl+Alt+P</kbd> opens a capture page. <kbd>⌘↵</kbd> encrypts, saves and syncs |
| **Editing** | Pages open as normal editor tabs. <kbd>⌘S</kbd> saves and syncs |
| **History** | <kbd>⌥⌘H</kbd> on an open page, or **⋯ → Version history** in the sidebar |
| **Accounts** | Click your name at the top of the sidebar, or the `🔒 @login` status-bar item |
| **Search** | <kbd>⌥⌘O</kbd> / <kbd>Ctrl+Alt+O</kbd> to jump to any page |

## Commands

```text
pb init [--no-vault] [--stdin]          connect account + private repo, unlock or create the vault
pb note [text…] [--title T] [--stdin]   quick note (opens the editor when given no text)
pb account                              show the account you sync as and the available gh accounts
pb account switch <login>               sync with a different GitHub account
pb unlock | pb lock                     store / forget the vault key on this device
pb encrypt                              create the vault and encrypt legacy plaintext notes
pb passphrase                           change the passphrase (notes are not re-encrypted)
pb vault                                encryption status

pb new <path> | pb edit <path>          built-in terminal editor (Ctrl+S save, Ctrl+Q quit, Ctrl+V paste image)
pb read <path> [--out <file>]
pb save <path> --stdin
pb paste <path> | pb copy <path>        clipboard images and text
pb versions <path> [--limit N]
pb show <path> <version> | pb restore <path> <version>
pb delete <path> [--yes]
pb list [prefix] [--refresh]
pb sync | pb status
pb upgrade [--yes] [--check] [--policy prompt|auto|manual]
pb logout                               remove local state and device keys
```

Global flags: `--repo <name>` picks the storage repo, and `--json` gives machine-readable output.
Passphrases are read from the terminal without echo, or one per line from stdin with `--stdin`. They are never passed as command arguments.

## Security model

| What | How |
|---|---|
| Note contents | AES-256-GCM with a random nonce per write. Ciphertext is bound to its object ID, so blobs can't be swapped |
| Note names | Never uploaded in clear. Objects are stored as `objects/<HMAC-SHA256(path)>`, and commit messages contain only the ID |
| Index | Encrypted (`meta/index.enc`) |
| Data key | Random 256-bit key, wrapped with **Argon2id** (64 MiB, t=3) from your passphrase, stored as `meta/vault.json` |
| On this device | Data key in the OS keychain (0600 file fallback). Local cache in a `0700` directory |
| GitHub access | Talks to `api.github.com` over HTTPS with the token of the account *you* selected (`gh auth token --user`). Tokens are never put in arguments or logs |
| Repo visibility | pb refuses to use a **public** repo |
| Updates | `pb upgrade` and the installers verify `SHA256SUMS` and only download from GitHub over HTTPS |
| VS Code | Strict CSP webviews (no remote content, nonce-only scripts, no HTML injection). `pbPath` is only read from user settings, and local builds need a trusted workspace |

**What is still visible to GitHub:** the number of notes, their approximate sizes, and when you save them.
**There is no passphrase recovery.** Keep the passphrase in a password manager. Losing a *machine* is fine: install pb on another one and unlock with the passphrase.
Decrypted copies are cached on your disk, so use full-disk encryption (FileVault or BitLocker) on your machines.

## Storage backends

GitHub is the only backend today. Storage sits behind a small provider-agnostic interface ([`internal/store/backend.go`](internal/store/backend.go)), and encryption, indexing and sync all live above it, so adding **GitLab** (good fit: it has a repository files API with per-file history) or **Bitbucket** only requires writing an adapter. Contributions are welcome.

## Local data layout

Under your user config directory (`~/Library/Application Support/pb` on macOS, `~/.config/pb` on Linux, `%AppData%\pb` on Windows):

- `config.json`: selected account, repo, device id, upgrade preferences
- `accounts/<owner>/<repo>/state/`: index, sync journal, autosave drafts
- `accounts/<owner>/<repo>/cache/files/`: decrypted local cache
- `keys/`: only used when no OS keychain is available

## Development

```bash
brew install actionlint shellcheck
git config core.hooksPath .githooks
./scripts/check.sh                      # gofmt, vet, tests, actionlint, shellcheck
cd vscode-extension && npm ci && npm test
```

## License

MIT, see [LICENSE](LICENSE).
