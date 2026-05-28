import { StatusReport, TrackedFile } from "./pbClient";

export type PastebinViewMode = "uninitialized" | "empty" | "ready" | "error";

export interface PastebinViewState {
  mode: PastebinViewMode;
  notes: string[];
  status?: StatusReport;
  filesByPath: Map<string, TrackedFile>;
  error?: string;
}

export type PastebinRow =
  | { kind: "account"; label: string; description: string; tooltip: string }
  | { kind: "action"; label: string; command: string; description?: string }
  | { kind: "note"; path: string; description: string }
  | { kind: "error"; label: string; description: string };

export function createReadyState(files: TrackedFile[], status: StatusReport): PastebinViewState {
  const notes = files.map((f) => f.path).sort((a, b) => a.localeCompare(b));
  return {
    mode: notes.length === 0 ? "empty" : "ready",
    notes,
    status,
    filesByPath: new Map(status.files.map((f) => [f.path, f]))
  };
}

export function createUninitializedState(): PastebinViewState {
  return {
    mode: "uninitialized",
    notes: [],
    filesByPath: new Map()
  };
}

export function createErrorState(error: string): PastebinViewState {
  return {
    mode: "error",
    notes: [],
    filesByPath: new Map(),
    error
  };
}

export function rowsForState(state: PastebinViewState, filter = ""): PastebinRow[] {
  const rows: PastebinRow[] = [];
  if (state.status) {
    rows.push(accountRow(state.status));
  }
  if (state.mode === "uninitialized") {
    rows.push({ kind: "action", label: "Initialize Pastebin", command: "pastebin.init", description: "Connect GitHub storage" });
    return rows;
  }
  if (state.mode === "error") {
    rows.push({ kind: "error", label: "Pastebin unavailable", description: state.error ?? "Unknown error" });
    return rows;
  }
  const query = filter.trim().toLowerCase();
  const filtered = query ? state.notes.filter((path) => path.toLowerCase().includes(query)) : state.notes;
  if (state.mode === "empty" || filtered.length === 0) {
    rows.push({ kind: "action", label: "Create First Note", command: "pastebin.newNote", description: "Start a note" });
    rows.push({ kind: "action", label: "Sync", command: "pastebin.sync", description: "Refresh GitHub state" });
    return rows;
  }
  for (const path of filtered) {
    rows.push(noteRow(path, state.filesByPath.get(path)));
  }
  return rows;
}

function accountRow(status: StatusReport): PastebinRow {
  return {
    kind: "account",
    label: `@${status.login}`,
    description: status.repo,
    tooltip: [
      `GitHub: @${status.login}`,
      `Repo: ${status.repo}`,
      `Pending writes: ${status.pending_writes.length}`,
      `Pending deletes: ${status.pending_delete.length}`,
      `Conflicts: ${status.conflicts.length}`
    ].join("\n")
  };
}

function noteRow(path: string, tracked: TrackedFile | undefined): PastebinRow {
  const tags: string[] = [];
  if (tracked?.pending_op === "upsert") {
    tags.push("pending");
  }
  if (tracked?.pending_op === "conflict" || tracked?.conflict_path) {
    tags.push("conflict");
  }
  if (tracked?.last_error) {
    tags.push("error");
  }
  return { kind: "note", path, description: tags.join(" ") };
}
