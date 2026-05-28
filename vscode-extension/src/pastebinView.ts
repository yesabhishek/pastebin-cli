import * as vscode from "vscode";
import { PBClient, PBError } from "./pbClient";
import {
  PastebinRow,
  PastebinViewState,
  createErrorState,
  createReadyState,
  createUninitializedState,
  rowsForState
} from "./viewState";

export const NOTE_SCHEME = "pb-note";

export function makeNoteURI(path: string): vscode.Uri {
  const query = new URLSearchParams({ path }).toString();
  return vscode.Uri.from({ scheme: NOTE_SCHEME, path: "/note", query });
}

export function notePathFromURI(uri: vscode.Uri): string | undefined {
  if (uri.scheme !== NOTE_SCHEME) {
    return undefined;
  }
  const params = new URLSearchParams(uri.query);
  const path = params.get("path");
  return path ?? undefined;
}

export class PastebinDocumentProvider implements vscode.TextDocumentContentProvider {
  private readonly emitter = new vscode.EventEmitter<vscode.Uri>();
  readonly onDidChange = this.emitter.event;

  constructor(private readonly client: PBClient) {}

  async provideTextDocumentContent(uri: vscode.Uri): Promise<string> {
    const path = notePathFromURI(uri);
    if (!path) {
      throw new Error("Missing note path");
    }
    return await this.client.readText(path);
  }

  refresh(uri: vscode.Uri): void {
    this.emitter.fire(uri);
  }
}

export class PastebinNoteItem extends vscode.TreeItem {
  constructor(public readonly notePath: string, description: string) {
    super(notePath, vscode.TreeItemCollapsibleState.None);
    this.contextValue = "pastebinNote";
    this.tooltip = notePath;
    this.command = {
      command: "pastebin.openNote",
      title: "Open Note",
      arguments: [this]
    };
    this.description = description;
    this.iconPath = new vscode.ThemeIcon("note");
  }
}

export class PastebinAccountItem extends vscode.TreeItem {
  constructor(row: Extract<PastebinRow, { kind: "account" }>) {
    super(row.label, vscode.TreeItemCollapsibleState.None);
    this.contextValue = "pastebinStatus";
    this.description = row.description;
    this.tooltip = row.tooltip;
    this.iconPath = new vscode.ThemeIcon("github");
    this.command = { command: "pastebin.showStatus", title: "Show Status" };
  }
}

export class PastebinActionItem extends vscode.TreeItem {
  constructor(row: Extract<PastebinRow, { kind: "action" }>) {
    super(row.label, vscode.TreeItemCollapsibleState.None);
    this.contextValue = "pastebinAction";
    this.description = row.description;
    this.tooltip = row.label;
    this.iconPath = new vscode.ThemeIcon(row.command === "pastebin.init" ? "rocket" : row.command === "pastebin.sync" ? "sync" : "new-file");
    this.command = { command: row.command, title: row.label };
  }
}

export class PastebinErrorItem extends vscode.TreeItem {
  constructor(row: Extract<PastebinRow, { kind: "error" }>) {
    super(row.label, vscode.TreeItemCollapsibleState.None);
    this.contextValue = "pastebinError";
    this.description = row.description;
    this.tooltip = row.description;
    this.iconPath = new vscode.ThemeIcon("warning");
  }
}

export class PastebinTreeProvider implements vscode.TreeDataProvider<vscode.TreeItem> {
  private readonly emitter = new vscode.EventEmitter<vscode.TreeItem | undefined>();
  readonly onDidChangeTreeData = this.emitter.event;

  private state: PastebinViewState = createUninitializedState();
  private filter = "";

  constructor(private readonly client: PBClient) {}

  async refresh(): Promise<void> {
    try {
      const status = await this.client.status();
      const files = await this.client.list();
      this.state = createReadyState(files, status);
    } catch (err) {
      if (err instanceof PBError && err.code === "uninitialized") {
        this.state = createUninitializedState();
      } else {
        const message = err instanceof Error ? err.message : String(err);
        this.state = createErrorState(message);
      }
    }
    this.emitter.fire(undefined);
  }

  setFilter(filter: string): void {
    this.filter = filter.trim().toLowerCase();
    this.emitter.fire(undefined);
  }

  getFilter(): string {
    return this.filter;
  }

  getNotePaths(): string[] {
    return [...this.state.notes];
  }

  getStatus() {
    return this.state.status;
  }

  getMode() {
    return this.state.mode;
  }

  getTreeItem(element: vscode.TreeItem): vscode.TreeItem {
    return element;
  }

  getChildren(element?: vscode.TreeItem): vscode.ProviderResult<vscode.TreeItem[]> {
    if (element) {
      return [];
    }
    return rowsForState(this.state, this.filter).map((row) => {
      switch (row.kind) {
        case "account":
          return new PastebinAccountItem(row);
        case "action":
          return new PastebinActionItem(row);
        case "error":
          return new PastebinErrorItem(row);
        case "note":
          return new PastebinNoteItem(row.path, row.description);
      }
    });
  }
}
