package ui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yesabhishek/pastebin-cli/internal/cache"
	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/model"
)

func TestListCommandPrintsTrackedFiles(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)

	app, err := NewApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	cfg := &model.Config{
		Owner:    "tester",
		Repo:     "pb-store",
		Login:    "tester",
		DeviceID: "device1",
	}
	if err := config.Save(app.paths, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	cacheMgr := cache.New(app.paths)
	state := &model.State{
		Version: model.StateVersion,
		Files: map[string]*model.TrackedFile{
			"notes/a.txt": {Path: "notes/a.txt"},
			"notes/b.txt": {Path: "notes/b.txt"},
			"trash.txt":   {Path: "trash.txt", Deleted: true},
		},
	}
	if err := cacheMgr.SaveState(state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := cacheMgr.SaveRecovery("device1", "notes/draft.txt", []byte("draft")); err != nil {
		t.Fatalf("save recovery: %v", err)
	}

	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	app.out = out
	app.errOut = errOut
	if err := app.Run(context.Background(), []string{"list", "notes/"}); err != nil {
		t.Fatalf("run list: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "notes/a.txt") || !strings.Contains(got, "notes/b.txt") {
		t.Fatalf("expected list output to contain tracked notes, got %q", got)
	}
	if strings.Contains(got, "trash.txt") {
		t.Fatalf("did not expect deleted file in list output")
	}
	if strings.Contains(got, "notes/draft.txt") {
		t.Fatalf("did not expect recovery draft in list output")
	}
}

func TestLogoutRemovesLocalConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)

	out := &bytes.Buffer{}
	app, err := NewApp(strings.NewReader(""), out, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if err := config.Save(app.paths, &model.Config{
		Owner:    "tester",
		Repo:     "pb-store",
		Login:    "tester",
		DeviceID: "device1",
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if err := app.Run(context.Background(), []string{"logout"}); err != nil {
		t.Fatalf("run logout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app.paths.RootDir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("expected config file to be removed, stat err=%v", err)
	}
	if !strings.Contains(out.String(), "Local pb state removed") {
		t.Fatalf("unexpected logout output: %q", out.String())
	}
}

func TestLoadEditorInitialPrefersRecoveryDraft(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)

	app, err := NewApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	cacheMgr := cache.New(app.paths)
	if err := cacheMgr.SaveRecovery("device1", "notes/draft.txt", []byte("draft body")); err != nil {
		t.Fatalf("save recovery: %v", err)
	}

	cfg := &model.Config{
		Owner:    "tester",
		Repo:     "pb-store",
		Login:    "tester",
		DeviceID: "device1",
	}
	initial, recovered, status, err := app.loadEditorInitial(context.Background(), nil, cacheMgr, cfg.DeviceID, "notes/draft.txt", true)
	if err != nil {
		t.Fatalf("load editor initial: %v", err)
	}
	if string(initial) != "draft body" {
		t.Fatalf("unexpected initial content: %q", string(initial))
	}
	if !recovered {
		t.Fatalf("expected recovery draft to be loaded")
	}
	if !strings.Contains(status, "Recovered local draft autosave") {
		t.Fatalf("unexpected recovery status: %q", status)
	}
}

func TestVersionCommandPrintsCurrentVersion(t *testing.T) {
	app, err := NewApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	out := &bytes.Buffer{}
	app.out = out
	if err := app.Run(context.Background(), []string{"version"}); err != nil {
		t.Fatalf("run version: %v", err)
	}
	if !strings.Contains(out.String(), "pb ") {
		t.Fatalf("unexpected version output: %q", out.String())
	}
}

func TestApplyUpgradePolicyRejectsUnknownValues(t *testing.T) {
	cfg := &model.Config{}
	if err := applyUpgradePolicy(cfg, "weird"); err == nil {
		t.Fatalf("expected invalid policy to fail")
	}
	if err := applyUpgradePolicy(cfg, model.UpgradePolicyAuto); err != nil {
		t.Fatalf("apply valid policy: %v", err)
	}
	if cfg.UpgradePolicy != model.UpgradePolicyAuto {
		t.Fatalf("unexpected policy: %q", cfg.UpgradePolicy)
	}
}
