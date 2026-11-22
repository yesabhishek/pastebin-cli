// Package storetest provides in-memory fakes of the GitLab and Bitbucket
// REST APIs for tests.
package storetest

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGit is a tiny in-memory git: files on one branch plus commit snapshots.
// FakeGit is a tiny in-memory git: files on one branch plus snapshots.
type FakeGit struct {
	Mu       sync.Mutex
	Exists   bool
	Private  bool
	Files    map[string][]byte
	last     map[string]string // path -> last commit touching it
	Commits  []fakeCommit      // oldest first
	snapshot map[string]map[string][]byte
	seq      int
	token    string
}

type fakeCommit struct {
	ID      string
	Message string
	Time    time.Time
	Paths   []string
}

func NewFakeGit(token string) *FakeGit {
	return &FakeGit{Private: true, Files: map[string][]byte{}, last: map[string]string{}, snapshot: map[string]map[string][]byte{}, token: token}
}

func (f *FakeGit) commit(msg string, set map[string][]byte, del []string) string {
	f.seq++
	sum := sha1.Sum([]byte(fmt.Sprintf("%d-%s", f.seq, msg)))
	id := hex.EncodeToString(sum[:])
	paths := []string{}
	for p, d := range set {
		f.Files[p] = append([]byte(nil), d...)
		f.last[p] = id
		paths = append(paths, p)
	}
	for _, p := range del {
		delete(f.Files, p)
		delete(f.last, p)
		paths = append(paths, p)
	}
	snap := map[string][]byte{}
	for k, v := range f.Files {
		snap[k] = v
	}
	f.snapshot[id] = snap
	f.Commits = append(f.Commits, fakeCommit{ID: id, Message: msg, Time: time.Unix(1700000000+int64(f.seq)*60, 0).UTC(), Paths: paths})
	return id
}

func (f *FakeGit) history(path string) []fakeCommit {
	out := []fakeCommit{}
	for i := len(f.Commits) - 1; i >= 0; i-- {
		for _, p := range f.Commits[i].Paths {
			if p == path {
				out = append(out, f.Commits[i])
				break
			}
		}
	}
	return out
}

