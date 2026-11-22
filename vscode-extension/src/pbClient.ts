import { spawn } from "node:child_process";

export type PBErrorCode =
  | "uninitialized"
  | "missing-cli"
  | "command-failed"
  | "legacy-json-flag-order"
  | "unsupported-cli"
  | "locked"
  | "not-encrypted"
  | "auth";

export class PBError extends Error {
  constructor(
    readonly code: PBErrorCode,
    message: string,
    readonly binaryPath?: string
  ) {
    super(message);
    this.name = "PBError";
  }
}

export interface TrackedFile {
  path: string;
  deleted: boolean;
  pending_op?: string;
  conflict_path?: string;
  last_error?: string;
  updated_at?: string;
}

export interface StatusReport {
  provider?: string;
  account?: string;
  login: string;
  repo: string;
  unlocked?: boolean;
  total_files: number;
  pending_writes: string[];
  pending_delete: string[];
  conflicts: string[];
  files: TrackedFile[];
}

export interface SyncResult {
  pulled: string[];
  pushed: string[];
  deleted: string[];
  conflicts: string[];
}

export interface SaveResult {
  path: string;
  remote_saved: boolean;
  message: string;
  version_id?: string;
  conflict_path?: string;
  locked?: boolean;
}

export interface VersionEntry {
  id: string;
  commit_sha: string;
  path: string;
  timestamp: string;
  reason: string;
}

export interface ShownVersion {
  path: string;
  version_id: string;
  commit_sha: string;
  timestamp: string;
  reason: string;
  content: string;
}

export interface Account {
  id: string;
  provider: string;
  login: string;
  host: string;
  active: boolean;
  ok: boolean;
}

export interface AccountView {
  current: string;
  provider: string;
  repo: string;
  accounts: Account[];
}

export interface VaultStatus {
  provider: string;
  id: string;
  account: string;
  repo: string;
  encrypted: boolean;
  unlocked: boolean;
  legacy_files: number;
  keystore: string;
  needs: "" | "unlock" | "encrypt";
}

export interface LoginResult {
  id: string;
  provider: string;
  host: string;
  login: string;
}

const ACCOUNT_ID = /^(github|gitlab|bitbucket):[A-Za-z0-9][A-Za-z0-9._-]{0,99}$/;

export type PBRunner = (args: string[], stdin?: string) => Promise<string>;

/** Thin, typed wrapper around the pb CLI. Secrets are only ever sent via stdin. */
export class PBClient {
  private readonly run: PBRunner;

  constructor(getBinaryPath: () => string, run?: PBRunner) {
    this.run = run ?? createProcessRunner(getBinaryPath);
  }

  async list(prefix = "", refresh = false): Promise<TrackedFile[]> {
    const args = ["list"];
    if (prefix) {
      args.push(prefix);
    }
    if (refresh) {
      args.push("--refresh");
    }
    return (await this.runJSON<TrackedFile[] | null>(args)) ?? [];
  }

  async init(): Promise<void> {
    await this.run(["init", "--no-vault"]);
  }

  async supportsSave(): Promise<boolean> {
    const help = await this.run(["help"]);
    return help.split(/\r?\n/).some((line) => line.trim().startsWith("pb save "));
  }

  async supportsVault(): Promise<boolean> {
    const help = await this.run(["help"]);
    return help.split(/\r?\n/).some((line) => line.trim().startsWith("pb unlock"));
  }

  async status(): Promise<StatusReport> {
    return normalizeStatus(await this.runJSON<StatusReport>(["status"]));
  }

  async vault(): Promise<VaultStatus> {
    return await this.runJSON<VaultStatus>(["vault"]);
  }

  async unlock(passphrase: string): Promise<void> {
    await this.runJSON(["unlock", "--stdin"], passphrase + "\n");
  }

  async encrypt(passphrase: string): Promise<string> {
    return await this.run(["encrypt", "--stdin"], passphrase + "\n");
  }

  async changePassphrase(current: string, next: string): Promise<void> {
    await this.run(["passphrase", "--stdin"], `${current}\n${next}\n`);
  }

  async lock(): Promise<void> {
    await this.run(["lock"]);
  }

  async accounts(): Promise<AccountView> {
    const view = await this.runJSON<AccountView>(["account"]);
    return { current: view.current ?? "", provider: view.provider ?? "", repo: view.repo ?? "", accounts: view.accounts ?? [] };
  }

  /** Switch to an account id such as "github:alice", "gitlab:bob" or "bitbucket:carol". */
  async switchAccount(id: string): Promise<VaultStatus> {
    if (!ACCOUNT_ID.test(id)) {
      throw new PBError("command-failed", `Invalid account: ${id}`);
    }
    return await this.runJSON<VaultStatus>(["account", "switch", id]);
  }

  /** Store a GitLab or Bitbucket token (sent on stdin) and return the account. */
  async login(provider: "gitlab" | "bitbucket", token: string, options: { host?: string; email?: string } = {}): Promise<LoginResult> {
    const args = ["login", provider, "--stdin"];
    if (options.host) {
      if (!/^[A-Za-z0-9.-]+(:[0-9]{1,5})?$/.test(options.host)) {
        throw new PBError("command-failed", `Invalid host: ${options.host}`);
      }
      args.push("--host", options.host);
    }
    if (options.email) {
      if (!/^[^\s@]+@[^\s@]+$/.test(options.email)) {
        throw new PBError("command-failed", "Enter a valid email address.");
      }
      args.push("--email", options.email);
    }
    return await this.runJSON<LoginResult>(args, token + "\n");
  }

  async sync(): Promise<SyncResult> {
    return normalizeSync(await this.runJSON<SyncResult>(["sync"]));
  }

