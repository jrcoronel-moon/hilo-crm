// Hilo CRM — the only script on the site (CSP: script-src 'self'). No
// dependencies. Every feature degrades: without JS, forms still submit.
"use strict";

// Interface strings in the page's language (a JSON block the server writes,
// keyed by the Spanish text). "%s" and "%d" are filled in order.
const I18N = (() => {
  try { return JSON.parse(document.getElementById("i18n")?.textContent || "{}"); } catch { return {}; }
})();
function tr(msg, ...args) {
  let i = 0;
  return (I18N[msg] || msg).replace(/%[sd]/g, () => String(args[i++] ?? ""));
}

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

// Dialog buttons close their dialog with a value (no <form method=dialog>,
// so the page carries no form without a CSRF token).
document.addEventListener("click", (e) => {
  const b = e.target instanceof Element ? e.target.closest("[data-dlg]") : null;
  if (b) b.closest("dialog")?.close(b.dataset.dlg);
});
document.addEventListener("keydown", (e) => {
  const d = document.querySelector("dialog[open]");
  if (e.key === "Enter" && d && e.target instanceof HTMLInputElement && d.contains(e.target) && !e.target.form) {
    e.preventDefault();
    d.close("ok");
  }
});

// ───── confirmation dialog: <form data-confirm="Title|Body"> ─────
const dlg = $("#confirm");
document.addEventListener("submit", (e) => {
  const form = e.target;
  if (!(form instanceof HTMLFormElement) || !form.dataset.confirm || form.dataset.confirmed || !dlg) return;
  if (e.submitter instanceof Element && e.submitter.hasAttribute("data-noconfirm")) return; // e.g. "validate" next to "import"
  e.preventDefault();
  const [title, body] = form.dataset.confirm.split("|");
  $("[data-title]", dlg).textContent = title || tr("¿Confirmar?");
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
    toast(el.getAttribute("data-copied") || tr("Copiado"));
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
    if (removed) { name.dataset.original = name.value; name.value = ""; name.placeholder = tr("%s (se elimina)", name.dataset.original); }
    else { name.value = name.dataset.original || ""; }
  });
});

// ───── funnel drag & drop (see board.html) ─────
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
      let msg = tr("No se pudo mover la oportunidad.");
      try { msg = (await res.json()).error || msg; } catch { /* keep generic */ }
      toast(msg, { error: true });
      return;
    }
    card.classList.add("fresh");
    setTimeout(() => card.classList.remove("fresh"), 4000);
    const title = $(".t", card)?.textContent || "Oportunidad";
    toast(`${title} → ${to.dataset.stageName}`, undoable ? {
      action: { label: tr("Deshacer"), run: () => move(card, to, from, "", false) },
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
    const input = $("[data-reason]", d);
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
function setupBulk(form) {
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
      toast(tr("Escribí el motivo de pérdida antes de marcar."), { error: true });
      $("input[name=reason]", form)?.focus();
    }
  });
}


// ───── ⌘K / Ctrl+K focuses the search box ─────
document.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
    const s = $("input[data-hotkey=k]") || $(".search input");
    if (s) { e.preventDefault(); s.focus(); s.select(); }
  }
});

// ───── sidebar: collapse and resize (persisted in the hilo_side cookie,
// which the server validates and turns into a class: no flash, no inline
// styles) ─────
const shell = $("[data-shell]");
if (shell) {
  const save = (v) => {
    const secure = location.protocol === "https:" ? "; Secure" : "";
    document.cookie = `hilo_side=${v}; Path=/; Max-Age=31536000; SameSite=Lax${secure}`;
  };
  const widthClass = () => [...shell.classList].find((c) => c.startsWith("sw-"));
  $("[data-collapse]")?.addEventListener("click", () => {
    const collapsed = shell.classList.toggle("collapsed");
    save(collapsed ? "c" : (widthClass() || "sw-232").slice(3));
  });
  phoneMenu();
  const handle = $("[data-resizer]");
  handle?.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    handle.setPointerCapture(e.pointerId);
    shell.classList.add("resizing");
    const left = shell.getBoundingClientRect().left + 16;
    let w = 0;
    const move = (ev) => {
      w = Math.min(360, Math.max(200, Math.round((ev.clientX - left) / 20) * 20));
      const old = widthClass();
      if (old) shell.classList.remove(old);
      shell.classList.add("sw-" + w);
    };
    const up = () => {
      shell.classList.remove("resizing");
      handle.removeEventListener("pointermove", move);
      if (w) save(String(w));
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up, { once: true });
  });
  handle?.addEventListener("dblclick", () => {
    const old = widthClass();
    if (old) shell.classList.remove(old);
    save("");
  });
}

