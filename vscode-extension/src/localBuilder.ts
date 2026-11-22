import { spawn } from "node:child_process";
import { mkdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import * as vscode from "vscode";

const MODULE_LINE = /^module\s+github\.com\/yesabhishek\/pastebin-cli\s*$/m;

/** Returns the pastebin-cli checkout in the workspace, if any. */
export async function repoRoot(): Promise<string | undefined> {
  for (const folder of vscode.workspace.workspaceFolders ?? []) {
    if (folder.uri.scheme !== "file") {
      continue;
    }
    try {
      const mod = await readFile(join(folder.uri.fsPath, "go.mod"), "utf8");
      if (MODULE_LINE.test(mod)) {
        return folder.uri.fsPath;
      }
    } catch {
      // not this folder
    }
  }
  return undefined;
}

/**
 * Builds pb from the pastebin-cli source in the workspace. Building runs the Go
 * toolchain on workspace files, so it is only allowed in trusted workspaces.
 */
export async function buildLocalPB(root: string): Promise<string> {
  if (!vscode.workspace.isTrusted) {
    throw new Error("Building pb requires a trusted workspace.");
  }
  const binDir = join(root, "bin");
  const target = join(binDir, process.platform === "win32" ? "pb.exe" : "pb");
  await mkdir(binDir, { recursive: true });
  await run("go", ["build", "-trimpath", "-o", target, "./cmd/pb"], root);
  return target;
}

function run(command: string, args: string[], cwd: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: "pipe", env: { ...process.env, GOFLAGS: "-mod=readonly" } });
    let stderr = "";
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => {
      stderr += chunk;
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(stderr.trim().slice(0, 2000) || `${command} exited with ${code}`));
    });
  });
}
