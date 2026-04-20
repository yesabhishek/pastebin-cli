package ui

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/auth"
	"github.com/yesabhishek/pastebin-cli/internal/cache"
	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/editor"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/store"
	syncer "github.com/yesabhishek/pastebin-cli/internal/sync"
)

type App struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer
	paths  config.Paths
	auth   *auth.GitHubAuth
}

func NewApp(in io.Reader, out, errOut io.Writer) (*App, error) {
	paths, err := config.NewPaths()
	if err != nil {
		return nil, err
	}
	if err := config.EnsureLayout(paths); err != nil {
		return nil, err
	}
	return &App{
		in:     in,
		out:    out,
		errOut: errOut,
		paths:  paths,
		auth:   auth.New(),
	}, nil
}

func (a *App) Run(ctx context.Context, args []string) error {
	global := flag.NewFlagSet("pb", flag.ContinueOnError)
	global.SetOutput(a.errOut)
	repoOverride := global.String("repo", "", "override GitHub storage repo")
	jsonOut := global.Bool("json", false, "emit JSON output")
	if err := global.Parse(args); err != nil {
		return errs.Wrap(errs.CodeUsage, "parse flags", err)
	}
	rest := global.Args()
	if len(rest) == 0 {
		a.printHelp()
		return nil
	}
	command := rest[0]
	commandArgs := rest[1:]

	switch command {
	case "help", "-h", "--help":
		a.printHelp()
		return nil
	case "init":
		cfg, err := a.initConfig(ctx, *repoOverride)
		if err != nil {
			return err
		}
		service := a.service(cfg)
		if err := service.Init(ctx); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Initialized %s/%s for %s\n", cfg.Owner, cfg.Repo, cfg.Login)
		return nil
	case "new":
		if len(commandArgs) != 1 {
			return errs.Wrap(errs.CodeUsage, "usage: pb new <path>", nil)
		}
		return a.editPath(ctx, commandArgs[0], true)
	case "edit":
		if len(commandArgs) != 1 {
			return errs.Wrap(errs.CodeUsage, "usage: pb edit <path>", nil)
		}
		return a.editPath(ctx, commandArgs[0], false)
	case "read":
		if len(commandArgs) != 1 {
			return errs.Wrap(errs.CodeUsage, "usage: pb read <path>", nil)
		}
		return a.readPath(ctx, commandArgs[0], *jsonOut)
	case "versions":
		if len(commandArgs) != 1 {
			return errs.Wrap(errs.CodeUsage, "usage: pb versions <path>", nil)
		}
		return a.versionsPath(ctx, commandArgs[0], *jsonOut)
	case "show":
		if len(commandArgs) != 2 {
			return errs.Wrap(errs.CodeUsage, "usage: pb show <path> <version-id>", nil)
		}
		return a.showVersion(ctx, commandArgs[0], commandArgs[1], *jsonOut)
	case "restore":
		if len(commandArgs) != 2 {
			return errs.Wrap(errs.CodeUsage, "usage: pb restore <path> <version-id>", nil)
		}
		return a.restoreVersion(ctx, commandArgs[0], commandArgs[1])
	case "delete":
		return a.deletePath(ctx, commandArgs, *jsonOut)
	case "list":
		return a.listPaths(ctx, commandArgs, *jsonOut)
	case "sync":
		return a.sync(ctx, *jsonOut)
	case "status":
		return a.status(ctx, *jsonOut)
	case "logout":
		return a.logout()
	default:
		return errs.Wrap(errs.CodeUsage, "unknown command: "+command, nil)
	}
}

func (a *App) printHelp() {
	fmt.Fprint(a.out, strings.TrimSpace(`
pb is a GitHub-backed personal pastebin CLI.

Commands:
  pb init
  pb new <path>
  pb edit <path>
  pb read <path>
  pb versions <path>
  pb show <path> <version-id>
  pb restore <path> <version-id>
  pb delete <path> [--yes]
  pb list [prefix] [--refresh]
  pb sync
  pb status
  pb logout

Global flags:
  --repo <name>  override GitHub storage repo
  --json         emit JSON output for read, versions, show, list, and status
`))
	fmt.Fprintln(a.out)
}

