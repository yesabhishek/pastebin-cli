package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/cache"
	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/media"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/store"
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

func TestReadImageOpensDefaultViewer(t *testing.T) {
	out := &bytes.Buffer{}
	viewer := &fakeViewer{}
	app := &App{out: out, viewer: viewer}

	if err := app.writeReadContent("shots/capture.png", testPNG, "", false); err != nil {
		t.Fatalf("write read content: %v", err)
	}
	if viewer.opened == "" {
		t.Fatalf("expected image viewer to open")
	}
	if !strings.HasSuffix(viewer.opened, ".png") {
		t.Fatalf("expected temp viewer path to keep image extension, got %q", viewer.opened)
	}
	if !strings.Contains(out.String(), "Opened shots/capture.png") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestReadImageOutWritesFile(t *testing.T) {
	out := &bytes.Buffer{}
	viewer := &fakeViewer{}
	app := &App{out: out, viewer: viewer}
	target := filepath.Join(t.TempDir(), "capture.png")

	if err := app.writeReadContent("shots/capture.png", testPNG, target, false); err != nil {
		t.Fatalf("write read content: %v", err)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !bytes.Equal(written, testPNG) {
		t.Fatalf("unexpected output bytes: %#v", written)
	}
	if viewer.opened != "" {
		t.Fatalf("did not expect viewer to open when --out is used")
	}
}

func TestReadBinaryRequiresOut(t *testing.T) {
	app := &App{out: &bytes.Buffer{}, viewer: &fakeViewer{}}
	err := app.writeReadContent("data/blob.bin", []byte{0, 1, 2}, "", false)
	if err == nil {
		t.Fatalf("expected binary read without --out to fail")
	}
	if !errs.IsCode(err, errs.CodeUsage) {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestPasteCommandSavesClipboardImageWithPNGExtension(t *testing.T) {
	app, fakeRemote, out := newClipboardTestApp(t)
	clip := &fakeClipboard{image: testPNG}
	app.clipboard = clip

	if err := app.pastePath(context.Background(), "shots/capture"); err != nil {
		t.Fatalf("paste path: %v", err)
	}
	got := fakeRemote.files["shots/capture.png"].content
	if !bytes.Equal(got, testPNG) {
		t.Fatalf("expected pasted PNG bytes, got %#v", got)
	}
	if !strings.Contains(out.String(), "shots/capture.png") {
		t.Fatalf("expected output to mention normalized path, got %q", out.String())
	}
}

func TestCopyCommandWritesImageToClipboard(t *testing.T) {
	app, _, _ := newClipboardTestApp(t)
	clip := &fakeClipboard{}
	app.clipboard = clip
	cacheMgr := cache.New(app.paths)
	if _, err := cacheMgr.SaveContent("shots/capture.png", testPNG); err != nil {
		t.Fatalf("save content: %v", err)
	}
	if err := cacheMgr.SaveState(&model.State{
		Version: model.StateVersion,
		Files: map[string]*model.TrackedFile{
			"shots/capture.png": {Path: "shots/capture.png"},
		},
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := app.copyPath(context.Background(), "shots/capture.png"); err != nil {
		t.Fatalf("copy path: %v", err)
	}
	if !bytes.Equal(clip.writtenImage, testPNG) {
		t.Fatalf("expected image bytes on clipboard, got %#v", clip.writtenImage)
	}
}

func TestCopyCommandConvertsJPEGToClipboardPNG(t *testing.T) {
	app, _, _ := newClipboardTestApp(t)
	clip := &fakeClipboard{}
	app.clipboard = clip
	jpegData := testJPEG(t)
	cacheMgr := cache.New(app.paths)
	if _, err := cacheMgr.SaveContent("shots/capture.jpg", jpegData); err != nil {
		t.Fatalf("save content: %v", err)
	}
	if err := cacheMgr.SaveState(&model.State{
		Version: model.StateVersion,
		Files: map[string]*model.TrackedFile{
			"shots/capture.jpg": {Path: "shots/capture.jpg"},
		},
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := app.copyPath(context.Background(), "shots/capture.jpg"); err != nil {
		t.Fatalf("copy path: %v", err)
	}
	if !bytes.HasPrefix(clip.writtenImage, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("expected clipboard image to be PNG encoded, got %#v", clip.writtenImage[:minInt(len(clip.writtenImage), 8)])
	}
}

func newClipboardTestApp(t *testing.T) (*App, *fakeRemoteStore, *bytes.Buffer) {
	t.Helper()

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	out := &bytes.Buffer{}
	app, err := NewApp(strings.NewReader(""), out, &bytes.Buffer{})
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
	fakeRemote := newFakeRemoteStore()
	app.remoteStore = func(*model.Config) store.RemoteStore {
		return fakeRemote
	}
	return app, fakeRemote, out
}

type fakeClipboard struct {
	image        []byte
	readImageErr error
	writtenImage []byte
	writtenText  []byte
}

func (f *fakeClipboard) ReadImage() ([]byte, error) {
	if f.readImageErr != nil {
		return nil, f.readImageErr
	}
	if len(f.image) == 0 {
		return nil, media.ErrNoClipboardImage
	}
	return append([]byte(nil), f.image...), nil
}

func (f *fakeClipboard) WriteImage(data []byte) error {
	f.writtenImage = append([]byte(nil), data...)
	return nil
}

func (f *fakeClipboard) WriteText(data []byte) error {
	f.writtenText = append([]byte(nil), data...)
	return nil
}

type fakeViewer struct {
	opened string
}

func (f *fakeViewer) Open(path string) error {
	f.opened = path
	return nil
}

type fakeRemoteStore struct {
	snapshot       *store.RemoteSnapshot
	files          map[string]fakeRemoteContent
	versions       map[string][]model.VersionEntry
	versionContent map[string][]byte
	upsertCount    int
}

type fakeRemoteContent struct {
	content []byte
	sha     string
}

func newFakeRemoteStore() *fakeRemoteStore {
	return &fakeRemoteStore{
		snapshot: &store.RemoteSnapshot{
			Index: &model.RemoteIndex{Version: model.IndexVersion, Files: map[string]*model.RemoteFile{}},
		},
		files:          map[string]fakeRemoteContent{},
		versions:       map[string][]model.VersionEntry{},
		versionContent: map[string][]byte{},
	}
}

func (f *fakeRemoteStore) EnsureRepo(context.Context) error {
	return nil
}

func (f *fakeRemoteStore) FetchIndex(context.Context) (*store.RemoteSnapshot, error) {
	return cloneTestSnapshot(f.snapshot), nil
}

func (f *fakeRemoteStore) FetchFile(_ context.Context, path string) ([]byte, string, error) {
	file, ok := f.files[path]
	if !ok {
		return nil, "", errs.Wrap(errs.CodeNotFound, "remote file not found", nil)
	}
	return append([]byte(nil), file.content...), file.sha, nil
}

func (f *fakeRemoteStore) FetchFileAtRevision(_ context.Context, _ string, revision string) ([]byte, error) {
	return append([]byte(nil), f.versionContent[revision]...), nil
}

func (f *fakeRemoteStore) ListVersions(_ context.Context, path string) ([]model.VersionEntry, error) {
	items := f.versions[path]
	cloned := make([]model.VersionEntry, len(items))
	copy(cloned, items)
	return cloned, nil
}

func (f *fakeRemoteStore) UpsertFile(_ context.Context, path string, content []byte, _ string, reason string) (*model.RemoteFile, error) {
	f.upsertCount++
	sha := fmt.Sprintf("blob-%d", f.upsertCount)
	commitSHA := fmt.Sprintf("commit-%012d", f.upsertCount)
	f.files[path] = fakeRemoteContent{content: append([]byte(nil), content...), sha: sha}
	record := &model.RemoteFile{
		Path:      path,
		Revision:  sha,
		Checksum:  "checksum-" + path,
		UpdatedAt: time.Now().UTC(),
	}
	f.snapshot.Index.Files[path] = record
	version := model.VersionEntry{
		ID:        commitSHA[:12],
		CommitSHA: commitSHA,
		Path:      path,
		Timestamp: record.UpdatedAt,
		Reason:    reason,
	}
	f.versions[path] = append([]model.VersionEntry{version}, f.versions[path]...)
	f.versionContent[commitSHA] = append([]byte(nil), content...)
	return record, nil
}

func (f *fakeRemoteStore) DeleteFile(_ context.Context, path string, _ string, _ string) error {
	delete(f.files, path)
	return nil
}

func (f *fakeRemoteStore) SaveIndex(_ context.Context, index *model.RemoteIndex, _ string) (string, error) {
	f.snapshot = &store.RemoteSnapshot{
		Index:    cloneTestIndex(index),
		IndexSHA: "index-next",
	}
	return f.snapshot.IndexSHA, nil
}

func cloneTestSnapshot(snapshot *store.RemoteSnapshot) *store.RemoteSnapshot {
	if snapshot == nil {
		return &store.RemoteSnapshot{Index: &model.RemoteIndex{Version: model.IndexVersion, Files: map[string]*model.RemoteFile{}}}
	}
	return &store.RemoteSnapshot{
		Index:    cloneTestIndex(snapshot.Index),
		IndexSHA: snapshot.IndexSHA,
	}
}

func cloneTestIndex(index *model.RemoteIndex) *model.RemoteIndex {
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

var testPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func testJPEG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
