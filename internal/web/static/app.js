// Hilo CRM — the only script on the site (CSP: script-src 'self').
// 1) <select data-autosubmit> submits its form on change.
// 2) <code class="copy" data-copy="…"> copies its value on click.
document.addEventListener("change", (e) => {
  const el = e.target;
  if (el instanceof HTMLSelectElement && el.hasAttribute("data-autosubmit") && el.form) el.form.requestSubmit();
});
document.addEventListener("click", async (e) => {
  const el = e.target instanceof Element ? e.target.closest("[data-copy]") : null;
  if (!el) return;
  try {
    await navigator.clipboard.writeText(el.getAttribute("data-copy") || "");
    el.classList.add("copied");
    setTimeout(() => el.classList.remove("copied"), 1200);
  } catch { /* clipboard unavailable: the text stays selectable */ }
});
// 3) <a data-navigate> follows itself on load (OAuth hand-back to the agent:
// a script navigation is not subject to CSP form-action, unlike a redirect
// after a form POST, so loopback and app-scheme redirect URIs work).
const nav = document.querySelector("a[data-navigate]");
if (nav) window.location.assign(nav.href);
