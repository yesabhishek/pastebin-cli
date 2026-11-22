package store

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
)

// MemoryBackend is an in-memory Backend with git-like history. It is used by
// tests and has the same optimistic-concurrency semantics as GitHub.
type MemoryBackend struct {
	mu      sync.Mutex
	files   map[string]*Blob
	history map[string][]Commit
	commits map[string]map[string]*Blob // commit sha -> path -> blob snapshot
	seq     int
	Public  bool
	Exists  bool
}

func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		files:   map[string]*Blob{},
		history: map[string][]Commit{},
		commits: map[string]map[string]*Blob{},
		Exists:  true,
	}
}

func (m *MemoryBackend) EnsureRepo(context.Context) (*RepoInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Exists && m.Public {
		return nil, errs.Wrap(errs.CodeAuth, "refusing to use public repository", nil)
	}
	created := !m.Exists
	m.Exists = true
	return &RepoInfo{Private: true, Created: created}, nil
}

func (m *MemoryBackend) Get(_ context.Context, path, ref string) (*Blob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ref != "" {
		snap := m.commits[ref]
		if snap == nil || snap[path] == nil {
			return nil, errs.Wrap(errs.CodeNotFound, "not found", nil)
		}
		b := snap[path]
		return &Blob{Data: append([]byte(nil), b.Data...), SHA: b.SHA}, nil
	}
	b := m.files[path]
	if b == nil {
		return nil, errs.Wrap(errs.CodeNotFound, "not found", nil)
	}
	return &Blob{Data: append([]byte(nil), b.Data...), SHA: b.SHA}, nil
}

func (m *MemoryBackend) Put(_ context.Context, path string, data []byte, prevSHA, message string) (*PutResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.files[path]
	if current != nil && current.SHA != prevSHA {
		return nil, errs.Wrap(errs.CodeConflict, "sha mismatch", nil)
	}
	if current == nil && prevSHA != "" {
		return nil, errs.Wrap(errs.CodeConflict, "file does not exist", nil)
	}
	sum := sha1.Sum(append([]byte(path+"\x00"), data...))
	blob := &Blob{Data: append([]byte(nil), data...), SHA: hex.EncodeToString(sum[:])}
	m.files[path] = blob
	commit := m.commit(path, message)
	return &PutResult{BlobSHA: blob.SHA, CommitSHA: commit}, nil
}

func (m *MemoryBackend) Delete(_ context.Context, path, sha, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.files[path]
	if current == nil {
		return errs.Wrap(errs.CodeNotFound, "not found", nil)
	}
	if current.SHA != sha {
		return errs.Wrap(errs.CodeConflict, "sha mismatch", nil)
	}
	delete(m.files, path)
	m.commit(path, message)
	return nil
}

func (m *MemoryBackend) History(_ context.Context, path string, limit int) ([]Commit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.history[path]
	out := make([]Commit, 0, len(h))
	for i := len(h) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, h[i])
	}
	return out, nil
}

func (m *MemoryBackend) commit(path, message string) string {
	m.seq++
	sha := fmt.Sprintf("%040x", m.seq)
	snap := make(map[string]*Blob, len(m.files))
	for k, v := range m.files {
		snap[k] = v
	}
	m.commits[sha] = snap
	m.history[path] = append(m.history[path], Commit{SHA: sha, Message: message, Time: time.Unix(int64(m.seq), 0).UTC()})
	return sha
}

// Paths lists every stored path, sorted.
func (m *MemoryBackend) Paths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.files))
	for k := range m.files {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Contains reports whether any stored blob, path or commit message contains s.
func (m *MemoryBackend) Contains(s string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for p, b := range m.files {
		if strings.Contains(p, s) || strings.Contains(string(b.Data), s) {
			return true
		}
	}
	for _, h := range m.history {
		for _, c := range h {
			if strings.Contains(c.Message, s) {
				return true
			}
		}
	}
	return false
}
