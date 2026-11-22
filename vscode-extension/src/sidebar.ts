import * as vscode from "vscode";
import { AccountView, PBClient, PBError } from "./pbClient";
import { SidebarState, createEmptyState, createReadyState, notePaths } from "./viewState";
import { buildHtml } from "./webviewHtml";

export type SidebarAction =
  | { type: "open" | "history" | "delete" | "copyPath"; path: string }
  | { type: "switchAccount"; id: string }
  | { type: "newNote" | "quickNote" | "sync" | "refresh" | "init" | "unlock" | "lock" | "addAccount" | "changePassphrase" | "buildLocal" | "configurePath" | "switchAccountMenu" };

const SIMPLE = new Set(["newNote", "quickNote", "sync", "refresh", "init", "unlock", "lock", "addAccount", "changePassphrase", "buildLocal", "configurePath", "switchAccountMenu"]);
const WITH_PATH = new Set(["open", "history", "delete", "copyPath"]);
const ACCOUNT_ID = /^(github|gitlab|bitbucket):[A-Za-z0-9][A-Za-z0-9._-]{0,99}$/;

/** The Pastebin sidebar. It owns the latest state and validates every webview message. */
export class SidebarProvider implements vscode.WebviewViewProvider {
  private view?: vscode.WebviewView;
  private state: SidebarState;
  private knownIds = new Set<string>();
  private lastSync?: Date;

  constructor(
    private readonly extensionUri: vscode.Uri,
    private readonly client: PBClient,
    private readonly shortcut: string,
    private readonly onAction: (action: SidebarAction) => Promise<void>
  ) {
    this.state = createEmptyState("loading", shortcut);
  }

  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    const media = vscode.Uri.joinPath(this.extensionUri, "media");
    view.webview.options = { enableScripts: true, localResourceRoots: [media] };
    const asset = (name: string) => view.webview.asWebviewUri(vscode.Uri.joinPath(media, name)).toString();
    view.webview.html = buildHtml({
      cspSource: view.webview.cspSource,
      styles: [asset("theme.css"), asset("sidebar.css")],
      scripts: [asset("sidebar.js")],
      bodyClass: "sidebar",
      body: `<div id="root"></div>`,
      title: "Pastebin"
    });
    view.webview.onDidReceiveMessage((msg) => void this.handle(msg));
    view.onDidChangeVisibility(() => {
      if (view.visible) {
        this.push();
      }
    });
  }

  getState(): SidebarState {
    return this.state;
  }

  notePaths(): string[] {
    return [...notePaths(this.state)].sort();
  }

  setBusy(busy: string | undefined): void {
    this.state = { ...this.state, busy };
    this.push();
  }

  markSynced(): void {
    this.lastSync = new Date();
  }

  async refresh(): Promise<void> {
    try {
      const status = await this.client.status();
      const files = await this.client.list();
      this.state = createReadyState(files, status, this.shortcut);
    } catch (err) {
      if (err instanceof PBError && err.code === "uninitialized") {
        this.state = createEmptyState("uninitialized", this.shortcut);
      } else if (err instanceof PBError && err.code === "missing-cli") {
        this.state = createEmptyState("missing-cli", this.shortcut, err.message);
      } else if (err instanceof PBError && err.code === "legacy-json-flag-order") {
        this.state = createEmptyState("outdated-cli", this.shortcut, err.message);
      } else {
        this.state = createEmptyState("error", this.shortcut, err instanceof Error ? err.message : String(err));
      }
    }
    this.push();
  }

  setOutdated(message: string): void {
    this.state = createEmptyState("outdated-cli", this.shortcut, message);
    this.push();
  }

  focusSearch(): void {
    void this.view?.webview.postMessage({ type: "focusSearch" });
  }

  private push(): void {
    const state = { ...this.state, lastSync: this.lastSync ? relative(this.lastSync) : undefined };
    void this.view?.webview.postMessage({ type: "state", state });
  }

  private async sendAccounts(): Promise<void> {
    let view: AccountView;
    try {
      view = await this.client.accounts();
    } catch (err) {
      view = { current: this.state.account?.id ?? "", provider: "", repo: "", accounts: [] };
      void vscode.window.showErrorMessage(err instanceof Error ? err.message : String(err));
    }
    this.knownIds = new Set(view.accounts.map((a) => a.id));
    void this.view?.webview.postMessage({ type: "accounts", view });
  }

  /** Never trust the webview: only allow-listed message shapes and known paths/logins pass. */
  private async handle(raw: unknown): Promise<void> {
    if (!raw || typeof raw !== "object") {
      return;
    }
    const msg = raw as Record<string, unknown>;
    const type = msg.type;
    if (typeof type !== "string") {
      return;
    }
    if (type === "ready") {
      this.push();
      return;
    }
    if (type === "accounts") {
      await this.sendAccounts();
      return;
    }
    if (SIMPLE.has(type)) {
      await this.onAction({ type } as SidebarAction);
      return;
    }
    if (WITH_PATH.has(type)) {
      const path = msg.path;
      if (typeof path !== "string" || !notePaths(this.state).has(path)) {
        return;
      }
      await this.onAction({ type, path } as SidebarAction);
      return;
    }
    if (type === "switchAccount") {
      const id = msg.id;
      if (typeof id !== "string" || !ACCOUNT_ID.test(id) || !this.knownIds.has(id)) {
        return;
      }
      await this.onAction({ type, id });
    }
  }
}

function relative(d: Date): string {
  const secs = Math.round((Date.now() - d.getTime()) / 1000);
  if (secs < 45) {
    return "just now";
  }
  const mins = Math.round(secs / 60);
  if (mins < 60) {
    return `${mins} min ago`;
  }
  return `at ${d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}
