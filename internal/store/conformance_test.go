package store

import (
	"context"
	"strings"
	"testing"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/storetest"
	"github.com/yesabhishek/pastebin-cli/internal/vault"
)

// allBackends returns every storage provider with a way to inspect raw
// stored bytes, so tests can prove nothing readable reaches the server.
func allBackends() []struct {
	name string
	make func(t *testing.T) (Backend, func(needle string) bool, func(public bool))
} {
	return []struct {
		name string
		make func(t *testing.T) (Backend, func(needle string) bool, func(public bool))
	}{
		{"memory", func(t *testing.T) (Backend, func(string) bool, func(bool)) {
			m := NewMemoryBackend()
			m.Exists = false
			return m, m.Contains, func(p bool) { m.Public = p; m.Exists = true }
		}},
		{"gitlab", func(t *testing.T) (Backend, func(string) bool, func(bool)) {
			srv, g := storetest.NewFakeGitLab(t, "alice", "pastebin-cli-store")
			b := NewGitLabBackend(srv.URL+"/api/v4", "alice", "pastebin-cli-store", "gl-token")
			return b, g.Contains, func(p bool) { g.Mu.Lock(); g.Exists, g.Private = true, !p; g.Mu.Unlock() }
		}},
		{"bitbucket", func(t *testing.T) (Backend, func(string) bool, func(bool)) {
			srv, g := storetest.NewFakeBitbucket(t, "alice", "pastebin-cli-store")
			b := NewBitbucketBackend(srv.URL, "alice", "pastebin-cli-store", "me@example.com", "bb-token")
			return b, g.Contains, func(p bool) { g.Mu.Lock(); g.Exists, g.Private = true, !p; g.Mu.Unlock() }
		}},
	}
}

func TestBackendConformance(t *testing.T) {
	for _, tc := range allBackends() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			b, _, _ := tc.make(t)

			info, err := b.EnsureRepo(ctx)
			if err != nil || !info.Private || !info.Created {
				t.Fatalf("first EnsureRepo: %+v %v", info, err)
			}
			if info, err := b.EnsureRepo(ctx); err != nil || info.Created {
				t.Fatalf("second EnsureRepo should find the repo: %+v %v", info, err)
			}
			if _, err := b.Get(ctx, "objects/a", ""); !errs.IsCode(err, errs.CodeNotFound) {
				t.Fatalf("missing file should be NotFound, got %v", err)
			}
			if h, err := b.History(ctx, "objects/a", 10); err != nil || len(h) != 0 {
				t.Fatalf("history on empty repo: %v %v", h, err)
			}

			r1, err := b.Put(ctx, "objects/a", []byte("v1"), "", "pb: save a")
			if err != nil || r1.BlobSHA == "" || r1.CommitSHA == "" {
				t.Fatalf("create: %+v %v", r1, err)
			}
			if _, err := b.Put(ctx, "objects/a", []byte("dup"), "", "pb: save a"); !errs.IsCode(err, errs.CodeConflict) {
				t.Fatalf("creating an existing file must conflict, got %v", err)
			}
			got, err := b.Get(ctx, "objects/a", "")
			if err != nil || string(got.Data) != "v1" || got.SHA != r1.BlobSHA {
				t.Fatalf("get v1: %+v %v (want sha %s)", got, err, r1.BlobSHA)
			}

			// Another file changing must not make this file's revision stale.
			if _, err := b.Put(ctx, "meta/index.enc", []byte("idx"), "", "pb: update index"); err != nil {
				t.Fatalf("put other file: %v", err)
			}
			r2, err := b.Put(ctx, "objects/a", []byte("v2"), r1.BlobSHA, "pb: save a")
			if err != nil {
				t.Fatalf("update with current revision: %v", err)
			}
			if _, err := b.Put(ctx, "objects/a", []byte("stale"), r1.BlobSHA, "pb: save a"); !errs.IsCode(err, errs.CodeConflict) {
				t.Fatalf("stale update must conflict, got %v", err)
			}

			hist, err := b.History(ctx, "objects/a", 10)
			if err != nil || len(hist) != 2 {
				t.Fatalf("history: %+v %v", hist, err)
			}
			if hist[0].SHA != r2.CommitSHA || !strings.HasPrefix(hist[0].Message, "pb: save") {
				t.Fatalf("history must be newest first with messages: %+v", hist)
			}
			old, err := b.Get(ctx, "objects/a", hist[1].SHA)
			if err != nil || string(old.Data) != "v1" {
				t.Fatalf("read at old revision: %+v %v", old, err)
			}
			if limited, _ := b.History(ctx, "objects/a", 1); len(limited) != 1 {
				t.Fatalf("history limit not honoured: %d", len(limited))
			}

			if err := b.Delete(ctx, "objects/a", r1.BlobSHA, "pb: delete a"); !errs.IsCode(err, errs.CodeConflict) {
				t.Fatalf("stale delete must conflict, got %v", err)
			}
			if err := b.Delete(ctx, "objects/a", r2.BlobSHA, "pb: delete a"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := b.Get(ctx, "objects/a", ""); !errs.IsCode(err, errs.CodeNotFound) {
				t.Fatalf("deleted file should be NotFound, got %v", err)
			}
		})
	}
}

