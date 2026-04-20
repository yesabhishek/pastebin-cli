package sync

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/cache"
	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/store"
)

func TestSaveContentCreatesConflictCopyWhenRemoteChanged(t *testing.T) {
	t.Parallel()

	svc, cacheMgr, fake := newTestService(t)
	state := &model.State{
		Version: model.StateVersion,
		Files: map[string]*model.TrackedFile{
			"notes/a.txt": {
				Path:         "notes/a.txt",
				BaseRevision: "rev-old",
			},
		},
	}
	if _, err := cacheMgr.SaveContent("notes/a.txt", []byte("local draft")); err != nil {
		t.Fatalf("save content: %v", err)
	}
	if err := cacheMgr.SaveState(state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	fake.snapshot = &store.RemoteSnapshot{
		Index: &model.RemoteIndex{
			Version: model.IndexVersion,
			Files: map[string]*model.RemoteFile{
				"notes/a.txt": {
					Path:     "notes/a.txt",
					Revision: "rev-new",
				},
			},
		},
		IndexSHA: "index-sha",
	}

	outcome, err := svc.SaveContent(context.Background(), "notes/a.txt", []byte("updated local draft"))
	if err != nil {
		t.Fatalf("save content: %v", err)
	}
	if outcome.ConflictPath == "" {
		t.Fatalf("expected conflict path")
	}
	if !strings.Contains(outcome.ConflictPath, ".conflict-") {
		t.Fatalf("unexpected conflict path: %s", outcome.ConflictPath)
	}

	loaded, err := cacheMgr.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.Files["notes/a.txt"].PendingOp != model.PendingConflict {
		t.Fatalf("expected original path to be marked conflict, got %q", loaded.Files["notes/a.txt"].PendingOp)
	}
	if loaded.Files[outcome.ConflictPath] == nil {
		t.Fatalf("expected conflict copy to be tracked")
	}
}

func TestSyncPullsRemoteAndPushesPendingWrites(t *testing.T) {
	t.Parallel()

	svc, cacheMgr, fake := newTestService(t)
	state := &model.State{
		Version: model.StateVersion,
		Files: map[string]*model.TrackedFile{
			"local.txt": {
				Path:      "local.txt",
				PendingOp: model.PendingUpsert,
			},
		},
	}
	if _, err := cacheMgr.SaveContent("local.txt", []byte("hello local")); err != nil {
		t.Fatalf("save local content: %v", err)
	}
	if err := cacheMgr.SaveState(state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := cacheMgr.UpsertJournalEntry(&model.JournalEntry{
		Path:      "local.txt",
		Operation: model.PendingUpsert,
		Reason:    "save",
	}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	fake.snapshot = &store.RemoteSnapshot{
		Index: &model.RemoteIndex{
			Version: model.IndexVersion,
			Files: map[string]*model.RemoteFile{
				"remote.txt": {
					Path:      "remote.txt",
					Revision:  "rev-remote",
					Checksum:  "sum-remote",
					UpdatedAt: time.Now().UTC(),
				},
			},
		},
		IndexSHA: "index-sha",
	}
	fake.files["remote.txt"] = fakeRemoteFile{content: []byte("hello remote"), sha: "rev-remote"}

	result, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(result.Pulled) != 1 || result.Pulled[0] != "remote.txt" {
		t.Fatalf("expected remote.txt to be pulled, got %#v", result.Pulled)
	}
	if len(result.Pushed) != 1 || result.Pushed[0] != "local.txt" {
		t.Fatalf("expected local.txt to be pushed, got %#v", result.Pushed)
	}
	if fake.files["local.txt"].sha == "" {
		t.Fatalf("expected local.txt to be uploaded to remote store")
	}

	content, err := cacheMgr.LoadContent("remote.txt")
	if err != nil {
		t.Fatalf("load pulled content: %v", err)
	}
	if string(content) != "hello remote" {
		t.Fatalf("unexpected pulled content: %q", string(content))
	}
}

func TestListShowAndRestoreVersion(t *testing.T) {
	t.Parallel()

	svc, cacheMgr, fake := newTestService(t)
	version1 := model.VersionEntry{
		ID:        "111111111111",
		CommitSHA: "111111111111aaaa",
		Path:      "notes/a.txt",
		Timestamp: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		Reason:    "save",
	}
	version2 := model.VersionEntry{
		ID:        "222222222222",
		CommitSHA: "222222222222bbbb",
		Path:      "notes/a.txt",
		Timestamp: time.Date(2026, 4, 20, 11, 0, 0, 0, time.UTC),
		Reason:    "restore",
	}
	fake.versions["notes/a.txt"] = []model.VersionEntry{version2, version1}
	fake.versionContent[version1.CommitSHA] = []byte("old body")
	fake.versionContent[version2.CommitSHA] = []byte("new body")
	fake.snapshot.Index.Files["notes/a.txt"] = &model.RemoteFile{
		Path:      "notes/a.txt",
		Revision:  "base-head",
		Checksum:  "sum-head",
		UpdatedAt: time.Now().UTC(),
	}

	versions, err := svc.ListVersions(context.Background(), "notes/a.txt")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 2 || versions[0].ID != version2.ID {
		t.Fatalf("unexpected versions: %#v", versions)
	}

	entry, content, err := svc.ShowVersion(context.Background(), "notes/a.txt", "11111111")
	if err != nil {
		t.Fatalf("show version: %v", err)
	}
	if entry.CommitSHA != version1.CommitSHA || string(content) != "old body" {
		t.Fatalf("unexpected version lookup result: entry=%#v content=%q", entry, string(content))
	}

	restored, outcome, err := svc.RestoreVersion(context.Background(), "notes/a.txt", version1.ID)
	if err != nil {
		t.Fatalf("restore version: %v", err)
	}
	if restored.CommitSHA != version1.CommitSHA {
		t.Fatalf("unexpected restored version: %#v", restored)
	}
	if !outcome.RemoteSaved {
		t.Fatalf("expected restore to create a durable remote version")
	}
	if fake.lastUpsertReason != "restore" {
		t.Fatalf("expected restore reason, got %q", fake.lastUpsertReason)
	}
	current, err := cacheMgr.LoadContent("notes/a.txt")
	if err != nil {
		t.Fatalf("load restored content: %v", err)
	}
	if string(current) != "old body" {
		t.Fatalf("unexpected restored content: %q", string(current))
	}
}

func TestShowVersionAmbiguousPrefix(t *testing.T) {
	t.Parallel()

	svc, _, fake := newTestService(t)
	fake.versions["notes/a.txt"] = []model.VersionEntry{
		{ID: "abc111111111", CommitSHA: "abc111111111aaaa", Path: "notes/a.txt", Timestamp: time.Now().UTC(), Reason: "save"},
		{ID: "abc222222222", CommitSHA: "abc222222222bbbb", Path: "notes/a.txt", Timestamp: time.Now().UTC(), Reason: "save"},
	}

	_, _, err := svc.ShowVersion(context.Background(), "notes/a.txt", "abc")
	if err == nil {
		t.Fatalf("expected ambiguous version error")
	}
}

func TestListVersionsHandlesLargeHistorySet(t *testing.T) {
	t.Parallel()

	svc, _, fake := newTestService(t)
	versions := make([]model.VersionEntry, 0, 150)
	for i := 0; i < 150; i++ {
		id := strings.Repeat("a", 8) + string(rune('a'+(i%26))) + strings.Repeat("b", 3)
		sha := id + strings.Repeat("c", 28)
		versions = append(versions, model.VersionEntry{
			ID:        sha[:12],
			CommitSHA: sha,
			Path:      "notes/history.txt",
			Timestamp: time.Now().UTC().Add(-time.Duration(i) * time.Minute),
			Reason:    "save",
		})
	}
	fake.versions["notes/history.txt"] = versions

	got, err := svc.ListVersions(context.Background(), "notes/history.txt")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(got) != 150 {
		t.Fatalf("expected 150 versions, got %d", len(got))
	}
}

func TestReadContentDoesNotUseRecoveryDraft(t *testing.T) {
	t.Parallel()

	svc, cacheMgr, _ := newTestService(t)
	if err := cacheMgr.SaveRecovery("device1", "notes/recovery.txt", []byte("draft only")); err != nil {
		t.Fatalf("save recovery: %v", err)
	}
	_, err := svc.ReadContent(context.Background(), "notes/recovery.txt")
	if err == nil {
		t.Fatalf("expected read to ignore recovery draft and fail without durable content")
	}
	if !errs.IsCode(err, errs.CodeNotFound) {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func newTestService(t *testing.T) (*Service, *cache.Manager, *fakeStore) {
	t.Helper()

	root := t.TempDir()
	paths := config.Paths{
		RootDir:     root,
		ConfigPath:  filepath.Join(root, "config.json"),
		StatePath:   filepath.Join(root, "state", "index.json"),
		JournalPath: filepath.Join(root, "state", "journal.json"),
		CacheDir:    filepath.Join(root, "cache"),
		RecoveryDir: filepath.Join(root, "recovery"),
	}
	if err := config.EnsureLayout(paths); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	cacheMgr := cache.New(paths)
	fake := &fakeStore{
		snapshot: &store.RemoteSnapshot{
			Index: &model.RemoteIndex{Version: model.IndexVersion, Files: map[string]*model.RemoteFile{}},
		},
		files:          map[string]fakeRemoteFile{},
		versions:       map[string][]model.VersionEntry{},
		versionContent: map[string][]byte{},
	}
	cfg := &model.Config{
		Owner:    "tester",
		Repo:     "pb-test",
		Login:    "tester",
		DeviceID: "device1",
	}
	return NewService(paths, cfg, cacheMgr, fake), cacheMgr, fake
}

type fakeStore struct {
	snapshot         *store.RemoteSnapshot
	files            map[string]fakeRemoteFile
	versions         map[string][]model.VersionEntry
	versionContent   map[string][]byte
	lastUpsertReason string
	lastDeleteReason string
}

type fakeRemoteFile struct {
	content []byte
	sha     string
}

func (f *fakeStore) EnsureRepo(context.Context) error {
	return nil
}

func (f *fakeStore) FetchIndex(context.Context) (*store.RemoteSnapshot, error) {
	return cloneSnapshot(f.snapshot), nil
}

func (f *fakeStore) FetchFile(_ context.Context, path string) ([]byte, string, error) {
	file, ok := f.files[path]
	if !ok {
		return nil, "", errs.Wrap(errs.CodeNotFound, "remote file not found", nil)
	}
	return append([]byte(nil), file.content...), file.sha, nil
}

func (f *fakeStore) FetchFileAtRevision(_ context.Context, _ string, revision string) ([]byte, error) {
	return append([]byte(nil), f.versionContent[revision]...), nil
}

func (f *fakeStore) ListVersions(_ context.Context, path string) ([]model.VersionEntry, error) {
	items := f.versions[path]
	cloned := make([]model.VersionEntry, len(items))
	copy(cloned, items)
	return cloned, nil
}

func (f *fakeStore) UpsertFile(_ context.Context, path string, content []byte, _ string, reason string) (*model.RemoteFile, error) {
	sha := "sha-" + strings.ReplaceAll(path, "/", "_")
	f.files[path] = fakeRemoteFile{content: append([]byte(nil), content...), sha: sha}
	f.lastUpsertReason = reason
	record := &model.RemoteFile{
		Path:      path,
		Revision:  sha,
		Checksum:  "checksum-" + path,
		UpdatedAt: time.Now().UTC(),
	}
	f.snapshot.Index.Files[path] = record
	return record, nil
}

func (f *fakeStore) DeleteFile(_ context.Context, path string, _ string, reason string) error {
	delete(f.files, path)
	f.lastDeleteReason = reason
	return nil
}

func (f *fakeStore) SaveIndex(_ context.Context, index *model.RemoteIndex, _ string) (string, error) {
	f.snapshot = &store.RemoteSnapshot{
		Index:    cloneIndex(index),
		IndexSHA: "index-next",
	}
	return f.snapshot.IndexSHA, nil
}

func cloneSnapshot(snapshot *store.RemoteSnapshot) *store.RemoteSnapshot {
	if snapshot == nil {
		return &store.RemoteSnapshot{Index: &model.RemoteIndex{Version: model.IndexVersion, Files: map[string]*model.RemoteFile{}}}
	}
	return &store.RemoteSnapshot{
		Index:    cloneIndex(snapshot.Index),
		IndexSHA: snapshot.IndexSHA,
	}
}

func cloneIndex(index *model.RemoteIndex) *model.RemoteIndex {
	if index == nil {
		return &model.RemoteIndex{Version: model.IndexVersion, Files: map[string]*model.RemoteFile{}}
	}
	cloned := &model.RemoteIndex{
		Version: index.Version,
		Files:   map[string]*model.RemoteFile{},
	}
	for k, v := range index.Files {
		copyRecord := *v
		cloned.Files[k] = &copyRecord
	}
	return cloned
}
