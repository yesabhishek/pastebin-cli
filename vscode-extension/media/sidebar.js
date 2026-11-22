// Pastebin sidebar webview. All dynamic text is inserted as text nodes –
// never parsed as HTML – so note names cannot inject markup or script.
(function () {
  const vscode = acquireVsCodeApi();
  const saved = vscode.getState() || {};
  const ui = { open: new Set(saved.open || ["notes/"]), query: saved.query || "" };
  let state = null;
  let accountView = null;

  const $ = (sel) => document.querySelector(sel);
  const root = $("#root");

  function persist() {
    vscode.setState({ open: [...ui.open], query: ui.query });
  }

  function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    if (attrs) {
      for (const [k, v] of Object.entries(attrs)) {
        if (v === undefined || v === null || v === false) continue;
        if (k === "class") node.className = v;
        else if (k === "style") node.setAttribute("style", v);
        else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
        else node.setAttribute(k, v === true ? "" : String(v));
      }
    }
    for (const child of children.flat()) {
      if (child === undefined || child === null || child === false) continue;
      node.append(child instanceof Node ? child : document.createTextNode(String(child)));
    }
    return node;
  }

  // Monochrome 24px line icons (drawn with DOM APIs, no markup strings).
  const ICONS = {
    search: ["c:11,11,7", "M20 20l-4.3-4.3"],
    zap: ["M13 2 4 14h7l-1 8 9-12h-7z"],
    plus: ["M12 5v14", "M5 12h14"],
    edit: ["M12 20h9", "M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"],
    sync: ["M20 11a8 8 0 1 0-2.3 5.7", "M20 4v7h-7"],
    clock: ["c:12,12,9", "M12 7v5l3 2"],
    trash: ["M4 7h16", "M9 7V4h6v3", "M18 7l-1 13H7L6 7"],
    copy: ["M9 9h11v11H9z", "M5 15H4V4h11v1"],
    open: ["M7 17 17 7", "M8 7h9v9"],
    lock: ["M5 11h14v10H5z", "M8 11V7a4 4 0 0 1 8 0v4"],
    unlock: ["M5 11h14v10H5z", "M8 11V7a4 4 0 0 1 7.5-2"],
    user: ["c:9,8,4", "M3 21a6 6 0 0 1 12 0", "M19 8v6", "M16 11h6"],
    key: ["c:7.5,15.5,4.5", "M10.7 12.3 20 3", "M17 6l3 3"],
    dots: ["c:5,12,1", "c:12,12,1", "c:19,12,1"]
  };
  const SVG = "http://www.w3.org/2000/svg";
  function icon(name) {
    const svg = document.createElementNS(SVG, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("width", "16");
    svg.setAttribute("height", "16");
    svg.setAttribute("fill", "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "1.8");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
    svg.setAttribute("aria-hidden", "true");
    for (const d of ICONS[name] || []) {
      let shape;
      if (d.startsWith("c:")) {
        const [cx, cy, r] = d.slice(2).split(",");
        shape = document.createElementNS(SVG, "circle");
        shape.setAttribute("cx", cx);
        shape.setAttribute("cy", cy);
        shape.setAttribute("r", r);
        if (name === "dots") shape.setAttribute("fill", "currentColor");
      } else {
        shape = document.createElementNS(SVG, "path");
        shape.setAttribute("d", d);
      }
      svg.append(shape);
    }
    return svg;
  }

  const post = (type, extra) => vscode.postMessage(Object.assign({ type }, extra || {}));

  function row(icon, label, onclick, trail, cls) {
    return el("button", { class: "row " + (cls || ""), onclick, title: label },
      el("span", { class: "ico", "aria-hidden": "true" }, ICONS[icon] ? iconNode(icon) : icon),
      el("span", { class: "label" }, label),
      trail ? el("span", { class: "trail" }, trail) : null);
  }

  function iconNode(name) { return icon(name); }

  function avatarColor(login) {
    const palette = ["#37352f", "#2383e2", "#0f7b6c", "#9065b0", "#d9730d", "#c4554d", "#337ea9"];
    let h = 0;
    for (const c of login || "?") h = (h * 31 + c.charCodeAt(0)) >>> 0;
    return palette[h % palette.length];
  }

  // ---------- render ----------

  function render() {
    root.replaceChildren();
    closeMenu();
    if (!state) {
      root.append(el("div", { class: "empty-hint" }, "Loading…"));
      return;
    }
    if (state.mode === "missing-cli" || state.mode === "outdated-cli") {
      root.append(el("div", { class: "welcome" },
        el("div", { class: "big" }, "🧰"),
        el("h2", null, state.mode === "missing-cli" ? "Install the pb CLI" : "Update the pb CLI"),
        el("p", null, state.error || "Pastebin needs the pb command-line tool."),
        el("div", { class: "actions" },
          el("button", { class: "pb-btn primary", onclick: () => post("buildLocal") }, "Build local pb"),
          " ",
          el("button", { class: "pb-btn outline", onclick: () => post("configurePath") }, "Set pb path"))));
      return;
    }
    if (state.mode === "uninitialized") {
      root.append(el("div", { class: "welcome" },
        el("div", { class: "big" }, "🗒️"),
        el("h2", null, "Welcome to Pastebin"),
        el("p", null, "Private, end-to-end encrypted notes that sync through your own GitHub account."),
        el("button", { class: "pb-btn primary", onclick: () => post("init") }, "Get started")));
      return;
    }
    if (state.mode === "error") {
      root.append(el("div", { class: "banner err" },
        el("strong", null, "Pastebin is unavailable"),
        el("p", null, state.error || "Unknown error"),
        el("div", { class: "actions" },
          el("button", { class: "pb-btn outline", onclick: () => post("refresh") }, "Try again"),
          el("button", { class: "pb-btn", onclick: () => post("switchAccountMenu") }, "Switch account"))));
      return;
    }

    const acc = state.account || { login: "?", repo: "", unlocked: false };
    const avatar = el("span", { class: "pb-avatar", style: "background:" + avatarColor(acc.login) }, (acc.login || "?").charAt(0));
    root.append(
      el("button", { class: "ws", id: "ws", title: "Switch account", onclick: (e) => openAccountMenu(e.currentTarget) },
        avatar,
        el("span", { class: "ws-name" }, acc.login + "’s Pastebin"),
        el("span", { class: "ws-chevron" }, "▾")),
      el("div", { class: "ws-sub", title: acc.repo },
        el("span", { class: "pb-tag " + (acc.unlocked ? "secure" : "pending") }, iconNode(acc.unlocked ? "lock" : "unlock"), acc.unlocked ? "Encrypted" : "Locked"),
        el("span", null, "@" + acc.login))
    );

    if (!acc.unlocked) {
      root.append(el("div", { class: "banner warn" },
        el("strong", null, "Vault locked on this device"),
        el("p", null, "Enter your passphrase once to sync encrypted notes here."),
        el("div", { class: "actions" },
          el("button", { class: "pb-btn primary", onclick: () => post("unlock") }, "Unlock"))));
    }

    const search = el("input", {
      type: "text",
      placeholder: "Search",
      value: ui.query,
      "aria-label": "Search pages",
      oninput: (e) => { ui.query = e.target.value; persist(); renderTree(); },
      onkeydown: (e) => {
        if (e.key === "Enter") {
          const first = document.querySelector(".tree .row.page");
          if (first) first.click();
        }
        if (e.key === "Escape") { e.target.value = ""; ui.query = ""; persist(); renderTree(); }
      }
    });
    root.append(
      el("label", { class: "search" }, el("span", { class: "ico", "aria-hidden": "true" }, iconNode("search")), search),
      row("zap", "Quick note", () => post("quickNote"), el("span", { class: "pb-kbd" }, state.shortcut)),
      row("edit", "New page", () => post("newNote")),
      row("sync", state.busy === "sync" ? "Syncing…" : "Sync", () => post("sync"),
        state.pending ? el("span", { class: "pb-tag pending" }, state.pending + " pending") : null)
    );

    if (state.recent && state.recent.length) {
      const list = el("div", { class: "tree" });
      for (const p of state.recent) list.append(pageRow(p, 0, true));
      root.append(el("div", { id: "recent", class: ui.query ? "hidden" : "" },
        el("div", { class: "section" }, el("span", null, "Recent")), list));
    }

    root.append(el("div", { class: "section" },
      el("span", null, "Pages" + (state.total ? " · " + state.total : "")),
      el("button", { class: "add", title: "New page", onclick: () => post("newNote") }, "+")));
    root.append(el("div", { class: "tree", id: "tree" }));
    renderTree();

    const dotClass = state.busy ? "busy" : state.conflicts ? "err" : state.pending ? "warn" : "";
    const status = state.busy ? "Syncing…"
      : state.conflicts ? state.conflicts + " conflict" + (state.conflicts > 1 ? "s" : "")
      : state.pending ? state.pending + " change" + (state.pending > 1 ? "s" : "") + " waiting to sync"
      : state.lastSync ? "Synced " + state.lastSync : "Up to date";
    root.append(el("div", { class: "footer" }, el("span", { class: "dot " + dotClass }), el("span", null, status)));

    if (document.activeElement === document.body && ui.query) search.focus();
  }

  function matches(node, q) {
    if (node.kind === "page") {
      return node.title.toLowerCase().includes(q) || node.path.toLowerCase().includes(q);
    }
    return (node.children || []).some((c) => matches(c, q));
  }

  function renderTree() {
    const tree = $("#tree");
    if (!tree) return;
    tree.replaceChildren();
    const q = ui.query.trim().toLowerCase();
    const recent = $("#recent");
    if (recent) recent.classList.toggle("hidden", !!q);
    const nodes = (state.pages || []).filter((n) => !q || matches(n, q));
    if (!nodes.length) {
      tree.append(el("div", { class: "empty-hint" }, q ? "No pages match “" + ui.query + "”" : "No pages yet. Press " + state.shortcut + " to jot one down."));
      return;
    }
    const walk = (list, depth) => {
      for (const n of list) {
        if (q && !matches(n, q)) continue;
        if (n.kind === "folder") {
          const open = q ? true : ui.open.has(n.path);
          tree.append(el("button", {
            class: "row folder" + (open ? " open" : ""),
            style: "--depth:" + depth,
            "aria-expanded": String(open),
            title: n.path,
            onclick: () => { open ? ui.open.delete(n.path) : ui.open.add(n.path); persist(); renderTree(); }
          },
            el("span", { class: "twist", "aria-hidden": "true" }, "▶"),
            el("span", { class: "ico", "aria-hidden": "true" }, n.emoji),
            el("span", { class: "label" }, n.title)));
          if (open) walk(n.children || [], depth + 1);
        } else {
          tree.append(pageRow(n, depth, false));
        }
      }
    };
    walk(nodes, 0);
  }

  function pageRow(n, depth, compact) {
    const trail = [];
    for (const t of n.tags || []) trail.push(el("span", { class: "pb-tag " + t }, t));
    if (compact && n.subtitle) trail.push(el("span", { class: "sub" }, n.subtitle.split(" · ")[0]));
    trail.push(el("span", {
      class: "more", role: "button", tabindex: "0", title: "More actions",
      onclick: (e) => { e.stopPropagation(); openPageMenu(e.currentTarget, n); },
      onkeydown: (e) => { if (e.key === "Enter") { e.stopPropagation(); openPageMenu(e.currentTarget, n); } }
    }, iconNode("dots")));
    return el("div", {
      class: "row page", role: "button", tabindex: "0",
      style: "--depth:" + depth,
      title: n.path + (n.subtitle ? "\n" + n.subtitle : ""),
      onclick: () => post("open", { path: n.path }),
      onkeydown: (e) => { if (e.key === "Enter") post("open", { path: n.path }); },
      oncontextmenu: (e) => { e.preventDefault(); openPageMenu(e.currentTarget, n); }
    },
      el("span", { class: "twist", "aria-hidden": "true", style: "visibility:hidden" }),
      el("span", { class: "ico", "aria-hidden": "true" }, n.emoji),
      el("span", { class: "label" }, n.title),
      el("span", { class: "trail" }, trail));
  }

  // ---------- menus ----------

  let menu = null;
  function closeMenu() {
    if (menu) { menu.remove(); menu = null; }
  }
  function showMenu(anchor, items) {
    closeMenu();
    menu = el("div", { class: "menu", role: "menu" }, items);
    document.body.append(menu);
    const r = anchor.getBoundingClientRect();
    const w = menu.offsetWidth, h = menu.offsetHeight;
    menu.style.left = Math.max(6, Math.min(r.left, window.innerWidth - w - 6)) + "px";
    menu.style.top = (r.bottom + h + 6 > window.innerHeight ? Math.max(6, r.top - h - 4) : r.bottom + 4) + "px";
    const first = menu.querySelector("button");
    if (first) first.focus();
  }
  document.addEventListener("mousedown", (e) => { if (menu && !menu.contains(e.target)) closeMenu(); });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeMenu();
    if (e.key === "/" && document.activeElement.tagName !== "INPUT") {
      e.preventDefault();
      const s = document.querySelector(".search input");
      if (s) s.focus();
    }
  });

  function openPageMenu(anchor, n) {
    const act = (type) => () => { closeMenu(); post(type, { path: n.path }); };
    showMenu(anchor, [
      el("div", { class: "hdr" }, n.path),
      row("open", "Open", act("open")),
      row("clock", "Version history", act("history")),
      row("copy", "Copy path", act("copyPath")),
      el("div", { class: "sep" }),
      row("trash", "Delete", act("delete"), null, "danger")
    ]);
  }

  function openAccountMenu(anchor) {
    accountView = null;
    showMenu(anchor, [el("div", { class: "hdr" }, "Loading accounts…")]);
    post("accounts");
  }

  function renderAccountMenu() {
    const anchor = $("#ws");
    if (!anchor || !accountView) return;
    const items = [el("div", { class: "hdr" }, "Sync notes as")];
    for (const a of accountView.accounts) {
      const current = a.login === accountView.current;
      items.push(el("button", {
        class: "row", title: a.ok ? "Switch to @" + a.login : "Session expired – run gh auth login",
        onclick: () => { closeMenu(); if (!current) post("switchAccount", { login: a.login }); }
      },
        el("span", { class: "pb-avatar", style: "width:18px;height:18px;font-size:10px;background:" + avatarColor(a.login) }, a.login.charAt(0)),
        el("span", { class: "label" }, "@" + a.login + (a.ok ? "" : " (expired)")),
        current ? el("span", { class: "check" }, "✓") : null));
    }
    items.push(el("div", { class: "sep" }));
    items.push(row("user", "Add GitHub account…", () => { closeMenu(); post("addAccount"); }));
    if (state && state.account && state.account.unlocked) {
      items.push(row("lock", "Lock vault on this device", () => { closeMenu(); post("lock"); }));
    } else {
      items.push(row("unlock", "Unlock vault…", () => { closeMenu(); post("unlock"); }));
    }
    items.push(row("key", "Change passphrase…", () => { closeMenu(); post("changePassphrase"); }));
    showMenu(anchor, items);
  }

  window.addEventListener("message", (event) => {
    const msg = event.data;
    if (!msg || typeof msg.type !== "string") return;
    if (msg.type === "state") { state = msg.state; render(); }
    else if (msg.type === "accounts") { accountView = msg.view; renderAccountMenu(); }
    else if (msg.type === "focusSearch") { const s = document.querySelector(".search input"); if (s) s.focus(); }
  });

  render();
  post("ready");
})();
