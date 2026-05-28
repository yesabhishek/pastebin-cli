import { spawn } from "node:child_process";

export type PBErrorCode = "uninitialized" | "missing-cli" | "command-failed" | "legacy-json-flag-order" | "unsupported-cli";

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
}

export interface StatusReport {
  login: string;
  repo: string;
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
}

export interface VersionEntry {
  id: string;
  commit_sha: string;
  path: string;
  timestamp: string;
  reason: string;
}

type PBRunner = (args: string[], stdin?: string) => Promise<string>;

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
    return await this.runJSON<TrackedFile[]>(args);
  }

  async init(): Promise<void> {
    await this.run(["init"]);
  }

  async supportsSave(): Promise<boolean> {
    const help = await this.run(["help"]);
    return help.split(/\r?\n/).some((line) => line.trim().startsWith("pb save "));
  }

  async status(): Promise<StatusReport> {
    const status = await this.runJSON<StatusReport>(["status"]);
    return normalizeStatus(status);
  }

  async sync(): Promise<SyncResult> {
    const result = await this.runJSON<SyncResult>(["sync"]);
    return normalizeSync(result);
  }

  async readText(path: string): Promise<string> {
    const payload = await this.runJSON<{ content?: string; binary?: boolean }>(["read", path]);
    if (payload.binary) {
      throw new Error("This note is binary. Use pb read --out for binary content.");
    }
    return payload.content ?? "";
  }

  async save(path: string, content: string): Promise<SaveResult> {
    try {
      return await this.runJSON<SaveResult>(["save", path, "--stdin"], content, "suffix");
    } catch (err) {
      if (err instanceof PBError && err.message.toLowerCase().includes("unknown command: save")) {
        throw new PBError("unsupported-cli", "Update pb CLI to use Push Current Note from VS Code.", undefined);
      }
      throw err;
    }
  }

  async delete(path: string): Promise<void> {
    await this.runJSON<Record<string, string>>(["delete", path, "--yes"]);
  }

  async versions(path: string): Promise<VersionEntry[]> {
    return await this.runJSON<VersionEntry[]>(["versions", path]);
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
  return new PBError("command-failed", `Failed to parse pb JSON output: ${String(cause)}\nOutput: ${output}`);
}

export function classifyPBError(message: string): PBError {
  const lower = message.toLowerCase();
  if (lower.includes("run `pb init` first") || lower.includes("run pb init first")) {
    return new PBError("uninitialized", message);
  }
  if (lower.includes("pb cli not found")) {
    return new PBError("missing-cli", message);
  }
  if (lower.includes("unknown command: save")) {
    return new PBError("unsupported-cli", "Update pb CLI to use Push Current Note from VS Code.");
  }
  return new PBError("command-failed", message);
}

function createProcessRunner(getBinaryPath: () => string): PBRunner {
  return async (args: string[], stdin?: string): Promise<string> =>
    await new Promise<string>((resolve, reject) => {
      const bin = getBinaryPath();
      const child = spawn(bin, args, { stdio: "pipe" });
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
        const details = (stderr.trim() || stdout.trim() || `exit code ${code}`);
        reject(classifyPBError(`pb command failed: ${bin} ${args.join(" ")}\n${details}`));
      });

      if (stdin !== undefined) {
        child.stdin.write(stdin);
      }
      child.stdin.end();
    });
}
