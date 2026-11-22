import * as vscode from "vscode";
import { PBClient, SaveResult } from "./pbClient";

export const NOTE_SCHEME = "pb-note";

export function noteUri(path: string): vscode.Uri {
  return vscode.Uri.from({ scheme: NOTE_SCHEME, path: "/" + path });
}

export function notePathFromUri(uri: vscode.Uri): string | undefined {
  if (uri.scheme !== NOTE_SCHEME) {
    return undefined;
  }
  const p = uri.path.replace(/^\/+/, "");
  if (!p || p.split("/").some((seg) => seg === "" || seg === "." || seg === "..")) {
    return undefined;
  }
  return p;
}

/**
 * Exposes pb notes as a virtual, editable file system. Saving a document
 * (Cmd/Ctrl+S) pipes it to `pb save --stdin`, which encrypts and syncs it.
 * Contents are held in memory only and never written to the workspace.
 */
export class NoteFileSystem implements vscode.FileSystemProvider {
  private readonly emitter = new vscode.EventEmitter<vscode.FileChangeEvent[]>();
  readonly onDidChangeFile = this.emitter.event;
  private readonly sizes = new Map<string, number>();
  private readonly mtimes = new Map<string, number>();

  constructor(
    private readonly client: PBClient,
    private readonly knownPaths: () => string[],
    private readonly onSaved: (path: string, result: SaveResult) => Promise<void>
  ) {}

  watch(): vscode.Disposable {
    return new vscode.Disposable(() => undefined);
  }

  /** Tell open editors that notes changed remotely (after a sync). */
  notifyChanged(paths: string[]): void {
    const events = paths.map((p) => ({ type: vscode.FileChangeType.Changed, uri: noteUri(p) }));
    for (const p of paths) {
      this.mtimes.set(p, Date.now());
    }
    if (events.length) {
      this.emitter.fire(events);
    }
  }

  stat(uri: vscode.Uri): vscode.FileStat {
    const path = notePathFromUri(uri);
    const now = Date.now();
    if (!path) {
      return { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
    }
    const known = this.knownPaths();
    if (known.includes(path) || this.sizes.has(path)) {
      return { type: vscode.FileType.File, ctime: 0, mtime: this.mtimes.get(path) ?? now, size: this.sizes.get(path) ?? 0 };
    }
    if (known.some((k) => k.startsWith(path + "/"))) {
      return { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
    }
    throw vscode.FileSystemError.FileNotFound(uri);
  }

  readDirectory(uri: vscode.Uri): [string, vscode.FileType][] {
    const prefix = notePathFromUri(uri);
    const base = prefix ? prefix + "/" : "";
    const out = new Map<string, vscode.FileType>();
    for (const p of this.knownPaths()) {
      if (!p.startsWith(base)) {
        continue;
      }
      const rest = p.slice(base.length).split("/");
      out.set(rest[0], rest.length > 1 ? vscode.FileType.Directory : vscode.FileType.File);
    }
    return [...out.entries()];
  }

  createDirectory(): void {
    // Folders are implicit in note paths.
  }

  async readFile(uri: vscode.Uri): Promise<Uint8Array> {
    const path = notePathFromUri(uri);
    if (!path) {
      throw vscode.FileSystemError.FileNotFound(uri);
    }
    if (!this.knownPaths().includes(path) && !this.sizes.has(path)) {
      throw vscode.FileSystemError.FileNotFound(uri);
    }
    const text = await this.client.readText(path);
    const bytes = Buffer.from(text, "utf8");
    this.sizes.set(path, bytes.byteLength);
    return bytes;
  }

  async writeFile(uri: vscode.Uri, content: Uint8Array): Promise<void> {
    const path = notePathFromUri(uri);
    if (!path) {
      throw vscode.FileSystemError.NoPermissions("Invalid note path");
    }
    const text = Buffer.from(content).toString("utf8");
    const result = await this.client.save(path, text);
    this.sizes.set(path, content.byteLength);
    this.mtimes.set(path, Date.now());
    this.emitter.fire([{ type: vscode.FileChangeType.Changed, uri }]);
    await this.onSaved(path, result);
  }

  async delete(uri: vscode.Uri): Promise<void> {
    const path = notePathFromUri(uri);
    if (!path) {
      throw vscode.FileSystemError.FileNotFound(uri);
    }
    await this.client.delete(path);
    this.sizes.delete(path);
    this.emitter.fire([{ type: vscode.FileChangeType.Deleted, uri }]);
  }

  rename(): void {
    throw vscode.FileSystemError.NoPermissions("Renaming notes is not supported yet");
  }

  /** Register a path that does not exist remotely yet (a brand-new page). */
  registerNew(path: string): void {
    this.sizes.set(path, 0);
  }
}
