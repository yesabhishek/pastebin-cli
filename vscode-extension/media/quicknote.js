// Quick-capture page. Text only leaves this webview via postMessage to the
// extension, which pipes it to `pb note --stdin`.
(function () {
  const vscode = acquireVsCodeApi();
  const title = document.getElementById("title");
  const body = document.getElementById("body");
  const save = document.getElementById("save");
  const status = document.getElementById("status");
  const toast = document.getElementById("toast");
  let saving = false;

  function grow() {
    body.style.height = "auto";
    body.style.height = Math.max(body.scrollHeight, window.innerHeight * 0.4) + "px";
  }

  function content() {
    const t = title.value.trim();
    const b = body.value.replace(/\s+$/, "");
    return { title: t, body: b };
  }

  function update() {
    const { title: t, body: b } = content();
    save.disabled = saving || (!t && !b);
    vscode.setState({ title: title.value, body: body.value });
  }

  function flash(text) {
    toast.textContent = text;
    toast.classList.add("show");
    setTimeout(() => toast.classList.remove("show"), 1800);
  }

  function submit() {
    if (save.disabled) return;
    saving = true;
    update();
    status.textContent = "Encrypting & syncing…";
    vscode.postMessage(Object.assign({ type: "save" }, content()));
  }

  title.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !(e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      body.focus();
    }
  });
  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      submit();
    } else if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
      e.preventDefault();
      submit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      vscode.postMessage(Object.assign({ type: "close" }, content()));
    }
  });
  body.addEventListener("input", () => { grow(); update(); });
  title.addEventListener("input", update);
  save.addEventListener("click", submit);
  document.getElementById("discard").addEventListener("click", () => {
    vscode.postMessage({ type: "discard" });
  });

  window.addEventListener("message", (event) => {
    const msg = event.data || {};
    if (msg.type === "init") {
      const restored = vscode.getState() || msg.draft || {};
      title.value = restored.title || "";
      body.value = restored.body || "";
      status.textContent = msg.hint || "";
      grow();
      update();
      (title.value ? body : title).focus();
    } else if (msg.type === "saved") {
      flash(msg.text || "Saved");
    } else if (msg.type === "error") {
      saving = false;
      status.textContent = msg.text || "Could not save";
      update();
    }
  });

  vscode.postMessage({ type: "ready" });
})();