func (a *App) initConfig(ctx context.Context, repoOverride string) (*model.Config, error) {
	info, err := a.auth.Info(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(a.paths)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		deviceID, err := config.NewDeviceID()
		if err != nil {
			return nil, err
		}
		cfg = &model.Config{
			Owner:    info.Login,
			Repo:     config.DefaultRepo(),
			Login:    info.Login,
			DeviceID: deviceID,
		}
	}
	if repoOverride != "" {
		cfg.Repo = repoOverride
	}
	cfg.Owner = info.Login
	cfg.Login = info.Login
	if cfg.DeviceID == "" {
		cfg.DeviceID, err = config.NewDeviceID()
		if err != nil {
			return nil, err
		}
	}
	if err := config.Save(a.paths, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (a *App) loadConfig() (*model.Config, error) {
	cfg, err := config.Load(a.paths)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errs.Wrap(errs.CodeUsage, "run `pb init` first", nil)
	}
	return cfg, nil
}

func (a *App) service(cfg *model.Config) *syncer.Service {
	cacheMgr := cache.New(a.paths)
	return syncer.NewService(a.paths, cfg, cacheMgr, store.NewGitHub(cfg.Owner, cfg.Repo))
}

func (a *App) editPath(ctx context.Context, filePath string, isNew bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	cacheMgr := cache.New(a.paths)
	svc := syncer.NewService(a.paths, cfg, cacheMgr, store.NewGitHub(cfg.Owner, cfg.Repo))
	initial, recovered, status, err := a.loadEditorInitial(ctx, svc, cacheMgr, cfg.DeviceID, filePath, isNew)
	if err != nil {
		return err
	}
	saver := &editorSaver{
		sessionID: cfg.DeviceID,
		filePath:  filePath,
		service:   svc,
		cache:     cacheMgr,
	}
	model := editor.New("pb editor", filePath, string(initial), saver, status, recovered)
	return editor.Run(a.in, a.out, model)
}

func (a *App) readPath(ctx context.Context, filePath string, jsonOut bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	content, err := a.service(cfg).ReadContent(ctx, filePath)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, map[string]string{
			"path":    filePath,
			"content": string(content),
		})
	}
	_, err = fmt.Fprint(a.out, string(content))
	return err
}

func (a *App) versionsPath(ctx context.Context, filePath string, jsonOut bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	versions, err := a.service(cfg).ListVersions(ctx, filePath)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, versions)
	}
	for _, version := range versions {
		fmt.Fprintf(a.out, "%s\t%s\t%s\n",
			version.ID,
			version.Timestamp.In(time.Local).Format("2006-01-02 15:04:05 MST"),
			version.Reason,
		)
	}
	return nil
}

func (a *App) showVersion(ctx context.Context, filePath string, versionID string, jsonOut bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	version, content, err := a.service(cfg).ShowVersion(ctx, filePath, versionID)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, map[string]any{
			"path":       filePath,
			"version_id": version.ID,
			"commit_sha": version.CommitSHA,
			"timestamp":  version.Timestamp,
			"reason":     version.Reason,
			"content":    string(content),
		})
	}
	_, err = fmt.Fprint(a.out, string(content))
	return err
}

func (a *App) restoreVersion(ctx context.Context, filePath string, versionID string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	version, outcome, err := a.service(cfg).RestoreVersion(ctx, filePath, versionID)
	if err != nil {
		return err
	}
	msg := outcome.Message
	if msg == "" {
		msg = "restored and synced to GitHub"
	}
	fmt.Fprintf(a.out, "Restored %s from %s (%s). %s\n",
		filePath,
		version.ID,
		version.Timestamp.In(time.Local).Format("2006-01-02 15:04:05 MST"),
		msg,
	)
	return nil
}

func (a *App) deletePath(ctx context.Context, args []string, jsonOut bool) error {
	deleteFS := flag.NewFlagSet("delete", flag.ContinueOnError)
	deleteFS.SetOutput(a.errOut)
	yes := deleteFS.Bool("yes", false, "skip confirmation")
	if err := deleteFS.Parse(args); err != nil {
		return errs.Wrap(errs.CodeUsage, "parse delete flags", err)
	}
	rest := deleteFS.Args()
	if len(rest) != 1 {
		return errs.Wrap(errs.CodeUsage, "usage: pb delete <path> [--yes]", nil)
	}
	filePath := rest[0]
	if !*yes {
		fmt.Fprintf(a.out, "Delete %s? [y/N]: ", filePath)
		var answer string
		if _, err := fmt.Fscanln(a.in, &answer); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(a.out, "aborted")
			return nil
		}
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if err := a.service(cfg).DeletePath(ctx, filePath); err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, map[string]string{"deleted": filePath, "status": "pending sync"})
	}
	fmt.Fprintf(a.out, "Marked %s for deletion. Run `pb sync` to push the tombstone.\n", filePath)
	return nil
}

