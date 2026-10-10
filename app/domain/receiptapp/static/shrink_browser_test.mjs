// shrink.mjs in a real Chrome: `make test-browser`. The page is a blank one
// served by this test, from this directory, so nothing else is in the way:
// the app's own page is share_browser_test.mjs's. Lifted from stewards.
//
// The photos are drawn in the page -- white, a red block in the top left as
// stored and a blue one in the bottom right -- and given an EXIF that says
// which way up they are and when they were taken. What each test checks is
// the promise shrink makes: that the photo it sends looks, to anybody who
// opens it, exactly as the original would have -- the same corners, the same
// shape, upright -- only smaller, and dated as the camera dated it.
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { launch, skip, stage } from "../../../../scripts/browser.mjs";

const here = dirname(fileURLToPath(import.meta.url));

let server, browser, page;

before(async () => {
  if (skip()) return;

  server = createServer(async (req, res) => {
    const name = new URL(req.url, "http://x").pathname.slice(1);
    if (name === "") {
      res.writeHead(200, { "Content-Type": "text/html" }).end('<!doctype html><title>shrink</title><link rel="icon" href="data:,">');
      return;
    }
    if (!/^[a-z]+\.mjs$/.test(name)) {
      res.writeHead(404).end();
      return;
    }
    try {
      res.writeHead(200, { "Content-Type": "text/javascript" }).end(await readFile(join(here, name)));
    } catch {
      res.writeHead(404).end();
    }
  });
  await new Promise((done) => server.listen(0, "127.0.0.1", done));

  stage("starting Chrome");
  browser = await launch();
  page = await browser.page();
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  stage("loading the modules into the page");

  // The helpers every test uses, in the page: T.photo draws one, T.look says
  // what a viewer would see of it and what its bytes say.
  await page.evaluate(`(async () => {
    const J = await import("/jpeg.mjs");
    const TJ = await import("/testjpeg.mjs");
    const { shrink } = await import("/shrink.mjs");

    const name = (d) =>
      d[0] > 200 && d[1] < 70 && d[2] < 70 ? "red" :
      d[2] > 200 && d[0] < 70 && d[1] < 70 ? "blue" :
      d[0] > 200 && d[1] > 200 && d[2] > 200 ? "white" : "rgb(" + d[0] + "," + d[1] + "," + d[2] + ")";

    window.T = {
      shrink,
      files: {},

      async photo(key, { width, height, orientation, png = false, exif = true }) {
        const c = new OffscreenCanvas(width, height);
        const ctx = c.getContext("2d");
        const bw = Math.round(width / 5), bh = Math.round(height / 5);
        ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, width, height);
        ctx.fillStyle = "#f00"; ctx.fillRect(0, 0, bw, bh);
        ctx.fillStyle = "#00f"; ctx.fillRect(width - bw, height - bh, bw, bh);
        const blob = await c.convertToBlob({ type: png ? "image/png" : "image/jpeg", quality: 0.92 });
        let b = new Uint8Array(await blob.arrayBuffer());
        if (!png && exif) b = TJ.afterSOI(b, TJ.exif({ orientation }));
        T.files[key] = new File([b], png ? "IMG.png" : "IMG.jpg", { type: png ? "image/png" : "image/jpeg" });
        return b.length;
      },

      async look(file) {
        const b = new Uint8Array(await file.arrayBuffer());
        const app1 = J.exifSegment(b);
        const bmp = await createImageBitmap(file);
        const c = new OffscreenCanvas(bmp.width, bmp.height);
        const ctx = c.getContext("2d", { willReadFrequently: true });
        ctx.drawImage(bmp, 0, 0);
        const at = (x, y) => name(ctx.getImageData(x, y, 1, 1).data);
        const w = bmp.width, h = bmp.height, m = 20;
        return {
          stored: J.jpegSize(b),
          shown: { width: w, height: h },
          orientation: app1 ? J.orientationOf(app1) : null,
          dated: TJ.has(b, "2026:05:14 09:30:00"),
          zoned: TJ.has(b, "-05:00"),
          corners: { topLeft: at(m, m), topRight: at(w - m, m), bottomLeft: at(m, h - m), bottomRight: at(w - m, h - m) },
        };
      },

      // same is whether shrink gave back the very file it was given.
      async same(key) {
        const f = T.files[key];
        return (await shrink(f)) === f;
      },
    };
  })()`);
  stage("ready");
}, { timeout: 120_000 });

after(async () => {
  await browser?.close();
  server?.close();
});

const opts = { skip: skip() };

const shrinkAndLook = (key) =>
  page.evaluate(`(async () => {
    const f = T.files[${JSON.stringify(key)}];
    const out = await T.shrink(f);
    return { before: await T.look(f), after: await T.look(out), type: out.type, name: out.name, smaller: out.size < f.size };
  })()`);

// ------------------------------------------------------------------ left alone

test("a photo within 4096 pixels is sent as the phone saved it", opts, async () => {
  await page.evaluate(`T.photo("ordinary", { width: 4080, height: 3072, orientation: 1 })`);
  assert.equal(await page.evaluate(`T.same("ordinary")`), true);

  await page.evaluate(`T.photo("portrait", { width: 4080, height: 2296, orientation: 6 })`);
  assert.equal(await page.evaluate(`T.same("portrait")`), true, "a phone's 9:16 photo, stored sideways");
});