// ───── phones: the navigation is a drawer from the left. It opens with the
// menu button or by swiping right anywhere on the page, follows the finger,
// and closes swiping left, tapping the page or pressing Escape. Swipes that
// start on fields or on content that scrolls sideways (the board, tables,
// code) are left alone. ─────
function phoneMenu() {
  const menu = $("[data-menu]");
  const drawer = $("#side-menu");
  const main = $(".main");
  if (!shell || !menu || !drawer || !main) return;
  const phone = matchMedia("(max-width: 860px)");
  const isOpen = () => shell.classList.contains("menu-open");
  const setOpen = (open) => {
    shell.style.removeProperty("--drawer-x");
    shell.classList.toggle("menu-open", open);
    document.documentElement.classList.toggle("menu-lock", open);
    menu.setAttribute("aria-expanded", String(open));
    menu.setAttribute("aria-label", open ? tr("Cerrar el menú") : tr("Abrir el menú"));
    drawer.inert = phone.matches && !open; // off-screen links are not tabbable
  };
  setOpen(false);
  phone.addEventListener("change", () => setOpen(false));
  menu.addEventListener("click", () => setOpen(!isOpen()));
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && isOpen()) setOpen(false); });
  main.addEventListener("click", (e) => {
    if (!isOpen()) return;
    e.preventDefault();
    e.stopPropagation();
    setOpen(false);
  }, true);

  const scrollsSideways = (el) => {
    for (; el && el !== document.body; el = el.parentElement) {
      if (el.scrollWidth > el.clientWidth + 1 && /auto|scroll/.test(getComputedStyle(el).overflowX)) return true;
    }
    return false;
  };
  let g = null; // the gesture in progress
  document.addEventListener("touchstart", (e) => {
    g = null;
    if (!phone.matches || e.touches.length !== 1) return;
    const target = e.target instanceof Element ? e.target : null;
    if (!target || target.closest("input, textarea, select, [contenteditable], dialog, [data-no-swipe]") || scrollsSideways(target)) return;
    const t = e.touches[0];
    g = { x: t.clientX, y: t.clientY, open: isOpen(), w: drawer.getBoundingClientRect().width, active: false,
      dx: 0, last: { x: t.clientX, t: e.timeStamp }, prev: { x: t.clientX, t: e.timeStamp } };
  }, { passive: true });
  document.addEventListener("touchmove", (e) => {
    if (!g) return;
    const t = e.touches[0];
    const dx = t.clientX - g.x, dy = t.clientY - g.y;
    if (!g.active) {
      if (Math.abs(dx) < 10 && Math.abs(dy) < 10) return;
      // Mostly horizontal, and towards opening (closed) or closing (open).
      if (Math.abs(dx) < Math.abs(dy) * 1.3 || (g.open ? dx > 0 : dx < 0)) { g = null; return; }
      g.active = true;
      shell.classList.add("menu-dragging");
    }
    e.preventDefault(); // the page does not scroll while the menu follows the finger
    g.dx = dx;
    g.prev = g.last;
    g.last = { x: t.clientX, t: e.timeStamp };
    const x = Math.min(g.w, Math.max(0, (g.open ? g.w : 0) + dx));
    shell.style.setProperty("--drawer-x", x + "px");
  }, { passive: false });
  const end = () => {
    const done = g;
    g = null;
    if (!done?.active) return;
    shell.classList.remove("menu-dragging");
    const v = (done.last.x - done.prev.x) / Math.max(1, done.last.t - done.prev.t); // px/ms at release
    const far = Math.abs(done.dx) > done.w * 0.35, flick = Math.abs(v) > 0.35;
    setOpen(done.open ? !((far || flick) && done.dx < 0 && v <= 0.1) : (far || flick) && done.dx > 0 && v >= -0.1);
  };
  document.addEventListener("touchend", end);
  document.addEventListener("touchcancel", end);
}

// ───── tag editor: chips over a comma-separated input (works without JS) ─────
const PALETTE = ["indigo", "amber", "red", "teal", ""];
function tagColor(tag) { // same FNV-1a 32 as the server's tagClass
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(tag.toLowerCase())) { h ^= b; h = Math.imul(h, 0x01000193) >>> 0; }
  return PALETTE[h % PALETTE.length];
}
$$("[data-tag-editor]").forEach((box) => {
  const source = $("[data-tag-source]", box);
  const hidden = document.createElement("input");
  hidden.type = "hidden"; hidden.name = source.name;
  source.removeAttribute("name");
  source.value.split(",").map((t) => t.trim().toLowerCase()).filter(Boolean).forEach(add);
  source.value = "";
  source.placeholder = "+ tag";
  box.append(hidden);
  sync();
  function add(t) {
    t = t.trim().toLowerCase();
    if (!t || tags().includes(t)) return;
    const chip = document.createElement("span");
    chip.className = ("tag " + tagColor(t)).trim();
    chip.dataset.tag = t;
    chip.append(t);
    const x = document.createElement("button");
    x.type = "button"; x.textContent = "×"; x.setAttribute("aria-label", tr("Quitar %s", t));
    x.addEventListener("click", () => { chip.remove(); sync(); });
    chip.append(x);
    box.insertBefore(chip, source);
    sync();
  }
  function tags() { return $$(".tag[data-tag]", box).map((c) => c.dataset.tag); }
  function sync() { hidden.value = tags().join(", "); }
  source.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === ",") { e.preventDefault(); add(source.value); source.value = ""; }
    else if (e.key === "Backspace" && !source.value) { $$(".tag[data-tag]", box).at(-1)?.remove(); sync(); }
  });
  source.addEventListener("change", () => { if (source.value.trim()) { add(source.value); source.value = ""; } });
  source.form?.addEventListener("submit", () => { if (source.value.trim()) { add(source.value); source.value = ""; } });
});

