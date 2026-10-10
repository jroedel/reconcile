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
let stand = () => "through";

// between runs fn with each file the page sends passed to answer, by the
// order it was sent in, counting from 1 and counting every try.
async function between(answer, fn) {
  let n = 0;
  stand = () => answer(++n);
  await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/receipts/share/one", requestStage: "Request" }] });
  try {
    return await fn();
  } finally {
    await page.send("Fetch.disable");
    stand = () => "through";
  }
}

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

  // Standing between the page and the server, for the tests of a signal
  // that drops: each file the script sends is given to stand(), which
  // answers what happens to it. Nothing stands there unless a test says so.
  page.on("Fetch.requestPaused", (ev) => {
    const r = stand(ev);
    if (r === "drop") {
      page.send("Fetch.failRequest", { requestId: ev.requestId, errorReason: "ConnectionReset" });
    } else if (r === "signed out") {
      page.send("Fetch.fulfillRequest", {
        requestId: ev.requestId, responseCode: 303,
        responseHeaders: [{ name: "Location", value: "/sign-in" }],
      });
    } else {
      page.send("Fetch.continueRequest", { requestId: ev.requestId });
    }
  });
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
  assert.equal(await page.evaluate(`location.search`), "?done=checks&n=0&waiting=2&already=0");
  assert.equal(posts("/receipts/share/one"), 2, "a file a request");
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

// colours is n files' names and colours, each its own, so none is taken for
// another; from makes them a batch of their own.
const colours = (n, from) =>
  Array.from({ length: n }, (_, i) => [`Screenshot_20260801-${String(from + i).padStart(6, "0")}.png`, `hsl(${(from + i) * 37 % 360}, 60%, 45%)`]);

// add shares files, and presses Add on the share page with the place it
// remembers, and gives back the inbox's query once the page has gone there.
async function add(files) {
  await share(files);
  await page.waitFor(`location.pathname === "/receipts/share" && document.querySelector("#share-files").files.length === ${files.length}`);
  await page.evaluate(`document.querySelector("form[data-share] button[type=submit]").click(), true`);
  return page.waitFor(`location.pathname === "${account}/receipts" && location.search`, 120_000);
}

test("a month of checks, more than one upload takes, goes a file at a time", opts, async () => {
  const before = posts("/receipts/share/one");

  assert.equal(await add(colours(25, 100)), "?done=checks&n=0&waiting=25&already=0");
  assert.equal(posts("/receipts/share/one") - before, 25);
  assert.match(await page.evaluate(`document.body.innerText`), /25 check images are waiting below/);

  // The same share again, as after a phone that lost its signal before it
  // heard: every file is known by its bytes, and nothing is added twice.
  assert.equal(await add(colours(25, 100)), "?done=checks&n=0&waiting=0&already=25");
  assert.match(await page.evaluate(`document.body.innerText`), /25 were in this inbox already/);
});

test("a file whose upload breaks off is tried again, and the share finishes", opts, async () => {
  const before = posts("/receipts/share/one");
  let tries = 0;

  const where = await between((n) => {
    tries = n;
    return n === 2 || n === 3 ? "drop" : "through";
  }, () => add(colours(2, 200)));

  assert.equal(where, "?done=checks&n=0&waiting=2&already=0");
  assert.equal(tries, 4, "two files, one of them tried three times");
  assert.equal(posts("/receipts/share/one") - before, 2, "and each arrived once");
});

test("pressing Add again sends only the files that did not arrive", opts, async () => {
  // The second file meets a session that has run out, which stops the
  // share at once with the first kept.
  await share(colours(2, 300));
  await page.waitFor(`location.pathname === "/receipts/share" && document.querySelector("#share-files").files.length === 2`);

  await between((n) => (n === 2 ? "signed out" : "through"), async () => {
    await page.evaluate(`document.querySelector("form[data-share] button[type=submit]").click(), true`);
    await page.waitFor(`/signed out while sending/.test(document.querySelector("#sent").innerText)`, 60_000);
  });
  assert.match(await page.evaluate(`document.querySelector("#sent").innerText`), /1 added/);

  const before = posts("/receipts/share/one");
  await page.evaluate(`document.querySelector("form[data-share] button[type=submit]").click(), true`);
  assert.equal(await page.waitFor(`location.pathname === "${account}/receipts" && location.search`, 60_000), "?done=checks&n=0&waiting=1&already=1");
  assert.equal(posts("/receipts/share/one") - before, 1, "only the second file was sent again");
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
  // Less the connections the tests themselves reset.
  assert.deepEqual(page.errors.filter((e) => !/ERR_CONNECTION_RESET/.test(e)), []);
  assert.doesNotMatch(server.log(), /level=ERROR/);
});