func (f *FakeGit) read(path, ref string) ([]byte, bool) {
	if ref == "" || ref == "main" {
		d, ok := f.Files[path]
		return d, ok
	}
	snap, ok := f.snapshot[ref]
	if !ok {
		return nil, false
	}
	d, ok := snap[path]
	return d, ok
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------- GitLab ----------------

// NewFakeGitLab serves the subset of GitLab API v4 pb uses (token "gl-token").
func NewFakeGitLab(t testing.TB, owner, repo string) (*httptest.Server, *FakeGit) {
	t.Helper()
	g := NewFakeGit("gl-token")
	projectID := url.PathEscape(owner + "/" + repo)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.Mu.Lock()
		defer g.Mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+g.token {
			writeJSON(w, 401, map[string]string{"message": "401 Unauthorized"})
			return
		}
		p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")
		q := r.URL.Query()
		switch {
		case p == "/user":
			writeJSON(w, 200, map[string]string{"username": owner, "name": "Test User"})
		case strings.HasPrefix(p, "/namespaces/"):
			writeJSON(w, 200, map[string]any{"id": 7, "kind": "user"})
		case p == "/projects" && r.Method == http.MethodPost:
			g.Exists = true
			writeJSON(w, 201, map[string]any{"visibility": "private"})
		case p == "/projects/"+projectID && r.Method == http.MethodGet:
			if !g.Exists {
				writeJSON(w, 404, map[string]string{"message": "404 Project Not Found"})
				return
			}
			vis := "private"
			if !g.Private {
				vis = "public"
			}
			branch := ""
			if len(g.Commits) > 0 {
				branch = "main"
			}
			writeJSON(w, 200, map[string]any{"visibility": vis, "default_branch": branch})
		case strings.HasPrefix(p, "/projects/"+projectID+"/repository/files/"):
			filePath, _ := url.PathUnescape(strings.TrimPrefix(p, "/projects/"+projectID+"/repository/files/"))
			data, ok := g.read(filePath, q.Get("ref"))
			if !ok {
				writeJSON(w, 404, map[string]string{"message": "404 File Not Found"})
				return
			}
			last := g.last[filePath]
			if ref := q.Get("ref"); ref != "" && ref != "main" {
				last = ref
			}
			writeJSON(w, 200, map[string]any{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64", "last_commit_id": last})
		case p == "/projects/"+projectID+"/repository/commits" && r.Method == http.MethodPost:
			var body struct {
				Branch  string `json:"branch"`
				Message string `json:"commit_message"`
				Actions []struct {
					Action       string `json:"action"`
					FilePath     string `json:"file_path"`
					Content      string `json:"content"`
					LastCommitID string `json:"last_commit_id"`
				} `json:"actions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			set := map[string][]byte{}
			del := []string{}
			for _, a := range body.Actions {
				_, exists := g.Files[a.FilePath]
				switch a.Action {
				case "create":
					if exists {
						writeJSON(w, 400, map[string]string{"message": "A file with this name already exists"})
						return
					}
				case "update", "delete":
					if !exists {
						writeJSON(w, 400, map[string]string{"message": "A file with this name doesn't exist"})
						return
					}
					if a.LastCommitID != "" && a.LastCommitID != g.last[a.FilePath] {
						writeJSON(w, 400, map[string]string{"message": "You are attempting to update a file that has changed since you started editing it."})
						return
					}
				}
				if a.Action == "delete" {
					del = append(del, a.FilePath)
				} else {
					d, _ := base64.StdEncoding.DecodeString(a.Content)
					set[a.FilePath] = d
				}
			}
			writeJSON(w, 201, map[string]string{"id": g.commit(body.Message, set, del)})
		case p == "/projects/"+projectID+"/repository/commits" && r.Method == http.MethodGet:
			if len(g.Commits) == 0 {
				writeJSON(w, 404, map[string]string{"message": "404 Branch Not Found"})
				return
			}
			hist := g.history(q.Get("path"))
			per, _ := strconv.Atoi(q.Get("per_page"))
			page, _ := strconv.Atoi(q.Get("page"))
			start := (page - 1) * per
			items := []map[string]any{}
			for i := start; i < len(hist) && i < start+per; i++ {
				items = append(items, map[string]any{"id": hist[i].ID, "message": hist[i].Message, "committed_date": hist[i].Time})
			}
			writeJSON(w, 200, items)
		default:
			writeJSON(w, 404, map[string]string{"message": "no route " + r.Method + " " + p})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, g
}

// ---------------- Bitbucket ----------------

// NewFakeBitbucket serves the subset of Bitbucket API 2.0 pb uses
// (Basic me@example.com:bb-token or Bearer bb-token).
func NewFakeBitbucket(t testing.TB, ws, repo string) (*httptest.Server, *FakeGit) {
	t.Helper()
	g := NewFakeGit("bb-token")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.Mu.Lock()
		defer g.Mu.Unlock()
		user, pass, basic := r.BasicAuth()
		if !(basic && user == "me@example.com" && pass == g.token) && r.Header.Get("Authorization") != "Bearer "+g.token {
			writeJSON(w, 401, map[string]any{"error": map[string]string{"message": "Unauthorized"}})
			return
		}
		p := r.URL.EscapedPath()
		base := "/repositories/" + ws + "/" + repo
		q := r.URL.Query()
		switch {
		case p == "/user":
			writeJSON(w, 200, map[string]string{"username": ws, "display_name": "Test User"})
		case p == base && r.Method == http.MethodGet:
			if !g.Exists {
				writeJSON(w, 404, map[string]any{"error": map[string]string{"message": "Repository not found"}})
				return
			}
			var main any
			if len(g.Commits) > 0 {
				main = map[string]string{"name": "main"}
			}
			writeJSON(w, 200, map[string]any{"is_private": g.Private, "mainbranch": main})
		case p == base && r.Method == http.MethodPost:
			g.Exists = true
			writeJSON(w, 200, map[string]any{"is_private": true})
		case strings.HasPrefix(p, base+"/src/") && r.Method == http.MethodGet:
			rest := strings.TrimPrefix(p, base+"/src/")
			i := strings.Index(rest, "/")
			ref, _ := url.PathUnescape(rest[:i])
			filePath, _ := url.PathUnescape(rest[i+1:])
			data, ok := g.read(filePath, ref)
			if !ok {
				writeJSON(w, 404, map[string]any{"error": map[string]string{"message": "No such file"}})
				return
			}
			_, _ = w.Write(data)
		case p == base+"/src" && r.Method == http.MethodPost:
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				writeJSON(w, 400, map[string]any{"error": map[string]string{"message": err.Error()}})
				return
			}
			set := map[string][]byte{}
			for name, fhs := range r.MultipartForm.File {
				fh, _ := fhs[0].Open()
				d, _ := io.ReadAll(fh)
				set[name] = d
			}
			id := g.commit(r.FormValue("message"), set, r.MultipartForm.Value["files"])
			w.Header().Set("Location", srv.URL+base+"/commit/"+id)
			w.WriteHeader(201)
		case strings.HasPrefix(p, base+"/filehistory/"):
			rest := strings.TrimPrefix(p, base+"/filehistory/")
			i := strings.Index(rest, "/")
			filePath, _ := url.PathUnescape(rest[i+1:])
			if _, ok := g.Files[filePath]; !ok {
				writeJSON(w, 404, map[string]any{"error": map[string]string{"message": "No such file"}})
				return
			}
			hist := g.history(filePath)
			pagelen, _ := strconv.Atoi(q.Get("pagelen"))
			page, _ := strconv.Atoi(q.Get("page"))
			if page == 0 {
				page = 1
			}
			start := (page - 1) * pagelen
			vals := []map[string]any{}
			for i := start; i < len(hist) && i < start+pagelen; i++ {
				vals = append(vals, map[string]any{"commit": map[string]any{"hash": hist[i].ID, "message": hist[i].Message, "date": hist[i].Time}})
			}
			out := map[string]any{"values": vals}
			if start+pagelen < len(hist) {
				nq := r.URL.Query()
				nq.Set("page", strconv.Itoa(page+1))
				out["next"] = srv.URL + p + "?" + nq.Encode()
			}
			writeJSON(w, 200, out)
		case strings.HasPrefix(p, base+"/commits/"):
			vals := []map[string]any{}
			for i := len(g.Commits) - 1; i >= 0 && len(vals) < 1; i-- {
				vals = append(vals, map[string]any{"hash": g.Commits[i].ID, "message": g.Commits[i].Message})
			}
			writeJSON(w, 200, map[string]any{"values": vals})
		default:
			writeJSON(w, 404, map[string]any{"error": map[string]string{"message": "no route " + r.Method + " " + p}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, g
}

// Contains reports whether any stored file, path or commit message contains needle.
func (f *FakeGit) Contains(needle string) bool {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for p, d := range f.Files {
		if strings.Contains(p, needle) || strings.Contains(string(d), needle) {
			return true
		}
	}
	for _, c := range f.Commits {
		if strings.Contains(c.Message, needle) {
			return true
		}
	}
	return false
}
