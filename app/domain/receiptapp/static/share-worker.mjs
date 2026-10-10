// The service worker that catches files shared to Reconcile from the
// phone's own photos app, and nothing else. Lifted from stewards.
//
// Somebody who has installed the app from Chrome ("Install app", which
// every page of somebody signed in offers through its manifest) finds
// Reconcile in Android's share sheet. Choosing it makes Chrome post the
// files, as one multipart form, to the manifest's share target,
// /receipts/shared. This worker's scope is that one address, so the post
// comes here instead of to the server, and the files are put in the
// phone's own Cache Storage. The worker then sends the person on to the
// share page, whose script (share.mjs) puts them in its file input as
// though they had been chosen there, and asks where they go.
//
// Why not let the post through to the server? It would arrive before
// anybody had said where the files go -- a checking account's checks, an
// account's receipts, a project's -- and the server would have to keep
// them somewhere meanwhile. So the server's handler for that address only
// ever sees a share this worker missed -- no page had registered it since
// the app was installed, or Chrome cleared it -- and asks for the share
// again.
//
// A classic script rather than a module, though it is served beside them:
// it imports nothing, and a classic worker registers in every Chrome that
// can install an app at all.
"use strict";

// SHARED is the share target, and the scope this worker is registered
// with. STASH is where the files wait; share.mjs reads it by the same
// names.
const SHARED = "/receipts/shared";
const STASH = "shared-files";
const PAGE = "/receipts/share";

// A new version takes over at once. It controls no page -- its only
// address is a post that is always answered with a redirect -- so there is
// nothing open that a change could break underneath.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (ev) => ev.waitUntil(self.clients.claim()));

self.addEventListener("fetch", (ev) => {
  const url = new URL(ev.request.url);
  if (ev.request.method !== "POST" || url.pathname !== SHARED) {
    return; // to the network, as though there were no worker
  }

  ev.respondWith(keep(ev.request));
});

// keep puts the shared files in the stash, replacing whatever an earlier
// share left there, and answers with the share page. ?shared= carries how
// many, so the page can tell a share that arrived empty from one whose
// files have since been added.
async function keep(req) {
  let n = 0;

  try {
    const form = await req.formData();
    const files = form.getAll("files").filter((f) => typeof f !== "string");

    await caches.delete(STASH);
    const cache = await caches.open(STASH);

    for (const [i, f] of files.entries()) {
      // The name goes in a header, so it is escaped: a header holds no
      // more than Latin-1, and a phone names a file in any script.
      await cache.put(`${SHARED}/${i}`, new Response(f, {
        headers: {
          "Content-Type": f.type || "application/octet-stream",
          "X-Name": encodeURIComponent(f.name || `shared-${i + 1}.jpg`),
          "X-Modified": String(f.lastModified || Date.now()),
        },
      }));
    }

    n = files.length;
  } catch {
    // The phone is out of room, or the post was not a form. The page says
    // the files did not arrive, which is true.
    return Response.redirect(new URL(`${PAGE}?shared=lost`, self.location.origin), 303);
  }

  return Response.redirect(new URL(`${PAGE}?shared=${n}`, self.location.origin), 303);
}
