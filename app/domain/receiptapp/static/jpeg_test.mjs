// jpeg.mjs under Node's own test runner: `make test-js`. No browser and no
// npm package; the JPEGs here are built byte by byte, so what is tested is
// the marker walking and the EXIF surgery, not a decoder.
//
// What only a browser can show -- the shrinking itself, which way up the
// result is, and the share page sending it -- is in shrink_browser_test.mjs
// and share_browser_test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";

import {
  MAX_SIDE,
  exifSegment,
  fitWithin,
  jpegSize,
  orientationOf,
  segments,
  withExif,
  withOrientation,
} from "./jpeg.mjs";
import { bytes, exif, has, jfif, jpeg, scan, sos, xmp } from "./testjpeg.mjs";

// ------------------------------------------------------------------ the size

test("a phone's ordinary photo is sent as it is", () => {
  assert.equal(MAX_SIDE, 4096);
  assert.deepEqual(fitWithin(4080, 3072), { width: 4080, height: 3072 });
  assert.deepEqual(fitWithin(3072, 4080), { width: 3072, height: 4080 });
  assert.deepEqual(fitWithin(4096, 4096), { width: 4096, height: 4096 });
});

test("a larger one is scaled to fit, keeping its shape", () => {
  for (const [w, h, want] of [
    [8160, 6144, { width: 4096, height: 3084 }], // 50 MP, 4:3
    [6144, 8160, { width: 3084, height: 4096 }], // the same, upright
    [5712, 4284, { width: 4096, height: 3072 }], // 24 MP
    [16320, 12240, { width: 4096, height: 3072 }], // 200 MP
    [4097, 1, { width: 4096, height: 1 }], // never a side of 0
  ]) {
    assert.deepEqual(fitWithin(w, h), want, `${w}x${h}`);
  }
});

test("the size is the frame's, whichever kind of frame", () => {
  assert.deepEqual(jpegSize(jpeg({ width: 8160, height: 6144 })), { width: 8160, height: 6144 });
  assert.deepEqual(jpegSize(jpeg({ width: 300, height: 200, frame: 0xc2 })), { width: 300, height: 200 }, "progressive");
});

test("Huffman tables before the frame are not taken for it", () => {
  // C4 sits among the frame markers, C0 to CF. Some cameras write their
  // tables first; read as a frame, a table's bytes would be a size of 0 by 0.
  assert.deepEqual(jpegSize(jpeg({ width: 8160, height: 6144, tablesFirst: true })), { width: 8160, height: 6144 });
});

test("fill bytes before a marker are walked over", () => {
  const b = jpeg();
  const filled = bytes(b.subarray(0, 2), [0xff, 0xff], b.subarray(2));
  assert.deepEqual(jpegSize(filled), { width: 4080, height: 3072 });
});

// ------------------------------------------------------------------ not a JPEG

test("what is not a whole JPEG gives null, and never throws", () => {
  const whole = jpeg();
  const cases = {
    empty: new Uint8Array(0),
    "a PNG": bytes([0x89], "PNG\r\n\x1a\n", new Array(40).fill(0)),
    "a shopping list": bytes("milk, eggs, compost"),
    "cut off in a segment": whole.subarray(0, 30),
    "cut off before the scan": whole.subarray(0, whole.length - scan().length - sos().length),
    "a length past the end": bytes([0xff, 0xd8, 0xff, 0xe0, 0xff, 0xff, 0x00]),
    "a length of zero": bytes([0xff, 0xd8, 0xff, 0xe0, 0x00, 0x00, 0xff, 0xda]),
    "not bytes at all": "IMG_0001.JPG",
  };

  for (const [name, b] of Object.entries(cases)) {
    assert.equal(segments(b), null, name);
    assert.equal(jpegSize(b), null, name);
    assert.equal(exifSegment(b), null, name);
  }
});

test("walking stops at the start of scan, whatever the image data holds", () => {
  const segs = segments(jpeg());
  assert.equal(segs.at(-1).marker, 0xda);
  assert.deepEqual(
    segs.map((s) => s.marker),
    [0xe0, 0xdb, 0xc0, 0xc4, 0xda],
  );
});

