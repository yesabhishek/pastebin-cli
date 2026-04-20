package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
)

const (
	DefaultOwnerRepo = "yesabhishek/pastebin-cli"
	CheckInterval    = 24 * time.Hour
)

type Release struct {
	TagName   string         `json:"tag_name"`
	HTMLURL   string         `json:"html_url"`
	Published time.Time      `json:"published_at"`
	Assets    []ReleaseAsset `json:"assets"`
}

type ReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type Result struct {
	Version    string
	Executable string
	Scheduled  bool
}

type Manager struct {
	client         *http.Client
	apiBaseURL     string
	ownerRepo      string
	currentVersion string
	goos           string
	goarch         string
	now            func() time.Time
	executablePath func() (string, error)
	startDetached  func(name string, args ...string) error
}

func NewManager(currentVersion string) *Manager {
	return &Manager{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		apiBaseURL:     "https://api.github.com",
		ownerRepo:      DefaultOwnerRepo,
		currentVersion: currentVersion,
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		now:            time.Now,
		executablePath: os.Executable,
		startDetached: func(name string, args ...string) error {
			return exec.Command(name, args...).Start()
		},
	}
}

func (m *Manager) CurrentVersion() string {
	return m.currentVersion
}

func (m *Manager) ShouldCheck(cfg *model.Config) bool {
	if cfg == nil {
		return false
	}
	if cfg.LastReleaseCheck.IsZero() {
		return true
	}
	return m.now().UTC().Sub(cfg.LastReleaseCheck.UTC()) >= CheckInterval
}

func (m *Manager) Latest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(m.apiBaseURL, "/")+"/repos/"+m.ownerRepo+"/releases/latest", nil)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, "build release request", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "pb/"+safeVersion(m.currentVersion))
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, "query latest pb release", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, errs.Wrap(errs.CodeRateLimit, "GitHub release API rate limit reached", nil)
	case resp.StatusCode >= 400:
		return nil, errs.Wrap(errs.CodeNetwork, fmt.Sprintf("release check failed with status %d", resp.StatusCode), nil)
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse latest pb release", err)
	}
	if release.TagName == "" {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "latest pb release is missing a tag", nil)
	}
	return &release, nil
}

func (m *Manager) IsNewer(tag string) bool {
	return compareSemver(tag, m.currentVersion) > 0
}

func (m *Manager) CanCompareCurrentVersion() bool {
	_, ok := parseSemver(m.currentVersion)
	return ok
}

func (m *Manager) Check(ctx context.Context, cfg *model.Config) (*Release, bool, error) {
	if !m.ShouldCheck(cfg) {
		return nil, false, nil
	}
	release, err := m.Latest(ctx)
	if cfg != nil {
		cfg.LastReleaseCheck = m.now().UTC()
	}
	if err != nil {
		return nil, false, err
	}
	if cfg != nil && cfg.IgnoredRelease != "" && cfg.IgnoredRelease == release.TagName {
		return release, false, nil
	}
	return release, m.IsNewer(release.TagName), nil
}

func (m *Manager) Install(ctx context.Context, release *Release) (*Result, error) {
	if release == nil {
		return nil, errs.Wrap(errs.CodeUsage, "release is required", nil)
	}
	asset, err := m.matchAsset(release)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, "build release download request", err)
	}
	req.Header.Set("User-Agent", "pb/"+safeVersion(m.currentVersion))
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, "download release asset", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errs.Wrap(errs.CodeNetwork, fmt.Sprintf("release asset download failed with status %d", resp.StatusCode), nil)
	}
	archiveData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, "read release asset", err)
	}
	executableData, err := extractExecutable(asset.Name, archiveData)
	if err != nil {
		return nil, err
	}
	target, err := m.executablePath()
	if err != nil {
		return nil, errs.Wrap(errs.CodeLocalCorruption, "resolve current executable", err)
	}
	if m.goos == "windows" {
		if err := m.scheduleWindowsInstall(target, executableData); err != nil {
			return nil, err
		}
		return &Result{Version: release.TagName, Executable: target, Scheduled: true}, nil
	}
	if err := installExecutable(target, executableData); err != nil {
		return nil, err
	}
	return &Result{Version: release.TagName, Executable: target}, nil
}