// ───── record page: moving to a lost stage needs a reason ─────
$$("form[data-move]").forEach((form) => form.addEventListener("submit", (e) => {
  const opt = $("select[name=stage]", form).selectedOptions[0];
  if (opt?.dataset.category === "lost" && !$("input[name=reason]", form).value.trim()) {
    e.preventDefault();
    toast(tr("Escribí el motivo de pérdida."), { error: true });
    $("input[name=reason]", form).focus();
  }
}));

// ───── live updates: the server streams every change in this workspace
// (GET /events); the page refetches itself and swaps its [data-live] regions,
// so what agents do shows up without reloading ─────
function hydrate(root) {
  $$("[data-board]", root).forEach(setupBoard);
  $$("form[data-bulk]", root).forEach(setupBulk);
}
hydrate(document);

if (window.EventSource && $("[data-live]")) {
  // Never swap content under the user's hands. A tab that is merely in the
  // background (covered by another window, e.g. the agent's) still refreshes,
  // so it is current the moment it comes into view.
  const busy = () => {
    const a = document.activeElement;
    return $("dialog[open]") || $(".dragging") || $$("form[data-bulk] input[name=id]").some((b) => b.checked) ||
      (a instanceof HTMLElement && a.matches("input, textarea, select") && a.closest("[data-live]"));
  };
  const flash = (id) => $$(`[data-opp="${id}"], [data-row="${id}"]`).forEach((n) => {
    n.classList.add("flash");
    setTimeout(() => n.classList.remove("flash"), 2500);
  });
  // Fixed windows, not debouncing: during a long burst (an import) the page
  // still refreshes and announces every so often instead of waiting for the end.
  let timer = null, focusId = null, pending = false, dropped = false;
  const schedule = (focus, delay = 400) => {
    if (focus) focusId = focus;
    if (!timer) timer = setTimeout(() => { timer = null; refresh(); }, delay);
  };
  const refresh = async () => {
    if (busy()) { pending = true; schedule(null, 2000); return; }
    pending = false;
    const focus = focusId;
    focusId = null;
    const res = await fetch(location.href, { credentials: "same-origin" }).catch(() => null);
    if (!res || !res.ok || res.redirected) return;
    const doc = new DOMParser().parseFromString(await res.text(), "text/html");
    $$("[data-live]").forEach((el) => {
      const fresh = doc.querySelector(`[data-live="${el.dataset.live}"]`);
      if (fresh) { el.replaceChildren(...fresh.childNodes); hydrate(el); }
    });
    if (focus) flash(focus);
  };
  // A burst (an import, a bulk_upsert) becomes one notice, not hundreds.
  let burst = [], burstTimer = null;
  const announce = () => {
    burstTimer = null;
    const actors = new Set(burst.map((ev) => ev.actor));
    if (burst.length === 1) toast(`${burst[0].actor} ${burst[0].summary}`);
    else toast(actors.size === 1 ? tr("%s hizo %d cambios", burst[0].actor, burst.length) : tr("%d cambios de %d personas o agentes", burst.length, actors.size));
    burst = [];
  };
  // Newest change this page has seen. The stream keeps it current; a slow
  // poll catches what a proxy that drops server-sent events never delivers.
  let lastId = 0;
  const poll = async () => {
    const res = await fetch("/events/last", { credentials: "same-origin" }).catch(() => null);
    if (!res || !res.ok || res.redirected) return;
    const { id } = await res.json();
    if (lastId && id > lastId) schedule();
    lastId = Math.max(lastId, id);
  };
  poll();
  setInterval(poll, 10000);
  const es = new EventSource("/events");
  es.addEventListener("change", (e) => {
    const ev = JSON.parse(e.data);
    lastId = Math.max(lastId, ev.id);
    if (!ev.self) {
      burst.push(ev);
      if (!burstTimer) burstTimer = setTimeout(announce, 800);
    }
    schedule(ev.entity_id);
  });
  // After a dropped connection, catch up on whatever was missed.
  es.addEventListener("error", () => { dropped = true; });
  es.addEventListener("open", () => { if (dropped) { dropped = false; schedule(); } });
  // Coming back to the tab or window: catch up on anything deferred.
  document.addEventListener("visibilitychange", () => { if (!document.hidden && pending) schedule(); });
  window.addEventListener("focus", () => { if (pending) schedule(); });
}
