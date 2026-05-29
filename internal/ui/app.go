package ui

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"strings"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/auth"
	"github.com/yesabhishek/pastebin-cli/internal/cache"
	"github.com/yesabhishek/pastebin-cli/internal/config"
	"github.com/yesabhishek/pastebin-cli/internal/editor"
	"github.com/yesabhishek/pastebin-cli/internal/errs"
	"github.com/yesabhishek/pastebin-cli/internal/media"
	"github.com/yesabhishek/pastebin-cli/internal/model"
	"github.com/yesabhishek/pastebin-cli/internal/store"
	syncer "github.com/yesabhishek/pastebin-cli/internal/sync"
	"github.com/yesabhishek/pastebin-cli/internal/update"
	"github.com/yesabhishek/pastebin-cli/internal/version"
)

type App struct {
	in          io.Reader
	out         io.Writer
	errOut      io.Writer
	paths       config.Paths
	auth        *auth.GitHubAuth
	updater     *update.Manager
	interactive bool
	clipboard   media.Clipboard
	viewer      media.Viewer
	remoteStore func(*model.Config) store.RemoteStore
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
		in:          in,
		out:         out,
		errOut:      errOut,
		paths:       paths,
		auth:        auth.New(),
		updater:     update.NewManager(version.Current),
		interactive: isInteractive(in, out),
		clipboard:   media.SystemClipboard{},
		viewer:      media.SystemViewer{},
		remoteStore: func(cfg *model.Config) store.RemoteStore {
			return store.NewGitHub(cfg.Owner, cfg.Repo)
		},
	}, nil
}

func (a *App) Run(ctx context.Context, args []string) error {
	opts, err := parseGlobalArgs(args)
	if err != nil {
		return err
	}
	rest := opts.args
	if len(rest) == 0 {
		a.printHelp()
		return nil
	}
	command := rest[0]
	commandArgs := rest[1:]

	if err := a.maybeCheckForUpgrade(ctx, command); err != nil {
		fmt.Fprintf(a.errOut, "pb: release check skipped: %v\n", err)
	}

	switch command {
	case "help", "-h", "--help":
		a.printHelp()
		return nil
	case "version":
		return a.printVersion()
	case "init":
		cfg, err := a.initConfig(ctx, opts.repoOverride)
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
		return a.readPath(ctx, commandArgs, opts.jsonOut)
	case "save":
		return a.savePath(ctx, commandArgs, opts.jsonOut)
	case "paste":
		if len(commandArgs) != 1 {
			return errs.Wrap(errs.CodeUsage, "usage: pb paste <path>", nil)
		}
		return a.pastePath(ctx, commandArgs[0])
	case "copy":
		if len(commandArgs) != 1 {
			return errs.Wrap(errs.CodeUsage, "usage: pb copy <path>", nil)
		}
		return a.copyPath(ctx, commandArgs[0])
	case "versions":
		if len(commandArgs) != 1 {
			return errs.Wrap(errs.CodeUsage, "usage: pb versions <path>", nil)
		}
		return a.versionsPath(ctx, commandArgs[0], opts.jsonOut)
	case "show":
		if len(commandArgs) != 2 {
			return errs.Wrap(errs.CodeUsage, "usage: pb show <path> <version-id>", nil)
		}
		return a.showVersion(ctx, commandArgs[0], commandArgs[1], opts.jsonOut)
	case "restore":
		if len(commandArgs) != 2 {
			return errs.Wrap(errs.CodeUsage, "usage: pb restore <path> <version-id>", nil)
		}
		return a.restoreVersion(ctx, commandArgs[0], commandArgs[1], opts.jsonOut)
	case "delete":
		return a.deletePath(ctx, commandArgs, opts.jsonOut)
	case "list":
		return a.listPaths(ctx, commandArgs, opts.jsonOut)
	case "sync":
		return a.sync(ctx, opts.jsonOut)
	case "status":
		return a.status(ctx, opts.jsonOut)
	case "logout":
		return a.logout()
	case "upgrade":
		return a.upgrade(ctx, commandArgs)
	default:
		return errs.Wrap(errs.CodeUsage, "unknown command: "+command, nil)
	}
}

