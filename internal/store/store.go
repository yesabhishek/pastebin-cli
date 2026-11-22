package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/vault"
)

// Remote repository layout.
const (
	VaultPath       = "meta/vault.json"
	EncIndexPath    = "meta/index.enc"
	LegacyIndexPath = "meta/index.json"
	objectsDir      = "objects/"
	legacyFilesDir  = "files/"

	// LegacyMarker replaces meta/index.json after encryption. It is not valid
	// JSON on purpose, so older pb releases fail closed instead of uploading
	// plaintext into an encrypted repository.
	LegacyMarker = "pb-encrypted-repository: this repository is end-to-end encrypted. Upgrade pb and run `pb unlock`.\n"

	DefaultHistoryLimit = 50
)

type RemoteSnapshot struct {
	Index    *model.RemoteIndex
	IndexSHA string
}

type RemoteStore interface {
	EnsureRepo(context.Context) error
	FetchIndex(context.Context) (*RemoteSnapshot, error)
	FetchFile(context.Context, string) ([]byte, string, error)
	FetchFileAtRevision(context.Context, string, string) ([]byte, error)
	ListVersions(context.Context, string) ([]model.VersionEntry, error)
	UpsertFile(context.Context, string, []byte, string, string) (*model.RemoteFile, error)
	DeleteFile(context.Context, string, string, string) error
	SaveIndex(context.Context, *model.RemoteIndex, string) (string, error)
}

// KeyProvider returns the unlocked vault keys, prompting or failing with
// ErrLocked when they are unavailable.
type KeyProvider func(ctx context.Context) (*vault.Keys, error)

// EncryptedStore implements RemoteStore on top of any Backend. Nothing is
// written to the backend in plaintext: note bodies, note paths and the index
// are all sealed with the vault keys.
type EncryptedStore struct {
	backend      Backend
	keys         KeyProvider
	HistoryLimit int
}

func NewEncrypted(backend Backend, keys KeyProvider) *EncryptedStore {
	return &EncryptedStore{backend: backend, keys: keys, HistoryLimit: DefaultHistoryLimit}
}

func (s *EncryptedStore) Backend() Backend { return s.backend }

func (s *EncryptedStore) EnsureRepo(ctx context.Context) error {
	_, err := s.backend.EnsureRepo(ctx)
	return err
}

func (s *EncryptedStore) FetchIndex(ctx context.Context) (*RemoteSnapshot, error) {
	keys, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	return FetchEncryptedIndex(ctx, s.backend, keys)
}

func (s *EncryptedStore) FetchFile(ctx context.Context, path string) ([]byte, string, error) {
	keys, err := s.keys(ctx)
	if err != nil {
		return nil, "", err
	}
	id := keys.ObjectID(path)
	blob, err := s.backend.Get(ctx, objectsDir+id, "")
	if err != nil {
		return nil, "", err
	}
	plain, err := keys.Open("obj", id, blob.Data)
	if err != nil {
		return nil, "", errs.Wrap(errs.CodeRemoteCorruption, "decrypt "+path, err)
	}
	return plain, blob.SHA, nil
}

func (s *EncryptedStore) FetchFileAtRevision(ctx context.Context, path, revision string) ([]byte, error) {
	keys, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	id := keys.ObjectID(path)
	blob, err := s.backend.Get(ctx, objectsDir+id, revision)
	if err != nil {
		return nil, err
	}
	plain, err := keys.Open("obj", id, blob.Data)
	if err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "decrypt historical version of "+path, err)
	}
	return plain, nil
}

func (s *EncryptedStore) ListVersions(ctx context.Context, path string) ([]model.VersionEntry, error) {
	keys, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	limit := s.HistoryLimit
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	commits, err := s.backend.History(ctx, objectsDir+keys.ObjectID(path), limit)
	if err != nil {
		return nil, err
	}
	versions := make([]model.VersionEntry, 0, len(commits))
	for _, c := range commits {
		versions = append(versions, model.VersionEntry{
			ID:        shortVersionID(c.SHA),
			CommitSHA: c.SHA,
			Path:      path,
			Timestamp: c.Time,
			Reason:    parseReason(c.Message),
		})
	}
	return versions, nil
}

