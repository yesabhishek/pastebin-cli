package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
)

const (
	defaultGitHubAPI = "https://api.github.com"
	maxResponseBytes = 150 << 20
)

// TokenFunc returns a GitHub token for the configured account.
type TokenFunc func(ctx context.Context) (string, error)

// GitHubBackend talks to the GitHub REST API directly over HTTPS. Tokens are
// sent only in the Authorization header to the GitHub API host and never
// appear in process arguments, logs or error messages.
type GitHubBackend struct {
	owner   string
	repo    string
	apiBase string
	client  *http.Client
	token   TokenFunc

	mu        sync.Mutex
	tokenOnce string
}

func NewGitHubBackend(owner, repo string, token TokenFunc) *GitHubBackend {
	return &GitHubBackend{
		owner:   owner,
		repo:    repo,
		apiBase: defaultGitHubAPI,
		client:  &http.Client{Timeout: 60 * time.Second},
		token:   token,
	}
}

// WithAPIBase overrides the API base URL (used by tests).
func (g *GitHubBackend) WithAPIBase(base string) *GitHubBackend {
	g.apiBase = strings.TrimRight(base, "/")
	return g
}

func (g *GitHubBackend) EnsureRepo(ctx context.Context) (*RepoInfo, error) {
	var repo struct {
		Private bool `json:"private"`
		Owner   struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	err := g.do(ctx, http.MethodGet, g.repoPath(""), nil, &repo)
	if err == nil {
		if !repo.Private {
			return nil, errs.Wrap(errs.CodeAuth, fmt.Sprintf("refusing to use %s/%s because it is PUBLIC. Make it private on GitHub or choose another repo with --repo", g.owner, g.repo), nil)
		}
		return &RepoInfo{Private: true}, nil
	}
	if !errs.IsCode(err, errs.CodeNotFound) {
		return nil, err
	}
	body := map[string]any{
		"name":          g.repo,
		"private":       true,
		"has_issues":    false,
		"has_wiki":      false,
		"has_projects":  false,
		"description":   "Encrypted notes storage for pb (pastebin-cli). Contents are end-to-end encrypted.",
		"auto_init":     false,
		"has_downloads": false,
	}
	var created struct {
		Private bool `json:"private"`
	}
	if err := g.do(ctx, http.MethodPost, "/user/repos", body, &created); err != nil {
		return nil, errs.Wrap(errs.CodeAuth, "create private storage repo", err)
	}
	if !created.Private {
		return nil, errs.Wrap(errs.CodeAuth, "GitHub created the storage repo without private visibility; aborting", nil)
	}
	return &RepoInfo{Private: true, Created: true}, nil
}

func (g *GitHubBackend) Get(ctx context.Context, path, ref string) (*Blob, error) {
	endpoint := g.repoPath("/contents/" + escapePath(path))
	if ref != "" {
		endpoint += "?ref=" + url.QueryEscape(ref)
	}
	var payload struct {
		Type     string `json:"type"`
		SHA      string `json:"sha"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
	}
	if err := g.do(ctx, http.MethodGet, endpoint, nil, &payload); err != nil {
		return nil, err
	}
	if payload.Type != "" && payload.Type != "file" {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "remote path is not a file: "+path, nil)
	}
	// The contents API omits bodies over 1 MB; fall back to the blob API.
	if payload.Encoding == "none" || (payload.Content == "" && payload.Size > 0) {
		var blob struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := g.do(ctx, http.MethodGet, g.repoPath("/git/blobs/"+url.PathEscape(payload.SHA)), nil, &blob); err != nil {
			return nil, err
		}
		payload.Content = blob.Content
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "decode remote file", err)
	}
	return &Blob{Data: data, SHA: payload.SHA}, nil
}

func (g *GitHubBackend) Put(ctx context.Context, path string, data []byte, prevSHA, message string) (*PutResult, error) {
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(data),
	}
	if prevSHA != "" {
		body["sha"] = prevSHA
	}
	var payload struct {
		Content struct {
			SHA string `json:"sha"`
		} `json:"content"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.do(ctx, http.MethodPut, g.repoPath("/contents/"+escapePath(path)), body, &payload); err != nil {
		return nil, err
	}
	return &PutResult{BlobSHA: payload.Content.SHA, CommitSHA: payload.Commit.SHA}, nil
}

func (g *GitHubBackend) Delete(ctx context.Context, path, sha, message string) error {
	if sha == "" {
		return nil
	}
	body := map[string]any{"message": message, "sha": sha}
	return g.do(ctx, http.MethodDelete, g.repoPath("/contents/"+escapePath(path)), body, nil)
}

func (g *GitHubBackend) History(ctx context.Context, path string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 50
	}
	commits := make([]Commit, 0, limit)
	for page := 1; len(commits) < limit; page++ {
		perPage := limit - len(commits)
		if perPage > 100 {
			perPage = 100
		}
		endpoint := g.repoPath("/commits?path=" + url.QueryEscape(path) +
			"&per_page=" + strconv.Itoa(perPage) + "&page=" + strconv.Itoa(page))
		var payload []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Date time.Time `json:"date"`
				} `json:"author"`
				Committer struct {
					Date time.Time `json:"date"`
				} `json:"committer"`
			} `json:"commit"`
		}
		if err := g.do(ctx, http.MethodGet, endpoint, nil, &payload); err != nil {
			if errs.IsCode(err, errs.CodeConflict) {
				// GitHub returns 409 for history on an empty repository.
				return commits, nil
			}
			return nil, err
		}
		for _, item := range payload {
			if item.SHA == "" {
				continue
			}
			ts := item.Commit.Author.Date
			if ts.IsZero() {
				ts = item.Commit.Committer.Date
			}
			commits = append(commits, Commit{SHA: item.SHA, Message: item.Commit.Message, Time: ts})
		}
		if len(payload) < perPage {
			break
		}
	}
	return commits, nil
}

