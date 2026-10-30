// The device's time zone, in a real Chrome against a real server:
// `make test-browser`.
//
// What only a browser can show: that the script runs under the header
// policy, that the sign-in form carries the zone Chrome says it is in and
// the server keeps it, and that the profile offers the device's zone when
// it has none, fills the field when taken, and saves it with the form.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { launch, skip, stage } from "../../../../scripts/browser.mjs";
import { sessionCookie, startServer } from "../../../../scripts/testserver.mjs";

const zone = "America/Sao_Paulo";

let server, base, browser;

before(async () => {
  if (skip()) return;

  server = await startServer("tz");
  ({ base } = server);

  stage("starting Chrome");
  browser = await launch();
});

after(async () => {
  await browser?.close();
  server?.stop();
});

// inZone is a new tab whose clock is in São Paulo, signed in as the
// person the server's first sign-in made, or nobody.
async function inZone(signedIn) {
  const page = await browser.page();
  await page.send("Emulation.setTimezoneOverride", { timezoneId: zone });
  if (signedIn) await page.send("Network.setCookie", sessionCookie(base, server.cookie));
  return page;
}

// Signing in comes first: the tabs share one browser's cookies, and once
// a session is set the sign-in page sends a visitor on.
test("signing in keeps the device's zone", { skip: skip() }, async () => {
  const page = await inZone(false);
  const email = "volunteer@example.org";

  await page.goto(`${base}/sign-in`);
  await page.evaluate(`(() => { document.getElementById("email").value = "${email}"; document.querySelector("form[action='/sign-in']").requestSubmit(); })()`);
  await page.waitFor(`location.pathname === "/sign-in/code"`);
  assert.equal(await page.evaluate(`document.querySelector("input[data-device-zone]").value`), zone);

  // With no mail relay, the server writes the message to its log.
  const sent = server.log().split("\n").filter((l) => l.includes(email)).pop() || "";
  const code = /\b(\d{6})\b/.exec(sent.replaceAll("\\n", "\n"))?.[1];
  assert.ok(code, `no code in the log for ${email}:\n${server.log()}`);

  await page.evaluate(`(() => { document.getElementById("code").value = "${code}"; document.querySelector("form[action='/sign-in/code']").requestSubmit(); })()`);
  await page.waitFor(`location.pathname === "/account"`);
  assert.equal(await page.evaluate(`document.getElementById("tz").value`), zone);
  assert.deepEqual(page.errors, []);
});

test("the profile offers the device's zone, and saves it with the form", { skip: skip() }, async () => {
  const page = await inZone(true);

  await page.goto(`${base}/account`);
  const said = await page.waitFor(`(() => { const o = document.querySelector("[data-zone-offer]"); return !o.hidden && o.textContent; })()`);
  assert.match(said, /America\/Sao_Paulo/);
  assert.equal(await page.evaluate(`document.getElementById("tz").value`), "");

  await page.evaluate(`document.querySelector("[data-zone-offer] button").click()`);
  assert.equal(await page.evaluate(`document.getElementById("tz").value`), zone);
  assert.equal(await page.evaluate(`document.querySelector("[data-zone-offer]").hidden`), true);

  await page.evaluate(`document.querySelector("form[action='/account/weekly']").requestSubmit()`);
  await page.waitFor(`location.search.includes("done=weekly")`);
  assert.equal(await page.evaluate(`document.getElementById("tz").value`), zone);
  assert.equal(await page.evaluate(`document.querySelector("[data-zone-offer]").hidden`), true, "no offer once a zone is saved");
  assert.deepEqual(page.errors, []);
});