// ------------------------------------------------------------------ EXIF

test("the EXIF is found after a JFIF header, and the XMP is not mistaken for it", () => {
  const app1 = exif({ orientation: 6 });
  const b = jpeg({ app: [jfif(), xmp(), app1] });

  assert.deepEqual(exifSegment(b), app1);
  assert.equal(exifSegment(jpeg({ app: [jfif(), xmp()] })), null);
});

test("the orientation is read in either byte order", () => {
  for (const little of [true, false]) {
    for (const o of [1, 3, 6, 8]) {
      assert.equal(orientationOf(exif({ orientation: o, little })), o, `${little ? "II" : "MM"} ${o}`);
    }
  }
});

test("no orientation, or nonsense, is 1: upright, as a viewer would show it", () => {
  assert.equal(orientationOf(exif()), 1);
  assert.equal(orientationOf(exif({ orientation: 0 })), 1);
  assert.equal(orientationOf(exif({ orientation: 9 })), 1);
  assert.equal(orientationOf(null), 1);
  assert.equal(orientationOf(bytes([0xff, 0xe1, 0, 8], "Exif\0\0")), 1, "cut short");
});

test("setting the orientation changes those two bytes, in a copy", () => {
  for (const little of [true, false]) {
    const app1 = exif({ orientation: 6, little });
    const upright = withOrientation(app1, 1);

    assert.equal(orientationOf(upright), 1);
    assert.equal(orientationOf(app1), 6, "the original is untouched");
    assert.equal(upright.length, app1.length);
    assert.equal(upright.filter((v, i) => v !== app1[i]).length, 1, "one byte differs: 6 to 1 is the low byte");
    assert.ok(has(upright, "2026:05:14 09:30:00"), "the date comes along");
  }
});

test("a segment with no orientation is copied as it is", () => {
  const app1 = exif();
  assert.deepEqual(withOrientation(app1, 1), app1);
});

// ------------------------------------------------------------------ moving it

test("EXIF goes into a canvas's JPEG after its JFIF header", () => {
  const canvas = jpeg({ width: 4096, height: 3084 }); // JFIF, no EXIF: what toBlob gives
  const app1 = withOrientation(exif({ orientation: 6 }), 1);
  const out = withExif(canvas, app1);

  assert.deepEqual(
    segments(out).map((s) => s.marker),
    [0xe0, 0xe1, 0xdb, 0xc0, 0xc4, 0xda],
  );
  assert.deepEqual(exifSegment(out), app1);
  assert.deepEqual(jpegSize(out), { width: 4096, height: 3084 });
  assert.equal(orientationOf(exifSegment(out)), 1);

  // Everything from the scan on is the canvas's, byte for byte.
  const tail = scan();
  assert.deepEqual(out.subarray(out.length - tail.length), tail);
  assert.equal(out.length, canvas.length + app1.length);
});

test("with no JFIF header it goes straight after SOI", () => {
  const out = withExif(jpeg({ app: [] }), exif({ orientation: 1 }));

  assert.equal(segments(out)[0].marker, 0xe1);
  assert.equal(orientationOf(exifSegment(out)), 1);
});

test("EXIF already there is replaced, not doubled", () => {
  const old = exif({ orientation: 8, date: "1999:01:01 00:00:00" });
  const app1 = exif({ orientation: 1 });
  const out = withExif(jpeg({ app: [jfif(), old, xmp()] }), app1);

  assert.equal(segments(out).filter((s) => s.marker === 0xe1 && has(out.subarray(s.start, s.end), "Exif\0\0")).length, 1);
  assert.ok(!has(out, "1999:01:01"));
  assert.ok(has(out, "http://ns.adobe.com/xap/1.0/"), "the XMP is not EXIF and stays");
  assert.deepEqual(withExif(out, app1), out, "doing it twice is doing it once");
});

test("a JPEG that does not parse is given back untouched", () => {
  const broken = jpeg().subarray(0, 30);
  assert.equal(withExif(broken, exif()), broken);
});
