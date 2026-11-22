package ui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/yesabhishek/pastebin-cli/internal/auth"
	"github.com/yesabhishek/pastebin-cli/internal/cache"
	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/keystore"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/store"
	"github.com/yesabhishek/pastebin-cli/internal/vault"
)

// Stable phrases the VS Code extension matches on.
const (
	msgLocked       = "vault is locked on this device. Run `pb unlock` and enter your passphrase"
	msgNotEncrypted = "this notes repo is not encrypted yet. Run `pb encrypt` to set a passphrase"
)

// cachedKeys returns keys from process memory or the device keystore.
func (a *App) cachedKeys(cfg *model.Config) (*vault.Keys, error) {
	account := a.keyAccount(cfg)
	if k := a.unlocked[account]; k != nil {
		return k, nil
	}
	secret, err := a.keys.Get(account)
	if err != nil {
		return nil, err
	}
	k, err := vault.Import(secret)
	if err != nil {
		return nil, err
	}
	a.unlocked[account] = k
	return k, nil
}

func (a *App) rememberKeys(cfg *model.Config, k *vault.Keys) error {
	account := a.keyAccount(cfg)
	a.unlocked[account] = k
	if err := a.keys.Set(account, k.Export()); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "store vault key on this device", err)
	}
	return nil
}

// keyProvider is handed to the encrypted store; it unlocks lazily so local
// commands (list, status) never need the key.
func (a *App) keyProvider(cfg *model.Config) store.KeyProvider {
	return func(ctx context.Context) (*vault.Keys, error) {
		if k, err := a.cachedKeys(cfg); err == nil {
			return k, nil
		}
		if !a.interactive {
			return nil, errs.Wrap(errs.CodeLocked, msgLocked, store.ErrLocked)
		}
		file, _, err := store.ReadVault(ctx, a.backendFor(cfg))
		if err != nil {
			return nil, err
		}
		if file == nil {
			return nil, errs.Wrap(errs.CodeLocked, msgNotEncrypted, store.ErrLocked)
		}
		pass, err := a.readSecret(fmt.Sprintf("Passphrase for %s/%s: ", cfg.Owner, cfg.Repo), false)
		if err != nil {
			return nil, err
		}
		k, err := file.Unlock(pass)
		wipe(pass)
		if err != nil {
			return nil, errs.Wrap(errs.CodeAuth, "unlock vault", err)
		}
		if err := a.rememberKeys(cfg, k); err != nil {
			return nil, err
		}
		return k, nil
	}
}

