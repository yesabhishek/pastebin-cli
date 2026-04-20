package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/model"
)

func TestIsNewer(t *testing.T) {
	manager := NewManager("v0.1.0")
	if !manager.IsNewer("v0.1.1") {
		t.Fatalf("expected v0.1.1 to be newer")
	}
	if manager.IsNewer("v0.1.0") {
		t.Fatalf("did not expect equal version to be newer")
	}
	if manager.IsNewer("dev") {
		t.Fatalf("did not expect invalid semver to be newer")
	}
}

func TestCheckRespectsIgnoredReleaseAndInterval(t *testing.T) {
	releaseJSON := `{"tag_name":"v0.1.1","assets":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releaseJSON))
	}))
	defer server.Close()

	manager := NewManager("v0.1.0")
	manager.client = server.Client()
	manager.apiBaseURL = server.URL
	manager.now = func() time.Time {
		return time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	}
	manager.ownerRepo = "ignored/ignored"

	cfg := &model.Config{UpgradePolicy: model.UpgradePolicyPrompt}
	release, available, err := manager.Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("check release: %v", err)
	}
	if release == nil || !available {
		t.Fatalf("expected available release, got release=%#v available=%v", release, available)
	}
	if cfg.LastReleaseCheck.IsZero() {
		t.Fatalf("expected last release check to be recorded")
	}

	cfg.IgnoredRelease = "v0.1.1"
	cfg.LastReleaseCheck = time.Time{}
	release, available, err = manager.Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("check ignored release: %v", err)
	}
	if release == nil || available {
		t.Fatalf("expected ignored release to suppress availability, got release=%#v available=%v", release, available)
	}

	cfg.LastReleaseCheck = manager.now()
	release, available, err = manager.Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("check interval: %v", err)
	}
	if release != nil || available {
		t.Fatalf("expected recent check to skip network lookup")
	}
}

func TestInstallReplacesExecutableOnUnix(t *testing.T) {
	archive := makeTarGz(t, "pb", []byte("new-binary"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "pb")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write initial executable: %v", err)
	}

	manager := NewManager("v0.1.0")
	manager.client = server.Client()
	manager.goos = "linux"
	manager.goarch = "amd64"
	manager.executablePath = func() (string, error) { return target, nil }

	release := &Release{
		TagName: "v0.1.1",
		Assets: []ReleaseAsset{
			{Name: "pb_linux_amd64.tar.gz", URL: server.URL + "/pb_linux_amd64.tar.gz"},
		},
	}
	result, err := manager.Install(context.Background(), release)
	if err != nil {
		t.Fatalf("install release: %v", err)
	}
	if result.Scheduled {
		t.Fatalf("did not expect Unix install to be scheduled")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read replaced executable: %v", err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("unexpected executable contents: %q", string(got))
	}
}

func makeTarGz(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(data)),
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("write tar payload: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}