type globalOptions struct {
	repoOverride string
	jsonOut      bool
	args         []string
}

func parseGlobalArgs(args []string) (*globalOptions, error) {
	opts := &globalOptions{args: make([]string, 0, len(args))}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			opts.jsonOut = true
		case arg == "--repo":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return nil, errs.Wrap(errs.CodeUsage, "parse flags: --repo requires a value", nil)
			}
			opts.repoOverride = args[i]
		case strings.HasPrefix(arg, "--repo="):
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--repo="))
			if value == "" {
				return nil, errs.Wrap(errs.CodeUsage, "parse flags: --repo requires a value", nil)
			}
			opts.repoOverride = value
		default:
			opts.args = append(opts.args, arg)
		}
	}
	return opts, nil
}

func (a *App) printHelp() {
	fmt.Fprint(a.out, strings.TrimSpace(`
pb is a GitHub-backed personal pastebin CLI.

Commands:
  pb init
  pb version
  pb new <path>
  pb edit <path>
  pb read <path> [--out <file>]
  pb save <path> --stdin
  pb paste <path>
  pb copy <path>
  pb versions <path>
  pb show <path> <version-id>
  pb restore <path> <version-id>
  pb delete <path> [--yes]
  pb list [prefix] [--refresh]
  pb sync
  pb status
  pb upgrade [--yes] [--check] [--policy prompt|auto|manual]
  pb logout

Global flags:
  --repo <name>  override GitHub storage repo
  --json         emit JSON output for read, save, versions, show, restore, delete, list, sync, and status
`))
	fmt.Fprintln(a.out)
}

func (a *App) printVersion() error {
	fmt.Fprintf(a.out, "pb %s\n", a.updater.CurrentVersion())
	return nil
}

func (a *App) maybeCheckForUpgrade(ctx context.Context, command string) error {
	if shouldSkipUpgradeCheck(command) {
		return nil
	}
	cfg, err := config.Load(a.paths)
	if err != nil || cfg == nil {
		return err
	}
	release, available, err := a.updater.Check(ctx, cfg)
	if saveErr := config.Save(a.paths, cfg); saveErr != nil {
		return saveErr
	}
	if err != nil || release == nil || !available {
		return err
	}
	switch cfg.UpgradePolicy {
	case model.UpgradePolicyAuto:
		return a.performUpgrade(ctx, cfg, release, true)
	case model.UpgradePolicyManual:
		return nil
	default:
		if !a.interactive {
			fmt.Fprintf(a.errOut, "pb: update available: %s -> %s (run `pb upgrade`)\n", a.updater.CurrentVersion(), release.TagName)
			return nil
		}
		return a.promptForUpgrade(ctx, cfg, release)
	}
}

func shouldSkipUpgradeCheck(command string) bool {
	switch command {
	case "help", "-h", "--help", "init", "version", "upgrade", "logout":
		return true
	default:
		return false
	}
}

func (a *App) promptForUpgrade(ctx context.Context, cfg *model.Config, release *update.Release) error {
	fmt.Fprintf(a.errOut, "pb update available: %s -> %s\n", a.updater.CurrentVersion(), release.TagName)
	fmt.Fprint(a.errOut, "Choose [u]pgrade now, [a]uto-upgrade, [s]kip this release, [n]ever prompt, [l]ater: ")
	var answer string
	if _, err := fmt.Fscanln(a.in, &answer); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "u":
		return a.performUpgrade(ctx, cfg, release, false)
	case "a":
		cfg.UpgradePolicy = model.UpgradePolicyAuto
		cfg.IgnoredRelease = ""
		if err := config.Save(a.paths, cfg); err != nil {
			return err
		}
		return a.performUpgrade(ctx, cfg, release, true)
	case "s":
		cfg.IgnoredRelease = release.TagName
		return config.Save(a.paths, cfg)
	case "n":
		cfg.UpgradePolicy = model.UpgradePolicyManual
		cfg.IgnoredRelease = ""
		return config.Save(a.paths, cfg)
	default:
		return nil
	}
}