// readSecret reads a passphrase from stdin (one line) when stdinMode is set,
// otherwise from the terminal without echo.
func (a *App) readSecret(prompt string, _ bool) ([]byte, error) {
	if a.secretFromStdin {
		if a.stdinReader == nil {
			a.stdinReader = bufio.NewReader(a.in)
		}
		line, err := a.stdinReader.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			return nil, errs.Wrap(errs.CodeUsage, "expected passphrase on stdin", err)
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
	if !a.interactive {
		return nil, errs.Wrap(errs.CodeLocked, "a passphrase is required; run this in a terminal or pass --stdin", nil)
	}
	fmt.Fprint(a.errOut, prompt)
	pass, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(a.errOut)
	if err != nil {
		return nil, errs.Wrap(errs.CodeUsage, "read passphrase", err)
	}
	return pass, nil
}

func (a *App) readNewPassphrase() ([]byte, error) {
	first, err := a.readSecret("New passphrase (min 10 chars, there is NO recovery if you forget it): ", true)
	if err != nil {
		return nil, err
	}
	if len(first) < vault.MinPassphraseLength {
		wipe(first)
		return nil, errs.Wrap(errs.CodeUsage, vault.ErrWeakPassphrase.Error(), nil)
	}
	if a.secretFromStdin {
		return first, nil
	}
	second, err := a.readSecret("Repeat passphrase: ", true)
	if err != nil {
		return nil, err
	}
	defer wipe(second)
	if !bytes.Equal(first, second) {
		wipe(first)
		return nil, errs.Wrap(errs.CodeUsage, "passphrases do not match", nil)
	}
	return first, nil
}

// setupVault unlocks an existing vault or creates a new one, then encrypts
// any plaintext notes left by older pb releases.
func (a *App) setupVault(ctx context.Context, cfg *model.Config) error {
	backend := a.backendFor(cfg)
	file, _, err := store.ReadVault(ctx, backend)
	if err != nil {
		return err
	}
	var keys *vault.Keys
	if file != nil {
		if k, err := a.cachedKeys(cfg); err == nil && k.VaultID == file.ID {
			keys = k
		} else {
			pass, err := a.readSecret(fmt.Sprintf("Passphrase for %s/%s: ", cfg.Owner, cfg.Repo), false)
			if err != nil {
				return err
			}
			keys, err = file.Unlock(pass)
			wipe(pass)
			if err != nil {
				return errs.Wrap(errs.CodeAuth, "unlock vault", err)
			}
		}
		if err := a.rememberKeys(cfg, keys); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Vault unlocked for %s/%s (key stored in %s).\n", cfg.Owner, cfg.Repo, a.keys.Kind())
	} else {
		fmt.Fprintf(a.out, "Setting up end-to-end encryption for %s/%s.\n", cfg.Owner, cfg.Repo)
		pass, err := a.readNewPassphrase()
		if err != nil {
			return err
		}
		file, keys, err = vault.Create(pass)
		wipe(pass)
		if err != nil {
			return errs.Wrap(errs.CodeUsage, "create vault", err)
		}
		if err := store.WriteVault(ctx, backend, file, ""); err != nil {
			return err
		}
		if err := a.rememberKeys(cfg, keys); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Vault created. Use the same passphrase to unlock on your other machines.\n")
	}
	report, err := store.MigrateLegacy(ctx, backend, keys, cache.ValidatePath)
	if err != nil {
		return err
	}
	if len(report.Migrated) > 0 {
		fmt.Fprintf(a.out, "Encrypted %d existing note(s) and removed their plaintext copies.\n", len(report.Migrated))
		fmt.Fprintf(a.out, "NOTE: older plaintext versions still exist in the repo's git history. To erase them, create a fresh repo (`pb --repo <new-name> init`) and delete %s/%s on GitHub.\n", cfg.Owner, cfg.Repo)
	}
	for _, skipped := range report.Skipped {
		fmt.Fprintf(a.errOut, "pb: skipped legacy file with unsafe or missing path: %q\n", skipped)
	}
	return nil
}

func (a *App) cmdUnlock(ctx context.Context, args []string) error {
	if err := a.parseSecretFlags("unlock", args); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	file, _, err := store.ReadVault(ctx, a.backendFor(cfg))
	if err != nil {
		return err
	}
	if file == nil {
		return errs.Wrap(errs.CodeLocked, msgNotEncrypted, nil)
	}
	pass, err := a.readSecret(fmt.Sprintf("Passphrase for %s/%s: ", cfg.Owner, cfg.Repo), false)
	if err != nil {
		return err
	}
	keys, err := file.Unlock(pass)
	wipe(pass)
	if err != nil {
		return errs.Wrap(errs.CodeAuth, "unlock vault", err)
	}
	if err := a.rememberKeys(cfg, keys); err != nil {
		return err
	}
	if a.jsonOut {
		return writeJSON(a.out, map[string]any{"unlocked": true, "keystore": a.keys.Kind()})
	}
	fmt.Fprintf(a.out, "Unlocked %s/%s. Key stored in %s.\n", cfg.Owner, cfg.Repo, a.keys.Kind())
	return nil
}

func (a *App) cmdEncrypt(ctx context.Context, args []string) error {
	if err := a.parseSecretFlags("encrypt", args); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if err := a.backendEnsure(ctx, cfg); err != nil {
		return err
	}
	return a.setupVault(ctx, cfg)
}

func (a *App) cmdLock() error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	delete(a.unlocked, a.keyAccount(cfg))
	if err := a.keys.Delete(a.keyAccount(cfg)); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "remove vault key", err)
	}
	fmt.Fprintf(a.out, "Locked %s/%s on this device. Run `pb unlock` to sync again.\n", cfg.Owner, cfg.Repo)
	return nil
}

