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
  for (const keyword of ["pastebin", "notes", "scratchpad", "github", "sync", "clipboard"]) {
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

  assert.deepEqual(primary, [
    "pastebin.newNote",
    "pastebin.sync",
    "pastebin.refresh"
  ]);
});

test("primary view title actions all have product icons", () => {
  const commands = new Map(manifest.contributes.commands.map((command) => [command.command, command]));
  for (const commandID of ["pastebin.newNote", "pastebin.sync", "pastebin.refresh"]) {
    const command = commands.get(commandID);
    assert.ok(command?.icon?.startsWith("$("), `${commandID} should have a product icon`);
  }
});

test("push current note is available from editor title with product icon", () => {
  const action = manifest.contributes.menus["editor/title"]
    .find((item) => item.command === "pastebin.pushCurrentNote");
  assert.ok(action, "push current note should be available in editor title");
  assert.equal(action?.group, "navigation@1");

  const command = manifest.contributes.commands
    .find((item) => item.command === "pastebin.pushCurrentNote");
  assert.equal(command?.icon, "$(cloud-upload)");
});

test("build local pb command is available in command palette", () => {
  const command = manifest.contributes.commands
    .find((item) => item.command === "pastebin.buildLocalPB");
  assert.equal(command?.title, "Build Local pb");
  assert.equal(command?.category, "Pastebin");
  assert.equal(command?.icon, "$(tools)");
});

test("filter is available only from overflow view menu", () => {
  const filter = manifest.contributes.menus["view/title"].find((item) => item.command === "pastebin.setFilter");
  assert.ok(filter, "filter command should be present in the view title menu");
  assert.equal(filter?.group?.startsWith("navigation"), undefined);
});
