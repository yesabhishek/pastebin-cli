package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/yesabhishek/pastebin-cli/internal/auth"
	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/keystore"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/store"
)

var (
	accountNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	hostPattern        = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+(:[0-9]{1,5})?$`)
)

// providerName returns the normalized provider, defaulting to GitHub.
func providerName(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "github", "gh":
		return model.ProviderGitHub
	case "gitlab", "gl":
		return model.ProviderGitLab
	case "bitbucket", "bb":
		return model.ProviderBitbucket
	default:
		return ""
	}
}

func providerLabel(p string) string {
	switch providerName(p) {
	case model.ProviderGitLab:
		return "GitLab"
	case model.ProviderBitbucket:
		return "Bitbucket"
	default:
		return "GitHub"
	}
}

func defaultHost(provider string) string {
	switch providerName(provider) {
	case model.ProviderGitLab:
		return store.DefaultGitLabHost
	case model.ProviderBitbucket:
		return "bitbucket.org"
	default:
		return auth.Host
	}
}

func cfgHost(cfg *model.Config) string {
	if cfg.Host != "" {
		return cfg.Host
	}
	return defaultHost(cfg.Provider)
}

// validOwner checks the account/namespace part of the storage repo. GitLab
// namespaces may contain nested groups ("group/sub").
func validOwner(provider, owner string) bool {
	if owner == "" {
		return false
	}
	parts := []string{owner}
	if providerName(provider) == model.ProviderGitLab {
		parts = strings.Split(owner, "/")
	}
	for _, part := range parts {
		if !accountNamePattern.MatchString(part) {
			return false
		}
	}
	return true
}

func validHost(h string) bool {
	return hostPattern.MatchString(h)
}

// AccountID is the stable identifier used by `pb account switch`.
func AccountID(provider, login string) string {
	return providerName(provider) + ":" + login
}

// parseAccountID accepts "provider:login" or a bare GitHub login.
func parseAccountID(id string) (provider, login string) {
	id = strings.TrimPrefix(strings.TrimSpace(id), "@")
	if i := strings.Index(id, ":"); i > 0 {
		return providerName(id[:i]), strings.TrimPrefix(id[i+1:], "@")
	}
	return model.ProviderGitHub, id
}

// scopeOwner keeps GitHub state where older releases put it and namespaces
// other providers so identical names never collide.
func scopeOwner(cfg *model.Config) string {
	if providerName(cfg.Provider) == model.ProviderGitHub {
		return cfg.Owner
	}
	return providerName(cfg.Provider) + "@" + cfgHost(cfg) + "~" + strings.ReplaceAll(cfg.Owner, "/", "~")
}

func (a *App) scopePaths(cfg *model.Config) config.Paths {
	return a.paths.Scope(scopeOwner(cfg), cfg.Repo)
}

// ---- credentials ----

type credential struct {
	Token string `json:"token"`
	Email string `json:"email,omitempty"`
}

func credentialKey(provider, host, login string) string {
	return "cred:" + providerName(provider) + ":" + strings.ToLower(host) + ":" + strings.ToLower(login)
}

func (a *App) loadCredential(provider, host, login string) (*credential, error) {
	secret, err := a.keys.Get(credentialKey(provider, host, login))
	if err != nil {
		return nil, errs.Wrap(errs.CodeAuth, fmt.Sprintf("no %s credentials for @%s on this device. Run `pb login %s`", providerLabel(provider), login, providerName(provider)), nil)
	}
	var c credential
	if err := json.Unmarshal([]byte(secret), &c); err != nil || c.Token == "" {
		return nil, errs.Wrap(errs.CodeAuth, "stored credentials are unreadable; run `pb login "+providerName(provider)+"` again", nil)
	}
	return &c, nil
}

func (a *App) saveCredential(provider, host, login string, c credential) error {
	data, _ := json.Marshal(c)
	if err := a.keys.Set(credentialKey(provider, host, login), string(data)); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "store credentials in the OS keychain", err)
	}
	return nil
}

// errBackend defers a configuration error until the remote is actually used,
// so local-only commands keep working.
type errBackend struct{ err error }

func (e errBackend) EnsureRepo(context.Context) (*store.RepoInfo, error)      { return nil, e.err }
func (e errBackend) Get(context.Context, string, string) (*store.Blob, error) { return nil, e.err }
func (e errBackend) Put(context.Context, string, []byte, string, string) (*store.PutResult, error) {
	return nil, e.err
}
func (e errBackend) Delete(context.Context, string, string, string) error { return e.err }
func (e errBackend) History(context.Context, string, int) ([]store.Commit, error) {
	return nil, e.err
}

func (a *App) defaultBackend(cfg *model.Config) store.Backend {
	switch providerName(cfg.Provider) {
	case model.ProviderGitLab:
		host := cfgHost(cfg)
		if !validHost(host) {
			return errBackend{errs.Wrap(errs.CodeUsage, "invalid GitLab host in pb config: "+host, nil)}
		}
		cred, err := a.loadCredential(cfg.Provider, host, cfg.Login)
		if err != nil {
			return errBackend{err}
		}
		return store.NewGitLabBackend(a.gitlabAPI(host), cfg.Owner, cfg.Repo, cred.Token)
	case model.ProviderBitbucket:
		cred, err := a.loadCredential(cfg.Provider, cfgHost(cfg), cfg.Login)
		if err != nil {
			return errBackend{err}
		}
		return store.NewBitbucketBackend(a.bitbucketAPI, cfg.Owner, cfg.Repo, cred.Email, cred.Token)
	default:
		login := cfg.Login
		return store.NewGitHubBackend(cfg.Owner, cfg.Repo, func(ctx context.Context) (string, error) {
			return a.auth.Token(ctx, login)
		})
	}
}

func (a *App) gitlabAPI(host string) string {
	if a.gitlabAPIOverride != "" {
		return a.gitlabAPIOverride
	}
	return store.GitLabAPIBase(host)
}

func (a *App) keyAccount(cfg *model.Config) string {
	if providerName(cfg.Provider) == model.ProviderGitHub {
		return keystore.Account(cfg.Owner, cfg.Repo)
	}
	return providerName(cfg.Provider) + ":" + strings.ToLower(cfgHost(cfg)) + ":" + keystore.Account(cfg.Owner, cfg.Repo)
}

// ---- pb login ----

func (a *App) cmdLogin(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errs.Wrap(errs.CodeUsage, "usage: pb login <github|gitlab|bitbucket> [--host <host>] [--email <atlassian-email>] [--stdin]", nil)
	}
	provider := providerName(args[0])
	if provider == "" {
		return errs.Wrap(errs.CodeUsage, "unknown provider "+args[0]+"; use github, gitlab or bitbucket", nil)
	}
	host, email := "", ""
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--stdin":
			a.secretFromStdin = true
		case (args[i] == "--host" || args[i] == "--email") && i+1 < len(args):
			if args[i] == "--host" {
				host = args[i+1]
			} else {
				email = args[i+1]
			}
			i++
		case strings.HasPrefix(args[i], "--host="):
			host = strings.TrimPrefix(args[i], "--host=")
		case strings.HasPrefix(args[i], "--email="):
			email = strings.TrimPrefix(args[i], "--email=")
		default:
			return errs.Wrap(errs.CodeUsage, "unknown login flag: "+args[i], nil)
		}
	}
	if provider == model.ProviderGitHub {
		fmt.Fprintln(a.out, "GitHub sign-in is handled by the GitHub CLI: run `gh auth login`, then `pb account switch github:<login>`.")
		return nil
	}
	if host == "" {
		host = defaultHost(provider)
	}
	if provider == model.ProviderGitLab && !validHost(host) {
		return errs.Wrap(errs.CodeUsage, "invalid GitLab host: "+host, nil)
	}
	if provider == model.ProviderBitbucket {
		host = "bitbucket.org"
		if email != "" && !strings.Contains(email, "@") {
			return errs.Wrap(errs.CodeUsage, "--email must be your Atlassian account email", nil)
		}
	}

	prompt := "GitLab personal access token (scope: api): "
	if provider == model.ProviderBitbucket {
		prompt = "Bitbucket API token (scopes: repository read/write/admin, account read): "
	}
	tokenBytes, err := a.readSecret(prompt, false)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(tokenBytes))
	wipe(tokenBytes)
	if token == "" {
		return errs.Wrap(errs.CodeUsage, "a token is required", nil)
	}

	var id *store.Identity
	if provider == model.ProviderGitLab {
		id, err = store.GitLabWhoAmI(ctx, a.gitlabAPI(host), token)
	} else {
		id, err = store.BitbucketWhoAmI(ctx, a.bitbucketAPI, email, token)
	}
	if err != nil {
		return err
	}
	if !accountNamePattern.MatchString(id.Login) {
		return errs.Wrap(errs.CodeAuth, "unexpected username from "+providerLabel(provider), nil)
	}
	if err := a.saveCredential(provider, host, id.Login, credential{Token: token, Email: email}); err != nil {
		return err
	}

	cfg, err := config.Load(a.paths)
	if err != nil {
		return err
	}
	if cfg == nil {
		deviceID, err := config.NewDeviceID()
		if err != nil {
			return err
		}
		cfg = &model.Config{Repo: config.DefaultRepo(), DeviceID: deviceID, UpgradePolicy: model.UpgradePolicyPrompt}
	}
	ref := model.LoginRef{Provider: provider, Host: host, Login: id.Login}
	found := false
	for i, l := range cfg.Logins {
		if l.Provider == ref.Provider && strings.EqualFold(l.Host, ref.Host) && strings.EqualFold(l.Login, ref.Login) {
			cfg.Logins[i] = ref
			found = true
		}
	}
	if !found {
		cfg.Logins = append(cfg.Logins, ref)
	}
	if err := config.Save(a.paths, cfg); err != nil {
		return err
	}
	accountID := AccountID(provider, id.Login)
	if a.jsonOut {
		return writeJSON(a.out, map[string]string{"id": accountID, "provider": provider, "host": host, "login": id.Login})
	}
	fmt.Fprintf(a.out, "Logged in to %s as @%s. Token stored in %s.\n", providerLabel(provider), id.Login, a.keys.Kind())
	fmt.Fprintf(a.out, "Sync with it: pb account switch %s\n", accountID)
	return nil
}

// storedAccounts lists accounts with credentials saved by `pb login`.
func (a *App) storedAccounts(cfg *model.Config) []auth.Account {
	out := []auth.Account{}
	if cfg == nil {
		return out
	}
	for _, l := range cfg.Logins {
		_, err := a.loadCredential(l.Provider, l.Host, l.Login)
		out = append(out, auth.Account{
			ID:       AccountID(l.Provider, l.Login),
			Provider: providerName(l.Provider),
			Login:    l.Login,
			Host:     l.Host,
			OK:       err == nil,
		})
	}
	return out
}

func (a *App) forgetCredentials(cfg *model.Config) {
	if cfg == nil {
		return
	}
	for _, l := range cfg.Logins {
		_ = a.keys.Delete(credentialKey(l.Provider, l.Host, l.Login))
	}
}