func (a *App) cmdPassphrase(ctx context.Context, args []string) error {
	if err := a.parseSecretFlags("passphrase", args); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	backend := a.backendFor(cfg)
	file, sha, err := store.ReadVault(ctx, backend)
	if err != nil {
		return err
	}
	if file == nil {
		return errs.Wrap(errs.CodeLocked, msgNotEncrypted, nil)
	}
	old, err := a.readSecret("Current passphrase: ", false)
	if err != nil {
		return err
	}
	keys, err := file.Unlock(old)
	wipe(old)
	if err != nil {
		return errs.Wrap(errs.CodeAuth, "unlock vault", err)
	}
	next, err := a.readNewPassphrase()
	if err != nil {
		return err
	}
	err = file.ChangePassphrase(keys, next)
	wipe(next)
	if err != nil {
		return errs.Wrap(errs.CodeUsage, "change passphrase", err)
	}
	if err := store.WriteVault(ctx, backend, file, sha); err != nil {
		return err
	}
	if err := a.rememberKeys(cfg, keys); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "Passphrase changed. Other devices stay unlocked; new devices need the new passphrase.")
	return nil
}

type vaultStatus struct {
	Provider    string `json:"provider"`
	ID          string `json:"id"`
	Account     string `json:"account"`
	Repo        string `json:"repo"`
	Encrypted   bool   `json:"encrypted"`
	Unlocked    bool   `json:"unlocked"`
	LegacyFiles int    `json:"legacy_files"`
	Keystore    string `json:"keystore"`
	Needs       string `json:"needs"`
}

func (a *App) vaultStatus(ctx context.Context, cfg *model.Config) (*vaultStatus, error) {
	backend := a.backendFor(cfg)
	file, _, err := store.ReadVault(ctx, backend)
	if err != nil {
		return nil, err
	}
	st := &vaultStatus{Provider: providerName(cfg.Provider), ID: AccountID(cfg.Provider, cfg.Login), Account: cfg.Login, Repo: cfg.Owner + "/" + cfg.Repo, Encrypted: file != nil, Keystore: a.keys.Kind()}
	if k, err := a.cachedKeys(cfg); err == nil && file != nil && k.VaultID == file.ID {
		st.Unlocked = true
	}
	legacy, _, err := store.LegacyState(ctx, backend)
	if err == nil && legacy != nil {
		for _, f := range legacy.Files {
			if f != nil && !f.Deleted {
				st.LegacyFiles++
			}
		}
	}
	switch {
	case file == nil:
		st.Needs = "encrypt"
	case !st.Unlocked:
		st.Needs = "unlock"
	case st.LegacyFiles > 0:
		st.Needs = "encrypt"
	}
	return st, nil
}

func (a *App) cmdVault(ctx context.Context) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	st, err := a.vaultStatus(ctx, cfg)
	if err != nil {
		return err
	}
	if a.jsonOut {
		return writeJSON(a.out, st)
	}
	fmt.Fprintf(a.out, "Account:    %s\nRepo:       %s (%s)\nEncrypted:  %t\nUnlocked:   %t\nKey store:  %s\n", st.ID, st.Repo, cfgHost(cfg), st.Encrypted, st.Unlocked, st.Keystore)
	if st.LegacyFiles > 0 {
		fmt.Fprintf(a.out, "Plaintext:  %d legacy note(s) - run `pb encrypt`\n", st.LegacyFiles)
	}
	return nil
}

func (a *App) backendEnsure(ctx context.Context, cfg *model.Config) error {
	_, err := a.backendFor(cfg).EnsureRepo(ctx)
	return err
}

func (a *App) parseSecretFlags(cmd string, args []string) error {
	for _, arg := range args {
		switch arg {
		case "--stdin":
			a.secretFromStdin = true
		default:
			return errs.Wrap(errs.CodeUsage, fmt.Sprintf("usage: pb %s [--stdin]", cmd), nil)
		}
	}
	return nil
}