test("a PNG, and anything that is not a JPEG, is left for the server", opts, async () => {
  await page.evaluate(`T.photo("png", { width: 6000, height: 4000, png: true })`);
  assert.equal(await page.evaluate(`T.same("png")`), true);

  assert.equal(
    await page.evaluate(`(async () => { const f = new File(["milk, eggs"], "list.jpg", { type: "image/jpeg" }); return (await T.shrink(f)) === f })()`),
    true,
    "a shopping list called .jpg",
  );
});

test("a JPEG that says it is large but will not decode is sent as it is", opts, async () => {
  // Headers of a 6000 by 4000 JPEG and a scan of nonsense: createImageBitmap
  // refuses it, and the server is the one to say so.
  assert.equal(
    await page.evaluate(`(async () => {
      const TJ = await import("/testjpeg.mjs");
      const f = new File([TJ.jpeg({ width: 6000, height: 4000 })], "IMG.jpg", { type: "image/jpeg" });
      return (await T.shrink(f)) === f;
    })()`),
    true,
  );
});

test("upside down and mirrored photos are left for the server to turn", opts, async () => {
  for (const o of [2, 3, 4, 5, 7]) {
    await page.evaluate(`T.photo("o${o}", { width: 6000, height: 4000, orientation: ${o} })`);
    assert.equal(await page.evaluate(`T.same("o${o}")`), true, `orientation ${o}`);
  }
});

// ------------------------------------------------------------------ shrunk

test("a large upright photo comes out 4096 wide, looking as it did, dated as it was", opts, async () => {
  await page.evaluate(`T.photo("upright", { width: 6000, height: 4000, orientation: 1 })`);
  const { before, after, type, name } = await shrinkAndLook("upright");

  assert.deepEqual(after.stored, { width: 4096, height: 2731 });
  assert.deepEqual(after.shown, after.stored);
  assert.deepEqual(after.corners, before.corners);
  assert.deepEqual(before.corners, { topLeft: "red", topRight: "white", bottomLeft: "white", bottomRight: "blue" });
  assert.equal(after.orientation, 1);
  assert.ok(after.dated && after.zoned, "the camera's date and time zone come along");
  assert.equal(type, "image/jpeg");
  assert.equal(name, "IMG.jpg");
});

test("a photo stored sideways comes out upright, as a viewer would have shown it", opts, async () => {
  // 6 is how a phone stores a photo taken in portrait: turned a quarter
  // clockwise to be seen, so the red block stored top left is seen top right.
  // 8 is the other quarter turn: red seen bottom left.
  for (const [o, red] of [
    [6, "topRight"],
    [8, "bottomLeft"],
  ]) {
    await page.evaluate(`T.photo("q${o}", { width: 6000, height: 4000, orientation: ${o} })`);
    const { before, after } = await shrinkAndLook(`q${o}`);

    assert.equal(before.corners[red], "red", `the original, orientation ${o}, is seen with red ${red}`);
    assert.deepEqual(after.stored, { width: 2731, height: 4096 }, `orientation ${o}: stored upright now`);
    assert.deepEqual(after.shown, after.stored, `orientation ${o}`);
    assert.deepEqual(after.corners, before.corners, `orientation ${o}: seen as before`);
    assert.equal(after.orientation, 1, `orientation ${o}: and says so, so the server does not turn it again`);
    assert.ok(after.dated && after.zoned, `orientation ${o}: dated`);
  }
});

test("a 50 MP photo comes out 12 MP, and smaller", opts, async () => {
  await page.evaluate(`T.photo("fifty", { width: 8160, height: 6144, orientation: 1 })`);
  const { after, smaller } = await shrinkAndLook("fifty");

  assert.deepEqual(after.stored, { width: 4096, height: 3084 });
  assert.ok(smaller);
});

test("a large photo with no EXIF is shrunk all the same", opts, async () => {
  await page.evaluate(`T.photo("bare", { width: 6000, height: 4000, exif: false })`);
  const { before, after } = await shrinkAndLook("bare");

  assert.deepEqual(after.stored, { width: 4096, height: 2731 });
  assert.deepEqual(after.corners, before.corners);
  assert.equal(after.orientation, null);
});

test("the same photo shrinks to the same bytes, so sending it again is recognised", opts, async () => {
  // The inbox knows a photo it has by a hash of its bytes. A shrink that
  // came out different each time would add a batch sent twice, twice.
  await page.evaluate(`T.photo("again", { width: 6000, height: 4000, orientation: 6 })`);
  const same = await page.evaluate(`(async () => {
    const f = T.files.again;
    const a = new Uint8Array(await (await T.shrink(f)).arrayBuffer());
    const b = new Uint8Array(await (await T.shrink(f)).arrayBuffer());
    return a.length === b.length && a.every((v, i) => v === b[i]);
  })()`);

  assert.equal(same, true);
});

test("nothing went wrong in the page", opts, () => {
  assert.deepEqual(page.errors, []);
});