func (a *App) performUpgrade(ctx context.Context, cfg *model.Config, release *update.Release, automatic bool) error {
	result, err := a.updater.Install(ctx, release)
	if err != nil {
		return err
	}
	if shouldPersistUpgradeConfig(cfg) {
		cfg.IgnoredRelease = ""
		cfg.LastReleaseCheck = time.Now().UTC()
		if err := config.Save(a.paths, cfg); err != nil {
			return err
		}
	}
	if result.Scheduled {
		if automatic {
			fmt.Fprintf(a.errOut, "pb: scheduled automatic upgrade to %s. Restart pb after this command finishes.\n", result.Version)
		} else {
			fmt.Fprintf(a.out, "Scheduled upgrade to %s. Restart pb after this command finishes.\n", result.Version)
		}
		return nil
	}
	if automatic {
		fmt.Fprintf(a.errOut, "pb: upgraded to %s at %s. Restart pb to use the new version.\n", result.Version, result.Executable)
		return nil
	}
	fmt.Fprintf(a.out, "Upgraded to %s at %s. Restart pb to use the new version.\n", result.Version, result.Executable)
	return nil
}

func (a *App) upgrade(ctx context.Context, args []string) error {
	upgradeFS := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	upgradeFS.SetOutput(a.errOut)
	yes := upgradeFS.Bool("yes", false, "install the latest release without confirmation")
	checkOnly := upgradeFS.Bool("check", false, "check for a newer release without installing")
	policy := upgradeFS.String("policy", "", "set saved upgrade policy: prompt, auto, or manual")
	if err := upgradeFS.Parse(args); err != nil {
		return errs.Wrap(errs.CodeUsage, "parse upgrade flags", err)
	}
	if len(upgradeFS.Args()) != 0 {
		return errs.Wrap(errs.CodeUsage, "usage: pb upgrade [--yes] [--check] [--policy prompt|auto|manual]", nil)
	}
	cfg, err := config.Load(a.paths)
	if err != nil {
		return err
	}
	if cfg != nil && *policy != "" {
		if err := applyUpgradePolicy(cfg, *policy); err != nil {
			return err
		}
		if err := config.Save(a.paths, cfg); err != nil {
			return err
		}
	} else if cfg == nil && *policy != "" {
		fmt.Fprintln(a.errOut, "pb: run `pb init` first to save an upgrade policy")
	}

	release, err := a.updater.Latest(ctx)
	if err != nil {
		return err
	}
	current := a.updater.CurrentVersion()
	isNewer := a.updater.IsNewer(release.TagName)
	comparable := a.updater.CanCompareCurrentVersion()
	if *checkOnly {
		if isNewer || !comparable {
			fmt.Fprintf(a.out, "Update available: %s -> %s\n", current, release.TagName)
		} else {
			fmt.Fprintf(a.out, "pb is up to date at %s\n", current)
		}
		return nil
	}
	if comparable && !isNewer {
		fmt.Fprintf(a.out, "pb is already at the latest release: %s\n", current)
		return nil
	}
	if !*yes && a.interactive {
		fmt.Fprintf(a.out, "Upgrade pb from %s to %s? [y/N]: ", current, release.TagName)
		var answer string
		if _, err := fmt.Fscanln(a.in, &answer); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(a.out, "aborted")
			return nil
		}
	}
	if cfg == nil {
		return a.performUpgrade(ctx, nil, release, false)
	}
	return a.performUpgrade(ctx, cfg, release, false)
}

