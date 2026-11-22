import * as vscode from "vscode";
import { PBClient, PBError, SaveResult, VaultStatus } from "./pbClient";
import { NOTE_SCHEME, NoteFileSystem, notePathFromUri, noteUri } from "./noteFs";
import { HistoryPanel, QuickNotePanel } from "./panels";
import { SidebarAction, SidebarProvider } from "./sidebar";
import { buildLocalPB, repoRoot } from "./localBuilder";
import { displayTitle } from "./viewState";

const C = {
  quickNote: "pastebin.quickNote",
  newNote: "pastebin.newNote",
  sync: "pastebin.sync",
  refresh: "pastebin.refresh",
  init: "pastebin.init",
  openNote: "pastebin.openNote",
  deleteNote: "pastebin.deleteNote",
  showHistory: "pastebin.showHistory",
  searchNotes: "pastebin.searchNotes",
  pushCurrentNote: "pastebin.pushCurrentNote",
  switchAccount: "pastebin.switchAccount",
  unlock: "pastebin.unlock",
  lock: "pastebin.lock",
  changePassphrase: "pastebin.changePassphrase",
  showStatus: "pastebin.showStatus",
  configurePbPath: "pastebin.configurePbPath",
  buildLocalPB: "pastebin.buildLocalPB"
} as const;

const SHORTCUT = process.platform === "darwin" ? "⌥⌘P" : "Ctrl+Alt+P";

/**
 * Only the user-level (machine) setting is honoured. A workspace could
 * otherwise point pbPath at a malicious binary inside a cloned repo.
 */
function getPBPath(): string {
  const inspected = vscode.workspace.getConfiguration("pastebin").inspect<string>("pbPath");
  const value = inspected?.globalValue ?? inspected?.defaultValue ?? "pb";
  return value.trim() || "pb";
}

