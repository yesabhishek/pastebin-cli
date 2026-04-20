package sync

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/cache"
	"github.com/yesabhishek/pastebin-cli/internal/config"
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
		files: map[string]fakeRemoteFile{},
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
	snapshot *store.RemoteSnapshot
	files    map[string]fakeRemoteFile
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
	file := f.files[path]
	return append([]byte(nil), file.content...), file.sha, nil
}

func (f *fakeStore) UpsertFile(_ context.Context, path string, content []byte, _ string) (*model.RemoteFile, error) {
	sha := "sha-" + strings.ReplaceAll(path, "/", "_")
	f.files[path] = fakeRemoteFile{content: append([]byte(nil), content...), sha: sha}
	record := &model.RemoteFile{
		Path:      path,
		Revision:  sha,
		Checksum:  "checksum-" + path,
		UpdatedAt: time.Now().UTC(),
	}
	f.snapshot.Index.Files[path] = record
	return record, nil
}

func (f *fakeStore) DeleteFile(_ context.Context, path string, _ string) error {
	delete(f.files, path)
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
