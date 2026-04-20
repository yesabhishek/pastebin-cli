package model

import "time"

const (
	StateVersion  = 1
	ConfigVersion = 2
	IndexVersion  = 1
)

const (
	UpgradePolicyPrompt = "prompt"
	UpgradePolicyAuto   = "auto"
	UpgradePolicyManual = "manual"
)

const (
	PendingNone     = ""
	PendingUpsert   = "upsert"
	PendingDelete   = "delete"
	PendingConflict = "conflict"
)

type Config struct {
	Version          int       `json:"version"`
	Owner            string    `json:"owner"`
	Repo             string    `json:"repo"`
	Login            string    `json:"login"`
	DeviceID         string    `json:"device_id"`
	UpgradePolicy    string    `json:"upgrade_policy,omitempty"`
	IgnoredRelease   string    `json:"ignored_release,omitempty"`
	LastReleaseCheck time.Time `json:"last_release_check,omitempty"`
}

type TrackedFile struct {
	Path           string    `json:"path"`
	Deleted        bool      `json:"deleted"`
	PendingOp      string    `json:"pending_op,omitempty"`
	LocalRevision  string    `json:"local_revision,omitempty"`
	BaseRevision   string    `json:"base_revision,omitempty"`
	RemoteRevision string    `json:"remote_revision,omitempty"`
	Checksum       string    `json:"checksum,omitempty"`
	RemoteChecksum string    `json:"remote_checksum,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	ConflictPath   string    `json:"conflict_path,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
}

type State struct {
	Version int                     `json:"version"`
	Files   map[string]*TrackedFile `json:"files"`
}

type JournalEntry struct {
	Path      string    `json:"path"`
	Operation string    `json:"operation"`
	Reason    string    `json:"reason,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Attempts  int       `json:"attempts"`
	Error     string    `json:"error,omitempty"`
}

type Journal struct {
	Version int                      `json:"version"`
	Entries map[string]*JournalEntry `json:"entries"`
}

type RemoteFile struct {
	Path      string    `json:"path"`
	Revision  string    `json:"revision"`
	Checksum  string    `json:"checksum"`
	Deleted   bool      `json:"deleted"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RemoteIndex struct {
	Version int                    `json:"version"`
	Files   map[string]*RemoteFile `json:"files"`
}

type SaveOutcome struct {
	Path         string
	ConflictPath string
	RemoteSaved  bool
	Message      string
}

type VersionEntry struct {
	ID        string    `json:"id"`
	CommitSHA string    `json:"commit_sha"`
	Path      string    `json:"path"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason"`
}

type SyncResult struct {
	Pulled    []string `json:"pulled"`
	Pushed    []string `json:"pushed"`
	Deleted   []string `json:"deleted"`
	Conflicts []string `json:"conflicts"`
}

type StatusReport struct {
	Login         string         `json:"login"`
	Repo          string         `json:"repo"`
	TotalFiles    int            `json:"total_files"`
	PendingWrites []string       `json:"pending_writes"`
	PendingDelete []string       `json:"pending_delete"`
	Conflicts     []string       `json:"conflicts"`
	Files         []*TrackedFile `json:"files"`
}