  async readText(path: string): Promise<string> {
    const payload = await this.runJSON<{ content?: string; binary?: boolean }>(["read", path]);
    if (payload.binary) {
      throw new PBError("command-failed", "This note is binary. Use `pb read <path> --out <file>` for binary content.");
    }
    return payload.content ?? "";
  }

  async save(path: string, content: string): Promise<SaveResult> {
    try {
      return await this.runJSON<SaveResult>(["save", path, "--stdin"], content, "suffix");
    } catch (err) {
      if (err instanceof PBError && err.message.toLowerCase().includes("unknown command: save")) {
        throw new PBError("unsupported-cli", "Update pb CLI to save notes from VS Code.", undefined);
      }
      throw err;
    }
  }

  /** Quick capture: the whole note (including a "# Title" line) goes over stdin. */
  async quickNote(content: string): Promise<SaveResult> {
    return await this.runJSON<SaveResult>(["note", "--stdin"], content);
  }

  async delete(path: string): Promise<void> {
    await this.runJSON<Record<string, string>>(["delete", path, "--yes"]);
  }

  async versions(path: string, limit = 50): Promise<VersionEntry[]> {
    return (await this.runJSON<VersionEntry[] | null>(["versions", path, "--limit", String(limit)])) ?? [];
  }

  async show(path: string, versionID: string): Promise<ShownVersion> {
    return await this.runJSON<ShownVersion>(["show", path, versionID]);
  }

  async restore(path: string, versionID: string): Promise<SaveResult> {
    return await this.runJSON<SaveResult>(["restore", path, versionID]);
  }

  private async runJSON<T>(commandArgs: string[], stdin?: string, jsonPosition: "prefix" | "suffix" = "prefix"): Promise<T> {
    const args = jsonPosition === "prefix" ? ["--json", ...commandArgs] : [...commandArgs, "--json"];
    const output = await this.run(args, stdin);
    try {
      return JSON.parse(output) as T;
    } catch (err) {
      throw classifyJSONParseError(output, err);
    }
  }
}

function normalizeStatus(status: StatusReport): StatusReport {
  return {
    ...status,
    pending_writes: status.pending_writes ?? [],
    pending_delete: status.pending_delete ?? [],
    conflicts: status.conflicts ?? [],
    files: status.files ?? []
  };
}

function normalizeSync(result: SyncResult): SyncResult {
  return {
    pulled: result.pulled ?? [],
    pushed: result.pushed ?? [],
    deleted: result.deleted ?? [],
    conflicts: result.conflicts ?? []
  };
}

export function classifyJSONParseError(output: string, cause: unknown): PBError {
  const trimmed = output.trimStart();
  if (trimmed.startsWith("Login:") || trimmed.startsWith("Repo:") || trimmed.startsWith("Files:")) {
    return new PBError(
      "legacy-json-flag-order",
      "pb returned plain text instead of JSON. Update the Pastebin extension or call pb with `--json` before the command."
    );
  }
  // Never echo raw output: it may contain note content.
  return new PBError("command-failed", `Failed to parse pb JSON output (${String(cause)}). Output was ${output.length} bytes.`);
}

export function classifyPBError(message: string): PBError {
  const lower = message.toLowerCase();
  if (lower.includes("run `pb init` first") || lower.includes("run pb init first")) {
    return new PBError("uninitialized", message);
  }
  if (lower.includes("pb cli not found")) {
    return new PBError("missing-cli", message);
  }
  if (lower.includes("vault is locked")) {
    return new PBError("locked", "Your Pastebin vault is locked on this device.");
  }
  if (lower.includes("not encrypted yet")) {
    return new PBError("not-encrypted", "Your Pastebin notes repo is not encrypted yet.");
  }
  if (lower.includes("unknown command: save")) {
    return new PBError("unsupported-cli", "Update pb CLI to save notes from VS Code.");
  }
  if (lower.includes("gh auth login") || lower.includes("not authenticated") || lower.includes("pb login") || lower.includes("rejected the token")) {
    return new PBError("auth", message);
  }
  return new PBError("command-failed", message);
}

/** Strip the "pb: " prefix and keep only the human-readable part of an error. */
export function cleanErrorText(stderr: string): string {
  const lines = stderr
    .split(/\r?\n/)
    .map((l) => l.replace(/^pb:\s*/, "").trim())
    .filter(Boolean);
  return lines.join("\n");
}

function createProcessRunner(getBinaryPath: () => string): PBRunner {
  return async (args: string[], stdin?: string): Promise<string> =>
    await new Promise<string>((resolve, reject) => {
      const bin = getBinaryPath();
      // shell: false (default) – arguments are never interpreted by a shell.
      const child = spawn(bin, args, { stdio: "pipe", windowsHide: true });
      let stdout = "";
      let stderr = "";

      child.stdout.setEncoding("utf8");
      child.stderr.setEncoding("utf8");
      child.stdout.on("data", (chunk: string) => {
        stdout += chunk;
      });
      child.stderr.on("data", (chunk: string) => {
        stderr += chunk;
      });
      child.on("error", (err: NodeJS.ErrnoException) => {
        if (err.code === "ENOENT") {
          reject(new PBError("missing-cli", `pb CLI not found at '${bin}'. Configure 'pastebin.pbPath' or install pb.`));
          return;
        }
        reject(err);
      });
      child.on("close", (code) => {
        if (code === 0) {
          resolve(stdout);
          return;
        }
        // Only the command name goes into the message – never stdin or full output.
        const details = cleanErrorText(stderr) || `exit code ${code}`;
        reject(classifyPBError(`pb ${args.find((a) => !a.startsWith("-")) ?? ""} failed: ${details}`));
      });

      if (stdin !== undefined) {
        child.stdin.write(stdin);
      }
      child.stdin.end();
    });
}
