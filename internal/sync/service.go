package sync

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/cache"
	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/store"
)

type Service struct {
	cache *cache.Manager
	store store.RemoteStore
	cfg   *model.Config
	paths config.Paths
	mu    sync.Mutex
}

func NewService(paths config.Paths, cfg *model.Config, cacheMgr *cache.Manager, remote store.RemoteStore) *Service {
	return &Service{
		cache: cacheMgr,
		store: remote,
		cfg:   cfg,
		paths: paths,
	}
}

func (s *Service) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.EnsureRepo(ctx); err != nil {
		return err
	}
	state, err := s.cache.LoadState()
	if err != nil {
		return err
	}
	if err := s.cache.SaveState(state); err != nil {
		return err
	}
	journal, err := s.cache.LoadJournal()
	if err != nil {
		return err
	}
	return s.cache.SaveJournal(journal)
}

func (s *Service) SaveContent(ctx context.Context, filePath string, content []byte) (*model.SaveOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := cache.ValidatePath(filePath); err != nil {
		return nil, err
	}
	state, err := s.cache.LoadState()
	if err != nil {
		return nil, err
	}
	tracked := state.Files[filePath]
	if tracked == nil {
		tracked = &model.TrackedFile{Path: filePath}
		state.Files[filePath] = tracked
	}
	checksum, err := s.cache.SaveContent(filePath, content)
	if err != nil {
		return nil, err
	}
	localRevision := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	tracked.Checksum = checksum
	tracked.LocalRevision = localRevision
	tracked.UpdatedAt = time.Now().UTC()
	tracked.PendingOp = model.PendingUpsert
	tracked.Deleted = false
	tracked.LastError = ""
	if err := s.cache.SaveState(state); err != nil {
		return nil, err
	}
	if err := s.cache.UpsertJournalEntry(&model.JournalEntry{
		Path:      filePath,
		Operation: model.PendingUpsert,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		return nil, err
	}

	snapshot, err := s.store.FetchIndex(ctx)
	if err != nil {
		return &model.SaveOutcome{Path: filePath, Message: "saved locally; remote sync pending"}, nil
	}
	outcome, err := s.applyUpsert(ctx, state, snapshot, tracked, filePath, content)
	if err != nil {
		tracked.LastError = err.Error()
		_ = s.cache.SaveState(state)
		return &model.SaveOutcome{Path: filePath, Message: "saved locally; remote sync pending"}, nil
	}
	return outcome, nil
}

func (s *Service) ReadContent(ctx context.Context, filePath string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := cache.ValidatePath(filePath); err != nil {
		return nil, err
	}
	state, err := s.cache.LoadState()
	if err != nil {
		return nil, err
	}
	if tracked, ok := state.Files[filePath]; ok && !tracked.Deleted {
		data, err := s.cache.LoadContent(filePath)
		if err == nil {
			return data, nil
		}
	}
	data, sha, err := s.store.FetchFile(ctx, filePath)
	if err != nil {
		return nil, err
	}
	checksum, err := s.cache.SaveContent(filePath, data)
	if err != nil {
		return nil, err
	}
	state.Files[filePath] = &model.TrackedFile{
		Path:           filePath,
		BaseRevision:   sha,
		RemoteRevision: sha,
		Checksum:       checksum,
		RemoteChecksum: checksum,
		UpdatedAt:      time.Now().UTC(),
	}
	if err := s.cache.SaveState(state); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Service) DeletePath(ctx context.Context, filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := cache.ValidatePath(filePath); err != nil {
		return err
	}
	state, err := s.cache.LoadState()
	if err != nil {
		return err
	}
	tracked := state.Files[filePath]
	if tracked == nil {
		tracked = &model.TrackedFile{Path: filePath}
		state.Files[filePath] = tracked
	}
	tracked.PendingOp = model.PendingDelete
	tracked.Deleted = true
	tracked.UpdatedAt = time.Now().UTC()
	if err := s.cache.SaveState(state); err != nil {
		return err
	}
	return s.cache.UpsertJournalEntry(&model.JournalEntry{
		Path:      filePath,
		Operation: model.PendingDelete,
		Timestamp: time.Now().UTC(),
	})
}

func (s *Service) List(prefix string) ([]*model.TrackedFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.cache.LoadState()
	if err != nil {
		return nil, err
	}
	items := make([]*model.TrackedFile, 0, len(state.Files))
	for _, item := range state.Files {
		if item.Deleted {
			continue
		}
		if prefix != "" && !strings.HasPrefix(item.Path, prefix) {
			continue
		}
		copyItem := *item
		items = append(items, &copyItem)
	}
	return items, nil
}

func (s *Service) Status() (*model.StatusReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.cache.LoadState()
	if err != nil {
		return nil, err
	}
	report := &model.StatusReport{
		Login:     s.cfg.Login,
		Repo:      path.Join(s.cfg.Owner, s.cfg.Repo),
		Files:     make([]*model.TrackedFile, 0, len(state.Files)),
		Conflicts: []string{},
	}
	for _, file := range state.Files {
		copyFile := *file
		report.Files = append(report.Files, &copyFile)
		if file.Deleted && file.PendingOp == model.PendingDelete {
			report.PendingDelete = append(report.PendingDelete, file.Path)
		}
		if file.PendingOp == model.PendingUpsert {
			report.PendingWrites = append(report.PendingWrites, file.Path)
		}
		if file.PendingOp == model.PendingConflict || file.ConflictPath != "" {
			report.Conflicts = append(report.Conflicts, file.Path)
		}
		if !file.Deleted {
			report.TotalFiles++
		}
	}
	return report, nil
}

func (s *Service) Sync(ctx context.Context) (*model.SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.cache.LoadState()
	if err != nil {
		return nil, err
	}
	snapshot, err := s.store.FetchIndex(ctx)
	if err != nil {
		return nil, err
	}
	result := &model.SyncResult{}
	seen := map[string]bool{}

	for remotePath, remoteFile := range snapshot.Index.Files {
		seen[remotePath] = true
		local := state.Files[remotePath]
		if remoteFile.Deleted {
			if local != nil && local.PendingOp == model.PendingUpsert {
				conflictPath, cerr := s.createConflictCopy(state, remotePath)
				if cerr != nil {
					return nil, cerr
				}
				result.Conflicts = append(result.Conflicts, conflictPath)
			}
			if local != nil && local.PendingOp == model.PendingNone {
				local.Deleted = true
				_ = s.cache.DeleteContent(remotePath)
				result.Deleted = append(result.Deleted, remotePath)
			}
			continue
		}
		if local == nil || (local.PendingOp == model.PendingNone && local.RemoteRevision != remoteFile.Revision) {
			content, sha, err := s.store.FetchFile(ctx, remotePath)
			if err != nil {
				return nil, err
			}
			checksum, err := s.cache.SaveContent(remotePath, content)
			if err != nil {
				return nil, err
			}
			state.Files[remotePath] = &model.TrackedFile{
				Path:           remotePath,
				BaseRevision:   sha,
				RemoteRevision: sha,
				Checksum:       checksum,
				RemoteChecksum: checksum,
				UpdatedAt:      remoteFile.UpdatedAt,
			}
			result.Pulled = append(result.Pulled, remotePath)
			continue
		}
		if local.PendingOp == model.PendingUpsert && local.BaseRevision != remoteFile.Revision {
			conflictPath, cerr := s.createConflictCopy(state, remotePath)
			if cerr != nil {
				return nil, cerr
			}
			result.Conflicts = append(result.Conflicts, conflictPath)
		}
	}

	for filePath, local := range state.Files {
		if local.PendingOp == model.PendingNone || local.PendingOp == model.PendingConflict {
			continue
		}
		if local.PendingOp == model.PendingDelete {
			var remoteRevision string
			if remote, ok := snapshot.Index.Files[filePath]; ok {
				remoteRevision = remote.Revision
			}
			if remoteRevision != "" && local.BaseRevision != "" && remoteRevision != local.BaseRevision {
				local.PendingOp = model.PendingConflict
				local.LastError = "remote changed while local delete was pending"
				result.Conflicts = append(result.Conflicts, filePath)
				continue
			}
			if err := s.store.DeleteFile(ctx, filePath, remoteRevision); err != nil && !errs.IsCode(err, errs.CodeNotFound) {
				local.LastError = err.Error()
				continue
			}
			snapshot.Index.Files[filePath] = &model.RemoteFile{
				Path:      filePath,
				Deleted:   true,
				UpdatedAt: time.Now().UTC(),
			}
			local.PendingOp = model.PendingNone
			local.RemoteRevision = ""
			local.BaseRevision = ""
			_ = s.cache.DeleteContent(filePath)
			_ = s.cache.DeleteJournalEntry(filePath)
			result.Deleted = append(result.Deleted, filePath)
			continue
		}
		content, err := s.cache.LoadContent(filePath)
		if err != nil {
			local.LastError = err.Error()
			continue
		}
		if !seen[filePath] && local.BaseRevision == "" {
			remote, err := s.store.UpsertFile(ctx, filePath, content, "")
			if err != nil {
				local.LastError = err.Error()
				continue
			}
			s.markSynced(local, remote)
			snapshot.Index.Files[filePath] = remote
			_ = s.cache.DeleteJournalEntry(filePath)
			result.Pushed = append(result.Pushed, filePath)
			continue
		}
		remote := snapshot.Index.Files[filePath]
		if remote != nil && local.BaseRevision != "" && remote.Revision != local.BaseRevision {
			conflictPath, cerr := s.createConflictCopy(state, filePath)
			if cerr != nil {
				return nil, cerr
			}
			result.Conflicts = append(result.Conflicts, conflictPath)
			continue
		}
		remoteRecord, err := s.store.UpsertFile(ctx, filePath, content, local.BaseRevision)
		if err != nil {
			local.LastError = err.Error()
			continue
		}
		s.markSynced(local, remoteRecord)
		snapshot.Index.Files[filePath] = remoteRecord
		_ = s.cache.DeleteJournalEntry(filePath)
		result.Pushed = append(result.Pushed, filePath)
	}

	if _, err := s.store.SaveIndex(ctx, snapshot.Index, snapshot.IndexSHA); err != nil {
		return nil, err
	}
	if err := s.cache.SaveState(state); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) applyUpsert(ctx context.Context, state *model.State, snapshot *store.RemoteSnapshot, tracked *model.TrackedFile, filePath string, content []byte) (*model.SaveOutcome, error) {
	remoteRecord := snapshot.Index.Files[filePath]
	if remoteRecord != nil && tracked.BaseRevision != "" && remoteRecord.Revision != tracked.BaseRevision {
		conflictPath, err := s.createConflictCopy(state, filePath)
		if err != nil {
			return nil, err
		}
		if err := s.cache.SaveState(state); err != nil {
			return nil, err
		}
		if _, err := s.store.SaveIndex(ctx, snapshot.Index, snapshot.IndexSHA); err != nil && !errs.IsCode(err, errs.CodeNotFound) {
			return nil, err
		}
		return &model.SaveOutcome{
			Path:         filePath,
			ConflictPath: conflictPath,
			Message:      "remote changed; saving future edits to conflict copy",
		}, nil
	}
	record, err := s.store.UpsertFile(ctx, filePath, content, tracked.BaseRevision)
	if err != nil {
		return nil, err
	}
	snapshot.Index.Files[filePath] = record
	newSHA, err := s.store.SaveIndex(ctx, snapshot.Index, snapshot.IndexSHA)
	if err != nil {
		return nil, err
	}
	snapshot.IndexSHA = newSHA
	s.markSynced(tracked, record)
	if err := s.cache.DeleteJournalEntry(filePath); err != nil {
		return nil, err
	}
	if err := s.cache.SaveState(state); err != nil {
		return nil, err
	}
	return &model.SaveOutcome{
		Path:        filePath,
		RemoteSaved: true,
		Message:     "saved locally and synced to GitHub",
	}, nil
}

func (s *Service) createConflictCopy(state *model.State, originalPath string) (string, error) {
	originalContent, err := s.cache.LoadContent(originalPath)
	if err != nil {
		return "", err
	}
	conflictPath := conflictName(originalPath, s.cfg.DeviceID, time.Now().UTC())
	checksum, err := s.cache.SaveContent(conflictPath, originalContent)
	if err != nil {
		return "", err
	}
	state.Files[conflictPath] = &model.TrackedFile{
		Path:          conflictPath,
		Checksum:      checksum,
		LocalRevision: fmt.Sprintf("%d", time.Now().UTC().UnixNano()),
		PendingOp:     model.PendingUpsert,
		UpdatedAt:     time.Now().UTC(),
	}
	if entry := state.Files[originalPath]; entry != nil {
		entry.PendingOp = model.PendingConflict
		entry.ConflictPath = conflictPath
		entry.LastError = "remote changed while local edits were pending"
	}
	if err := s.cache.UpsertJournalEntry(&model.JournalEntry{
		Path:      conflictPath,
		Operation: model.PendingUpsert,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		return "", err
	}
	return conflictPath, nil
}

func conflictName(originalPath, deviceID string, now time.Time) string {
	ext := path.Ext(originalPath)
	base := strings.TrimSuffix(originalPath, ext)
	stamp := now.Format("20060102-150405")
	if ext == "" {
		return fmt.Sprintf("%s.conflict-%s-%s.txt", base, deviceID, stamp)
	}
	return fmt.Sprintf("%s.conflict-%s-%s%s", base, deviceID, stamp, ext)
}

func (s *Service) markSynced(tracked *model.TrackedFile, remote *model.RemoteFile) {
	tracked.PendingOp = model.PendingNone
	tracked.Deleted = remote.Deleted
	tracked.RemoteRevision = remote.Revision
	tracked.BaseRevision = remote.Revision
	tracked.RemoteChecksum = remote.Checksum
	tracked.Checksum = remote.Checksum
	tracked.UpdatedAt = remote.UpdatedAt
	tracked.ConflictPath = ""
	tracked.LastError = ""
}
