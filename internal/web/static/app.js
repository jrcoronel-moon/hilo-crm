// Hilo CRM — the only script on the site (CSP: script-src 'self'). No
// dependencies. Every feature degrades: without JS, forms still submit.
"use strict";

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

// ───── toasts ─────
function toast(text, opts = {}) {
  const box = $("#toasts");
  if (!box) return;
  const t = document.createElement("div");
  t.className = "toast" + (opts.error ? " err" : "");
  t.setAttribute("role", opts.error ? "alert" : "status");
  const ic = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  ic.setAttribute("class", "i sm");
  const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
  use.setAttribute("href", opts.error ? "#i-alert" : "#i-check");
  ic.append(use);
  const span = document.createElement("span");
  span.textContent = text; // never innerHTML: messages may contain user data
  t.append(ic, span);
  if (opts.action) {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = opts.action.label;
    b.addEventListener("click", () => { t.remove(); opts.action.run(); });
    t.append(b);
  }
  box.append(t);
  setTimeout(() => t.remove(), opts.action ? 8000 : 4000);
}
$$(".toast[data-autohide]").forEach((t) => setTimeout(() => t.remove(), 4000));

// ───── confirmation dialog: <form data-confirm="Title|Body"> ─────
const dlg = $("#confirm");
document.addEventListener("submit", (e) => {
  const form = e.target;
  if (!(form instanceof HTMLFormElement) || !form.dataset.confirm || form.dataset.confirmed || !dlg) return;
  e.preventDefault();
  const [title, body] = form.dataset.confirm.split("|");
  $("[data-title]", dlg).textContent = title || "¿Confirmar?";
  $("[data-body]", dlg).textContent = body || "";
  dlg.returnValue = "";
  dlg.showModal();
  dlg.addEventListener("close", () => {
    if (dlg.returnValue === "ok") {
      form.dataset.confirmed = "1";
      form.requestSubmit(e.submitter || undefined);
    }
  }, { once: true });
});

// ───── small behaviours ─────
document.addEventListener("change", (e) => {
  const el = e.target;
  if (el instanceof HTMLSelectElement && el.hasAttribute("data-autosubmit") && el.form) el.form.requestSubmit();
});
document.addEventListener("click", async (e) => {
  const el = e.target instanceof Element ? e.target.closest("[data-copy]") : null;
  if (!el) return;
  try {
    await navigator.clipboard.writeText(el.getAttribute("data-copy") || "");
    toast(el.getAttribute("data-copied") || "Copiado");
  } catch { /* clipboard unavailable: the text stays selectable */ }
});
// OAuth hand-back: script navigation is not subject to CSP form-action.
const nav = $("a[data-navigate]");
if (nav) window.location.assign(nav.href);

// ───── generic drag-to-reorder within a container ─────
function sortable(container, itemSel, onDrop) {
  let dragged = null;
  container.addEventListener("dragstart", (e) => {
    dragged = e.target instanceof Element ? e.target.closest(itemSel) : null;
    if (!dragged) return;
    dragged.classList.add("dragging");
    e.dataTransfer.effectAllowed = "move";
  });
  container.addEventListener("dragend", () => { if (dragged) dragged.classList.remove("dragging"); dragged = null; });
  container.addEventListener("dragover", (e) => {
    if (!dragged || !container.contains(dragged)) return;
    e.preventDefault();
    const after = $$(itemSel + ":not(.dragging)", container).find((el) => e.clientY < el.getBoundingClientRect().top + el.offsetHeight / 2);
    if (after) container.insertBefore(dragged, after); else container.append(dragged);
  });
  container.addEventListener("drop", (e) => { e.preventDefault(); if (onDrop) onDrop(); });
}

// ───── pipeline editor ─────
$$("form[data-pipeline]").forEach((form) => {
  const list = $("[data-stages]", form);
  const tpl = $("template[data-new-stage]", form);
  sortable(list, "[data-stage-row]");
  $("[data-add-stage]", form)?.addEventListener("click", () => {
    list.append(tpl.content.cloneNode(true));
    $$("[data-stage-row]", list).at(-1)?.querySelector("input[type=text]")?.focus();
  });
  list.addEventListener("click", (e) => {
    const btn = e.target instanceof Element ? e.target.closest("[data-remove-stage]") : null;
    if (!btn) return;
    const row = btn.closest("[data-stage-row]");
    const name = row.querySelector("input[name=stage_name]");
    const id = row.querySelector("input[name=stage_id]")?.value;
    if (!id) { row.remove(); return; }
    // Existing stage: an empty name tells the server to delete it.
    const removed = row.classList.toggle("removed");
    if (removed) { name.dataset.original = name.value; name.value = ""; name.placeholder = name.dataset.original + " (se elimina)"; }
    else { name.value = name.dataset.original || ""; }
  });
});