func (g *GitHubBackend) repoPath(suffix string) string {
	return "/repos/" + url.PathEscape(g.owner) + "/" + url.PathEscape(g.repo) + suffix
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (g *GitHubBackend) getToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tokenOnce != "" {
		return g.tokenOnce, nil
	}
	if g.token == nil {
		return "", errs.Wrap(errs.CodeAuth, "no GitHub token source configured", nil)
	}
	tok, err := g.token(ctx)
	if err != nil {
		return "", err
	}
	g.tokenOnce = tok
	return tok, nil
}

func (g *GitHubBackend) do(ctx context.Context, method, endpoint string, body any, out any) error {
	token, err := g.getToken(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return errs.Wrap(errs.CodeLocalCorruption, "encode request", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.apiBase+endpoint, reader)
	if err != nil {
		return errs.Wrap(errs.CodeNetwork, "build GitHub request", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "pb-cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errs.Wrap(errs.CodeNetwork, "GitHub request cancelled", ctx.Err())
		}
		return errs.Wrap(errs.CodeNetwork, "GitHub network request failed", sanitizeNetErr(err))
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxResponseBytes)
	if resp.StatusCode >= 300 {
		msg := readGitHubMessage(limited)
		return classifyStatus(resp, msg)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return errs.Wrap(errs.CodeRemoteCorruption, "parse GitHub response", err)
	}
	return nil
}

func readGitHubMessage(r io.Reader) string {
	var payload struct {
		Message string `json:"message"`
	}
	data, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	if json.Unmarshal(data, &payload) == nil && payload.Message != "" {
		return payload.Message
	}
	return strings.TrimSpace(string(data))
}

func classifyStatus(resp *http.Response, msg string) error {
	status := resp.StatusCode
	detail := fmt.Sprintf("GitHub API %d", status)
	if msg != "" {
		detail += ": " + msg
	}
	switch {
	case status == http.StatusUnauthorized:
		return errs.Wrap(errs.CodeAuth, "GitHub rejected the token. Run `gh auth login` for this account", nil)
	case status == http.StatusNotFound:
		return errs.Wrap(errs.CodeNotFound, "GitHub resource not found", nil)
	case status == http.StatusConflict || status == http.StatusUnprocessableEntity:
		return errs.Wrap(errs.CodeConflict, detail, nil)
	case status == http.StatusTooManyRequests,
		status == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(msg), "rate limit")):
		return errs.Wrap(errs.CodeRateLimit, "GitHub API rate limit reached; try again later", nil)
	case status == http.StatusForbidden:
		return errs.Wrap(errs.CodeAuth, detail, nil)
	case status >= 500:
		return errs.Wrap(errs.CodeNetwork, detail, nil)
	default:
		return errs.Wrap(errs.CodeUnknown, detail, nil)
	}
}

// sanitizeNetErr strips the request URL from transport errors so nothing but
// the API host can end up in messages.
func sanitizeNetErr(err error) error {
	var uerr *url.Error
	if ok := asURLError(err, &uerr); ok {
		return uerr.Err
	}
	return err
}

func asURLError(err error, target **url.Error) bool {
	for err != nil {
		if u, ok := err.(*url.Error); ok {
			*target = u
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
