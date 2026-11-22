package auth

import (
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
)

const Host = "github.com"

// Info describes the active GitHub CLI account.
type Info struct {
	Login string `json:"login"`
}

// Account is one GitHub account known to the GitHub CLI.
type Account struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Login    string `json:"login"`
	Host     string `json:"host"`
	Active   bool   `json:"active"`
	OK       bool   `json:"ok"`
}

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// ValidLogin reports whether s looks like a GitHub username. This keeps
// arbitrary strings out of command arguments and API paths.
func ValidLogin(s string) bool {
	return loginPattern.MatchString(s)
}

type GitHubAuth struct {
	run func(ctx context.Context, args ...string) ([]byte, error)
}

func New() *GitHubAuth {
	return &GitHubAuth{run: runGH}
}

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	return cmd.Output()
}

func (g *GitHubAuth) EnsureCLI() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return errs.Wrap(errs.CodeAuth, "GitHub CLI `gh` is required. Install it, then run `gh auth login`.", err)
	}
	return nil
}

// Accounts lists the accounts the GitHub CLI is logged in to.
func (g *GitHubAuth) Accounts(ctx context.Context) ([]Account, error) {
	if err := g.EnsureCLI(); err != nil {
		return nil, err
	}
	out, err := g.run(ctx, "auth", "status", "--hostname", Host, "--json", "hosts")
	if err != nil && len(out) == 0 {
		return nil, errs.Wrap(errs.CodeAuth, "GitHub CLI is not authenticated. Run `gh auth login`.", nil)
	}
	var payload struct {
		Hosts map[string][]struct {
			State  string `json:"state"`
			Active bool   `json:"active"`
			Host   string `json:"host"`
			Login  string `json:"login"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, errs.Wrap(errs.CodeAuth, "parse `gh auth status` output (upgrade gh to 2.60 or newer)", err)
	}
	accounts := []Account{}
	for _, entry := range payload.Hosts[Host] {
		if !ValidLogin(entry.Login) {
			continue
		}
		accounts = append(accounts, Account{
			ID:       "github:" + entry.Login,
			Provider: "github",
			Login:    entry.Login,
			Host:     Host,
			Active:   entry.Active,
			OK:       entry.State == "success",
		})
	}
	if len(accounts) == 0 {
		return nil, errs.Wrap(errs.CodeAuth, "GitHub CLI is not authenticated. Run `gh auth login`.", nil)
	}
	return accounts, nil
}

// Info returns the GitHub CLI's active account.
func (g *GitHubAuth) Info(ctx context.Context) (*Info, error) {
	accounts, err := g.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		if a.Active {
			return &Info{Login: a.Login}, nil
		}
	}
	return &Info{Login: accounts[0].Login}, nil
}

// Token returns the OAuth token for a specific login without changing the
// GitHub CLI's active account. The token stays in memory only.
func (g *GitHubAuth) Token(ctx context.Context, login string) (string, error) {
	if !ValidLogin(login) {
		return "", errs.Wrap(errs.CodeAuth, "invalid GitHub login in pb config", nil)
	}
	if err := g.EnsureCLI(); err != nil {
		return "", err
	}
	out, err := g.run(ctx, "auth", "token", "--hostname", Host, "--user", login)
	token := strings.TrimSpace(string(out))
	if err != nil || token == "" {
		return "", errs.Wrap(errs.CodeAuth, "GitHub CLI has no session for @"+login+". Run `gh auth login` and sign in as @"+login+", or switch with `pb account switch`.", nil)
	}
	return token, nil
}

func (g *GitHubAuth) LogoutHint() string {
	return "remove or refresh your GitHub CLI session with `gh auth logout` or `gh auth login`"
}
