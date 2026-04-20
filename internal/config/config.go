package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
)

const defaultRepo = "pastebin-cli-store"

type Paths struct {
	RootDir     string
	ConfigPath  string
	StatePath   string
	JournalPath string
	CacheDir    string
	RecoveryDir string
}

func NewPaths() (Paths, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, errs.Wrap(errs.CodeLocalCorruption, "resolve user config dir", err)
	}
	root = filepath.Join(root, "pb")
	return Paths{
		RootDir:     root,
		ConfigPath:  filepath.Join(root, "config.json"),
		StatePath:   filepath.Join(root, "state", "index.json"),
		JournalPath: filepath.Join(root, "state", "journal.json"),
		CacheDir:    filepath.Join(root, "cache", "files"),
		RecoveryDir: filepath.Join(root, "state", "recovery"),
	}, nil
}

func EnsureLayout(paths Paths) error {
	for _, dir := range []string{
		paths.RootDir,
		filepath.Dir(paths.StatePath),
		filepath.Dir(paths.JournalPath),
		paths.CacheDir,
		paths.RecoveryDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return errs.Wrap(errs.CodeLocalCorruption, fmt.Sprintf("create %s", dir), err)
		}
	}
	return nil
}

func Load(paths Paths) (*model.Config, error) {
	data, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errs.Wrap(errs.CodeLocalCorruption, "read config", err)
	}
	var cfg model.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, errs.Wrap(errs.CodeLocalCorruption, "parse config", err)
	}
	if cfg.UpgradePolicy == "" {
		cfg.UpgradePolicy = model.UpgradePolicyPrompt
	}
	return &cfg, nil
}

func Save(paths Paths, cfg *model.Config) error {
	cfg.Version = model.ConfigVersion
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "encode config", err)
	}
	tmp := paths.ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "write config", err)
	}
	if err := os.Rename(tmp, paths.ConfigPath); err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "persist config", err)
	}
	return nil
}

func DefaultRepo() string {
	return defaultRepo
}

func NewDeviceID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", errs.Wrap(errs.CodeLocalCorruption, "create device id", err)
	}
	return hex.EncodeToString(buf), nil
}
