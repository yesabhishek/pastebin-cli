package cache

import (
	"path/filepath"
	"testing"

	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/model"
)

func TestValidatePath(t *testing.T) {
	t.Parallel()

	valid := []string{
		"notes/today.txt",
		"todo.md",
		"nested/path/file.log",
	}
	for _, candidate := range valid {
		if err := ValidatePath(candidate); err != nil {
			t.Fatalf("expected %q to be valid, got %v", candidate, err)
		}
	}

	invalid := []string{
		"",
		"/tmp/file.txt",
		"../secrets.txt",
		"notes/../../file.txt",
		"CON.txt",
		`bad:name.txt`,
	}
	for _, candidate := range invalid {
		if err := ValidatePath(candidate); err == nil {
			t.Fatalf("expected %q to be invalid", candidate)
		}
	}
}

func TestSaveAndLoadStateAndJournal(t *testing.T) {
	t.Parallel()

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
	manager := New(paths)

	checksum, err := manager.SaveContent("notes/alpha.txt", []byte("hello world"))
	if err != nil {
		t.Fatalf("save content: %v", err)
	}
	state := &model.State{
		Version: model.StateVersion,
		Files: map[string]*model.TrackedFile{
			"notes/alpha.txt": {
				Path:      "notes/alpha.txt",
				Checksum:  checksum,
				PendingOp: model.PendingUpsert,
			},
		},
	}
	if err := manager.SaveState(state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := manager.UpsertJournalEntry(&model.JournalEntry{
		Path:      "notes/alpha.txt",
		Operation: model.PendingUpsert,
	}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	loadedState, err := manager.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if got := loadedState.Files["notes/alpha.txt"].Checksum; got != checksum {
		t.Fatalf("unexpected checksum: got %q want %q", got, checksum)
	}

	journal, err := manager.LoadJournal()
	if err != nil {
		t.Fatalf("load journal: %v", err)
	}
	if journal.Entries["notes/alpha.txt"] == nil {
		t.Fatalf("expected journal entry to exist")
	}

	content, err := manager.LoadContent("notes/alpha.txt")
	if err != nil {
		t.Fatalf("load content: %v", err)
	}
	if string(content) != "hello world" {
		t.Fatalf("unexpected content: %q", string(content))
	}
}

func TestSaveAndLoadRecovery(t *testing.T) {
	t.Parallel()

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
	manager := New(paths)

	if err := manager.SaveRecovery("device1", "notes/draft.txt", []byte("draft body")); err != nil {
		t.Fatalf("save recovery: %v", err)
	}
	data, err := manager.LoadRecovery("device1", "notes/draft.txt")
	if err != nil {
		t.Fatalf("load recovery: %v", err)
	}
	if string(data) != "draft body" {
		t.Fatalf("unexpected recovery content: %q", string(data))
	}
	if err := manager.RemoveRecovery("device1", "notes/draft.txt"); err != nil {
		t.Fatalf("remove recovery: %v", err)
	}
	if _, err := manager.LoadRecovery("device1", "notes/draft.txt"); err == nil {
		t.Fatalf("expected removed recovery to be missing")
	}
}
