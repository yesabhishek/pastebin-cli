import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import * as vscode from "vscode";

export function repoRoot(context: vscode.ExtensionContext): string | undefined {
  const folders = vscode.workspace.workspaceFolders ?? [];
  for (const folder of folders) {
    if (folder.uri.scheme !== "file") {
      continue;
    }
    if (context.extensionUri.scheme === "file" && context.extensionUri.fsPath.startsWith(folder.uri.fsPath)) {
      return folder.uri.fsPath;
    }
  }
  return folders.find((folder) => folder.uri.scheme === "file")?.uri.fsPath;
}

export async function buildLocalPB(root: string): Promise<string> {
  const binDir = join(root, "bin");
  const target = join(binDir, process.platform === "win32" ? "pb.exe" : "pb");
  await mkdir(binDir, { recursive: true });
  await run("go", ["build", "-o", target, "./cmd/pb"], root);
  return target;
}

function run(command: string, args: string[], cwd: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: "pipe" });
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
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(stderr.trim() || stdout.trim() || `${command} exited with ${code}`));
    });
  });
}
