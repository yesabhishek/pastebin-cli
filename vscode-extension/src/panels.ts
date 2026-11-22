import * as vscode from "vscode";
import { PBClient, SaveResult, VersionEntry } from "./pbClient";
import { displayTitle } from "./viewState";
import { buildHtml } from "./webviewHtml";

function mediaRoots(extensionUri: vscode.Uri) {
  return [vscode.Uri.joinPath(extensionUri, "media")];
}

function asset(webview: vscode.Webview, extensionUri: vscode.Uri, name: string): string {
  return webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, "media", name)).toString();
}

export function composeQuickNote(title: string, body: string): string {
  const t = title.trim().replace(/[\r\n]+/g, " ");
  const b = body.replace(/\s+$/, "");
  if (t && b) {
    return `# ${t}\n\n${b}\n`;
  }
  if (t) {
    return `# ${t}\n`;
  }
  return b + "\n";
}

/**
 * Quick capture page. Opens instantly via keyboard shortcut; Cmd/Ctrl+Enter
 * encrypts, saves and syncs, then closes. Drafts live in memory only.
 */
export class QuickNotePanel {
  private static current?: QuickNotePanel;
  private static draft: { title: string; body: string } | undefined;

  static show(extensionUri: vscode.Uri, client: PBClient, onSaved: (r: SaveResult) => Promise<void>): void {
    if (QuickNotePanel.current) {
      QuickNotePanel.current.panel.reveal(vscode.ViewColumn.Active);
      return;
    }
    const panel = vscode.window.createWebviewPanel("pastebin.quickNote", "Quick note", vscode.ViewColumn.Active, {
      enableScripts: true,
      localResourceRoots: mediaRoots(extensionUri),
      retainContextWhenHidden: true
    });
    QuickNotePanel.current = new QuickNotePanel(panel, extensionUri, client, onSaved);
  }

  private constructor(
    private readonly panel: vscode.WebviewPanel,
    extensionUri: vscode.Uri,
    private readonly client: PBClient,
    private readonly onSaved: (r: SaveResult) => Promise<void>
  ) {
    const w = panel.webview;
    panel.iconPath = vscode.Uri.joinPath(extensionUri, "media", "icon.png");
    const mod = process.platform === "darwin" ? "⌘" : "Ctrl";
    w.html = buildHtml({
      cspSource: w.cspSource,
      styles: [asset(w, extensionUri, "theme.css")],
      scripts: [asset(w, extensionUri, "quicknote.js")],
      bodyClass: "page",
      title: "Quick note",
      body: `
<div class="page-wrap">
  <div class="page-crumbs"><span>🔒</span><span>Private</span><span>/</span><span>Notes</span><span>/</span><span>Quick note</span></div>
  <div class="page-icon" aria-hidden="true">⚡</div>
  <input id="title" class="page-title" placeholder="Untitled" autocomplete="off" spellcheck="true" maxlength="200" aria-label="Title">
  <div class="page-meta"><span>Saved to notes/ · encrypted before it leaves this machine</span></div>
  <textarea id="body" class="page-body" placeholder="Start writing… Markdown works." spellcheck="true" aria-label="Note"></textarea>
</div>
<div class="page-footer">
  <span id="status"></span>
  <div class="actions">
    <button id="discard" class="pb-btn">Discard</button>
    <button id="save" class="pb-btn primary" disabled>Save &amp; sync <span class="pb-kbd" style="color:inherit;box-shadow:inset 0 0 0 1px rgba(255,255,255,.4)">${mod}↵</span></button>
  </div>
</div>
<div id="toast" class="toast" role="status"></div>`
    });
    w.onDidReceiveMessage((msg) => void this.handle(msg));
    panel.onDidDispose(() => {
      QuickNotePanel.current = undefined;
    });
  }

  private async handle(raw: unknown): Promise<void> {
    if (!raw || typeof raw !== "object") {
      return;
    }
    const msg = raw as { type?: unknown; title?: unknown; body?: unknown };
    const title = typeof msg.title === "string" ? msg.title.slice(0, 200) : "";
    const body = typeof msg.body === "string" ? msg.body.slice(0, 5_000_000) : "";
    switch (msg.type) {
      case "ready":
        void this.panel.webview.postMessage({ type: "init", draft: QuickNotePanel.draft, hint: "Esc to close · draft is kept until you save" });
        return;
      case "close":
        QuickNotePanel.draft = title || body ? { title, body } : undefined;
        this.panel.dispose();
        return;
      case "discard":
        QuickNotePanel.draft = undefined;
        this.panel.dispose();
        return;
      case "save": {
        if (!title.trim() && !body.trim()) {
          return;
        }
        try {
          const result = await this.client.quickNote(composeQuickNote(title, body));
          QuickNotePanel.draft = undefined;
          void this.panel.webview.postMessage({ type: "saved", text: result.remote_saved ? "Saved & synced" : "Saved on this device" });
          await this.onSaved(result);
          this.panel.dispose();
        } catch (err) {
          QuickNotePanel.draft = { title, body };
          void this.panel.webview.postMessage({ type: "error", text: err instanceof Error ? err.message : String(err) });
        }
        return;
      }
    }
  }
}

