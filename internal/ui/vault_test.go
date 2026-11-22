package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/keystore"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/store"
)

func newDeviceApp(t *testing.T, backend store.Backend, device string) (*App, *keystore.Memory) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)
	app, err := NewApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(app.paths, &model.Config{Owner: "tester", Repo: "pb-store", Login: "tester", DeviceID: device}); err != nil {
		t.Fatal(err)
	}
	ks := &keystore.Memory{}
	app.keys = ks
	app.backendFor = func(*model.Config) store.Backend { return backend }
	return app, ks
}

func run(t *testing.T, app *App, stdin string, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	app.out = out
	app.errOut = &bytes.Buffer{}
	app.in = strings.NewReader(stdin)
	app.stdinReader = nil
	app.secretFromStdin = false
	app.paths = app.paths.Scope("", "")
	err := app.Run(context.Background(), args)
	return out.String(), err
}

func TestTwoDevicesShareEncryptedNotes(t *testing.T) {
	backend := store.NewMemoryBackend()
	legacy, _ := json.Marshal(model.RemoteIndex{Version: 1, Files: map[string]*model.RemoteFile{"old/legacy.txt": {Path: "old/legacy.txt"}}})
	_, _ = backend.Put(context.Background(), store.LegacyIndexPath, legacy, "", "seed")
	_, _ = backend.Put(context.Background(), "files/old/legacy.txt", []byte("legacy plaintext"), "", "seed")

	// Device A creates the vault and migrates the legacy note.
	a, aKeys := newDeviceApp(t, backend, "deviceA")
	out, err := run(t, a, "correct horse battery\n", "encrypt", "--stdin")
	if err != nil {
		t.Fatalf("encrypt: %v (%s)", err, out)
	}
	if !strings.Contains(out, "Encrypted 1 existing note") {
		t.Fatalf("expected migration message, got %q", out)
	}
	if len(aKeys.M) != 1 {
		t.Fatalf("expected key cached on device A")
	}
	out, err = run(t, a, "", "--json", "note", "milk", "eggs", "--title", "Groceries")
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	var saved map[string]any
	_ = json.Unmarshal([]byte(out), &saved)
	path, _ := saved["path"].(string)
	if !strings.HasPrefix(path, "notes/") || !strings.HasSuffix(path, "-groceries.md") || saved["remote_saved"] != true {
		t.Fatalf("unexpected note result: %s", out)
	}
	if saved["version_id"] == "" {
		t.Fatalf("expected a version id")
	}
	for _, leak := range []string{"Groceries", "milk", "legacy plaintext", "legacy.txt", "notes/"} {
		if backend.Contains(leak) {
			t.Fatalf("remote leaked %q", leak)
		}
	}

	// Device B is locked until it unlocks with the passphrase.
	b, _ := newDeviceApp(t, backend, "deviceB")
	if _, err := run(t, b, "", "sync"); !errs.IsCode(err, errs.CodeLocked) || !strings.Contains(err.Error(), "vault is locked") {
		t.Fatalf("expected locked error, got %v", err)
	}
	if _, err := run(t, b, "wrong passphrase!!\n", "unlock", "--stdin"); err == nil {
		t.Fatalf("expected wrong passphrase to fail")
	}
	if _, err := run(t, b, "correct horse battery\n", "unlock", "--stdin"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	out, err = run(t, b, "", "--json", "sync")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	var res model.SyncResult
	_ = json.Unmarshal([]byte(out), &res)
	if len(res.Pulled) != 2 {
		t.Fatalf("expected 2 pulled notes, got %s", out)
	}
	out, err = run(t, b, "", "read", path)
	if err != nil || !strings.Contains(out, "# Groceries") || !strings.Contains(out, "milk eggs") {
		t.Fatalf("read on device B: %q %v", out, err)
	}
	out, err = run(t, b, "", "read", "old/legacy.txt")
	if err != nil || out != "legacy plaintext" {
		t.Fatalf("legacy read on device B: %q %v", out, err)
	}

	// History is available and decrypts.
	out, err = run(t, b, "", "--json", "versions", path, "--limit", "5")
	if err != nil || !strings.Contains(out, "commit_sha") {
		t.Fatalf("versions: %q %v", out, err)
	}

	// Locking removes the device key.
	if _, err := run(t, b, "", "lock"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if _, err := run(t, b, "", "sync"); !errs.IsCode(err, errs.CodeLocked) {
		t.Fatalf("expected locked after lock, got %v", err)
	}
}

func TestPassphraseChangeKeepsNotesReadable(t *testing.T) {
	backend := store.NewMemoryBackend()
	a, _ := newDeviceApp(t, backend, "deviceA")
	if _, err := run(t, a, "correct horse battery\n", "encrypt", "--stdin"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a, "hello", "save", "x.txt", "--stdin"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a, "correct horse battery\nan even better passphrase\n", "passphrase", "--stdin"); err != nil {
		t.Fatalf("passphrase: %v", err)
	}
	b, _ := newDeviceApp(t, backend, "deviceB")
	if _, err := run(t, b, "correct horse battery\n", "unlock", "--stdin"); err == nil {
		t.Fatalf("old passphrase should no longer unlock")
	}
	if _, err := run(t, b, "an even better passphrase\n", "unlock", "--stdin"); err != nil {
		t.Fatalf("new passphrase: %v", err)
	}
	if out, err := run(t, b, "", "read", "x.txt"); err != nil || out != "hello" {
		t.Fatalf("read: %q %v", out, err)
	}
}

func TestSaveWhileLockedStaysLocal(t *testing.T) {
	backend := store.NewMemoryBackend()
	a, _ := newDeviceApp(t, backend, "deviceA")
	out, err := run(t, a, "draft", "--json", "save", "d.txt", "--stdin")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(out, `"locked": true`) {
		t.Fatalf("expected locked flag, got %s", out)
	}
	if len(backend.Paths()) != 0 {
		t.Fatalf("nothing should reach the remote while locked: %v", backend.Paths())
	}
}

func TestQuickNotePath(t *testing.T) {
	now := time.Date(2026, 11, 22, 9, 5, 0, 0, time.UTC)
	cases := map[string][2]string{
		"notes/2026-11-22-0905-meeting-notes.md": {"Meeting Notes!", ""},
		"notes/2026-11-22-0905-first-line.md":    {"", "\n# First line\nmore"},
		"notes/2026-11-22-0905-note.md":          {"", "   "},
		"notes/2026-11-22-0905-etc-passwd.md":    {"../../etc/passwd", ""},
	}
	for want, in := range cases {
		if got := QuickNotePath(in[0], in[1], now); got != want {
			t.Errorf("QuickNotePath(%q,%q)=%q want %q", in[0], in[1], got, want)
		}
	}
}

func TestPublicRepoIsRefused(t *testing.T) {
	backend := store.NewMemoryBackend()
	backend.Public = true
	a, _ := newDeviceApp(t, backend, "deviceA")
	if _, err := run(t, a, "correct horse battery\n", "encrypt", "--stdin"); err == nil {
		t.Fatalf("expected refusal for public repo")
	}
	if len(backend.Paths()) != 0 {
		t.Fatalf("nothing should be written to a public repo")
	}
}