// ---- accounts ----

type accountView struct {
	Current  string         `json:"current"`
	Provider string         `json:"provider"`
	Repo     string         `json:"repo"`
	Accounts []auth.Account `json:"accounts"`
}

// allAccounts merges GitHub CLI accounts with GitLab/Bitbucket logins.
func (a *App) allAccounts(ctx context.Context, cfg *model.Config) []auth.Account {
	accounts := []auth.Account{}
	if gh, err := a.auth.Accounts(ctx); err == nil {
		accounts = append(accounts, gh...)
	}
	return append(accounts, a.storedAccounts(cfg)...)
}

func (a *App) cmdAccount(ctx context.Context, args []string) error {
	if len(args) >= 1 && (args[0] == "switch" || args[0] == "remove") {
		sub := args[0]
		owner := ""
		rest := []string{}
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--stdin":
				a.secretFromStdin = true
			case (args[i] == "--owner" || args[i] == "--workspace" || args[i] == "--group") && i+1 < len(args):
				owner = args[i+1]
				i++
			case strings.HasPrefix(args[i], "--owner="):
				owner = strings.TrimPrefix(args[i], "--owner=")
			default:
				rest = append(rest, args[i])
			}
		}
		if len(rest) != 1 {
			return errs.Wrap(errs.CodeUsage, "usage: pb account "+sub+" <provider:login> [--owner <group-or-workspace>]", nil)
		}
		if sub == "remove" {
			return a.removeAccount(rest[0])
		}
		return a.switchAccount(ctx, rest[0], owner)
	}
	if len(args) != 0 {
		return errs.Wrap(errs.CodeUsage, "usage: pb account [switch <provider:login> | remove <provider:login>]", nil)
	}
	cfg, err := config.Load(a.paths)
	if err != nil {
		return err
	}
	view := accountView{Accounts: a.allAccounts(ctx, cfg)}
	if cfg != nil && cfg.Owner != "" {
		view.Current = AccountID(cfg.Provider, cfg.Login)
		view.Provider = providerName(cfg.Provider)
		view.Repo = cfg.Owner + "/" + cfg.Repo
	}
	if a.jsonOut {
		return writeJSON(a.out, view)
	}
	if view.Current != "" {
		fmt.Fprintf(a.out, "Syncing as %s -> %s on %s\n\n", view.Current, view.Repo, cfgHost(cfg))
	} else {
		fmt.Fprintln(a.out, "pb is not set up yet. Run `pb init` (GitHub) or `pb login gitlab|bitbucket`.")
	}
	if len(view.Accounts) == 0 {
		fmt.Fprintln(a.out, "No accounts yet. Sign in with `gh auth login`, `pb login gitlab` or `pb login bitbucket`.")
		return nil
	}
	fmt.Fprintln(a.out, "Accounts:")
	for _, acc := range view.Accounts {
		marker := "  "
		if acc.ID == view.Current {
			marker = "* "
		}
		note := ""
		if !acc.OK {
			note = " (signed out - sign in again)"
		}
		fmt.Fprintf(a.out, "%s%-32s %s%s\n", marker, acc.ID, acc.Host, note)
	}
	fmt.Fprintln(a.out, "\nSwitch with: pb account switch <provider:login>")
	return nil
}

