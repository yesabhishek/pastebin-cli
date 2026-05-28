import * as vscode from "vscode";
import { PBClient } from "./pbClient";
import {
  NOTE_SCHEME,
  PastebinDocumentProvider,
  PastebinNoteItem,
  PastebinTreeProvider,
  makeNoteURI,
  notePathFromURI
} from "./pastebinView";
import { buildLocalPB, repoRoot } from "./localBuilder";
import { PBError } from "./pbClient";

const COMMANDS = {
  refresh: "pastebin.refresh",
  init: "pastebin.init",
  sync: "pastebin.sync",
  newNote: "pastebin.newNote",
  pushCurrentNote: "pastebin.pushCurrentNote",
  buildLocalPB: "pastebin.buildLocalPB",
  openNote: "pastebin.openNote",
  deleteNote: "pastebin.deleteNote",
  showStatus: "pastebin.showStatus",
  configurePbPath: "pastebin.configurePbPath",
  setFilter: "pastebin.setFilter"
};

export function activate(context: vscode.ExtensionContext): void {
  const client = new PBClient(() => getPBPath());
  const tree = new PastebinTreeProvider(client);
  const docs = new PastebinDocumentProvider(client);

  context.subscriptions.push(
    vscode.workspace.registerTextDocumentContentProvider(NOTE_SCHEME, docs)
  );
  context.subscriptions.push(
    vscode.window.registerTreeDataProvider("pastebin.notesView", tree)
  );

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.refresh, async () => {
    await withPBHandling(async () => {
      await tree.refresh();
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.init, async () => {
    await withPBHandling(async () => {
      await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Initializing Pastebin"
        },
        async () => {
          await client.init();
          await tree.refresh();
        }
      );
      vscode.window.showInformationMessage("Pastebin initialized. Create your first note and push it when ready.");
      await openNewNote();
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.sync, async () => {
    await withPBHandling(async () => {
      const result = await client.sync();
      await tree.refresh();
      vscode.window.showInformationMessage(
        `Synced. Pulled ${result.pulled.length}, pushed ${result.pushed.length}, deleted ${result.deleted.length}, conflicts ${result.conflicts.length}.`
      );
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.newNote, async () => {
    await openNewNote();
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.pushCurrentNote, async () => {
    await withPBHandling(async () => {
      await ensureSaveSupport(client);
      const editor = vscode.window.activeTextEditor;
      if (!editor) {
        throw new Error("No active editor to push.");
      }
      const doc = editor.document;
      const existingPath = notePathFromURI(doc.uri);
      const proposedPath = existingPath ?? suggestPathFromDocument(doc);
      const path = await vscode.window.showInputBox({
        title: "Pastebin Path",
        prompt: "Enter note path in pastebin (for example notes/today.md)",
        value: proposedPath
      });
      if (!path) {
        return;
      }
      const result = await client.save(path, doc.getText());
      await tree.refresh();
      if (existingPath) {
        docs.refresh(doc.uri);
      }
      vscode.window.showInformationMessage(result.message || `Saved ${path}`);
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.buildLocalPB, async () => {
    await withPBHandling(async () => {
      await buildAndConfigureLocalPB(context, tree);
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.openNote, async (item?: PastebinNoteItem) => {
    await withPBHandling(async () => {
      const path = item?.notePath ?? await pickNotePath(tree);
      if (!path) {
        return;
      }
      const uri = makeNoteURI(path);
      const doc = await vscode.workspace.openTextDocument(uri);
      await vscode.window.showTextDocument(doc, { preview: false });
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.deleteNote, async (item?: PastebinNoteItem) => {
    await withPBHandling(async () => {
      const path = item?.notePath ?? await pickNotePath(tree);
      if (!path) {
        return;
      }
      const confirm = await vscode.window.showWarningMessage(
        `Delete ${path} from pastebin?`,
        { modal: true },
        "Delete"
      );
      if (confirm !== "Delete") {
        return;
      }
      await client.delete(path);
      await tree.refresh();
      vscode.window.showInformationMessage(`Deleted ${path}`);
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.showStatus, async () => {
    await withPBHandling(async () => {
      const status = tree.getStatus() ?? await client.status();
      vscode.window.showInformationMessage(
        `GitHub @${status.login} | Repo ${status.repo} | Files ${status.total_files} | Pending ${status.pending_writes.length} | Conflicts ${status.conflicts.length} | pb ${getPBPath()}`
      );
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.configurePbPath, async () => {
    const cfg = vscode.workspace.getConfiguration("pastebin");
    const current = getPBPath();
    const value = await vscode.window.showInputBox({
      title: "Configure pb Path",
      prompt: "Set pb binary path",
      value: current
    });
    if (!value) {
      return;
    }
    await cfg.update("pbPath", value, vscode.ConfigurationTarget.Global);
  }));

  context.subscriptions.push(vscode.commands.registerCommand(COMMANDS.setFilter, async () => {
    const value = await vscode.window.showInputBox({
      title: "Filter Pastebin Notes",
      prompt: "Filter notes by path text",
      value: tree.getFilter()
    });
    if (value === undefined) {
      return;
    }
    tree.setFilter(value);
  }));

  void withPBHandling(async () => {
    await tree.refresh();
  });
}

export function deactivate(): void {}

function getPBPath(): string {
  return vscode.workspace.getConfiguration("pastebin").get<string>("pbPath", "pb");
}

async function openNewNote(): Promise<void> {
  const doc = await vscode.workspace.openTextDocument({ content: "", language: "markdown" });
  await vscode.window.showTextDocument(doc, { preview: false });
  vscode.window.showInformationMessage("Write your note, then run Pastebin: Push Current Note.");
}

async function ensureSaveSupport(client: PBClient): Promise<void> {
  if (await client.supportsSave()) {
    return;
  }
  const selected = await vscode.window.showErrorMessage(
    `The configured pb CLI does not support Push Current Note: ${getPBPath()}`,
    { modal: true },
    "Build Local pb",
    "Configure pb Path"
  );
  if (selected === "Build Local pb") {
    await vscode.commands.executeCommand(COMMANDS.buildLocalPB);
    return;
  }
  if (selected === "Configure pb Path") {
    await vscode.commands.executeCommand(COMMANDS.configurePbPath);
    return;
  }
  throw new PBError("unsupported-cli", `The configured pb CLI does not support Push Current Note: ${getPBPath()}`);
}

async function buildAndConfigureLocalPB(context: vscode.ExtensionContext, tree: PastebinTreeProvider): Promise<void> {
  const root = repoRoot(context);
  if (!root) {
    throw new Error("Open the pastebin-cli repository workspace to build a local pb binary.");
  }
  const target = await vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: "Building local pb"
    },
    async () => await buildLocalPB(root)
  );
  await vscode.workspace.getConfiguration("pastebin").update("pbPath", target, vscode.ConfigurationTarget.Global);
  await tree.refresh();
  vscode.window.showInformationMessage(`Pastebin now uses ${target}`);
}

async function pickNotePath(tree: PastebinTreeProvider): Promise<string | undefined> {
  if (tree.getNotePaths().length === 0) {
    await tree.refresh();
  }
  const picked = await vscode.window.showQuickPick(tree.getNotePaths(), {
    title: "Select Pastebin Note"
  });
  return picked ?? undefined;
}

function suggestPathFromDocument(doc: vscode.TextDocument): string {
  if (doc.isUntitled) {
    return "notes/new-note.md";
  }
  const name = doc.fileName.split(/[\\/]/).pop() ?? "note.md";
  return `notes/${name}`;
}

async function withPBHandling(fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    vscode.window.showErrorMessage(message);
  }
}
