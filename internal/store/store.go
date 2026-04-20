package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
)

type RemoteSnapshot struct {
	Index    *model.RemoteIndex
	IndexSHA string
}

type RemoteStore interface {
	EnsureRepo(context.Context) error
	FetchIndex(context.Context) (*RemoteSnapshot, error)
	FetchFile(context.Context, string) ([]byte, string, error)
	FetchFileAtRevision(context.Context, string, string) ([]byte, error)
	ListVersions(context.Context, string) ([]model.VersionEntry, error)
	UpsertFile(context.Context, string, []byte, string, string) (*model.RemoteFile, error)
	DeleteFile(context.Context, string, string, string) error
	SaveIndex(context.Context, *model.RemoteIndex, string) (string, error)
}

type GitHubStore struct {
	owner string
	repo  string
}

func NewGitHub(owner, repo string) *GitHubStore {
	return &GitHubStore{owner: owner, repo: repo}
}

func (s *GitHubStore) EnsureRepo(ctx context.Context) error {
	_, err := s.gh(ctx, "api", fmt.Sprintf("repos/%s/%s", s.owner, s.repo))
	if err == nil {
		return nil
	}
	if !errs.IsCode(err, errs.CodeNotFound) {
		return err
	}
	_, err = s.gh(ctx, "repo", "create", fmt.Sprintf("%s/%s", s.owner, s.repo), "--private", "--disable-issues", "--confirm")
	if err != nil {
		return errs.Wrap(errs.CodeAuth, "create private storage repo", err)
	}
	index := &model.RemoteIndex{Version: model.IndexVersion, Files: map[string]*model.RemoteFile{}}
	_, err = s.SaveIndex(ctx, index, "")
	return err
}

func (s *GitHubStore) FetchIndex(ctx context.Context) (*RemoteSnapshot, error) {
	out, err := s.gh(ctx, "api", s.contentsEndpoint("meta/index.json"))
	if err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			return &RemoteSnapshot{
				Index: &model.RemoteIndex{Version: model.IndexVersion, Files: map[string]*model.RemoteFile{}},
			}, nil
		}
		return nil, err
	}
	var payload contentResponse
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse remote index", err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "decode remote index", err)
	}
	var index model.RemoteIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "unmarshal remote index", err)
	}
	if index.Files == nil {
		index.Files = map[string]*model.RemoteFile{}
	}
	return &RemoteSnapshot{Index: &index, IndexSHA: payload.SHA}, nil
}

func (s *GitHubStore) FetchFile(ctx context.Context, path string) ([]byte, string, error) {
	out, err := s.gh(ctx, "api", s.contentsEndpoint("files/"+path))
	if err != nil {
		return nil, "", err
	}
	var payload contentResponse
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, "", errs.Wrap(errs.CodeRemoteCorruption, "parse remote file", err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return nil, "", errs.Wrap(errs.CodeRemoteCorruption, "decode remote file", err)
	}
	return data, payload.SHA, nil
}

func (s *GitHubStore) FetchFileAtRevision(ctx context.Context, path string, revision string) ([]byte, error) {
	out, err := s.gh(ctx, "api", s.contentsEndpointWithRef("files/"+path, revision))
	if err != nil {
		return nil, err
	}
	var payload contentResponse
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse historical file", err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "decode historical file", err)
	}
	return data, nil
}

func (s *GitHubStore) ListVersions(ctx context.Context, path string) ([]model.VersionEntry, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/commits?path=%s&per_page=100",
		s.owner,
		s.repo,
		url.QueryEscape("files/"+path),
	)
	out, err := s.gh(ctx, "api", "--paginate", "--slurp", endpoint)
	if err != nil {
		return nil, err
	}
	var payload [][]struct {
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
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse file version history", err)
	}
	total := 0
	for _, page := range payload {
		total += len(page)
	}
	versions := make([]model.VersionEntry, 0, total)
	for _, page := range payload {
		for _, item := range page {
			if item.SHA == "" {
				continue
			}
			timestamp := item.Commit.Author.Date
			if timestamp.IsZero() {
				timestamp = item.Commit.Committer.Date
			}
			versions = append(versions, model.VersionEntry{
				ID:        shortVersionID(item.SHA),
				CommitSHA: item.SHA,
				Path:      path,
				Timestamp: timestamp,
				Reason:    parseReason(item.Commit.Message),
			})
		}
	}
	return versions, nil
}

