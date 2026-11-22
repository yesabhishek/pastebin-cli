package store

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
)

const DefaultBitbucketAPI = "https://api.bitbucket.org/2.0"

// BitbucketBackend stores pb data in a private Bitbucket Cloud repository via
// the REST API 2.0. Bitbucket has no per-file compare-and-swap on writes, so
// the backend re-reads the file's latest commit immediately before writing and
// rejects stale updates; the remaining race window is milliseconds and any
// overwrite is still recoverable from history.
type BitbucketBackend struct {
	workspace string
	repo      string
	api       *apiClient

	mu     sync.Mutex
	branch string
}

// BitbucketAuth builds the request authenticator. With an Atlassian account
// email it uses HTTP Basic (email + API token); otherwise the token is sent as
// a Bearer access token.
func BitbucketAuth(email, token string) func(*http.Request) {
	return func(r *http.Request) {
		if email != "" {
			r.SetBasicAuth(email, token)
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	}
}

func NewBitbucketBackend(apiBase, workspace, repo, email, token string) *BitbucketBackend {
	if apiBase == "" {
		apiBase = DefaultBitbucketAPI
	}
	return &BitbucketBackend{
		workspace: workspace,
		repo:      strings.ToLower(repo),
		api:       newAPIClient(apiBase, "Bitbucket", BitbucketAuth(email, token)),
	}
}

// BitbucketWhoAmI verifies credentials and returns the account username.
func BitbucketWhoAmI(ctx context.Context, apiBase, email, token string) (*Identity, error) {
	if apiBase == "" {
		apiBase = DefaultBitbucketAPI
	}
	api := newAPIClient(apiBase, "Bitbucket", BitbucketAuth(email, token))
	resp, err := api.do(ctx, http.MethodGet, "/user", nil, "")
	if err != nil {
		return nil, err
	}
	if resp.Status >= 300 {
		return nil, api.classify(resp, bitbucketMessage(resp.Body))
	}
	var u struct {
		Username    string `json:"username"`
		Nickname    string `json:"nickname"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(resp.Body, &u); err != nil {
		return nil, errs.Wrap(errs.CodeAuth, "parse Bitbucket user", err)
	}
	login := u.Username
	if login == "" {
		login = u.Nickname
	}
	if login == "" {
		return nil, errs.Wrap(errs.CodeAuth, "Bitbucket did not return a username for these credentials", nil)
	}
	return &Identity{Login: login, Name: u.DisplayName}, nil
}

func (b *BitbucketBackend) repoPath(suffix string) string {
	return "/repositories/" + url.PathEscape(b.workspace) + "/" + url.PathEscape(b.repo) + suffix
}

func (b *BitbucketBackend) EnsureRepo(ctx context.Context) (*RepoInfo, error) {
	resp, err := b.api.do(ctx, http.MethodGet, b.repoPath(""), nil, "")
	if err != nil {
		return nil, err
	}
	if resp.Status < 300 {
		var r struct {
			IsPrivate  bool `json:"is_private"`
			Mainbranch *struct {
				Name string `json:"name"`
			} `json:"mainbranch"`
		}
		if err := json.Unmarshal(resp.Body, &r); err != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse Bitbucket repository", err)
		}
		if !r.IsPrivate {
			return nil, errs.Wrap(errs.CodeAuth, "refusing to use "+b.workspace+"/"+b.repo+" because it is PUBLIC. Make it private on Bitbucket or choose another repo with --repo", nil)
		}
		if r.Mainbranch != nil {
			b.setBranch(r.Mainbranch.Name)
		}
		return &RepoInfo{Private: true}, nil
	}
	if resp.Status != http.StatusNotFound {
		return nil, b.api.classify(resp, bitbucketMessage(resp.Body))
	}
	data, _ := json.Marshal(map[string]any{
		"scm":         "git",
		"is_private":  true,
		"has_issues":  false,
		"has_wiki":    false,
		"description": "Encrypted notes storage for pb (pastebin-cli). Contents are end-to-end encrypted.",
	})
	resp, err = b.api.do(ctx, http.MethodPost, b.repoPath(""), bytes.NewReader(data), "application/json")
	if err != nil {
		return nil, err
	}
	if resp.Status >= 300 {
		return nil, errs.Wrap(errs.CodeAuth, "create private Bitbucket repository", b.api.classify(resp, bitbucketMessage(resp.Body)))
	}
	var created struct {
		IsPrivate bool `json:"is_private"`
	}
	_ = json.Unmarshal(resp.Body, &created)
	if !created.IsPrivate {
		return nil, errs.Wrap(errs.CodeAuth, "Bitbucket created the repository without private visibility; aborting", nil)
	}
	return &RepoInfo{Private: true, Created: true}, nil
}

func (b *BitbucketBackend) setBranch(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if name != "" {
		b.branch = name
	}
}

func (b *BitbucketBackend) mainBranch(ctx context.Context) string {
	b.mu.Lock()
	br := b.branch
	b.mu.Unlock()
	if br != "" {
		return br
	}
	if resp, err := b.api.do(ctx, http.MethodGet, b.repoPath("?fields=mainbranch.name"), nil, ""); err == nil && resp.Status < 300 {
		var r struct {
			Mainbranch *struct {
				Name string `json:"name"`
			} `json:"mainbranch"`
		}
		if json.Unmarshal(resp.Body, &r) == nil && r.Mainbranch != nil {
			b.setBranch(r.Mainbranch.Name)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.branch == "" {
		b.branch = "main"
	}
	return b.branch
}

// lastCommit returns the newest commit that touched path, or "" if the file
// does not exist on the branch.
func (b *BitbucketBackend) lastCommit(ctx context.Context, path string) (string, error) {
	commits, err := b.fileHistory(ctx, b.mainBranch(ctx), path, 1)
	if err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			return "", nil
		}
		return "", err
	}
	if len(commits) == 0 {
		return "", nil
	}
	return commits[0].SHA, nil
}

func (b *BitbucketBackend) Get(ctx context.Context, path, ref string) (*Blob, error) {
	at := ref
	if at == "" {
		at = b.mainBranch(ctx)
	}
	resp, err := b.api.do(ctx, http.MethodGet, b.repoPath("/src/"+url.PathEscape(at)+"/"+escapePath(path)), nil, "")
	if err != nil {
		return nil, err
	}
	if resp.Status >= 300 {
		return nil, b.api.classify(resp, bitbucketMessage(resp.Body))
	}
	sha := ref
	if sha == "" {
		if sha, err = b.lastCommit(ctx, path); err != nil {
			return nil, err
		}
	}
	return &Blob{Data: resp.Body, SHA: sha}, nil
}

func (b *BitbucketBackend) checkRevision(ctx context.Context, path, prevSHA string) error {
	current, err := b.lastCommit(ctx, path)
	if err != nil {
		return err
	}
	if current != prevSHA {
		if prevSHA == "" {
			return errs.Wrap(errs.CodeConflict, "Bitbucket: "+path+" already exists", nil)
		}
		return errs.Wrap(errs.CodeConflict, "Bitbucket: "+path+" changed since it was last read", nil)
	}
	return nil
}

// srcCommit posts a multipart form to /src and returns the new commit hash.
func (b *BitbucketBackend) srcCommit(ctx context.Context, message string, files map[string][]byte, deletes []string) (string, error) {
	branch := b.mainBranch(ctx)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("message", message)
	_ = w.WriteField("branch", branch)
	for path, data := range files {
		part, err := w.CreateFormFile(path, "blob")
		if err != nil {
			return "", errs.Wrap(errs.CodeLocalCorruption, "build Bitbucket upload", err)
		}
		_, _ = part.Write(data)
	}
	for _, path := range deletes {
		_ = w.WriteField("files", path)
	}
	if err := w.Close(); err != nil {
		return "", errs.Wrap(errs.CodeLocalCorruption, "build Bitbucket upload", err)
	}
	resp, err := b.api.do(ctx, http.MethodPost, b.repoPath("/src"), &buf, w.FormDataContentType())
	if err != nil {
		return "", err
	}
	if resp.Status >= 300 {
		return "", b.api.classify(resp, bitbucketMessage(resp.Body))
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		if i := strings.LastIndex(loc, "/commit/"); i >= 0 {
			return strings.TrimSuffix(loc[i+len("/commit/"):], "/"), nil
		}
	}
	// Fall back to the branch tip.
	commits, err := b.branchCommits(ctx, branch, 1)
	if err != nil || len(commits) == 0 {
		return "", errs.Wrap(errs.CodeRemoteCorruption, "Bitbucket did not report the new commit", err)
	}
	return commits[0].SHA, nil
}

func (b *BitbucketBackend) Put(ctx context.Context, path string, data []byte, prevSHA, message string) (*PutResult, error) {
	if err := b.checkRevision(ctx, path, prevSHA); err != nil {
		return nil, err
	}
	id, err := b.srcCommit(ctx, message, map[string][]byte{path: data}, nil)
	if err != nil {
		return nil, err
	}
	return &PutResult{BlobSHA: id, CommitSHA: id}, nil
}

func (b *BitbucketBackend) Delete(ctx context.Context, path, sha, message string) error {
	if sha == "" {
		return nil
	}
	if err := b.checkRevision(ctx, path, sha); err != nil {
		return err
	}
	_, err := b.srcCommit(ctx, message, nil, []string{path})
	return err
}

func (b *BitbucketBackend) History(ctx context.Context, path string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 50
	}
	commits, err := b.fileHistory(ctx, b.mainBranch(ctx), path, limit)
	if errs.IsCode(err, errs.CodeNotFound) {
		return []Commit{}, nil
	}
	return commits, err
}

type bbCommit struct {
	Hash    string    `json:"hash"`
	Message string    `json:"message"`
	Date    time.Time `json:"date"`
}

func (b *BitbucketBackend) fileHistory(ctx context.Context, branch, path string, limit int) ([]Commit, error) {
	pagelen := limit
	if pagelen > 100 {
		pagelen = 100
	}
	endpoint := b.repoPath("/filehistory/"+url.PathEscape(branch)+"/"+escapePath(path)) +
		"?pagelen=" + strconv.Itoa(pagelen) +
		"&fields=" + url.QueryEscape("next,values.commit.hash,values.commit.message,values.commit.date")
	out := []Commit{}
	for endpoint != "" && len(out) < limit {
		resp, err := b.api.do(ctx, http.MethodGet, endpoint, nil, "")
		if err != nil {
			return nil, err
		}
		if resp.Status >= 300 {
			return nil, b.api.classify(resp, bitbucketMessage(resp.Body))
		}
		var page struct {
			Next   string `json:"next"`
			Values []struct {
				Commit bbCommit `json:"commit"`
			} `json:"values"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse Bitbucket history", err)
		}
		for _, v := range page.Values {
			if v.Commit.Hash == "" || len(out) >= limit {
				continue
			}
			// filehistory can list the same commit twice for renames; dedupe.
			if n := len(out); n > 0 && out[n-1].SHA == v.Commit.Hash {
				continue
			}
			out = append(out, Commit{SHA: v.Commit.Hash, Message: v.Commit.Message, Time: v.Commit.Date})
		}
		endpoint = b.relative(page.Next)
	}
	return out, nil
}

func (b *BitbucketBackend) branchCommits(ctx context.Context, branch string, limit int) ([]Commit, error) {
	resp, err := b.api.do(ctx, http.MethodGet, b.repoPath("/commits/"+url.PathEscape(branch)+"?pagelen="+strconv.Itoa(limit)), nil, "")
	if err != nil {
		return nil, err
	}
	if resp.Status >= 300 {
		return nil, b.api.classify(resp, bitbucketMessage(resp.Body))
	}
	var page struct {
		Values []bbCommit `json:"values"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse Bitbucket commits", err)
	}
	out := make([]Commit, 0, len(page.Values))
	for _, c := range page.Values {
		out = append(out, Commit{SHA: c.Hash, Message: c.Message, Time: c.Date})
	}
	return out, nil
}

// relative turns an absolute "next" link into an endpoint on the same API
// base. Links pointing anywhere else are ignored so credentials never leave
// the configured API host.
func (b *BitbucketBackend) relative(next string) string {
	if next == "" || !strings.HasPrefix(next, b.api.base+"/") {
		return ""
	}
	return strings.TrimPrefix(next, b.api.base)
}

func bitbucketMessage(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error.Message != "" {
		return payload.Error.Message
	}
	return strings.TrimSpace(string(body))
}
