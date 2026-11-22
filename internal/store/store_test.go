package store

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/vault"
)

func newKeys(t *testing.T) *vault.Keys {
	t.Helper()
	_, keys, err := vault.Create([]byte("correct horse battery"))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestEncryptedStoreNeverWritesPlaintext(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	keys := newKeys(t)
	s := NewEncrypted(backend, func(context.Context) (*vault.Keys, error) { return keys, nil })

	rec, err := s.UpsertFile(ctx, "secret/plans.md", []byte("launch codes 1234"), "", "save")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if rec.CommitSHA == "" {
		t.Fatalf("expected commit sha on write result")
	}
	index := &model.RemoteIndex{Files: map[string]*model.RemoteFile{"secret/plans.md": rec}}
	if _, err := s.SaveIndex(ctx, index, ""); err != nil {
		t.Fatalf("save index: %v", err)
	}
	for _, needle := range []string{"launch codes", "secret", "plans.md"} {
		if backend.Contains(needle) {
			t.Fatalf("remote leaked %q; stored paths: %v", needle, backend.Paths())
		}
	}

	data, sha, err := s.FetchFile(ctx, "secret/plans.md")
	if err != nil || string(data) != "launch codes 1234" || sha != rec.Revision {
		t.Fatalf("fetch: %q %q %v", data, sha, err)
	}
	snap, err := s.FetchIndex(ctx)
	if err != nil || snap.Index.Files["secret/plans.md"] == nil {
		t.Fatalf("fetch index: %+v %v", snap, err)
	}

	rec2, err := s.UpsertFile(ctx, "secret/plans.md", []byte("v2"), rec.Revision, "save")
	if err != nil {
		t.Fatalf("upsert v2: %v", err)
	}
	versions, err := s.ListVersions(ctx, "secret/plans.md")
	if err != nil || len(versions) != 2 || versions[0].Reason != "save" {
		t.Fatalf("versions: %+v %v", versions, err)
	}
	old, err := s.FetchFileAtRevision(ctx, "secret/plans.md", versions[1].CommitSHA)
	if err != nil || string(old) != "launch codes 1234" {
		t.Fatalf("history fetch: %q %v", old, err)
	}
	if _, err := s.UpsertFile(ctx, "secret/plans.md", []byte("stale"), rec.Revision, "save"); !errs.IsCode(err, errs.CodeConflict) {
		t.Fatalf("expected conflict on stale sha, got %v", err)
	}
	_ = rec2
}

func TestEncryptedStoreRejectsOtherVaultKey(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	k1, k2 := newKeys(t), newKeys(t)
	s1 := NewEncrypted(backend, func(context.Context) (*vault.Keys, error) { return k1, nil })
	if _, err := s1.SaveIndex(ctx, &model.RemoteIndex{Files: map[string]*model.RemoteFile{}}, ""); err != nil {
		t.Fatal(err)
	}
	s2 := NewEncrypted(backend, func(context.Context) (*vault.Keys, error) { return k2, nil })
	if _, err := s2.FetchIndex(ctx); err == nil {
		t.Fatalf("expected decryption failure with a foreign key")
	}
}

func TestMigrateLegacyEncryptsAndRemovesPlaintext(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	legacy := model.RemoteIndex{Version: 1, Files: map[string]*model.RemoteFile{
		"notes/a.md":    {Path: "notes/a.md"},
		"notes/gone.md": {Path: "notes/gone.md", Deleted: true},
		"../evil":       {Path: "../evil"},
	}}
	data, _ := json.Marshal(legacy)
	mustPut(t, backend, LegacyIndexPath, data)
	mustPut(t, backend, "files/notes/a.md", []byte("plaintext body"))
	keys := newKeys(t)

	validate := func(p string) error {
		if strings.Contains(p, "..") {
			return errs.Wrap(errs.CodeUsage, "bad", nil)
		}
		return nil
	}
	report, err := MigrateLegacy(ctx, backend, keys, validate)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(report.Migrated) != 1 || len(report.Skipped) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if _, err := backend.Get(ctx, "files/notes/a.md", ""); !errs.IsCode(err, errs.CodeNotFound) {
		t.Fatalf("plaintext copy was not removed")
	}
	marker, _ := backend.Get(ctx, LegacyIndexPath, "")
	if string(marker.Data) != LegacyMarker {
		t.Fatalf("legacy index not replaced with marker: %q", marker.Data)
	}
	if json.Valid(marker.Data) {
		t.Fatalf("marker must not be valid JSON so old clients fail closed")
	}
	s := NewEncrypted(backend, func(context.Context) (*vault.Keys, error) { return keys, nil })
	got, _, err := s.FetchFile(ctx, "notes/a.md")
	if err != nil || string(got) != "plaintext body" {
		t.Fatalf("migrated content: %q %v", got, err)
	}
	// Re-running is a no-op.
	if _, err := MigrateLegacy(ctx, backend, keys, validate); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func mustPut(t *testing.T, b Backend, path string, data []byte) {
	t.Helper()
	if _, err := b.Put(context.Background(), path, data, "", "seed"); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubBackendSendsBodyAndTokenSafely(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/repos/me/store/contents/objects/abc":
			_, _ = w.Write([]byte(`{"content":{"sha":"blob1"},"commit":{"sha":"commit1"}}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()
	b := NewGitHubBackend("me", "store", func(context.Context) (string, error) { return "tok123", nil }).WithAPIBase(srv.URL)
	res, err := b.Put(context.Background(), "objects/abc", []byte("hello"), "", "pb: save abc")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if res.BlobSHA != "blob1" || res.CommitSHA != "commit1" {
		t.Fatalf("unexpected result %+v", res)
	}
	if gotAuth != "Bearer tok123" {
		t.Fatalf("auth header: %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"content":"aGVsbG8="`) {
		t.Fatalf("body: %s", gotBody)
	}
	_, err = b.Get(context.Background(), "missing", "")
	if !errs.IsCode(err, errs.CodeNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "tok123") {
		t.Fatalf("token leaked into error")
	}
}

func TestGitHubBackendRefusesPublicRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"private":false,"owner":{"login":"me"}}`))
	}))
	defer srv.Close()
	b := NewGitHubBackend("me", "store", func(context.Context) (string, error) { return "t", nil }).WithAPIBase(srv.URL)
	if _, err := b.EnsureRepo(context.Background()); err == nil || !strings.Contains(err.Error(), "PUBLIC") {
		t.Fatalf("expected public repo refusal, got %v", err)
	}
}

func TestGitHubBackendLargeFileFallsBackToBlobAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/me/store/contents/objects/big":
			_, _ = w.Write([]byte(`{"type":"file","sha":"s1","content":"","encoding":"none","size":2000000}`))
		case "/repos/me/store/git/blobs/s1":
			_, _ = w.Write([]byte(`{"content":"aGVsbG8=","encoding":"base64"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	b := NewGitHubBackend("me", "store", func(context.Context) (string, error) { return "t", nil }).WithAPIBase(srv.URL)
	blob, err := b.Get(context.Background(), "objects/big", "")
	if err != nil || string(blob.Data) != "hello" {
		t.Fatalf("blob fallback: %v %q", err, blob)
	}
}