func (a *App) removeAccount(id string) error {
	provider, login := parseAccountID(id)
	cfg, err := config.Load(a.paths)
	if err != nil {
		return err
	}
	if cfg == nil {
		return errs.Wrap(errs.CodeUsage, "no accounts to remove", nil)
	}
	if provider == model.ProviderGitHub {
		return errs.Wrap(errs.CodeUsage, "GitHub accounts are managed by the GitHub CLI: run `gh auth logout --user "+login+"`", nil)
	}
	if AccountID(cfg.Provider, cfg.Login) == AccountID(provider, login) && cfg.Owner != "" {
		return errs.Wrap(errs.CodeUsage, "switch to another account before removing the one you sync with", nil)
	}
	kept := cfg.Logins[:0]
	removed := false
	for _, l := range cfg.Logins {
		if providerName(l.Provider) == provider && strings.EqualFold(l.Login, login) {
			_ = a.keys.Delete(credentialKey(l.Provider, l.Host, l.Login))
			removed = true
			continue
		}
		kept = append(kept, l)
	}
	if !removed {
		return errs.Wrap(errs.CodeNotFound, "no stored login "+id, nil)
	}
	cfg.Logins = kept
	if err := config.Save(a.paths, cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Removed %s and its token from this device.\n", AccountID(provider, login))
	return nil
}

func (a *App) switchAccount(ctx context.Context, id, owner string) error {
	provider, login := parseAccountID(id)
	if provider == "" || !accountNamePattern.MatchString(login) {
		return errs.Wrap(errs.CodeUsage, "invalid account: "+id+" (use provider:login, e.g. gitlab:alice)", nil)
	}
	cfg, err := config.Load(a.paths)
	if err != nil {
		return err
	}
	host := defaultHost(provider)
	switch provider {
	case model.ProviderGitHub:
		accounts, err := a.auth.Accounts(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, acc := range accounts {
			if strings.EqualFold(acc.Login, login) {
				login = acc.Login
				found = true
			}
		}
		if !found {
			return errs.Wrap(errs.CodeAuth, "GitHub CLI is not signed in as @"+login+". Run `gh auth login` first.", nil)
		}
	default:
		found := false
		if cfg != nil {
			for _, l := range cfg.Logins {
				if providerName(l.Provider) == provider && strings.EqualFold(l.Login, login) {
					login, host, found = l.Login, l.Host, true
				}
			}
		}
		if !found {
			return errs.Wrap(errs.CodeAuth, "not signed in to "+providerLabel(provider)+" as @"+login+". Run `pb login "+provider+"` first.", nil)
		}
	}
	if owner == "" {
		owner = login
	}
	if !validOwner(provider, owner) {
		return errs.Wrap(errs.CodeUsage, "invalid owner/workspace: "+owner, nil)
	}
	if cfg == nil {
		deviceID, err := config.NewDeviceID()
		if err != nil {
			return err
		}
		cfg = &model.Config{Repo: config.DefaultRepo(), DeviceID: deviceID, UpgradePolicy: model.UpgradePolicyPrompt}
	}
	cfg.Provider = provider
	cfg.Host = host
	if provider == model.ProviderGitHub {
		cfg.Host = ""
	}
	cfg.Login = login
	cfg.Owner = owner
	if err := config.Save(a.paths, cfg); err != nil {
		return err
	}
	a.paths = a.scopePaths(cfg)
	if err := config.EnsureLayout(a.paths); err != nil {
		return err
	}
	if err := a.backendEnsure(ctx, cfg); err != nil {
		return err
	}
	st, err := a.vaultStatus(ctx, cfg)
	if err != nil {
		return err
	}
	if st.Needs != "" && (a.interactive || a.secretFromStdin) {
		if err := a.setupVault(ctx, cfg); err != nil {
			return err
		}
		st, _ = a.vaultStatus(ctx, cfg)
	}
	if a.jsonOut {
		return writeJSON(a.out, st)
	}
	fmt.Fprintf(a.out, "Now syncing as %s -> %s\n", AccountID(provider, login), st.Repo)
	if st.Needs == "unlock" {
		fmt.Fprintln(a.out, "Run `pb unlock` to unlock this account's vault.")
	} else if st.Needs == "encrypt" {
		fmt.Fprintln(a.out, "Run `pb encrypt` to set a passphrase for this account.")
	}
	return nil
}

// ---- quick note ----

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugUnsafe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return s
}

// QuickNotePath builds notes/YYYY-MM-DD-HHMM-<slug>.md.
func QuickNotePath(title, body string, now time.Time) string {
	seed := title
	if strings.TrimSpace(seed) == "" {
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) != "" {
				seed = strings.TrimLeft(line, "# ")
				break
			}
		}
	}
	slug := slugify(seed)
	stamp := now.Format("2006-01-02-1504")
	if slug == "" {
		slug = "note"
	}
	return "notes/" + stamp + "-" + slug + ".md"
}