func (a *App) listPaths(ctx context.Context, args []string, jsonOut bool) error {
	listFS := flag.NewFlagSet("list", flag.ContinueOnError)
	listFS.SetOutput(a.errOut)
	refresh := listFS.Bool("refresh", false, "refresh from GitHub before listing")
	if err := listFS.Parse(args); err != nil {
		return errs.Wrap(errs.CodeUsage, "parse list flags", err)
	}
	rest := listFS.Args()
	prefix := ""
	if len(rest) > 1 {
		return errs.Wrap(errs.CodeUsage, "usage: pb list [prefix] [--refresh]", nil)
	}
	if len(rest) == 1 {
		prefix = rest[0]
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	svc := a.service(cfg)
	if *refresh {
		if _, err := svc.Sync(ctx); err != nil {
			return err
		}
	}
	items, err := svc.List(prefix)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, items)
	}
	for _, item := range items {
		fmt.Fprintln(a.out, item.Path)
	}
	return nil
}

func (a *App) sync(ctx context.Context, jsonOut bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	result, err := a.service(cfg).Sync(ctx)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, result)
	}
	fmt.Fprintf(a.out, "Pulled: %d, Pushed: %d, Deleted: %d, Conflicts: %d\n", len(result.Pulled), len(result.Pushed), len(result.Deleted), len(result.Conflicts))
	for _, conflict := range result.Conflicts {
		fmt.Fprintf(a.out, "Conflict: %s\n", conflict)
	}
	return nil
}

func (a *App) status(ctx context.Context, jsonOut bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	report, err := a.service(cfg).Status()
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, report)
	}
	fmt.Fprintf(a.out, "Login: %s\nRepo: %s\nFiles: %d\nPending writes: %d\nPending delete: %d\nConflicts: %d\n",
		report.Login, report.Repo, report.TotalFiles, len(report.PendingWrites), len(report.PendingDelete), len(report.Conflicts))
	return nil
}

func (a *App) logout() error {
	if err := os.RemoveAll(a.paths.RootDir); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(errs.CodeLocalCorruption, "remove local pb state", err)
	}
	fmt.Fprintln(a.out, "Local pb state removed. GitHub CLI auth is unchanged.")
	return nil
}

type editorSaver struct {
	sessionID string
	filePath  string
	service   *syncer.Service
	cache     *cache.Manager
}

func (e *editorSaver) Save(ctx context.Context, content string) (editor.SaveResult, error) {
	previousPath := e.filePath
	outcome, err := e.service.SaveContent(ctx, e.filePath, []byte(content))
	if err != nil {
		return editor.SaveResult{}, err
	}
	_ = e.cache.RemoveRecovery(e.sessionID, previousPath)
	if outcome.ConflictPath != "" {
		e.filePath = outcome.ConflictPath
	}
	return editor.SaveResult{
		Path:         outcome.Path,
		ConflictPath: outcome.ConflictPath,
		Message:      outcome.Message,
		RemoteSaved:  outcome.RemoteSaved,
	}, nil
}

func (e *editorSaver) SaveRecovery(_ context.Context, content string) error {
	return e.cache.SaveRecovery(e.sessionID, e.filePath, []byte(content))
}

func (e *editorSaver) ClearRecovery() error {
	return e.cache.RemoveRecovery(e.sessionID, e.filePath)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *App) loadEditorInitial(ctx context.Context, svc *syncer.Service, cacheMgr *cache.Manager, deviceID string, filePath string, isNew bool) ([]byte, bool, string, error) {
	var initial []byte
	if !isNew {
		content, err := svc.ReadContent(ctx, filePath)
		if err != nil && !errs.IsCode(err, errs.CodeNotFound) {
			return nil, false, "", err
		}
		initial = content
	}
	recovery, err := cacheMgr.LoadRecovery(deviceID, filePath)
	if err == nil {
		return recovery, true, "Recovered local draft autosave • Ctrl+S save • Ctrl+Q quit", nil
	}
	if err != nil && !errs.IsCode(err, errs.CodeNotFound) {
		return nil, false, "", err
	}
	return initial, false, "", nil
}
