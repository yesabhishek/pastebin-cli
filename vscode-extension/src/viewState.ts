import { StatusReport, TrackedFile } from "./pbClient";

export type SidebarMode = "loading" | "uninitialized" | "ready" | "error" | "missing-cli" | "outdated-cli";

export interface AccountInfo {
  id: string;
  provider: string;
  login: string;
  repo: string;
  unlocked: boolean;
}

export const PROVIDER_LABELS: Record<string, string> = { github: "GitHub", gitlab: "GitLab", bitbucket: "Bitbucket" };

export function providerLabel(provider: string | undefined): string {
  return PROVIDER_LABELS[provider ?? "github"] ?? "GitHub";
}

export interface PageNode {
  kind: "folder" | "page";
  name: string;
  /** Full note path for pages, folder prefix for folders. */
  path: string;
  title: string;
  subtitle?: string;
  emoji: string;
  tags: string[];
  children?: PageNode[];
}

export interface SidebarState {
  mode: SidebarMode;
  account?: AccountInfo;
  pages: PageNode[];
  recent: PageNode[];
  total: number;
  pending: number;
  conflicts: number;
  busy?: string;
  error?: string;
  lastSync?: string;
  shortcut: string;
}

const DATE_PREFIX = /^(\d{4})-(\d{2})-(\d{2})(?:-(\d{2})(\d{2}))?-?/;
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** Human-friendly page title from a file path: "notes/2026-11-22-0930-team-sync.md" -> "Team sync". */
export function displayTitle(path: string): { title: string; subtitle?: string } {
  const file = path.split("/").pop() ?? path;
  let stem = file.replace(/\.(md|markdown|txt)$/i, "");
  let subtitle: string | undefined;
  const m = DATE_PREFIX.exec(stem);
  if (m && stem.length > m[0].length) {
    const month = MONTHS[Number(m[2]) - 1];
    subtitle = month ? `${month} ${Number(m[3])}, ${m[1]}` : undefined;
    if (subtitle && m[4]) {
      subtitle += ` · ${m[4]}:${m[5]}`;
    }
    stem = stem.slice(m[0].length);
  }
  const words = stem.replace(/[-_]+/g, " ").trim();
  const title = words ? words.charAt(0).toUpperCase() + words.slice(1) : file;
  return { title, subtitle };
}

export function emojiFor(path: string): string {
  const lower = path.toLowerCase();
  if (/\.(png|jpe?g|gif|webp)$/.test(lower)) {
    return "🖼️";
  }
  if (/\.(json|ya?ml|toml|ts|js|go|py|sh|sql)$/.test(lower)) {
    return "🧩";
  }
  if (lower.includes(".conflict-")) {
    return "⚠️";
  }
  return "📄";
}

export function tagsFor(tracked: TrackedFile | undefined): string[] {
  const tags: string[] = [];
  if (tracked?.pending_op === "upsert" || tracked?.pending_op === "delete") {
    tags.push("pending");
  }
  if (tracked?.pending_op === "conflict" || tracked?.conflict_path) {
    tags.push("conflict");
  }
  if (tracked?.last_error) {
    tags.push("error");
  }
  return tags;
}

/** Build a folder tree. Folders first, then pages, each alphabetical (pages newest-first by date prefix). */
export function buildTree(files: TrackedFile[], byPath: Map<string, TrackedFile>): PageNode[] {
  const root: PageNode = { kind: "folder", name: "", path: "", title: "", emoji: "", tags: [], children: [] };
  for (const file of files) {
    if (file.deleted) {
      continue;
    }
    const parts = file.path.split("/");
    let node = root;
    for (let i = 0; i < parts.length - 1; i++) {
      const prefix = parts.slice(0, i + 1).join("/") + "/";
      let next = node.children!.find((c) => c.kind === "folder" && c.path === prefix);
      if (!next) {
        const name = parts[i];
        next = { kind: "folder", name, path: prefix, title: name.charAt(0).toUpperCase() + name.slice(1), emoji: "📁", tags: [], children: [] };
        node.children!.push(next);
      }
      node = next;
    }
    const { title, subtitle } = displayTitle(file.path);
    node.children!.push({
      kind: "page",
      name: parts[parts.length - 1],
      path: file.path,
      title,
      subtitle,
      emoji: emojiFor(file.path),
      tags: tagsFor(byPath.get(file.path) ?? file)
    });
  }
  sortTree(root);
  return root.children!;
}

function sortTree(node: PageNode): void {
  if (!node.children) {
    return;
  }
  node.children.sort((a, b) => {
    if (a.kind !== b.kind) {
      return a.kind === "folder" ? -1 : 1;
    }
    if (a.kind === "page" && DATE_PREFIX.test(a.name) && DATE_PREFIX.test(b.name)) {
      return b.name.localeCompare(a.name);
    }
    return a.title.localeCompare(b.title);
  });
  node.children.forEach(sortTree);
}

/** Most recent pages, using the date prefix or updated_at. */
export function recentPages(files: TrackedFile[], byPath: Map<string, TrackedFile>, limit = 5): PageNode[] {
  const scored = files
    .filter((f) => !f.deleted)
    .map((f) => ({ f, t: Date.parse(byPath.get(f.path)?.updated_at ?? f.updated_at ?? "") || 0 }))
    .sort((a, b) => b.t - a.t)
    .slice(0, limit);
  return scored.map(({ f }) => {
    const { title, subtitle } = displayTitle(f.path);
    return { kind: "page", name: f.path.split("/").pop() ?? f.path, path: f.path, title, subtitle, emoji: emojiFor(f.path), tags: tagsFor(byPath.get(f.path)) };
  });
}

export function createReadyState(files: TrackedFile[], status: StatusReport, shortcut: string): SidebarState {
  const byPath = new Map(status.files.map((f) => [f.path, f]));
  const live = files.filter((f) => !f.deleted);
  return {
    mode: "ready",
    account: {
      id: status.account || `github:${status.login}`,
      provider: status.provider || "github",
      login: status.login,
      repo: status.repo,
      unlocked: status.unlocked ?? false
    },
    pages: buildTree(live, byPath),
    recent: recentPages(live, byPath),
    total: live.length,
    pending: status.pending_writes.length + status.pending_delete.length,
    conflicts: status.conflicts.length,
    shortcut
  };
}

export function createEmptyState(mode: SidebarMode, shortcut: string, error?: string): SidebarState {
  return { mode, pages: [], recent: [], total: 0, pending: 0, conflicts: 0, error, shortcut };
}

/** Every note path present in a state – used to validate webview messages. */
export function notePaths(state: SidebarState): Set<string> {
  const out = new Set<string>();
  const walk = (nodes: PageNode[]) => {
    for (const n of nodes) {
      if (n.kind === "page") {
        out.add(n.path);
      } else if (n.children) {
        walk(n.children);
      }
    }
  };
  walk(state.pages);
  return out;
}