func (s *EncryptedStore) UpsertFile(ctx context.Context, path string, content []byte, previousSHA, reason string) (*model.RemoteFile, error) {
	keys, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	id := keys.ObjectID(path)
	sealed, err := keys.Seal("obj", id, content)
	if err != nil {
		return nil, errs.Wrap(errs.CodeLocalCorruption, "encrypt "+path, err)
	}
	res, err := s.backend.Put(ctx, objectsDir+id, sealed, previousSHA, commitMessage(reason, id))
	if err != nil {
		return nil, err
	}
	return &model.RemoteFile{
		Path:      path,
		Revision:  res.BlobSHA,
		Checksum:  checksum(content),
		UpdatedAt: time.Now().UTC(),
		CommitSHA: res.CommitSHA,
	}, nil
}

func (s *EncryptedStore) DeleteFile(ctx context.Context, path, sha, reason string) error {
	keys, err := s.keys(ctx)
	if err != nil {
		return err
	}
	id := keys.ObjectID(path)
	return s.backend.Delete(ctx, objectsDir+id, sha, commitMessage(reason, id))
}

func (s *EncryptedStore) SaveIndex(ctx context.Context, index *model.RemoteIndex, previousSHA string) (string, error) {
	keys, err := s.keys(ctx)
	if err != nil {
		return "", err
	}
	return SaveEncryptedIndex(ctx, s.backend, keys, index, previousSHA)
}

// FetchEncryptedIndex reads and decrypts meta/index.enc.
func FetchEncryptedIndex(ctx context.Context, backend Backend, keys *vault.Keys) (*RemoteSnapshot, error) {
	blob, err := backend.Get(ctx, EncIndexPath, "")
	if err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			return &RemoteSnapshot{Index: emptyIndex()}, nil
		}
		return nil, err
	}
	plain, err := keys.Open("index", keys.VaultID, blob.Data)
	if err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "decrypt remote index (is the cached key for a different vault? run `pb unlock`)", err)
	}
	var index model.RemoteIndex
	if err := json.Unmarshal(plain, &index); err != nil {
		return nil, errs.Wrap(errs.CodeRemoteCorruption, "parse remote index", err)
	}
	if index.Files == nil {
		index.Files = map[string]*model.RemoteFile{}
	}
	return &RemoteSnapshot{Index: &index, IndexSHA: blob.SHA}, nil
}

// SaveEncryptedIndex seals and writes meta/index.enc.
func SaveEncryptedIndex(ctx context.Context, backend Backend, keys *vault.Keys, index *model.RemoteIndex, previousSHA string) (string, error) {
	index.Version = model.IndexVersion
	data, err := json.Marshal(index)
	if err != nil {
		return "", errs.Wrap(errs.CodeLocalCorruption, "encode remote index", err)
	}
	sealed, err := keys.Seal("index", keys.VaultID, data)
	if err != nil {
		return "", errs.Wrap(errs.CodeLocalCorruption, "encrypt remote index", err)
	}
	res, err := backend.Put(ctx, EncIndexPath, sealed, previousSHA, "pb: update index")
	if err != nil {
		return "", err
	}
	return res.BlobSHA, nil
}

// ReadVault fetches meta/vault.json. It returns (nil, "", nil) when absent.
func ReadVault(ctx context.Context, backend Backend) (*vault.File, string, error) {
	blob, err := backend.Get(ctx, VaultPath, "")
	if err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			return nil, "", nil
		}
		return nil, "", err
	}
	file, err := vault.Parse(blob.Data)
	if err != nil {
		return nil, "", errs.Wrap(errs.CodeRemoteCorruption, "read vault", err)
	}
	return file, blob.SHA, nil
}

// WriteVault stores meta/vault.json.
func WriteVault(ctx context.Context, backend Backend, file *vault.File, previousSHA string) error {
	data, err := file.Marshal()
	if err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "encode vault", err)
	}
	msg := "pb: create vault"
	if previousSHA != "" {
		msg = "pb: change passphrase"
	}
	_, err = backend.Put(ctx, VaultPath, data, previousSHA, msg)
	return err
}

// LegacyState reports whether plaintext data from older pb releases remains.
func LegacyState(ctx context.Context, backend Backend) (*model.RemoteIndex, string, error) {
	blob, err := backend.Get(ctx, LegacyIndexPath, "")
	if err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			return nil, "", nil
		}
		return nil, "", err
	}
	if strings.HasPrefix(string(blob.Data), "pb-encrypted-repository") {
		return nil, blob.SHA, nil
	}
	var index model.RemoteIndex
	if err := json.Unmarshal(blob.Data, &index); err != nil {
		return nil, blob.SHA, errs.Wrap(errs.CodeRemoteCorruption, "parse legacy index", err)
	}
	if index.Files == nil {
		index.Files = map[string]*model.RemoteFile{}
	}
	return &index, blob.SHA, nil
}

