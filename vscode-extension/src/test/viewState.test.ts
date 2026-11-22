import test from "node:test";
import assert from "node:assert/strict";
import { buildTree, createReadyState, displayTitle, notePaths } from "../viewState";
import { StatusReport, TrackedFile } from "../pbClient";

test("display titles read like Notion page names", () => {
  assert.deepEqual(displayTitle("notes/2026-11-22-0930-team-sync.md"), { title: "Team sync", subtitle: "Nov 22, 2026 · 09:30" });
  assert.deepEqual(displayTitle("scratch/todo_list.txt"), { title: "Todo list", subtitle: undefined });
  assert.equal(displayTitle("a.md").title, "A");
});

test("tree puts folders first and nests pages", () => {
  const files: TrackedFile[] = [
    { path: "zeta.md", deleted: false },
    { path: "notes/2026-01-02-b.md", deleted: false },
    { path: "notes/2026-03-04-a.md", deleted: false },
    { path: "notes/deep/x.md", deleted: false },
    { path: "gone.md", deleted: true }
  ];
  const tree = buildTree(files, new Map());
  assert.equal(tree[0].kind, "folder");
  assert.equal(tree[0].title, "Notes");
  assert.equal(tree[1].path, "zeta.md");
  const notes = tree[0].children!;
  assert.equal(notes[0].kind, "folder");
  assert.equal(notes[1].path, "notes/2026-03-04-a.md", "dated pages are newest first");
  assert.equal(tree.length, 2, "deleted files are hidden");
});

test("ready state exposes account, counts and a path allow-list", () => {
  const files: TrackedFile[] = [
    { path: "notes/a.md", deleted: false },
    { path: "scratch/b.md", deleted: false, pending_op: "upsert" }
  ];
  const status: StatusReport = {
    login: "tester",
    repo: "tester/pastebin-cli-store",
    unlocked: true,
    total_files: 2,
    pending_writes: ["scratch/b.md"],
    pending_delete: [],
    conflicts: [],
    files
  };
  const state = createReadyState(files, status, "⌥⌘P");
  assert.deepEqual(state.account, { login: "tester", repo: "tester/pastebin-cli-store", unlocked: true });
  assert.equal(state.total, 2);
  assert.equal(state.pending, 1);
  const paths = notePaths(state);
  assert.ok(paths.has("notes/a.md") && paths.has("scratch/b.md"));
  assert.ok(!paths.has("../etc/passwd"));
  const scratch = state.pages.find((p) => p.title === "Scratch")!;
  assert.deepEqual(scratch.children![0].tags, ["pending"]);
});