func (s *GitHubStore) UpsertFile(ctx context.Context, path string, content []byte, previousSHA string, reason string) (*model.RemoteFile, error) {
	b64 := base64.StdEncoding.EncodeToString(content)
	args := []string{"api", "--method", "PUT", s.contentsEndpoint("files/" + path),
		"-f", "message=pb: " + reason + " " + path,
		"-f", "content=" + b64,
	}
	if previousSHA != "" {
		args = append(args, "-f", "sha="+previousSHA)
	}
	out, err := s.gh(ctx, args...)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Content struct {
			SHA string `json:"sha"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse upsert response", err)
	}
	checksum := checksum(content)
	return &model.RemoteFile{
		Path:      path,
		Revision:  payload.Content.SHA,
		Checksum:  checksum,
		Deleted:   false,
		UpdatedAt: time.Now().UTC(),
	}, nil
}

func (s *GitHubStore) DeleteFile(ctx context.Context, path string, sha string, reason string) error {
	if sha == "" {
		return nil
	}
	_, err := s.gh(ctx, "api", "--method", "DELETE", s.contentsEndpoint("files/"+path),
		"-f", "message=pb: "+reason+" "+path,
		"-f", "sha="+sha,
	)
	return err
}

func (s *GitHubStore) SaveIndex(ctx context.Context, index *model.RemoteIndex, previousSHA string) (string, error) {
	index.Version = model.IndexVersion
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return "", errs.Wrap(errs.CodeLocalCorruption, "encode remote index", err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	args := []string{"api", "--method", "PUT", s.contentsEndpoint("meta/index.json"),
		"-f", "message=pb: update index",
		"-f", "content=" + b64,
	}
	if previousSHA != "" {
		args = append(args, "-f", "sha="+previousSHA)
	}
	out, err := s.gh(ctx, args...)
	if err != nil {
		return "", err
	}
	var payload struct {
		Content struct {
			SHA string `json:"sha"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", errs.Wrap(errs.CodeRemoteCorruption, "parse index update response", err)
	}
	return payload.Content.SHA, nil
}

type contentResponse struct {
	SHA     string `json:"sha"`
	Content string `json:"content"`
}

func (s *GitHubStore) contentsEndpoint(path string) string {
	escaped := url.PathEscape(path)
	escaped = strings.ReplaceAll(escaped, "%2F", "/")
	return fmt.Sprintf("repos/%s/%s/contents/%s", s.owner, s.repo, escaped)
}

func (s *GitHubStore) contentsEndpointWithRef(path string, ref string) string {
	return s.contentsEndpoint(path) + "?ref=" + url.QueryEscape(ref)
}

func (s *GitHubStore) gh(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, nil
	}
	msg := strings.TrimSpace(string(out))
	switch {
	case strings.Contains(msg, "HTTP 404"):
		return nil, errs.Wrap(errs.CodeNotFound, "GitHub resource not found", err)
	case strings.Contains(strings.ToLower(msg), "rate limit"):
		return nil, errs.Wrap(errs.CodeRateLimit, "GitHub API rate limit reached", err)
	case strings.Contains(strings.ToLower(msg), "timed out"), strings.Contains(strings.ToLower(msg), "connection"):
		return nil, errs.Wrap(errs.CodeNetwork, "GitHub network request failed", err)
	default:
		return nil, errs.Wrap(errs.CodeAuth, strings.TrimSpace(msg), err)
	}
}

func checksum(content []byte) string {
	return modelChecksum(content)
}

func shortVersionID(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func parseReason(message string) string {
	message = strings.TrimSpace(strings.ToLower(message))
	if strings.HasPrefix(message, "pb: ") {
		message = strings.TrimPrefix(message, "pb: ")
	}
	fields := strings.Fields(message)
	if len(fields) == 0 {
		return "unknown"
	}
	switch fields[0] {
	case "save", "sync", "restore", "delete":
		return fields[0]
	default:
		return "unknown"
	}
}