export function activate(context: vscode.ExtensionContext): void {
  const client = new PBClient(() => getPBPath());
  let sidebar!: SidebarProvider;
  const status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 50);
  status.command = C.switchAccount;
  context.subscriptions.push(status);

  const fs = new NoteFileSystem(client, () => sidebar.notePaths(), async (path, result) => {
    await afterSave(result, path);
  });
  context.subscriptions.push(vscode.workspace.registerFileSystemProvider(NOTE_SCHEME, fs, { isCaseSensitive: true }));

  sidebar = new SidebarProvider(context.extensionUri, client, SHORTCUT, (a) => handleSidebar(a));
  context.subscriptions.push(
    vscode.window.registerWebviewViewProvider("pastebin.notesView", sidebar, { webviewOptions: { retainContextWhenHidden: true } })
  );

  // ---------- helpers ----------

  const updateStatusBar = () => {
    const st = sidebar.getState();
    if (st.mode !== "ready" || !st.account) {
      status.hide();
      return;
    }
    status.text = `${st.account.unlocked ? "$(lock)" : "$(unlock)"} @${st.account.login}`;
    status.tooltip = new vscode.MarkdownString(
      `**Pastebin** · syncing as **@${st.account.login}**\n\n${st.account.repo}\n\n${st.account.unlocked ? "🔒 Vault unlocked on this device" : "🔐 Vault locked – click to unlock or switch"}\n\n${st.total} pages${st.pending ? ` · ${st.pending} pending` : ""}`
    );
    status.show();
  };

  const refresh = async () => {
    await sidebar.refresh();
    updateStatusBar();
  };

  async function run<T>(fn: () => PromiseLike<T>): Promise<T | undefined> {
    try {
      return await fn();
    } catch (err) {
      if (err instanceof PBError && (err.code === "locked" || err.code === "not-encrypted")) {
        const ok = err.code === "locked" ? await promptUnlock() : await promptEncrypt();
        if (ok) {
          try {
            return await fn();
          } catch (retryErr) {
            showError(retryErr);
            return undefined;
          }
        }
        return undefined;
      }
      showError(err);
      return undefined;
    }
  }

  function showError(err: unknown): void {
    const message = err instanceof Error ? err.message : String(err);
    if (err instanceof PBError && err.code === "auth") {
      void vscode.window.showErrorMessage(message, "Sign in with gh").then((choice) => {
        if (choice) {
          openGhLogin();
        }
      });
      return;
    }
    void vscode.window.showErrorMessage(message);
  }

  async function promptUnlock(): Promise<boolean> {
    const account = sidebar.getState().account;
    for (let attempt = 0; attempt < 3; attempt++) {
      const pass = await vscode.window.showInputBox({
        title: "Unlock Pastebin",
        prompt: `Enter the vault passphrase${account ? ` for ${account.repo}` : ""}. It's stored in your OS keychain so you only do this once per device.`,
        password: true,
        ignoreFocusOut: true,
        placeHolder: "Passphrase"
      });
      if (!pass) {
        return false;
      }
      try {
        await vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title: "Unlocking vault…" }, () => client.unlock(pass));
        await refresh();
        void syncNow(true);
        return true;
      } catch (err) {
        const wrong = err instanceof Error && /wrong passphrase/i.test(err.message);
        if (!wrong) {
          showError(err);
          return false;
        }
        void vscode.window.showWarningMessage("Wrong passphrase, try again.");
      }
    }
    return false;
  }

  async function promptEncrypt(): Promise<boolean> {
    const proceed = await vscode.window.showInformationMessage(
      "Protect your notes with a passphrase",
      {
        modal: true,
        detail:
          "Notes are encrypted on this machine before they're uploaded, so GitHub only ever stores ciphertext. " +
          "You'll enter the same passphrase once on each new device.\n\nThere is no way to recover notes if you forget it — store it in your password manager."
      },
      "Create passphrase"
    );
    if (proceed !== "Create passphrase") {
      return false;
    }
    const first = await vscode.window.showInputBox({
      title: "Create vault passphrase (1/2)",
      prompt: "At least 10 characters. A few random words work well.",
      password: true,
      ignoreFocusOut: true,
      validateInput: (v) => (v.length < 10 ? "Use at least 10 characters" : undefined)
    });
    if (!first) {
      return false;
    }
    const second = await vscode.window.showInputBox({
      title: "Confirm vault passphrase (2/2)",
      password: true,
      ignoreFocusOut: true,
      validateInput: (v) => (v !== first ? "Passphrases don't match" : undefined)
    });
    if (second !== first) {
      return false;
    }
    try {
      const out = await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Notification, title: "Encrypting your notes…" },
        () => client.encrypt(first)
      );
      await refresh();
      const migrated = /Encrypted (\d+) existing/.exec(out ?? "");
      void vscode.window.showInformationMessage(
        migrated ? `Vault ready. ${migrated[1]} existing notes are now encrypted.` : "Vault ready. Your notes are end-to-end encrypted.",
        ...(migrated ? ["About old history"] : [])
      ).then((c) => {
        if (c) {
          void vscode.window.showInformationMessage(
            "Older plaintext versions still exist in the repo's git history. To erase them, create a fresh repo with `pb --repo <new-name> init` and delete the old repo on GitHub.",
            { modal: true }
          );
        }
      });
      return true;
    } catch (err) {
      showError(err);
      return false;
    }
  }

  async function handleVaultNeeds(st: VaultStatus): Promise<void> {
    if (st.needs === "unlock") {
      await promptUnlock();
    } else if (st.needs === "encrypt") {
      await promptEncrypt();
    }
  }

  async function afterSave(result: SaveResult, path: string): Promise<void> {
    await refresh();
    if (result.locked) {
      const choice = await vscode.window.showWarningMessage(`Saved "${displayTitle(path).title}" on this device. Unlock to sync it.`, "Unlock");
      if (choice && (await promptUnlock())) {
        await syncNow(false);
      }
      return;
    }
    if (result.conflict_path) {
      void vscode.window.showWarningMessage(`"${path}" changed on another device. Your edit was saved as ${result.conflict_path}.`);
      return;
    }
    vscode.window.setStatusBarMessage(
      result.remote_saved ? `$(check) Saved & synced ${displayTitle(path).title}` : `$(cloud) Saved locally – will sync later`,
      3000
    );
  }

  let syncing: Promise<void> | undefined;
  async function syncNow(quiet: boolean): Promise<void> {
    if (syncing) {
      return syncing;
    }
    syncing = (async () => {
      sidebar.setBusy("sync");
      try {
        const result = await (quiet ? client.sync() : run(() => client.sync()));
        if (result) {
          sidebar.markSynced();
          fs.notifyChanged([...result.pulled]);
          if (!quiet) {
            const parts = [
              result.pulled.length && `${result.pulled.length} pulled`,
              result.pushed.length && `${result.pushed.length} pushed`,
              result.deleted.length && `${result.deleted.length} deleted`
            ].filter(Boolean);
            vscode.window.setStatusBarMessage(`$(check) Pastebin synced${parts.length ? ": " + parts.join(", ") : ""}`, 3000);
          }
          if (result.conflicts.length) {
            void vscode.window.showWarningMessage(`Pastebin: ${result.conflicts.length} conflict copy created. Look for ".conflict-" pages.`);
          }
        }
      } catch {
        // quiet background sync: errors surface through the sidebar state
      } finally {
        sidebar.setBusy(undefined);
        await refresh();
        syncing = undefined;
      }
    })();
    return syncing;
  }

  async function openNote(path: string): Promise<void> {
    const doc = await vscode.workspace.openTextDocument(noteUri(path));
    if (/\.(md|markdown)$/i.test(path) && doc.languageId !== "markdown") {
      await vscode.languages.setTextDocumentLanguage(doc, "markdown");
    }
    await vscode.window.showTextDocument(doc, { preview: false });
  }

  async function newPage(): Promise<void> {
    const title = await vscode.window.showInputBox({
      title: "New page",
      prompt: "Page title (you can also type a path like work/ideas.md)",
      placeHolder: "Untitled",
      ignoreFocusOut: true
    });
    if (title === undefined) {
      return;
    }
    const path = pathForTitle(title);
    fs.registerNew(path);
    const header = title.includes("/") || !title.trim() ? "" : `# ${title.trim()}\n\n`;
    // Create through the provider so the page exists and Cmd+S keeps saving to pb.
    await vscode.workspace.fs.writeFile(noteUri(path), Buffer.from(header, "utf8"));
    await openNote(path);
    const editor = vscode.window.activeTextEditor;
    if (editor) {
      const end = editor.document.positionAt(editor.document.getText().length);
      editor.selection = new vscode.Selection(end, end);
    }
  }

  async function pickNote(title: string): Promise<string | undefined> {
    if (sidebar.notePaths().length === 0) {
      await refresh();
    }
    const items = sidebar.notePaths().map((p) => {
      const t = displayTitle(p);
      return { label: `$(note) ${t.title}`, description: p, detail: t.subtitle, path: p };
    });
    const picked = await vscode.window.showQuickPick(items, { title, matchOnDescription: true, placeHolder: "Search pages" });
    return picked?.path;
  }

  async function deleteNote(path: string): Promise<void> {
    const ok = await vscode.window.showWarningMessage(
      `Delete "${displayTitle(path).title}"?`,
      { modal: true, detail: `${path}\n\nIt will be removed on your other devices after sync. Old versions stay in the repo's history.` },
      "Delete"
    );
    if (ok !== "Delete") {
      return;
    }
    await run(async () => {
      await client.delete(path);
      await syncNow(true);
    });
  }

  async function switchAccount(login?: string): Promise<void> {
    if (!login) {
      const view = await run(() => client.accounts());
      if (!view) {
        return;
      }
      const items: (vscode.QuickPickItem & { login?: string; add?: boolean })[] = view.accounts.map((a) => ({
        label: `${a.login === view.current ? "$(check)" : "$(account)"} @${a.login}`,
        description: a.login === view.current ? "syncing now" : a.ok ? "" : "session expired",
        login: a.login
      }));
      items.push({ label: "$(add) Add GitHub account…", add: true });
      const picked = await vscode.window.showQuickPick(items, {
        title: view.current ? `Pastebin · syncing as @${view.current}` : "Pastebin · choose account",
        placeHolder: "Pick the GitHub account to sync notes with"
      });
      if (!picked) {
        return;
      }
      if (picked.add) {
        openGhLogin();
        return;
      }
      if (picked.login === view.current) {
        return;
      }
      login = picked.login;
    }
    if (!login) {
      return;
    }
    const target = login;
    const st = await run(() =>
      vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title: `Switching to @${target}…` }, () => client.switchAccount(target))
    );
    if (!st) {
      return;
    }
    await refresh();
    await handleVaultNeeds(st);
    void vscode.window.showInformationMessage(`Pastebin now syncs as @${st.account} → ${st.repo}`);
    void syncNow(true);
  }

  function openGhLogin(): void {
    const term = vscode.window.createTerminal({ name: "GitHub sign-in" });
    term.show();
    term.sendText("gh auth login --hostname github.com --git-protocol https --web");
    void vscode.window.showInformationMessage("Finish signing in in the terminal, then pick the account from the Pastebin menu.");
  }

  async function initialize(): Promise<void> {
    const ok = await run(() =>
      vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title: "Connecting to GitHub…" }, () => client.init())
    );
    if (ok === undefined) {
      return;
    }
    const st = await run(() => client.vault());
    await refresh();
    if (st) {
      await handleVaultNeeds(st);
    }
    await syncNow(true);
  }

  async function changePassphrase(): Promise<void> {
    const current = await vscode.window.showInputBox({ title: "Change passphrase (1/3)", prompt: "Current passphrase", password: true, ignoreFocusOut: true });
    if (!current) {
      return;
    }
    const next = await vscode.window.showInputBox({
      title: "Change passphrase (2/3)",
      prompt: "New passphrase (at least 10 characters)",
      password: true,
      ignoreFocusOut: true,
      validateInput: (v) => (v.length < 10 ? "Use at least 10 characters" : undefined)
    });
    if (!next) {
      return;
    }
    const confirm = await vscode.window.showInputBox({
      title: "Change passphrase (3/3)",
      prompt: "Repeat the new passphrase",
      password: true,
      ignoreFocusOut: true,
      validateInput: (v) => (v !== next ? "Passphrases don't match" : undefined)
    });
    if (confirm !== next) {
      return;
    }
    const done = await run(async () => {
      await client.changePassphrase(current, next);
      return true;
    });
    if (done) {
      void vscode.window.showInformationMessage("Passphrase changed. New devices will need the new passphrase.");
    }
  }

  async function handleSidebar(a: SidebarAction): Promise<void> {
    switch (a.type) {
      case "open":
        await run(() => openNote(a.path));
        return;
      case "history":
        HistoryPanel.show(context.extensionUri, client, a.path, async () => {
          await refresh();
          fs.notifyChanged([a.path]);
          vscode.window.setStatusBarMessage("$(history) Version restored", 3000);
        });
        return;
      case "delete":
        await deleteNote(a.path);
        return;
      case "copyPath":
        await vscode.env.clipboard.writeText(a.path);
        vscode.window.setStatusBarMessage("$(copy) Path copied", 2000);
        return;
      case "switchAccount":
        await switchAccount(a.login);
        return;
      case "switchAccountMenu":
        await switchAccount();
        return;
      case "newNote":
        await run(newPage);
        return;
      case "quickNote":
        await vscode.commands.executeCommand(C.quickNote);
        return;
      case "sync":
        await syncNow(false);
        return;
      case "refresh":
        await refresh();
        return;
      case "init":
        await initialize();
        return;
      case "unlock":
        await promptUnlock();
        return;
      case "lock":
        await run(async () => {
          await client.lock();
          await refresh();
        });
        return;
      case "addAccount":
        openGhLogin();
        return;
      case "changePassphrase":
        await changePassphrase();
        return;
      case "buildLocal":
        await vscode.commands.executeCommand(C.buildLocalPB);
        return;
      case "configurePath":
        await vscode.commands.executeCommand(C.configurePbPath);
        return;
    }
  }

  // ---------- commands ----------

  const reg = (id: string, fn: (...args: any[]) => unknown) => context.subscriptions.push(vscode.commands.registerCommand(id, fn));

  reg(C.quickNote, () =>
    QuickNotePanel.show(context.extensionUri, client, async (result) => {
      await afterSave(result, result.path);
      if (!result.locked) {
        void syncNow(true);
      }
    })
  );
  reg(C.newNote, () => run(newPage));
  reg(C.sync, () => syncNow(false));
  reg(C.refresh, refresh);
  reg(C.init, initialize);
  reg(C.unlock, promptUnlock);
  reg(C.lock, () => handleSidebar({ type: "lock" }));
  reg(C.changePassphrase, changePassphrase);
  reg(C.switchAccount, () => switchAccount());
  reg(C.openNote, async (arg?: string | vscode.Uri) => {
    const path = typeof arg === "string" ? arg : arg instanceof vscode.Uri ? notePathFromUri(arg) : await pickNote("Open page");
    if (path) {
      await run(() => openNote(path));
    }
  });
  reg(C.searchNotes, async () => {
    const path = await pickNote("Search pages");
    if (path) {
      await run(() => openNote(path));
    }
  });
  reg(C.deleteNote, async (arg?: vscode.Uri) => {
    const path = arg instanceof vscode.Uri ? notePathFromUri(arg) : await pickNote("Delete page");
    if (path) {
      await deleteNote(path);
    }
  });
  reg(C.showHistory, async (arg?: vscode.Uri) => {
    const fromEditor = vscode.window.activeTextEditor ? notePathFromUri(vscode.window.activeTextEditor.document.uri) : undefined;
    const path = (arg instanceof vscode.Uri ? notePathFromUri(arg) : undefined) ?? fromEditor ?? (await pickNote("Version history"));
    if (path) {
      await handleSidebar({ type: "history", path });
    }
  });
  reg(C.pushCurrentNote, async () => {
    const editor = vscode.window.activeTextEditor;
    if (!editor) {
      void vscode.window.showErrorMessage("No active editor to save to Pastebin.");
      return;
    }
    const doc = editor.document;
    const existing = notePathFromUri(doc.uri);
    if (existing) {
      await doc.save();
      return;
    }
    const name = doc.isUntitled ? "new-note.md" : doc.fileName.split(/[\\/]/).pop() ?? "note.md";
    const path = await vscode.window.showInputBox({
      title: "Save to Pastebin",
      prompt: "Page path (folders are created automatically)",
      value: `notes/${name}`,
      ignoreFocusOut: true,
      validateInput: validatePath
    });
    if (!path) {
      return;
    }
    const result = await run(() => client.save(path, doc.getText()));
    if (result) {
      await afterSave(result, path);
    }
  });
  reg(C.showStatus, async () => {
    const st = await run(() => client.vault());
    if (st) {
      void vscode.window.showInformationMessage(
        `@${st.account} → ${st.repo} · ${st.encrypted ? (st.unlocked ? "encrypted, unlocked" : "encrypted, locked") : "NOT encrypted"} · key in ${st.keystore}`
      );
    }
  });
  reg(C.configurePbPath, async () => {
    const value = await vscode.window.showInputBox({ title: "Path to pb", prompt: "Absolute path to the pb binary (user setting)", value: getPBPath() });
    if (value) {
      await vscode.workspace.getConfiguration("pastebin").update("pbPath", value, vscode.ConfigurationTarget.Global);
      await boot();
    }
  });
  reg(C.buildLocalPB, async () => {
    const root = await repoRoot();
    if (!root) {
      void vscode.window.showErrorMessage("Open the pastebin-cli repository to build pb locally.");
      return;
    }
    const target = await run(() =>
      vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title: "Building pb…" }, () => buildLocalPB(root))
    );
    if (target) {
      await vscode.workspace.getConfiguration("pastebin").update("pbPath", target, vscode.ConfigurationTarget.Global);
      void vscode.window.showInformationMessage(`Pastebin now uses ${target}`);
      await boot();
    }
  });

  context.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("pastebin.pbPath")) {
        void boot();
      }
    })
  );

  // Periodic quiet sync while VS Code is focused.
  const timer = setInterval(() => {
    const enabled = vscode.workspace.getConfiguration("pastebin").get<boolean>("autoSync", true);
    const st = sidebar.getState();
    if (enabled && vscode.window.state.focused && st.mode === "ready" && st.account?.unlocked) {
      void syncNow(true);
    }
  }, 10 * 60 * 1000);
  context.subscriptions.push({ dispose: () => clearInterval(timer) });

  async function boot(): Promise<void> {
    try {
      if (!(await client.supportsVault())) {
        sidebar.setOutdated("Your pb CLI is older than this extension and doesn't support encryption. Run `pb upgrade`, or build it locally.");
        updateStatusBar();
        return;
      }
    } catch {
      // handled by refresh (missing CLI etc.)
    }
    await refresh();
    const st = sidebar.getState();
    if (st.mode === "ready" && st.account?.unlocked && vscode.workspace.getConfiguration("pastebin").get<boolean>("syncOnStartup", true)) {
      void syncNow(true);
    }
  }

  void boot();
}

export function deactivate(): void {}

export function validatePath(value: string): string | undefined {
  const v = value.trim();
  if (!v) {
    return "Enter a path";
  }
  if (v.startsWith("/") || v.includes("\\") || v.split("/").some((s) => s === "" || s === "." || s === "..")) {
    return "Use a relative path like notes/today.md";
  }
  if (/[<>:"|?*]/.test(v)) {
    return "Path contains characters that aren't allowed";
  }
  return undefined;
}

export function pathForTitle(title: string): string {
  const t = title.trim();
  if (t.includes("/") && !validatePath(t)) {
    return /\.[a-z0-9]+$/i.test(t) ? t : `${t}.md`;
  }
  const slug = t
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 60)
    .replace(/-+$/g, "");
  const now = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  const stamp = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}`;
  return `notes/${stamp}-${slug || "untitled"}.md`;
}
