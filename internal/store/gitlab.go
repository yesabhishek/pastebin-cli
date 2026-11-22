package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
)

const DefaultGitLabHost = "gitlab.com"

// GitLabBackend stores pb data in a private GitLab project using the REST API
// v4. Writes go through the Commits API so every save is one commit, and
// updates carry last_commit_id so concurrent edits are rejected instead of
// silently overwritten.
type GitLabBackend struct {
	owner string
	repo  string
	api   *apiClient

	mu     sync.Mutex
	branch string
}

// GitLabAPIBase returns the API root for a GitLab host (gitlab.com or a
// self-managed instance). Only HTTPS is used.
func GitLabAPIBase(host string) string {
	if host == "" {
		host = DefaultGitLabHost
	}
	return "https://" + host + "/api/v4"
}

func NewGitLabBackend(apiBase, owner, repo, token string) *GitLabBackend {
	return &GitLabBackend{
		owner: owner,
		repo:  repo,
		api: newAPIClient(apiBase, "GitLab", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
		}),
	}
}

// GitLabWhoAmI verifies a token and returns its username.
func GitLabWhoAmI(ctx context.Context, apiBase, token string) (*Identity, error) {
	api := newAPIClient(apiBase, "GitLab", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
	resp, err := api.do(ctx, http.MethodGet, "/user", nil, "")
	if err != nil {
		return nil, err
	}
	if resp.Status >= 300 {
		return nil, api.classify(resp, gitlabMessage(resp.Body))
	}
	var u struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(resp.Body, &u); err != nil || u.Username == "" {
		return nil, errs.Wrap(errs.CodeAuth, "GitLab did not return a username for this token", err)
	}
	return &Identity{Login: u.Username, Name: u.Name}, nil
}

func (g *GitLabBackend) project() string {
	return "/projects/" + url.PathEscape(g.owner+"/"+g.repo)
}

func (g *GitLabBackend) EnsureRepo(ctx context.Context) (*RepoInfo, error) {
	resp, err := g.api.do(ctx, http.MethodGet, g.project(), nil, "")
	if err != nil {
		return nil, err
	}
	if resp.Status < 300 {
		var p struct {
			Visibility    string `json:"visibility"`
			DefaultBranch string `json:"default_branch"`
		}
		if err := json.Unmarshal(resp.Body, &p); err != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse GitLab project", err)
		}
		if p.Visibility != "private" {
			return nil, errs.Wrap(errs.CodeAuth, "refusing to use "+g.owner+"/"+g.repo+" because it is not PRIVATE (visibility: "+p.Visibility+"). Make it private on GitLab or choose another repo with --repo", nil)
		}
		g.setBranch(p.DefaultBranch)
		return &RepoInfo{Private: true}, nil
	}
	if resp.Status != http.StatusNotFound {
		return nil, g.api.classify(resp, gitlabMessage(resp.Body))
	}
	body := map[string]any{
		"name":                   g.repo,
		"path":                   g.repo,
		"visibility":             "private",
		"description":            "Encrypted notes storage for pb (pastebin-cli). Contents are end-to-end encrypted.",
		"initialize_with_readme": false,
		"issues_access_level":    "disabled",
		"wiki_access_level":      "disabled",
	}
	if ns, err := g.namespaceID(ctx); err == nil && ns != 0 {
		body["namespace_id"] = ns
	}
	data, _ := json.Marshal(body)
	resp, err = g.api.do(ctx, http.MethodPost, "/projects", bytes.NewReader(data), "application/json")
	if err != nil {
		return nil, err
	}
	if resp.Status >= 300 {
		return nil, errs.Wrap(errs.CodeAuth, "create private GitLab project", g.api.classify(resp, gitlabMessage(resp.Body)))
	}
	var created struct {
		Visibility string `json:"visibility"`
	}
	_ = json.Unmarshal(resp.Body, &created)
	if created.Visibility != "private" {
		return nil, errs.Wrap(errs.CodeAuth, "GitLab created the project without private visibility; aborting", nil)
	}
	return &RepoInfo{Private: true, Created: true}, nil
}

