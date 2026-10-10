// Files shared to Reconcile from the phone's photos app: this registers
// the worker that catches them (share-worker.mjs), and on the share page
// puts them in the file input, as though they had been chosen there.
// Lifted from stewards.
//
// It is loaded by every page of somebody signed in, with the manifest that
// offers "Install app" (page.Renderer's OfferApp), so that the worker is
// in place before the first share whichever page the app was installed
// from. A share that comes before the worker does reaches the server
// instead, which asks for it again (receiptapp's shared handler).
//
// The files wait in Cache Storage, under STASH, from the share until an
// inbox opens with what was added -- where the share page's form ends up.
// So a shared batch survives Android closing the tab while the person
// looks something up, and is gone once it has been added. A batch that was
// shared and never added waits until the next share replaces it.
//
// Everything here is an improvement on a page that works without it, so
// every failure is quiet but one: files that were shared and cannot be put
// in the form, which the page says, so the person chooses them instead.
// What it says is written in the page by the server, in the page's
// language; this only shows it.
const WORKER = "/receipts/static/share-worker.mjs";
const SHARED = "/receipts/shared";
const STASH = "shared-files";
const PAGE = "/receipts/share";

// REMEMBER is where the phone keeps the last place a share went, so that a
// month of checks shared in batches goes to the same account each time
// without choosing it again. The phone's own: another phone, or another
// person on this one, may share somewhere else.
const REMEMBER = "reconcile-share-to";

(async () => {
  if (!window.isSecureContext) {
    return;
  }

  if (navigator.serviceWorker) {
    navigator.serviceWorker.register(WORKER, { scope: SHARED }).catch(() => {});
  }

  const form = document.querySelector("form[data-share]");
  if (form) {
    remember(form);
  }

  if (!window.caches) {
    return;
  }

  // An inbox that says what was just added is where a shared batch ends
  // up, whether it was added or refused: either way it is done with. Only
  // there, not on every page, so that somebody who looks something up
  // between the share and Add comes back to the files still there.
  const q = new URLSearchParams(location.search);
  if (/^\/(accounts|projects)\/[^/]+\/receipts$/.test(location.pathname) && q.has("done")) {
    await caches.delete(STASH).catch(() => {});
    return;
  }

  const shared = q.get("shared");
  const max = Number(form?.dataset.max) || 20;
  if (form && location.pathname === PAGE && /^[0-9]+$/.test(shared || "") && Number(shared) > 0 && Number(shared) <= max) {
    await fill(form);
  }
})();

// fill puts the stashed files in the form's file input, and shows the
// page's sentence for how that went.
async function fill(form) {
  const input = form.querySelector('input[type="file"]');

  let files = [];
  try {
    files = await take();
  } catch {
    files = [];
  }

  if (files.length === 0) {
    show("shared-gone");
    return;
  }

  try {
    const chosen = new DataTransfer();
    for (const f of files) {
      chosen.items.add(f);
    }
    input.files = chosen.files;
  } catch {
    show("shared-stuck");
    return;
  }

  show("shared-ready");
}

// take is the stashed files, in the order they were shared.
async function take() {
  if (!(await caches.has(STASH))) {
    return [];
  }

  const cache = await caches.open(STASH);
  const keys = await cache.keys();
  const order = (req) => Number(new URL(req.url).pathname.split("/").pop());
  keys.sort((a, b) => order(a) - order(b));

  const files = [];
  for (const req of keys) {
    const res = await cache.match(req);
    if (!res) {
      continue;
    }

    const blob = await res.blob();
    let name = "shared.jpg";
    try {
      name = decodeURIComponent(res.headers.get("X-Name") || name);
    } catch {
      // keep the plain name
    }

    files.push(new File([blob], name, {
      type: blob.type,
      lastModified: Number(res.headers.get("X-Modified")) || Date.now(),
    }));
  }

  return files;
}

// remember chooses the place the last share went, when the page has not
// chosen one, and keeps the one chosen now.
function remember(form) {
  const choices = Array.from(form.querySelectorAll('input[name="to"]'));

  let saved = null;
  try {
    saved = localStorage.getItem(REMEMBER);
  } catch {
    saved = null; // a private window, or storage turned off
  }

  if (saved && !choices.some((c) => c.checked)) {
    for (const c of choices) {
      c.checked = c.value === saved;
    }
  }

  form.addEventListener("submit", () => {
    const chosen = choices.find((c) => c.checked);
    if (!chosen) {
      return;
    }

    try {
      localStorage.setItem(REMEMBER, chosen.value);
    } catch {
      // not remembered, which only means choosing it again next time
    }
  });
}

function show(id) {
  const p = document.getElementById(id);
  if (p) {
    p.hidden = false;
  }
}