// MigrationReport summarises an encryption migration.
type MigrationReport struct {
	Migrated []string
	Skipped  []string
}

// MigrateLegacy encrypts every plaintext file left by older pb releases,
// merges it into the encrypted index, writes the fail-closed marker and then
// deletes the plaintext copies. It is safe to re-run after an interruption.
func MigrateLegacy(ctx context.Context, backend Backend, keys *vault.Keys, validPath func(string) error) (*MigrationReport, error) {
	report := &MigrationReport{}
	legacy, legacySHA, err := LegacyState(ctx, backend)
	if err != nil {
		return nil, err
	}
	snapshot, err := FetchEncryptedIndex(ctx, backend, keys)
	if err != nil {
		return nil, err
	}
	type plainFile struct {
		path string
		sha  string
	}
	toDelete := []plainFile{}
	if legacy != nil {
		for path, rf := range legacy.Files {
			if rf == nil || rf.Deleted {
				continue
			}
			if validPath != nil && validPath(path) != nil {
				report.Skipped = append(report.Skipped, path)
				continue
			}
			blob, err := backend.Get(ctx, legacyFilesDir+path, "")
			if err != nil {
				if errs.IsCode(err, errs.CodeNotFound) {
					report.Skipped = append(report.Skipped, path)
					continue
				}
				return nil, err
			}
			toDelete = append(toDelete, plainFile{path: path, sha: blob.SHA})
			if existing := snapshot.Index.Files[path]; existing != nil && !existing.Deleted {
				// Already migrated by a previous (interrupted) run.
				continue
			}
			id := keys.ObjectID(path)
			sealed, err := keys.Seal("obj", id, blob.Data)
			if err != nil {
				return nil, err
			}
			prev := ""
			if current, err := backend.Get(ctx, objectsDir+id, ""); err == nil {
				prev = current.SHA
			}
			res, err := backend.Put(ctx, objectsDir+id, sealed, prev, commitMessage("save", id))
			if err != nil {
				return nil, err
			}
			updated := rf.UpdatedAt
			if updated.IsZero() {
				updated = time.Now().UTC()
			}
			snapshot.Index.Files[path] = &model.RemoteFile{
				Path:      path,
				Revision:  res.BlobSHA,
				Checksum:  checksum(blob.Data),
				UpdatedAt: updated,
			}
			report.Migrated = append(report.Migrated, path)
		}
	}
	newSHA, err := SaveEncryptedIndex(ctx, backend, keys, snapshot.Index, snapshot.IndexSHA)
	if err != nil {
		return nil, err
	}
	_ = newSHA
	if legacy != nil || legacySHA == "" {
		if _, err := backend.Put(ctx, LegacyIndexPath, []byte(LegacyMarker), legacySHA, "pb: mark repository as encrypted"); err != nil {
			return nil, err
		}
	}
	for _, f := range toDelete {
		if err := backend.Delete(ctx, legacyFilesDir+f.path, f.sha, "pb: remove plaintext copy"); err != nil && !errs.IsCode(err, errs.CodeNotFound) {
			return nil, err
		}
	}
	return report, nil
}

func emptyIndex() *model.RemoteIndex {
	return &model.RemoteIndex{Version: model.IndexVersion, Files: map[string]*model.RemoteFile{}}
}

func commitMessage(reason, id string) string {
	switch reason {
	case "save", "sync", "restore", "delete":
	default:
		reason = "save"
	}
	short := id
	if len(short) > 10 {
		short = short[:10]
	}
	return "pb: " + reason + " " + short
}

func checksum(content []byte) string {
	return modelChecksum(content)
}

func shortVersionID(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func parseReason(message string) string {
	message = strings.TrimSpace(strings.ToLower(message))
	message = strings.TrimPrefix(message, "pb: ")
	fields := strings.Fields(message)
	if len(fields) == 0 {
		return "unknown"
	}
	switch fields[0] {
	case "save", "sync", "restore", "delete":
		return fields[0]
	default:
		return "unknown"
	}
}

// ErrLocked is returned when the vault keys are not available locally.
var ErrLocked = errors.New("vault is locked")