// namespaceID resolves a group namespace; personal namespaces need no ID.
func (g *GitLabBackend) namespaceID(ctx context.Context) (int, error) {
	resp, err := g.api.do(ctx, http.MethodGet, "/namespaces/"+url.PathEscape(g.owner), nil, "")
	if err != nil || resp.Status >= 300 {
		return 0, err
	}
	var ns struct {
		ID   int    `json:"id"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(resp.Body, &ns); err != nil {
		return 0, err
	}
	if ns.Kind == "user" {
		return 0, nil
	}
	return ns.ID, nil
}

func (g *GitLabBackend) setBranch(b string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b != "" {
		g.branch = b
	}
}

// defaultBranch returns the project's default branch ("main" for an empty
// project). It is fetched once per process.
func (g *GitLabBackend) defaultBranch(ctx context.Context) string {
	g.mu.Lock()
	b := g.branch
	g.mu.Unlock()
	if b != "" {
		return b
	}
	if resp, err := g.api.do(ctx, http.MethodGet, g.project(), nil, ""); err == nil && resp.Status < 300 {
		var p struct {
			DefaultBranch string `json:"default_branch"`
		}
		_ = json.Unmarshal(resp.Body, &p)
		g.setBranch(p.DefaultBranch)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.branch == "" {
		g.branch = "main"
	}
	return g.branch
}

func (g *GitLabBackend) Get(ctx context.Context, path, ref string) (*Blob, error) {
	if ref == "" {
		ref = g.defaultBranch(ctx)
	}
	endpoint := g.project() + "/repository/files/" + url.PathEscape(path) + "?ref=" + url.QueryEscape(ref)
	resp, err := g.api.do(ctx, http.MethodGet, endpoint, nil, "")
	if err != nil {
		return nil, err
	}
	if resp.Status >= 300 {
		return nil, g.api.classify(resp, gitlabMessage(resp.Body))
	}
	var f struct {
		Content      string `json:"content"`
		Encoding     string `json:"encoding"`
		LastCommitID string `json:"last_commit_id"`
	}
	if err := json.Unmarshal(resp.Body, &f); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse GitLab file", err)
	}
	var data []byte
	if f.Encoding == "base64" {
		data, err = base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "decode GitLab file", err)
		}
	} else {
		data = []byte(f.Content)
	}
	// The last commit touching the file is the revision used for optimistic
	// concurrency on the next write.
	return &Blob{Data: data, SHA: f.LastCommitID}, nil
}

func (g *GitLabBackend) commit(ctx context.Context, message string, action map[string]any) (string, error) {
	body := map[string]any{
		"branch":         g.defaultBranch(ctx),
		"commit_message": message,
		"actions":        []map[string]any{action},
	}
	data, _ := json.Marshal(body)
	resp, err := g.api.do(ctx, http.MethodPost, g.project()+"/repository/commits", bytes.NewReader(data), "application/json")
	if err != nil {
		return "", err
	}
	if resp.Status >= 300 {
		return "", g.api.classify(resp, gitlabMessage(resp.Body),
			"changed since", "already exists", "doesn't exist", "does not exist", "conflict")
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &c); err != nil || c.ID == "" {
		return "", errs.Wrap(errs.CodeRemoteCorruption, "parse GitLab commit response", err)
	}
	return c.ID, nil
}

func (g *GitLabBackend) Put(ctx context.Context, path string, data []byte, prevSHA, message string) (*PutResult, error) {
	action := map[string]any{
		"action":    "create",
		"file_path": path,
		"content":   base64.StdEncoding.EncodeToString(data),
		"encoding":  "base64",
	}
	if prevSHA != "" {
		action["action"] = "update"
		action["last_commit_id"] = prevSHA
	}
	id, err := g.commit(ctx, message, action)
	if err != nil {
		return nil, err
	}
	return &PutResult{BlobSHA: id, CommitSHA: id}, nil
}

func (g *GitLabBackend) Delete(ctx context.Context, path, sha, message string) error {
	if sha == "" {
		return nil
	}
	_, err := g.commit(ctx, message, map[string]any{
		"action":         "delete",
		"file_path":      path,
		"last_commit_id": sha,
	})
	return err
}

func (g *GitLabBackend) History(ctx context.Context, path string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 50
	}
	branch := g.defaultBranch(ctx)
	out := make([]Commit, 0, limit)
	for page := 1; len(out) < limit; page++ {
		perPage := limit - len(out)
		if perPage > 100 {
			perPage = 100
		}
		endpoint := g.project() + "/repository/commits?ref_name=" + url.QueryEscape(branch) +
			"&path=" + url.QueryEscape(path) + "&per_page=" + strconv.Itoa(perPage) + "&page=" + strconv.Itoa(page)
		resp, err := g.api.do(ctx, http.MethodGet, endpoint, nil, "")
		if err != nil {
			return nil, err
		}
		if resp.Status == http.StatusNotFound {
			return out, nil // empty project
		}
		if resp.Status >= 300 {
			return nil, g.api.classify(resp, gitlabMessage(resp.Body))
		}
		var items []struct {
			ID            string    `json:"id"`
			Message       string    `json:"message"`
			CommittedDate time.Time `json:"committed_date"`
			CreatedAt     time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse GitLab history", err)
		}
		for _, it := range items {
			ts := it.CommittedDate
			if ts.IsZero() {
				ts = it.CreatedAt
			}
			out = append(out, Commit{SHA: it.ID, Message: it.Message, Time: ts})
		}
		if len(items) < perPage {
			break
		}
	}
	return out, nil
}

func gitlabMessage(body []byte) string {
	var payload struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		switch m := payload.Message.(type) {
		case string:
			return m
		case nil:
		default:
			b, _ := json.Marshal(m)
			return string(b)
		}
		if payload.Error != "" {
			return payload.Error
		}
	}
	return strings.TrimSpace(string(body))
}