func TestBackendsRefusePublicRepos(t *testing.T) {
	for _, tc := range allBackends() {
		t.Run(tc.name, func(t *testing.T) {
			b, _, setPublic := tc.make(t)
			setPublic(true)
			if _, err := b.EnsureRepo(context.Background()); err == nil {
				t.Fatalf("public repository must be refused")
			}
		})
	}
}

func TestEncryptedStoreOnEveryBackend(t *testing.T) {
	for _, tc := range allBackends() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			b, contains, _ := tc.make(t)
			if _, err := b.EnsureRepo(ctx); err != nil {
				t.Fatal(err)
			}
			file, keys, err := vault.Create([]byte("correct horse battery"))
			if err != nil {
				t.Fatal(err)
			}
			if err := WriteVault(ctx, b, file, ""); err != nil {
				t.Fatalf("write vault: %v", err)
			}
			s := NewEncrypted(b, func(context.Context) (*vault.Keys, error) { return keys, nil })
			rec, err := s.UpsertFile(ctx, "diary/secret-plans.md", []byte("meet at the old mill"), "", "save")
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if _, err := s.SaveIndex(ctx, &model.RemoteIndex{Files: map[string]*model.RemoteFile{"diary/secret-plans.md": rec}}, ""); err != nil {
				t.Fatalf("index: %v", err)
			}
			rec2, err := s.UpsertFile(ctx, "diary/secret-plans.md", []byte("meet at the new mill"), rec.Revision, "save")
			if err != nil {
				t.Fatalf("second upsert: %v", err)
			}
			for _, leak := range []string{"old mill", "new mill", "secret-plans", "diary"} {
				if contains(leak) {
					t.Fatalf("%s stored %q in clear", tc.name, leak)
				}
			}
			data, sha, err := s.FetchFile(ctx, "diary/secret-plans.md")
			if err != nil || string(data) != "meet at the new mill" || sha != rec2.Revision {
				t.Fatalf("fetch: %q %s %v", data, sha, err)
			}
			versions, err := s.ListVersions(ctx, "diary/secret-plans.md")
			if err != nil || len(versions) != 2 || versions[0].Reason != "save" {
				t.Fatalf("versions: %+v %v", versions, err)
			}
			old, err := s.FetchFileAtRevision(ctx, "diary/secret-plans.md", versions[1].CommitSHA)
			if err != nil || string(old) != "meet at the old mill" {
				t.Fatalf("old version: %q %v", old, err)
			}
			read, _, err := ReadVault(ctx, b)
			if err != nil || read == nil || read.ID != file.ID {
				t.Fatalf("read vault: %v %v", read, err)
			}
		})
	}
}

func TestRemoteAuthErrorsNeverLeakTokens(t *testing.T) {
	ctx := context.Background()
	gl, _ := storetest.NewFakeGitLab(t, "alice", "store")
	if _, err := NewGitLabBackend(gl.URL+"/api/v4", "alice", "store", "wrong-token-123").Get(ctx, "x", "main"); !errs.IsCode(err, errs.CodeAuth) || strings.Contains(err.Error(), "wrong-token-123") {
		t.Fatalf("gitlab auth error: %v", err)
	}
	bb, _ := storetest.NewFakeBitbucket(t, "alice", "store")
	if _, err := NewBitbucketBackend(bb.URL, "alice", "store", "", "wrong-token-456").Get(ctx, "x", "main"); !errs.IsCode(err, errs.CodeAuth) || strings.Contains(err.Error(), "wrong-token-456") {
		t.Fatalf("bitbucket auth error: %v", err)
	}
	if id, err := GitLabWhoAmI(ctx, gl.URL+"/api/v4", "gl-token"); err != nil || id.Login != "alice" {
		t.Fatalf("gitlab whoami: %+v %v", id, err)
	}
	if id, err := BitbucketWhoAmI(ctx, bb.URL, "me@example.com", "bb-token"); err != nil || id.Login != "alice" {
		t.Fatalf("bitbucket whoami: %+v %v", id, err)
	}
}

func TestBitbucketIgnoresForeignPaginationLinks(t *testing.T) {
	b := NewBitbucketBackend("https://api.bitbucket.org/2.0", "w", "r", "", "t")
	if got := b.relative("https://evil.example.com/2.0/repositories/w/r?page=2"); got != "" {
		t.Fatalf("foreign next link must be ignored, got %q", got)
	}
	if got := b.relative("https://api.bitbucket.org/2.0/repositories/w/r?page=2"); got != "/repositories/w/r?page=2" {
		t.Fatalf("same-host next link: %q", got)
	}
}
