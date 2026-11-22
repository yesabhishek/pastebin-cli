import test from "node:test";
import assert from "node:assert/strict";
import { PBClient, PBError, classifyJSONParseError, classifyPBError } from "../pbClient";

test("init connects storage without prompting for a passphrase", async () => {
  const calls: string[][] = [];
  const client = new PBClient(
    () => "pb",
    async (args) => {
      calls.push(args);
      return "Initialized tester/pastebin-cli-store for tester\n";
    }
  );

  await client.init();
  assert.deepEqual(calls[0], ["init", "--no-vault"]);
});

test("save sends stdin content and JSON flag", async () => {
  const calls: Array<{ args: string[]; stdin?: string }> = [];
  const client = new PBClient(
    () => "pb",
    async (args, stdin) => {
      calls.push({ args, stdin });
      return JSON.stringify({
        path: "notes/test.md",
        remote_saved: true,
        message: "Saved"
      });
    }
  );

  const result = await client.save("notes/test.md", "hello");
  assert.equal(result.path, "notes/test.md");
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].args, ["save", "notes/test.md", "--stdin", "--json"]);
  assert.equal(calls[0].stdin, "hello");
});

test("capability check detects save support", async () => {
  const client = new PBClient(
    () => "pb",
    async () => "Commands:\n  pb read <path>\n  pb save <path> --stdin\n"
  );

  assert.equal(await client.supportsSave(), true);
});

test("capability check detects old pb without save", async () => {
  const client = new PBClient(
    () => "pb",
    async () => "Commands:\n  pb read <path>\n  pb sync\n"
  );

  assert.equal(await client.supportsSave(), false);
});

test("status prefixes JSON flag and normalizes nullable arrays", async () => {
  const calls: string[][] = [];
  const client = new PBClient(
    () => "pb",
    async (args) => {
      calls.push(args);
      return JSON.stringify({
        login: "tester",
        repo: "tester/pastebin-cli-store",
        total_files: 0,
        pending_writes: null,
        pending_delete: null,
        conflicts: null,
        files: null
      });
    }
  );

  const result = await client.status();
  assert.deepEqual(calls[0], ["--json", "status"]);
  assert.deepEqual(result.pending_writes, []);
  assert.deepEqual(result.pending_delete, []);
  assert.deepEqual(result.conflicts, []);
  assert.deepEqual(result.files, []);
});

test("sync prefixes JSON flag and normalizes nullable arrays", async () => {
  const calls: string[][] = [];
  const client = new PBClient(
    () => "pb",
    async (args) => {
      calls.push(args);
      return JSON.stringify({
        pulled: null,
        pushed: ["b.txt"],
        deleted: null,
        conflicts: null
      });
    }
  );

  const result = await client.sync();
  assert.deepEqual(calls[0], ["--json", "sync"]);
  assert.deepEqual(result.pulled, []);
  assert.deepEqual(result.pushed, ["b.txt"]);
  assert.deepEqual(result.deleted, []);
  assert.deepEqual(result.conflicts, []);
});

test("delete calls pb delete with --yes and --json", async () => {
  const calls: string[][] = [];
  const client = new PBClient(
    () => "pb",
    async (args) => {
      calls.push(args);
      return JSON.stringify({ deleted: "notes/a.md", status: "pending sync" });
    }
  );

  await client.delete("notes/a.md");
  assert.deepEqual(calls[0], ["--json", "delete", "notes/a.md", "--yes"]);
});

test("classifies uninitialized pb errors", () => {
  const err = classifyPBError("pb command failed: pb status --json\npb: run `pb init` first");
  assert.equal(err.code, "uninitialized");
});

test("classifies legacy text output as JSON flag order problem", () => {
  const err = classifyJSONParseError("Login: tester\nRepo: tester/pastebin-cli-store", new SyntaxError("bad JSON"));
  assert.equal(err.code, "legacy-json-flag-order");
});

test("save unsupported CLI errors are actionable", async () => {
  const client = new PBClient(
    () => "pb",
    async () => {
      throw new PBError("command-failed", "pb command failed: pb save notes/a.md --stdin --json\nunknown command: save");
    }
  );

  await assert.rejects(
    async () => await client.save("notes/a.md", "hello"),
    (err) => err instanceof PBError && err.code === "unsupported-cli" && err.message.includes("Update pb CLI")
  );
});

test("passphrases are sent on stdin, never as arguments", async () => {
  const calls: Array<{ args: string[]; stdin?: string }> = [];
  const client = new PBClient(() => "pb", async (args, stdin) => {
    calls.push({ args, stdin });
    return "{}";
  });
  await client.unlock("hunter2 hunter2");
  await client.encrypt("correct horse battery");
  await client.changePassphrase("old passphrase", "new passphrase!");
  for (const call of calls) {
    assert.ok(!call.args.some((a) => a.includes("hunter2") || a.includes("horse") || a.includes("passphrase!")), "secret leaked into argv");
    assert.ok(call.args.includes("--stdin"));
  }
  assert.equal(calls[0].stdin, "hunter2 hunter2\n");
  assert.equal(calls[2].stdin, "old passphrase\nnew passphrase!\n");
});

test("quick note sends content on stdin", async () => {
  const calls: Array<{ args: string[]; stdin?: string }> = [];
  const client = new PBClient(() => "pb", async (args, stdin) => {
    calls.push({ args, stdin });
    return JSON.stringify({ path: "notes/x.md", remote_saved: true, message: "ok" });
  });
  await client.quickNote("# Secret title\n\nbody\n");
  assert.deepEqual(calls[0].args, ["--json", "note", "--stdin"]);
  assert.equal(calls[0].stdin, "# Secret title\n\nbody\n");
});

test("switchAccount rejects suspicious logins before spawning pb", async () => {
  let spawned = false;
  const client = new PBClient(() => "pb", async () => {
    spawned = true;
    return "{}";
  });
  await assert.rejects(() => client.switchAccount("--repo=evil"));
  await assert.rejects(() => client.switchAccount("a b"));
  assert.equal(spawned, false);
});

test("locked and not-encrypted errors are classified", () => {
  assert.equal(classifyPBError("pb sync failed: vault is locked on this device. Run `pb unlock`").code, "locked");
  assert.equal(classifyPBError("pb sync failed: this notes repo is not encrypted yet. Run `pb encrypt`").code, "not-encrypted");
});

test("JSON parse errors never echo note content", () => {
  const err = classifyJSONParseError("super secret note body", new Error("bad"));
  assert.ok(!err.message.includes("super secret"));
});
