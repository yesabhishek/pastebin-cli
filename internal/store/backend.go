package store

import (
	"context"
	"time"
)

// Blob is a raw file stored in the backing repository.
type Blob struct {
	Data []byte
	SHA  string
}

// Commit is one history entry touching a file.
type Commit struct {
	SHA     string
	Message string
	Time    time.Time
}

// PutResult reports the identifiers produced by a write.
type PutResult struct {
	BlobSHA   string
	CommitSHA string
}

// RepoInfo describes the storage repository.
type RepoInfo struct {
	Private bool
	Created bool
}

// Backend is a minimal, provider-agnostic versioned blob store. GitHub is the
// only implementation today; GitLab or Bitbucket adapters only need to
// implement these five operations because encryption, indexing and sync all
// live above this layer.
type Backend interface {
	// EnsureRepo verifies the storage repository exists and is private,
	// creating a private repository if it does not exist.
	EnsureRepo(ctx context.Context) (*RepoInfo, error)
	// Get returns a file at ref (empty ref = default branch).
	Get(ctx context.Context, path, ref string) (*Blob, error)
	// Put creates or updates a file. prevSHA must match the current blob SHA
	// when updating (optimistic concurrency).
	Put(ctx context.Context, path string, data []byte, prevSHA, message string) (*PutResult, error)
	// Delete removes a file.
	Delete(ctx context.Context, path, sha, message string) error
	// History lists commits touching path, newest first.
	History(ctx context.Context, path string, limit int) ([]Commit, error)
}
