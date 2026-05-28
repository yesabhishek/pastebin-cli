import test from "node:test";
import assert from "node:assert/strict";
import {
  createReadyState,
  createUninitializedState,
  rowsForState
} from "../viewState";
import { StatusReport, TrackedFile } from "../pbClient";

test("renders initialize action when pastebin is uninitialized", () => {
  const rows = rowsForState(createUninitializedState());
  assert.equal(rows.length, 1);
  assert.deepEqual(rows[0], {
    kind: "action",
    label: "Initialize Pastebin",
    command: "pastebin.init",
    description: "Connect GitHub storage"
  });
});

test("renders account and first-note actions when initialized with zero notes", () => {
  const status = makeStatus([]);
  const rows = rowsForState(createReadyState([], status));
  assert.equal(rows[0].kind, "account");
  assert.equal(rows[0].label, "@tester");
  assert.equal(rows[0].description, "tester/pastebin-cli-store");
  assert.equal(rows[1].kind, "action");
  assert.equal(rows[1].label, "Create First Note");
  assert.equal(rows[2].kind, "action");
  assert.equal(rows[2].label, "Sync");
});

test("renders account and filtered note rows", () => {
  const files: TrackedFile[] = [
    { path: "notes/a.md", deleted: false },
    { path: "scratch/b.md", deleted: false, pending_op: "upsert" }
  ];
  const rows = rowsForState(createReadyState(files, makeStatus(files)), "scratch");
  assert.equal(rows[0].kind, "account");
  assert.equal(rows[1].kind, "note");
  assert.equal(rows[1].path, "scratch/b.md");
  assert.equal(rows[1].description, "pending");
});

function makeStatus(files: TrackedFile[]): StatusReport {
  return {
    login: "tester",
    repo: "tester/pastebin-cli-store",
    total_files: files.length,
    pending_writes: [],
    pending_delete: [],
    conflicts: [],
    files
  };
}