func applyUpgradePolicy(cfg *model.Config, policy string) error {
	switch policy {
	case model.UpgradePolicyPrompt, model.UpgradePolicyAuto, model.UpgradePolicyManual:
		cfg.UpgradePolicy = policy
		if policy != model.UpgradePolicyPrompt {
			cfg.IgnoredRelease = ""
		}
		return nil
	default:
		return errs.Wrap(errs.CodeUsage, "upgrade policy must be one of: prompt, auto, manual", nil)
	}
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
			Owner:         info.Login,
			Repo:          config.DefaultRepo(),
			Login:         info.Login,
			DeviceID:      deviceID,
			UpgradePolicy: model.UpgradePolicyPrompt,
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
	if cfg.UpgradePolicy == "" {
		cfg.UpgradePolicy = model.UpgradePolicyPrompt
	}
	if err := config.Save(a.paths, cfg); err != nil {
		return nil, err
	}
	a.paths = a.paths.Scope(cfg.Owner, cfg.Repo)
	if err := config.EnsureLayout(a.paths); err != nil {
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
	a.paths = a.paths.Scope(cfg.Owner, cfg.Repo)
	if err := config.EnsureLayout(a.paths); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (a *App) service(cfg *model.Config) *syncer.Service {
	cacheMgr := cache.New(a.paths)
	return syncer.NewService(a.paths, cfg, cacheMgr, a.remoteStore(cfg))
}

func (a *App) editPath(ctx context.Context, filePath string, isNew bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	cacheMgr := cache.New(a.paths)
	svc := syncer.NewService(a.paths, cfg, cacheMgr, a.remoteStore(cfg))
	initial, recovered, status, err := a.loadEditorInitial(ctx, svc, cacheMgr, cfg.DeviceID, filePath, isNew)
	if err != nil {
		return err
	}
	saver := &editorSaver{
		sessionID: cfg.DeviceID,
		filePath:  filePath,
		service:   svc,
		cache:     cacheMgr,
		clipboard: a.clipboard,
	}
	model := editor.New("pb editor", filePath, string(initial), saver, status, recovered)
	return editor.Run(a.in, a.out, model)
}

func (a *App) readPath(ctx context.Context, args []string, jsonOut bool) error {
	filePath, outPath, err := parseReadArgs(args)
	if err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	content, err := a.service(cfg).ReadContent(ctx, filePath)
	if err != nil {
		return err
	}
	return a.writeReadContent(filePath, content, outPath, jsonOut)
}

func (a *App) savePath(ctx context.Context, args []string, jsonOut bool) error {
	stdin := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--stdin":
			stdin = true
		default:
			if strings.HasPrefix(arg, "-") {
				return errs.Wrap(errs.CodeUsage, "unknown save flag: "+arg, nil)
			}
			rest = append(rest, arg)
		}
	}
	if len(rest) != 1 {
		return errs.Wrap(errs.CodeUsage, "usage: pb save <path> --stdin", nil)
	}
	if !stdin {
		return errs.Wrap(errs.CodeUsage, "usage: pb save <path> --stdin", nil)
	}
	content, err := io.ReadAll(a.in)
	if err != nil {
		return errs.Wrap(errs.CodeLocalCorruption, "read stdin", err)
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	outcome, err := a.service(cfg).SaveContent(ctx, rest[0], content)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, map[string]any{
			"path":          outcome.Path,
			"remote_saved":  outcome.RemoteSaved,
			"message":       outcome.Message,
			"version_id":    outcome.VersionID,
			"conflict_path": outcome.ConflictPath,
		})
	}
	msg := outcome.Message
	if msg == "" {
		msg = "saved"
	}
	fmt.Fprintf(a.out, "%s: %s\n", outcome.Path, msg)
	return nil
}

