// Package keystore keeps unlocked vault keys on this device so the passphrase
// is only needed once per machine. The OS credential store (macOS Keychain,
// Windows Credential Manager, Secret Service on Linux) is preferred; a 0600
// file inside pb's private config directory is used when none is available
// or when PB_KEYSTORE=file is set.
package keystore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "pb-cli vault"

var ErrNotFound = errors.New("no key stored on this device")

type Store interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
	Kind() string
}

// New returns the best available store for this machine.
func New(rootDir string) Store {
	file := &FileStore{Dir: filepath.Join(rootDir, "keys")}
	if strings.EqualFold(os.Getenv("PB_KEYSTORE"), "file") {
		return file
	}
	return &chain{primary: osStore{}, fallback: file}
}

// Account builds the per-account, per-repo key name.
func Account(owner, repo string) string {
	return strings.ToLower(owner) + "/" + strings.ToLower(repo)
}

type osStore struct{}

func (osStore) Get(account string) (string, error) {
	v, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (osStore) Set(account, secret string) error { return keyring.Set(service, account, secret) }

func (osStore) Delete(account string) error {
	err := keyring.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func (osStore) Kind() string { return "os-keychain" }

// chain prefers the OS store and falls back to a private file.
type chain struct {
	primary  Store
	fallback Store
	used     string
}

func (c *chain) Get(account string) (string, error) {
	v, err := c.primary.Get(account)
	if err == nil {
		c.used = c.primary.Kind()
		return v, nil
	}
	fv, ferr := c.fallback.Get(account)
	if ferr == nil {
		c.used = c.fallback.Kind()
		return fv, nil
	}
	if errors.Is(err, ErrNotFound) || errors.Is(ferr, ErrNotFound) {
		return "", ErrNotFound
	}
	return "", err
}

func (c *chain) Set(account, secret string) error {
	if err := c.primary.Set(account, secret); err == nil {
		c.used = c.primary.Kind()
		_ = c.fallback.Delete(account)
		return nil
	}
	c.used = c.fallback.Kind()
	return c.fallback.Set(account, secret)
}

func (c *chain) Delete(account string) error {
	err1 := c.primary.Delete(account)
	err2 := c.fallback.Delete(account)
	if err2 != nil {
		return err2
	}
	_ = err1 // the OS store may be unavailable; the file copy is what matters then
	return nil
}

func (c *chain) Kind() string {
	if c.used != "" {
		return c.used
	}
	return c.primary.Kind()
}

// FileStore keeps keys in 0600 files inside a 0700 directory.
type FileStore struct {
	Dir string
}

func (f *FileStore) path(account string) string {
	sum := sha256.Sum256([]byte(account))
	return filepath.Join(f.Dir, hex.EncodeToString(sum[:16])+".key")
}

func (f *FileStore) Get(account string) (string, error) {
	data, err := os.ReadFile(f.path(account))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (f *FileStore) Set(account, secret string) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(f.Dir, 0o700)
	target := f.path(account)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(secret), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func (f *FileStore) Delete(account string) error {
	err := os.Remove(f.path(account))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (f *FileStore) Kind() string { return "file" }

// Memory is an in-memory store for tests.
type Memory struct{ M map[string]string }

func (m *Memory) Get(a string) (string, error) {
	if v, ok := m.M[a]; ok {
		return v, nil
	}
	return "", ErrNotFound
}
func (m *Memory) Set(a, s string) error {
	if m.M == nil {
		m.M = map[string]string{}
	}
	m.M[a] = s
	return nil
}
func (m *Memory) Delete(a string) error { delete(m.M, a); return nil }
func (m *Memory) Kind() string          { return "memory" }