/** Version history for a single note with decrypted preview and restore. */
export class HistoryPanel {
  private static panels = new Map<string, HistoryPanel>();
  private versions: VersionEntry[] = [];

  static show(extensionUri: vscode.Uri, client: PBClient, path: string, onRestored: (r: SaveResult) => Promise<void>): void {
    const existing = HistoryPanel.panels.get(path);
    if (existing) {
      existing.panel.reveal();
      return;
    }
    const { title } = displayTitle(path);
    const panel = vscode.window.createWebviewPanel("pastebin.history", `History · ${title}`, vscode.ViewColumn.Active, {
      enableScripts: true,
      localResourceRoots: mediaRoots(extensionUri)
    });
    HistoryPanel.panels.set(path, new HistoryPanel(panel, extensionUri, client, path, onRestored));
  }

  private constructor(
    private readonly panel: vscode.WebviewPanel,
    extensionUri: vscode.Uri,
    private readonly client: PBClient,
    private readonly path: string,
    private readonly onRestored: (r: SaveResult) => Promise<void>
  ) {
    const w = panel.webview;
    panel.iconPath = vscode.Uri.joinPath(extensionUri, "media", "icon.png");
    w.html = buildHtml({
      cspSource: w.cspSource,
      styles: [asset(w, extensionUri, "theme.css")],
      scripts: [asset(w, extensionUri, "history.js")],
      bodyClass: "page history",
      title: "Version history",
      body: `
<div class="history-grid">
  <main class="history-main">
    <h1 id="heading"></h1>
    <div class="sub" id="meta"></div>
    <pre id="preview" class="loading">Loading history…</pre>
  </main>
  <aside class="history-side">
    <div class="side-hdr"><span>VERSION HISTORY</span><button id="refresh" class="pb-btn" style="height:22px;padding:0 6px" title="Reload">↻</button></div>
    <div class="list" id="versions"></div>
    <div class="side-ftr"><button id="restore" class="pb-btn primary" disabled>Restore this version</button></div>
  </aside>
</div>`
    });
    w.onDidReceiveMessage((msg) => void this.handle(msg));
    panel.onDidDispose(() => HistoryPanel.panels.delete(path));
  }

  private async load(): Promise<void> {
    try {
      this.versions = await this.client.versions(this.path, 50);
      void this.panel.webview.postMessage({ type: "versions", title: displayTitle(this.path).title, versions: this.versions });
    } catch (err) {
      void this.panel.webview.postMessage({ type: "error", text: err instanceof Error ? err.message : String(err) });
    }
  }

  private async handle(raw: unknown): Promise<void> {
    if (!raw || typeof raw !== "object") {
      return;
    }
    const msg = raw as { type?: unknown; id?: unknown };
    if (msg.type === "ready" || msg.type === "reload") {
      await this.load();
      return;
    }
    // Only version IDs we listed ourselves are accepted.
    const version = typeof msg.id === "string" ? this.versions.find((v) => v.id === msg.id) : undefined;
    if (!version) {
      return;
    }
    if (msg.type === "show") {
      try {
        const shown = await this.client.show(this.path, version.id);
        void this.panel.webview.postMessage({ type: "content", id: version.id, content: shown.content });
      } catch (err) {
        void this.panel.webview.postMessage({ type: "error", text: err instanceof Error ? err.message : String(err) });
      }
    } else if (msg.type === "restore") {
      const when = new Date(version.timestamp).toLocaleString();
      const ok = await vscode.window.showWarningMessage(
        `Restore "${displayTitle(this.path).title}" to the version from ${when}?`,
        { modal: true, detail: "The current content stays in history, so you can undo this later." },
        "Restore"
      );
      if (ok !== "Restore") {
        return;
      }
      try {
        const result = await this.client.restore(this.path, version.id);
        await this.onRestored(result);
        await this.load();
      } catch (err) {
        void vscode.window.showErrorMessage(err instanceof Error ? err.message : String(err));
      }
    }
  }
}
