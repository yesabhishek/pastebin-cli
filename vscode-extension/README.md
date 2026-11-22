# Pastebin — Encrypted Notes for VS Code

A Notion-style notes sidebar for VS Code. Notes are **end-to-end encrypted on your machine** and synced through a private repo in **your own GitHub account**. There is no server and no subscription, and nobody else can read them.

## Highlights

- **Notion-style sidebar:** workspace header, search, recent pages, a nested page tree, and hover actions.
- **Quick note** with <kbd>⌥⌘P</kbd> / <kbd>Ctrl+Alt+P</kbd> from anywhere in VS Code. Type a title and some text, then press <kbd>⌘↵</kbd>. It's encrypted, saved and synced.
- **Native editing:** pages open as regular editor tabs (Markdown, multi-cursor, everything). <kbd>⌘S</kbd> syncs.
- **Version history:** browse decrypted past versions side by side and restore with one click (<kbd>⌥⌘H</kbd>).
- **See and switch your GitHub account:** click your name in the sidebar or the `🔒 @login` status-bar item.
- **Encrypted with AES-256-GCM**, with the key derived using Argon2id from your passphrase. Note names are encrypted too.
- **Unlock once per device.** The key is kept in your OS keychain.

## Requirements

1. [GitHub CLI](https://cli.github.com/), signed in with `gh auth login`.
2. The [`pb` CLI](https://github.com/yesabhishek/pastebin-cli#install) v0.3 or newer:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/yesabhishek/pastebin-cli/main/scripts/install.sh | sh
   ```

## Getting started

1. Open the **Pastebin** view in the activity bar and click **Get started**.
2. Pick a passphrase. It encrypts your notes, and you'll enter it once on each new device. **There is no recovery if you forget it.**
3. Press <kbd>⌥⌘P</kbd> and write your first note.

Already using pb on another machine? **Get started** detects your existing vault and asks for the passphrase instead.

## Commands

| Command | Shortcut |
|---|---|
| Pastebin: Quick Note | <kbd>⌥⌘P</kbd> / <kbd>Ctrl+Alt+P</kbd> |
| Pastebin: Search Pages | <kbd>⌥⌘O</kbd> / <kbd>Ctrl+Alt+O</kbd> |
| Pastebin: Version History | <kbd>⌥⌘H</kbd> / <kbd>Ctrl+Alt+H</kbd> (on a page) |
| Pastebin: New Page | |
| Pastebin: Sync | |
| Pastebin: Switch GitHub Account | |
| Pastebin: Unlock Vault / Lock Vault on This Device | |
| Pastebin: Change Vault Passphrase | |
| Pastebin: Save Current Editor to Pastebin | |

## Settings

- `pastebin.pbPath`: path to the `pb` binary (user setting only. Workspaces can't override it).
- `pastebin.syncOnStartup`: sync in the background when VS Code starts (default on).
- `pastebin.autoSync`: sync every 10 minutes while VS Code is focused (default on).

## Privacy & security

- The extension never sees your GitHub token. pb fetches it from the GitHub CLI for the account you selected.
- Passphrases go to `pb` over stdin only, never as command-line arguments, and are never stored by the extension.
- Webviews run under a strict Content-Security-Policy: no remote content, and no HTML built from note data.
- In untrusted workspaces, building pb from source is disabled.

## Troubleshooting

- **"Install the pb CLI"**: install pb, or set `pastebin.pbPath` to its full path.
- **"Update the pb CLI"**: run `pb upgrade`.
- **"Vault locked on this device"**: click **Unlock** and enter your passphrase.
- **Wrong account?** Click your name at the top of the sidebar and pick another one, or choose **Add GitHub account…**.

## Development

```bash
cd vscode-extension
npm ci
npm test
npx @vscode/vsce package
```