func parseReadArgs(args []string) (string, string, error) {
	var filePath string
	var outPath string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--out":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return "", "", errs.Wrap(errs.CodeUsage, "usage: pb read <path> [--out <file>]", nil)
			}
			outPath = args[i]
		case strings.HasPrefix(arg, "--out="):
			outPath = strings.TrimPrefix(arg, "--out=")
			if outPath == "" {
				return "", "", errs.Wrap(errs.CodeUsage, "usage: pb read <path> [--out <file>]", nil)
			}
		case strings.HasPrefix(arg, "-"):
			return "", "", errs.Wrap(errs.CodeUsage, "unknown read flag: "+arg, nil)
		default:
			if filePath != "" {
				return "", "", errs.Wrap(errs.CodeUsage, "usage: pb read <path> [--out <file>]", nil)
			}
			filePath = arg
		}
	}
	if filePath == "" {
		return "", "", errs.Wrap(errs.CodeUsage, "usage: pb read <path> [--out <file>]", nil)
	}
	return filePath, outPath, nil
}

func (a *App) pastePath(ctx context.Context, filePath string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	image, err := a.clipboard.ReadImage()
	if err != nil {
		return errs.Wrap(errs.CodeUsage, "read image from clipboard", err)
	}
	target := media.NormalizeImagePath(filePath)
	outcome, err := a.service(cfg).SaveContent(ctx, target, image)
	if err != nil {
		return err
	}
	msg := outcome.Message
	if msg == "" {
		msg = "saved image"
	}
	fmt.Fprintf(a.out, "%s: %s\n", outcome.Path, msg)
	return nil
}

func (a *App) copyPath(ctx context.Context, filePath string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	content, err := a.service(cfg).ReadContent(ctx, filePath)
	if err != nil {
		return err
	}
	if media.IsImage(filePath, content) {
		clipboardImage, err := media.PNGForClipboard(content)
		if err != nil {
			return errs.Wrap(errs.CodeUsage, "copy image to clipboard", err)
		}
		if err := a.clipboard.WriteImage(clipboardImage); err != nil {
			return errs.Wrap(errs.CodeUsage, "copy image to clipboard", err)
		}
		fmt.Fprintf(a.out, "Copied image %s to clipboard\n", filePath)
		return nil
	}
	if !media.IsText(content) {
		return errs.Wrap(errs.CodeUsage, "binary content requires `pb read <path> --out <file>`", nil)
	}
	if err := a.clipboard.WriteText(content); err != nil {
		return errs.Wrap(errs.CodeUsage, "copy text to clipboard", err)
	}
	fmt.Fprintf(a.out, "Copied text %s to clipboard\n", filePath)
	return nil
}

func (a *App) writeReadContent(filePath string, content []byte, outPath string, jsonOut bool) error {
	contentType := media.ContentType(filePath, content)
	if outPath != "" {
		if err := os.WriteFile(outPath, content, 0o600); err != nil {
			return errs.Wrap(errs.CodeLocalCorruption, "write output file", err)
		}
		if !jsonOut {
			fmt.Fprintf(a.out, "Wrote %s (%d bytes)\n", outPath, len(content))
			return nil
		}
	}
	if jsonOut {
		if media.IsText(content) && !media.IsImage(filePath, content) {
			payload := map[string]any{
				"path":         filePath,
				"content":      string(content),
				"content_type": contentType,
				"size":         len(content),
			}
			if outPath != "" {
				payload["out"] = outPath
			}
			return writeJSON(a.out, payload)
		}
		payload := map[string]any{
			"path":         filePath,
			"binary":       true,
			"content_type": contentType,
			"size":         len(content),
		}
		if outPath != "" {
			payload["out"] = outPath
		}
		return writeJSON(a.out, payload)
	}
	if media.IsImage(filePath, content) {
		viewPath, err := writeTempImage(filePath, content)
		if err != nil {
			return err
		}
		if err := a.viewer.Open(viewPath); err != nil {
			return errs.Wrap(errs.CodeLocalCorruption, "open image", err)
		}
		fmt.Fprintf(a.out, "Opened %s in the default image viewer\n", filePath)
		return nil
	}
	if !media.IsText(content) {
		return errs.Wrap(errs.CodeUsage, "binary content requires `pb read <path> --out <file>`", nil)
	}
	_, err := fmt.Fprint(a.out, string(content))
	return err
}