func (a *App) cmdNote(ctx context.Context, args []string) error {
	title := ""
	target := ""
	fromStdin := false
	words := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--stdin":
			fromStdin = true
		case arg == "--title" || arg == "--path":
			if i+1 >= len(args) {
				return errs.Wrap(errs.CodeUsage, arg+" requires a value", nil)
			}
			i++
			if arg == "--title" {
				title = args[i]
			} else {
				target = args[i]
			}
		case strings.HasPrefix(arg, "--title="):
			title = strings.TrimPrefix(arg, "--title=")
		case strings.HasPrefix(arg, "--path="):
			target = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			return errs.Wrap(errs.CodeUsage, "unknown note flag: "+arg, nil)
		default:
			words = append(words, arg)
		}
	}
	body := strings.Join(words, " ")
	if fromStdin {
		data, err := readAllLimited(a.in, 50<<20)
		if err != nil {
			return err
		}
		body = string(data)
	}
	if strings.TrimSpace(body) == "" && strings.TrimSpace(title) == "" {
		if !a.interactive {
			return errs.Wrap(errs.CodeUsage, "usage: pb note [text...] [--title <title>] [--stdin]", nil)
		}
		if target == "" {
			target = QuickNotePath("", "", time.Now())
		}
		return a.editPath(ctx, target, true)
	}
	content := body
	if strings.TrimSpace(title) != "" {
		content = "# " + strings.TrimSpace(title) + "\n\n" + strings.TrimLeft(body, "\n")
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if target == "" {
		target = QuickNotePath(title, body, time.Now())
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	outcome, err := a.service(cfg).SaveContent(ctx, target, []byte(content))
	if err != nil {
		return err
	}
	if a.jsonOut {
		return writeJSON(a.out, saveOutcomeJSON(outcome))
	}
	fmt.Fprintf(a.out, "%s: %s\n", outcome.Path, outcome.Message)
	return nil
}

func saveOutcomeJSON(outcome *model.SaveOutcome) map[string]any {
	return map[string]any{
		"path":          outcome.Path,
		"remote_saved":  outcome.RemoteSaved,
		"message":       outcome.Message,
		"version_id":    outcome.VersionID,
		"conflict_path": outcome.ConflictPath,
		"locked":        outcome.Locked,
	}
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, errs.Wrap(errs.CodeLocalCorruption, "read stdin", err)
	}
	if int64(len(data)) > limit {
		return nil, errs.Wrap(errs.CodeUsage, "input is larger than "+strconv.FormatInt(limit>>20, 10)+" MB", nil)
	}
	return data, nil
}

// forgetAllKeys removes device keys for every account pb has state for.
func (a *App) forgetAllKeys() {
	accountsDir := filepath.Join(a.paths.RootDir, "accounts")
	owners, _ := os.ReadDir(accountsDir)
	names := []string{}
	for _, owner := range owners {
		repos, _ := os.ReadDir(filepath.Join(accountsDir, owner.Name()))
		for _, repo := range repos {
			ownerDir := owner.Name()
			if at, tilde := strings.Index(ownerDir, "@"), strings.Index(ownerDir, "~"); at > 0 && tilde > at {
				// provider@host~owner (GitLab/Bitbucket scopes)
				names = append(names, ownerDir[:at]+":"+strings.ToLower(ownerDir[at+1:tilde])+":"+
					keystore.Account(strings.ReplaceAll(ownerDir[tilde+1:], "~", "/"), repo.Name()))
				continue
			}
			names = append(names, keystore.Account(ownerDir, repo.Name()))
		}
	}
	if cfg, err := config.Load(a.paths); err == nil && cfg != nil {
		names = append(names, a.keyAccount(cfg))
	}
	sort.Strings(names)
	for _, n := range names {
		_ = a.keys.Delete(n)
	}
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
