package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
)

type Manager struct {
	paths config.Paths
	mu    sync.Mutex
}

func New(paths config.Paths) *Manager {
	return &Manager{paths: paths}
}

func (m *Manager) LoadState() (*model.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadStateUnlocked()
}

func (m *Manager) SaveState(state *model.State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveStateUnlocked(state)
}

func (m *Manager) LoadJournal() (*model.Journal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadJournalUnlocked()
}

func (m *Manager) SaveJournal(journal *model.Journal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveJournalUnlocked(journal)
}

func (m *Manager) UpsertJournalEntry(entry *model.JournalEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	journal, err := m.loadJournalUnlocked()
	if err != nil {
		return err
	}
	journal.Entries[entry.Path] = entry
	return m.saveJournalUnlocked(journal)
}

func (m *Manager) DeleteJournalEntry(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	journal, err := m.loadJournalUnlocked()
	if err != nil {
		return err
	}
	delete(journal.Entries, path)
	return m.saveJournalUnlocked(journal)
}

func (m *Manager) SaveContent(path string, content []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target := m.contentPath(path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", errs.Wrap(errs.CodeLocalCorruption, "create cache dir", err)
	}
	if err := atomicWrite(target, content, 0o600); err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func (m *Manager) LoadContent(path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.contentPath(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.Wrap(errs.CodeNotFound, "content not found in cache", err)
		}
		return nil, errs.Wrap(errs.CodeLocalCorruption, "read cached content", err)
	}
	return data, nil
}

func (m *Manager) DeleteContent(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := os.Remove(m.contentPath(path))
	if err != nil && !os.IsNotExist(err) {
		return errs.Wrap(errs.CodeLocalCorruption, "delete cached content", err)
	}
	return nil
}

func (m *Manager) SaveRecovery(sessionID, path string, content []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := sanitizeFilename(sessionID+"-"+path) + ".recovery"
	target := filepath.Join(m.paths.RecoveryDir, name)
	return atomicWrite(target, content, 0o600)
}

func (m *Manager) RemoveRecovery(sessionID, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := sanitizeFilename(sessionID+"-"+path) + ".recovery"
	err := os.Remove(filepath.Join(m.paths.RecoveryDir, name))
	if err != nil && !os.IsNotExist(err) {
		return errs.Wrap(errs.CodeLocalCorruption, "remove recovery file", err)
	}
	return nil
}

func (m *Manager) contentPath(path string) string {
	ext := filepath.Ext(path)
	name := sanitizeFilename(path)
	if ext == "" {
		ext = ".txt"
	}
	return filepath.Join(m.paths.CacheDir, name+ext)
}

func sanitizeFilename(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:12])
}

func (m *Manager) loadStateUnlocked() (*model.State, error) {
	data, err := os.ReadFile(m.paths.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &model.State{Version: model.StateVersion, Files: map[string]*model.TrackedFile{}}, nil
		}
		return nil, errs.Wrap(errs.CodeLocalCorruption, "read local index", err)
	}
	var state model.State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, errs.Wrap(errs.CodeLocalCorruption, "parse local index", err)
	}
	if state.Files == nil {
		state.Files = map[string]*model.TrackedFile{}
	}
	return &state, nil
}

func (m *Manager) saveStateUnlocked(state *model.State) error {
	state.Version = model.StateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "encode local index", err)
	}
	return atomicWrite(m.paths.StatePath, data, 0o600)
}

func (m *Manager) loadJournalUnlocked() (*model.Journal, error) {
	data, err := os.ReadFile(m.paths.JournalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &model.Journal{Version: model.StateVersion, Entries: map[string]*model.JournalEntry{}}, nil
		}
		return nil, errs.Wrap(errs.CodeLocalCorruption, "read journal", err)
	}
	var journal model.Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, errs.Wrap(errs.CodeLocalCorruption, "parse journal", err)
	}
	if journal.Entries == nil {
		journal.Entries = map[string]*model.JournalEntry{}
	}
	return &journal, nil
}

func (m *Manager) saveJournalUnlocked(journal *model.Journal) error {
	journal.Version = model.StateVersion
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "encode journal", err)
	}
	return atomicWrite(m.paths.JournalPath, data, 0o600)
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "create parent directory", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, fmt.Sprintf("write %s", path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, fmt.Sprintf("persist %s", path), err)
	}
	return nil
}

func ValidatePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errs.Wrap(errs.CodeUsage, "path is required", nil)
	}
	if strings.HasPrefix(path, "/") {
		return errs.Wrap(errs.CodeUsage, "absolute paths are not allowed", nil)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == "" {
		return errs.Wrap(errs.CodeUsage, "path is required", nil)
	}
	if strings.HasPrefix(clean, "../") || clean == ".." {
		return errs.Wrap(errs.CodeUsage, "path traversal is not allowed", nil)
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errs.Wrap(errs.CodeUsage, "invalid path segment", nil)
		}
		nameOnly := strings.TrimSuffix(segment, filepath.Ext(segment))
		upper := strings.ToUpper(nameOnly)
		switch upper {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "LPT1", "LPT2", "LPT3":
			return errs.Wrap(errs.CodeUsage, "reserved path name is not allowed", nil)
		}
		if strings.ContainsAny(segment, `<>:"\|?*`) {
			return errs.Wrap(errs.CodeUsage, "path contains invalid characters", nil)
		}
	}
	return nil
}
