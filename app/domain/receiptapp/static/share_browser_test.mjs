// A share from the phone's photos app, in a real Chrome against a real
// server: `make test-browser`. Lifted from stewards.
//
// Android's share sheet cannot be driven from here, but what it does to
// the app can: Chrome posts the files to the manifest's share target as
// one multipart form, a navigation of the browser's own. A form posted
// from the page to the same address is that navigation, and goes through
// the same service worker. What this checks is the part only a browser
// can show: that the worker registers under the header policy, takes the
// post before the server sees it, and that the files then sit in the share
// page's file input, go where the page is told, and that the page
// remembers where.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { launch, skip, stage } from "../../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../../scripts/testserver.mjs";

let server, base, browser, page, account;

before(async () => {
  if (skip()) return;

  server = await startServer("share");
  ({ base } = server);

  stage("making an organization with a checking account");
  const org = (await server.post("/orgs", { name: "St. Joseph Parish" })).replace(/\?.*$/, "").replace("/orgs/", "");
  account = (await server.post("/accounts", { org, name: "Parish checking", kind: "checking", last4: "1234" })).replace(/\?.*$/, "");

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();
  await page.setCookie(sessionCookie(base, server.cookie));
  stage("ready");
}, { timeout: 300_000 });

after(async () => {
  await browser?.close();
  server?.stop();
});

const opts = { skip: skip() };

const posts = (path) => server.log().split("\n").filter((l) => l.includes("method=POST") && l.includes(`path=${path} `)).length;

const workerReady = `navigator.serviceWorker.getRegistration("/receipts/shared").then((r) => !!(r && r.active))`;

// share posts files to the share target as Chrome does for a share: drawn
// here, invented, each a colour of its own so none is taken for another.
async function share(files) {
  stage(`sharing ${files.length} files`);
  await page.evaluate(`(async () => {
    const chosen = new DataTransfer();
    for (const [name, colour] of ${JSON.stringify(files)}) {
      const c = new OffscreenCanvas(900, 1200);
      const ctx = c.getContext("2d");
      ctx.fillStyle = colour; ctx.fillRect(0, 0, 900, 1200);
      ctx.fillStyle = "#fff"; ctx.fillRect(100, 100, 600, 250);
      const blob = await c.convertToBlob({ type: "image/png" });
      chosen.items.add(new File([blob], name, { type: "image/png" }));
    }

    const form = document.createElement("form");
    form.method = "post";
    form.action = "/receipts/shared";
    form.enctype = "multipart/form-data";
    const input = document.createElement("input");
    input.type = "file";
    input.name = "files";
    input.multiple = true;
    input.files = chosen.files;
    form.append(input);
    document.body.append(form);
    form.submit();
    return true;
  })()`);
}

test("files shared to the app wait in the share page, and go where it says", opts, async () => {
  // The front page, where the treasurer first installed the app: any page
  // of somebody signed in registers the worker.
  stage("opening the front page, which registers the worker");
  await page.goto(`${base}/`);
  await page.waitFor(workerReady);

  await share([["Screenshot_20260712-101500.png", "#b03030"], ["Screenshot_20260712-101530.png", "#30a040"]]);

  await page.waitFor(`location.pathname === "/receipts/share" && document.querySelector("#share-files").files.length === 2`);
  assert.equal(await page.evaluate(`location.search`), "?shared=2");
  assert.equal(posts("/receipts/shared"), 0, "the worker took the share, and the server never saw it");

  assert.deepEqual(
    await page.evaluate(`Array.from(document.querySelector("#share-files").files, (f) => [f.name, f.type])`),
    [["Screenshot_20260712-101500.png", "image/png"], ["Screenshot_20260712-101530.png", "image/png"]],
    "in the order shared, with their names",
  );
  assert.equal(await page.evaluate(`document.querySelector("#shared-ready").hidden`), false);

  // Looking something up on another page and coming back -- or Android
  // reloading a tab it put away -- finds them still there.
  await page.goto(`${base}/`);
  await page.goto(`${base}/receipts/share?shared=2`);
  await page.waitFor(`document.querySelector("#share-files").files.length === 2`);

  const checks = account.replace("/accounts/", "checks:");
  await page.evaluate(`document.querySelector('input[name="to"][value="${checks}"]').click(), true`);
  await page.evaluate(`document.querySelector("form[data-share] button[type=submit]").click(), true`);

  await page.waitFor(`location.pathname === "${account}/receipts"`, 60_000);
  assert.match(await page.evaluate(`location.search`), /done=checks/);
  assert.match(await page.evaluate(`document.body.innerText`), /2 check images are waiting below/);

  // Added, and the inbox has opened: the phone keeps no copy.
  await page.waitFor(`caches.has("shared-files").then((has) => !has)`);

  // A share already added says so rather than showing an empty form.
  await page.goto(`${base}/receipts/share?shared=2`);
  await page.waitFor(`!document.querySelector("#shared-gone").hidden`);
  assert.equal(await page.evaluate(`document.querySelector("#share-files").files.length`), 0);

  // And the next share goes where the last one went, unless told otherwise.
  await share([["Screenshot_20260712-101600.png", "#3050c0"]]);
  await page.waitFor(`location.search === "?shared=1" && document.querySelector("#share-files").files.length === 1`);
  assert.equal(await page.evaluate(`document.querySelector('input[name="to"]:checked')?.value`), checks);
});

test("a share the worker missed is asked for again, and the next one is caught", opts, async () => {
  stage("unregistering the worker");
  await page.evaluate(`navigator.serviceWorker.getRegistration("/receipts/shared").then((r) => r.unregister())`);

  await share([["Screenshot_20260712-101700.png", "#c0a030"]]);
  await page.waitFor(`location.search === "?shared=again"`);
  assert.equal(posts("/receipts/shared"), 1, "the share reached the server");
  assert.match(await page.evaluate(`document.querySelector("#shared").innerText`), /did not reach this page/);

  // The share page registered the worker again.
  await page.waitFor(workerReady);
  await share([["Screenshot_20260712-101700.png", "#c0a030"]]);
  await page.waitFor(`location.search === "?shared=1" && document.querySelector("#share-files").files.length === 1`);
  assert.equal(posts("/receipts/shared"), 1, "and the second did not");
});

test("the pages ran with nothing blocked and nothing thrown", opts, () => {
  assert.deepEqual(page.errors, []);
  assert.doesNotMatch(server.log(), /level=ERROR/);
});