func (m *Manager) matchAsset(release *Release) (*ReleaseAsset, error) {
	name := assetName(m.goos, m.goarch)
	for _, asset := range release.Assets {
		if asset.Name == name {
			return &asset, nil
		}
	}
	return nil, errs.Wrap(errs.CodeNotFound, "no release asset found for "+name, nil)
}

func (m *Manager) scheduleWindowsInstall(target string, executableData []byte) error {
	dir := filepath.Dir(target)
	tmpFile, err := os.CreateTemp(dir, "pb-update-*.exe")
	if err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "create temporary update file", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(executableData); err != nil {
		tmpFile.Close()
		return errs.Wrap(errs.CodeLocalCorruption, "write temporary update file", err)
	}
	if err := tmpFile.Close(); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "close temporary update file", err)
	}
	scriptPath := strings.TrimSuffix(tmpPath, ".exe") + ".cmd"
	script := fmt.Sprintf(`@echo off
setlocal
:retry
copy /Y "%s" "%s" >nul 2>nul
if errorlevel 1 (
  ping 127.0.0.1 -n 2 >nul
  goto retry
)
del /Q "%s" >nul 2>nul
del /Q "%%~f0" >nul 2>nul
`, tmpPath, target, tmpPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "write Windows updater script", err)
	}
	if err := m.startDetached("cmd", "/C", scriptPath); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "start Windows updater", err)
	}
	return nil
}

func installExecutable(target string, executableData []byte) error {
	mode := os.FileMode(0o755)
	if stat, err := os.Stat(target); err == nil {
		mode = stat.Mode() & os.ModePerm
		if mode == 0 {
			mode = 0o755
		}
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(target), "pb-update-*")
	if err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "create temporary executable", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(executableData); err != nil {
		tmpFile.Close()
		return errs.Wrap(errs.CodeLocalCorruption, "write temporary executable", err)
	}
	if err := tmpFile.Close(); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "close temporary executable", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "chmod temporary executable", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "replace current executable", err)
	}
	return nil
}

func extractExecutable(assetName string, archiveData []byte) ([]byte, error) {
	switch {
	case strings.HasSuffix(assetName, ".tar.gz"):
		return extractTarGz(archiveData)
	case strings.HasSuffix(assetName, ".zip"):
		return extractZip(archiveData)
	default:
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "unsupported release archive format", nil)
	}
}

func extractTarGz(archiveData []byte) ([]byte, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(archiveData))
	if err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "open tar.gz release asset", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "read tar.gz release asset", err)
		}
		if hdr.FileInfo().Mode().IsRegular() && filepath.Base(hdr.Name) == "pb" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, errs.Wrap(errs.CodeRemoteCorruption, "read executable from archive", err)
			}
			return data, nil
		}
	}
	return nil, errs.Wrap(errs.CodeRemoteCorruption, "release archive did not contain pb", nil)
}

func extractZip(archiveData []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "open zip release asset", err)
	}
	for _, file := range zr.File {
		if filepath.Base(file.Name) != "pb.exe" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "open executable in zip", err)
		}
		data, readErr := io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "read executable in zip", readErr)
		}
		if closeErr != nil {
			return nil, errs.Wrap(errs.CodeRemoteCorruption, "close executable in zip", closeErr)
		}
		return data, nil
	}
	return nil, errs.Wrap(errs.CodeRemoteCorruption, "release archive did not contain pb.exe", nil)
}

func assetName(goos string, goarch string) string {
	if goos == "windows" {
		return fmt.Sprintf("pb_%s_%s.zip", goos, goarch)
	}
	return fmt.Sprintf("pb_%s_%s.tar.gz", goos, goarch)
}

func normalizeSemver(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

func safeVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}

func compareSemver(a string, b string) int {
	parsedA, okA := parseSemver(a)
	parsedB, okB := parseSemver(b)
	if !okA || !okB {
		return 0
	}
	for i := range parsedA {
		switch {
		case parsedA[i] > parsedB[i]:
			return 1
		case parsedA[i] < parsedB[i]:
			return -1
		}
	}
	return 0
}

func parseSemver(v string) ([3]int, bool) {
	var parsed [3]int
	v = strings.TrimPrefix(normalizeSemver(v), "v")
	if v == "" {
		return parsed, false
	}
	if idx := strings.IndexRune(v, '-'); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return parsed, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return parsed, false
		}
		parsed[i] = n
	}
	return parsed, true
}
