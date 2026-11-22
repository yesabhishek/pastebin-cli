// Version history page: list on the right, decrypted preview on the left.
(function () {
  const vscode = acquireVsCodeApi();
  const list = document.getElementById("versions");
  const preview = document.getElementById("preview");
  const meta = document.getElementById("meta");
  const restore = document.getElementById("restore");
  const heading = document.getElementById("heading");
  let versions = [];
  let selected = null;

  const fmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
  const rel = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  function ago(ts) {
    const diff = (Date.parse(ts) - Date.now()) / 1000;
    const units = [["year", 31536000], ["month", 2592000], ["day", 86400], ["hour", 3600], ["minute", 60]];
    for (const [u, s] of units) {
      if (Math.abs(diff) >= s) return rel.format(Math.round(diff / s), u);
    }
    return "just now";
  }

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  function renderList() {
    list.replaceChildren();
    if (!versions.length) {
      list.append(el("div", "empty", "No synced versions yet."));
      return;
    }
    versions.forEach((v, i) => {
      const item = el("button", "version" + (selected && selected.id === v.id ? " active" : ""));
      item.append(el("div", "when", ago(v.timestamp)));
      const sub = el("div", "what");
      sub.append(el("span", "pb-tag" + (i === 0 ? " secure" : ""), i === 0 ? "current" : v.reason));
      sub.append(el("span", "time", fmt.format(new Date(v.timestamp))));
      item.append(sub);
      item.addEventListener("click", () => select(v));
      list.append(item);
    });
  }

  function select(v) {
    selected = v;
    renderList();
    preview.textContent = "Decrypting…";
    preview.classList.add("loading");
    meta.textContent = fmt.format(new Date(v.timestamp)) + " · " + v.id;
    restore.disabled = versions[0] && versions[0].id === v.id;
    vscode.postMessage({ type: "show", id: v.id });
  }

  restore.addEventListener("click", () => {
    if (selected) vscode.postMessage({ type: "restore", id: selected.id });
  });
  document.getElementById("refresh").addEventListener("click", () => vscode.postMessage({ type: "reload" }));
  document.addEventListener("keydown", (e) => {
    if (!versions.length || !selected) return;
    const idx = versions.findIndex((v) => v.id === selected.id);
    if (e.key === "ArrowDown" && idx < versions.length - 1) { e.preventDefault(); select(versions[idx + 1]); }
    if (e.key === "ArrowUp" && idx > 0) { e.preventDefault(); select(versions[idx - 1]); }
  });

  window.addEventListener("message", (event) => {
    const msg = event.data || {};
    if (msg.type === "versions") {
      heading.textContent = msg.title;
      versions = Array.isArray(msg.versions) ? msg.versions : [];
      selected = null;
      renderList();
      if (versions.length) select(versions[0]);
      else { preview.textContent = ""; meta.textContent = ""; restore.disabled = true; }
    } else if (msg.type === "content" && selected && msg.id === selected.id) {
      preview.classList.remove("loading");
      preview.textContent = msg.content;
    } else if (msg.type === "error") {
      preview.classList.remove("loading");
      preview.textContent = msg.text;
    }
  });

  vscode.postMessage({ type: "ready" });
})();
