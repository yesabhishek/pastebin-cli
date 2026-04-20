package auth

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
)

type Info struct {
	Login string `json:"login"`
}

type GitHubAuth struct{}

func New() *GitHubAuth {
	return &GitHubAuth{}
}

func (g *GitHubAuth) EnsureCLI() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return errs.Wrap(errs.CodeAuth, "GitHub CLI `gh` is required. Install it, then run `gh auth login`.", err)
	}
	return nil
}

func (g *GitHubAuth) Info(ctx context.Context) (*Info, error) {
	if err := g.EnsureCLI(); err != nil {
		return nil, err
	}
	tokenCmd := exec.CommandContext(ctx, "gh", "auth", "token")
	tokenOut, err := tokenCmd.Output()
	if err != nil || strings.TrimSpace(string(tokenOut)) == "" {
		return nil, errs.Wrap(errs.CodeAuth, "GitHub CLI is not authenticated. Run `gh auth login`.", err)
	}
	cmd := exec.CommandContext(ctx, "gh", "api", "user")
	out, err := cmd.Output()
	if err != nil {
		return nil, errs.Wrap(errs.CodeAuth, "failed to query GitHub user via `gh api user`", err)
	}
	var payload struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, errs.Wrap(errs.CodeAuth, "parse GitHub user response", err)
	}
	if payload.Login == "" {
		return nil, errs.Wrap(errs.CodeAuth, "GitHub login is empty", nil)
	}
	return &Info{Login: payload.Login}, nil
}

func (g *GitHubAuth) LogoutHint() string {
	return "remove or refresh your GitHub CLI session with `gh auth logout` or `gh auth login`"
}