// ───── funnel drag & drop (see board.html) ─────
const board = $("[data-board]");
if (board) setupBoard(board);

function setupBoard(board) {
  const csrf = board.dataset.csrf;
  let dragged = null;
  board.addEventListener("dragstart", (e) => {
    dragged = e.target instanceof Element ? e.target.closest("[data-opp]") : null;
    if (!dragged) return;
    dragged.classList.add("dragging");
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", dragged.dataset.opp);
  });
  board.addEventListener("dragend", () => {
    dragged?.classList.remove("dragging");
    $$(".col.drop", board).forEach((c) => c.classList.remove("drop"));
  });
  board.addEventListener("dragover", (e) => {
    const col = e.target instanceof Element ? e.target.closest(".col") : null;
    if (!dragged || !col) return;
    e.preventDefault();
    $$(".col.drop", board).forEach((c) => c !== col && c.classList.remove("drop"));
    col.classList.add("drop");
  });
  board.addEventListener("drop", async (e) => {
    const col = e.target instanceof Element ? e.target.closest(".col") : null;
    if (!dragged || !col) return;
    e.preventDefault();
    col.classList.remove("drop");
    const card = dragged;
    const from = card.closest(".col");
    if (from === col) return;
    let reason = "";
    if (col.dataset.category === "lost") {
      reason = await askReason(col.dataset.stageName);
      if (reason === null) return;
    }
    move(card, from, col, reason, true);
  });

  async function move(card, from, to, reason, undoable) {
    $("ol", to).prepend(card);
    recount(from); recount(to);
    const body = new URLSearchParams({ csrf, opportunity: card.dataset.opp, stage: to.dataset.stage, reason });
    let res;
    try {
      res = await fetch("/board/move", { method: "POST", body, headers: { Accept: "application/json" }, credentials: "same-origin" });
    } catch { res = null; }
    if (!res || !res.ok) {
      $("ol", from).prepend(card); recount(from); recount(to);
      let msg = "No se pudo mover la oportunidad.";
      try { msg = (await res.json()).error || msg; } catch { /* keep generic */ }
      toast(msg, { error: true });
      return;
    }
    card.classList.add("fresh");
    setTimeout(() => card.classList.remove("fresh"), 4000);
    const title = $(".t", card)?.textContent || "Oportunidad";
    toast(`${title} → ${to.dataset.stageName}`, undoable ? {
      action: { label: "Deshacer", run: () => move(card, to, from, "", false) },
    } : {});
  }

  function recount(col) {
    const n = $(".n", col);
    if (n) n.textContent = String($$("[data-opp]", col).length);
  }
}

// Ask for the reason of a lost deal with the shared dialog; resolves to the
// text, or null when cancelled.
function askReason(stage) {
  return new Promise((resolve) => {
    const d = $("#reason");
    if (!d) { resolve(""); return; }
    $("[data-stage]", d).textContent = stage;
    const input = $("input", d);
    input.value = "";
    d.returnValue = "";
    d.showModal();
    d.addEventListener("close", () => resolve(d.returnValue === "ok" && input.value.trim() ? input.value.trim() : null), { once: true });
  });
}

// ───── dialogs: <button data-open="#id">, <button data-close> ─────
document.addEventListener("click", (e) => {
  const opener = e.target instanceof Element ? e.target.closest("[data-open]") : null;
  if (opener) {
    const d = $(opener.dataset.open);
    if (d instanceof HTMLDialogElement) { d.showModal(); $("input:not([type=hidden])", d)?.focus(); }
    return;
  }
  const closer = e.target instanceof Element ? e.target.closest("[data-close]") : null;
  if (closer) closer.closest("dialog")?.close();
});

// ───── bulk selection (opportunities list) ─────
$$("form[data-bulk]").forEach((form) => {
  const bar = $("[data-bulkbar]", form);
  const boxes = () => $$("input[name=id]", form);
  const update = () => {
    const n = boxes().filter((b) => b.checked).length;
    if (bar) { bar.hidden = n === 0; $("[data-count]", bar).textContent = String(n); }
    boxes().forEach((b) => b.closest("tr")?.classList.toggle("sel", b.checked));
  };
  form.addEventListener("change", (e) => {
    if (e.target instanceof HTMLInputElement && e.target.hasAttribute("data-select-all")) {
      boxes().forEach((b) => { b.checked = e.target.checked; });
    }
    update();
  });
  form.addEventListener("submit", (e) => {
    if (e.submitter?.value === "lost" && !$("input[name=reason]", form)?.value.trim()) {
      e.preventDefault();
      toast("Escribí el motivo de pérdida antes de marcar.", { error: true });
      $("input[name=reason]", form)?.focus();
    }
  });
});

// ───── ⌘K / Ctrl+K focuses the search box ─────
document.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
    const s = $("input[data-hotkey=k]") || $(".search input");
    if (s) { e.preventDefault(); s.focus(); s.select(); }
  }
});
