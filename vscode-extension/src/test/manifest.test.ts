import test from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

interface CommandContribution {
  command: string;
  title: string;
  category?: string;
  icon?: string;
}

interface MenuContribution {
  command: string;
  group?: string;
}

interface PackageManifest {
  icon: string;
  capabilities: { untrustedWorkspaces: { restrictedConfigurations: string[] } };
  repository: {
    type: string;
    url: string;
  };
  homepage: string;
  bugs: {
    url: string;
  };
  license: string;
  keywords: string[];
  contributes: {
    commands: CommandContribution[];
    keybindings: Array<{ command: string; key: string; mac?: string; when?: string }>;
    views: { pastebin: Array<{ id: string; type?: string }> };
    configuration: { properties: Record<string, { scope?: string }> };
    viewsContainers: {
      activitybar: Array<{
        id: string;
        icon: string;
      }>;
    };
    menus: {
      "view/title": MenuContribution[];
      "editor/title": MenuContribution[];
    };
  };
}

const manifest = JSON.parse(
  readFileSync(join(__dirname, "..", "..", "package.json"), "utf8")
) as PackageManifest;
const extensionRoot = join(__dirname, "..", "..");

test("marketplace metadata is publish-ready", () => {
  assert.equal(manifest.icon, "media/icon.png");
  assert.equal(manifest.repository.url, "https://github.com/yesabhishek/pastebin-cli.git");
  assert.equal(manifest.homepage, "https://github.com/yesabhishek/pastebin-cli#readme");
  assert.equal(manifest.bugs.url, "https://github.com/yesabhishek/pastebin-cli/issues");
  assert.equal(manifest.license, "SEE LICENSE IN LICENSE");
  for (const keyword of ["pastebin", "notes", "github", "sync", "encrypted"]) {
    assert.ok(manifest.keywords.includes(keyword), `missing keyword ${keyword}`);
  }
});

test("marketplace and activity icons exist", () => {
  const activityIcon = manifest.contributes.viewsContainers.activitybar
    .find((container) => container.id === "pastebin")?.icon;
  assert.equal(activityIcon, "media/activity-icon.svg");
  assert.ok(existsSync(join(extensionRoot, manifest.icon)), "marketplace icon should exist");
  assert.ok(existsSync(join(extensionRoot, activityIcon ?? "")), "activity icon should exist");
  assert.ok(existsSync(join(extensionRoot, "LICENSE")), "extension LICENSE should exist");
});

test("command titles use category instead of Pastebin prefix", () => {
  for (const command of manifest.contributes.commands) {
    assert.equal(command.category, "Pastebin", `${command.command} should use the Pastebin category`);
    assert.equal(command.title.includes("Pastebin:"), false, `${command.command} should not include a title prefix`);
  }
});

test("primary view title actions are icon-only essentials", () => {
  const primary = manifest.contributes.menus["view/title"]
    .filter((item) => item.group?.startsWith("navigation"))
    .map((item) => item.command);
  assert.deepEqual(primary, ["pastebin.quickNote", "pastebin.sync", "pastebin.switchAccount"]);
  const commands = new Map(manifest.contributes.commands.map((command) => [command.command, command]));
  for (const id of primary) {
    assert.ok(commands.get(id)?.icon?.startsWith("$("), `${id} should have a product icon`);
  }
});

test("quick note has a global keyboard shortcut", () => {
  const binding = manifest.contributes.keybindings.find((k) => k.command === "pastebin.quickNote");
  assert.equal(binding?.mac, "cmd+alt+p");
  assert.equal(binding?.key, "ctrl+alt+p");
  assert.equal(binding?.when, undefined, "quick note should work from anywhere");
});

test("sidebar is a webview view", () => {
  const view = manifest.contributes.views.pastebin.find((v) => v.id === "pastebin.notesView");
  assert.equal(view?.type, "webview");
});

test("pb path cannot be set by a workspace", () => {
  const prop = manifest.contributes.configuration.properties["pastebin.pbPath"];
  assert.equal(prop.scope, "machine");
  assert.ok(manifest.capabilities.untrustedWorkspaces.restrictedConfigurations.includes("pastebin.pbPath"));
});

test("save current editor is in the editor title", () => {
  const action = manifest.contributes.menus["editor/title"].find((item) => item.command === "pastebin.pushCurrentNote");
  assert.ok(action?.group?.startsWith("navigation"));
  const command = manifest.contributes.commands.find((item) => item.command === "pastebin.pushCurrentNote");
  assert.equal(command?.icon, "$(cloud-upload)");
});

test("build local pb requires a trusted workspace", () => {
  const command = manifest.contributes.commands.find((item) => item.command === "pastebin.buildLocalPB") as CommandContribution & { enablement?: string };
  assert.equal(command?.title, "Build Local pb");
  assert.equal(command?.enablement, "isWorkspaceTrusted");
});

test("webview scripts never use innerHTML or eval", () => {
  for (const file of ["sidebar.js", "quicknote.js", "history.js"]) {
    const src = readFileSync(join(extensionRoot, "media", file), "utf8");
    assert.ok(!/innerHTML|outerHTML|insertAdjacentHTML|eval\(|new Function/.test(src), `${file} must not build HTML from strings`);
  }
});