func writeTempImage(filePath string, content []byte) (string, error) {
	ext := pathpkg.Ext(filePath)
	if !media.IsImageExtension(filePath) {
		switch media.ImageContentType(content) {
		case media.ContentTypePNG:
			ext = ".png"
		case media.ContentTypeJPEG:
			ext = ".jpg"
		case media.ContentTypeGIF:
			ext = ".gif"
		case media.ContentTypeWEBP:
			ext = ".webp"
		default:
			ext = ".img"
		}
	}
	file, err := os.CreateTemp("", "pb-view-*"+ext)
	if err != nil {
		return "", errs.Wrap(errs.CodeLocalCorruption, "create temp image", err)
	}
	defer file.Close()
	if _, err := file.Write(content); err != nil {
		return "", errs.Wrap(errs.CodeLocalCorruption, "write temp image", err)
	}
	return file.Name(), nil
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

func (a *App) restoreVersion(ctx context.Context, filePath string, versionID string, jsonOut bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	version, outcome, err := a.service(cfg).RestoreVersion(ctx, filePath, versionID)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(a.out, map[string]any{
			"path":          filePath,
			"source":        version,
			"outcome":       outcome,
			"remote_saved":  outcome.RemoteSaved,
			"version_id":    outcome.VersionID,
			"conflict_path": outcome.ConflictPath,
		})
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
	yes := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--yes":
			yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				return errs.Wrap(errs.CodeUsage, "unknown delete flag: "+arg, nil)
			}
			rest = append(rest, arg)
		}
	}
	if len(rest) != 1 {
		return errs.Wrap(errs.CodeUsage, "usage: pb delete <path> [--yes]", nil)
	}
	filePath := rest[0]
	if !yes {
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
	refresh := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--refresh":
			refresh = true
		default:
			if strings.HasPrefix(arg, "-") {
				return errs.Wrap(errs.CodeUsage, "unknown list flag: "+arg, nil)
			}
			rest = append(rest, arg)
		}
	}
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
	if refresh {
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
	clipboard media.Clipboard
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

func (e *editorSaver) PasteImage(ctx context.Context) (editor.SaveResult, error) {
	image, err := e.clipboard.ReadImage()
	if err != nil {
		return editor.SaveResult{}, err
	}
	previousPath := e.filePath
	target := media.NormalizeImagePath(e.filePath)
	outcome, err := e.service.SaveContent(ctx, target, image)
	if err != nil {
		return editor.SaveResult{}, err
	}
	_ = e.cache.RemoveRecovery(e.sessionID, previousPath)
	e.filePath = target
	if outcome.ConflictPath != "" {
		e.filePath = outcome.ConflictPath
	}
	msg := outcome.Message
	if msg == "" {
		msg = "Image pasted and saved"
	}
	return editor.SaveResult{
		Path:         outcome.Path,
		ConflictPath: outcome.ConflictPath,
		Message:      msg,
		RemoteSaved:  outcome.RemoteSaved,
	}, nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func isInteractive(in io.Reader, out io.Writer) bool {
	inFile, ok := in.(*os.File)
	if !ok {
		return false
	}
	outFile, ok := out.(*os.File)
	if !ok {
		return false
	}
	inInfo, err := inFile.Stat()
	if err != nil {
		return false
	}
	outInfo, err := outFile.Stat()
	if err != nil {
		return false
	}
	return (inInfo.Mode()&os.ModeCharDevice) != 0 && (outInfo.Mode()&os.ModeCharDevice) != 0
}

func shouldPersistUpgradeConfig(cfg *model.Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.Owner != "" || cfg.Repo != "" || cfg.Login != "" || cfg.DeviceID != ""
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
