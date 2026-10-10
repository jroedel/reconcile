// shrink is what the share page does to a photo before sending it: nothing,
// for a photo already within MAX_SIDE, and otherwise a JPEG scaled down to
// it, turned upright, with the original's EXIF. Lifted from stewards.
//
// Why on the phone and not on the server: what costs is the trip. A 50 MP
// photo is 15 to 25 MB on one bar of signal -- past the 20 MB a receipt may
// be -- and the server's work on it (decoding at full size) is what
// foundation/imaging bounds. Shrunk here, the phone sends about 3 MB, and the
// receipt is still sharper than its page shows.
//
// The rule that governs every branch below: when in any doubt, send the
// original. The server keeps any photo up to 20 MB as it was taken, and
// turns its pictures upright itself, so the original is never wrong -- only
// slower, or too big. A shrunk photo that came out sideways, or without its
// date, would be wrong, and nobody would notice until the accountant did.
//
// A screenshot is a PNG, and goes as it is: a check's image from a bank's
// app is one, and is a megabyte or two.
//
// The parts that are plain bytes are in jpeg.mjs and tested under Node; this
// file is the part that needs a browser, and is tested in one
// (shrink_browser_test.mjs).
import { MAX_SIDE, exifSegment, fitWithin, jpegSize, orientationOf, withExif, withOrientation } from "./jpeg.mjs";

// QUALITY is the JPEG quality the shrunk photo is saved at: at 4096 pixels a
// second generation at 0.9 cannot be told from the first.
export const QUALITY = 0.9;

// shrink resolves to file itself, or to a smaller JPEG File of the same name.
// It never rejects: a failure anywhere is the original, sent as it is.
export async function shrink(file, max = MAX_SIDE) {
  try {
    return await shrunk(file, max);
  } catch {
    return file;
  }
}

async function shrunk(file, max) {
  const original = new Uint8Array(await file.arrayBuffer());

  // A PNG, a HEIC the phone did not convert, a screenshot: not a camera
  // JPEG, and left to the server to take or refuse.
  const stored = jpegSize(original);
  if (!stored || Math.max(stored.width, stored.height) <= max) {
    return file;
  }

  const app1 = exifSegment(original);
  const orientation = orientationOf(app1);

  // Which way up the pixels are stored. 1 is upright; 6 and 8 are a quarter
  // turn, the way every phone stores a photo taken in portrait. The rest --
  // upside down, and the mirrored ones -- are rare enough to leave to the
  // server, because a browser that ignored them would give a picture of the
  // same size and there is no telling it from one that did not.
  const quarter = orientation === 6 || orientation === 8;
  if (orientation !== 1 && !quarter) {
    return file;
  }

  const bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
  try {
    // A browser that turned a quarter-turned photo upright gives its sides
    // swapped. One that did not -- an older one, which knows no
    // imageOrientation -- gives them as stored, and the photo goes as it is
    // rather than sideways.
    const want = quarter ? { width: stored.height, height: stored.width } : stored;
    if (bitmap.width !== want.width || bitmap.height !== want.height) {
      return file;
    }

    const to = fitWithin(bitmap.width, bitmap.height, max);
    const blob = await encode(bitmap, to.width, to.height);
    let out = new Uint8Array(await blob.arrayBuffer());

    // The camera's date and the rest of its EXIF, saying upright now: the
    // pixels have been turned, and the server must not turn them again.
    if (app1) {
      out = withExif(out, withOrientation(app1, 1));
    }

    return new File([out], file.name, { type: "image/jpeg", lastModified: file.lastModified });
  } finally {
    bitmap.close();
  }
}

// encode draws bitmap at width by height and saves it as a JPEG: off the page
// where the browser can, and on a canvas element where it cannot.
async function encode(bitmap, width, height) {
  if (typeof OffscreenCanvas === "function") {
    const canvas = new OffscreenCanvas(width, height);
    draw(canvas.getContext("2d"), bitmap, width, height);

    return canvas.convertToBlob({ type: "image/jpeg", quality: QUALITY });
  }

  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  draw(canvas.getContext("2d"), bitmap, width, height);

  return new Promise((done, fail) => {
    canvas.toBlob((blob) => (blob ? done(blob) : fail(new Error("the canvas gave no JPEG"))), "image/jpeg", QUALITY);
  });
}

function draw(ctx, bitmap, width, height) {
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(bitmap, 0, 0, width, height);
}
