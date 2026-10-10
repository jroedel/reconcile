// The few things the share page needs to know about a JPEG's bytes: how big
// it is, where its EXIF is, which way up it is, and how to put that EXIF into
// another JPEG. Lifted from stewards, unchanged but for these words.
// Nothing here touches a canvas or the page, so all of it is tested under
// Node (jpeg_test.mjs), and the parts only a browser can do are in
// shrink.mjs.
//
// Written here rather than imported, as foundation/imaging/exif.go is on the
// server, and for the same reason: a few dozen lines of marker walking
// against bytes a stranger chose, every offset checked against the length
// before it is used. A file that does not parse gives null or the input back,
// never an exception and never a read outside the array.

// MAX_SIDE is the longest side a photo is sent at, in pixels. It is about what
// a phone's ordinary 12 MP camera makes (4080 by 3072), so the receipts most
// people photograph go up exactly as the phone saved them, and only the 24,
// 48, 50 and 200 MP modes are shrunk -- whose photos are often past the 20
// MB a receipt may be. Twice and more the 1600 a receipt's page shows, so
// the accountant's copy reads as well as the shop's paper; and well under
// the 60 MP the server will not make pictures of (imaging.MaxPixels).
export const MAX_SIDE = 4096;

// fitWithin is the size a width by height picture is sent at: itself when its
// longer side is within max, and otherwise scaled down until it is.
export function fitWithin(width, height, max = MAX_SIDE) {
  const long = Math.max(width, height);
  if (long <= max) {
    return { width, height };
  }

  const scale = max / long;
  if (width >= height) {
    return { width: max, height: Math.max(1, Math.round(height * scale)) };
  }

  return { width: Math.max(1, Math.round(width * scale)), height: max };
}

// segments is a JPEG's marker segments, from the first after SOI up to and
// including the start of scan, as {marker, start, end}: start is the 0xFF of
// the marker and end is one past its last byte. null when the bytes are not a
// JPEG or a segment runs past the end. The walk stops at SOS because what
// follows is image data, in which 0xFF means something else.
export function segments(b) {
  if (!(b instanceof Uint8Array) || b.length < 4 || b[0] !== 0xff || b[1] !== 0xd8) {
    return null;
  }

  const out = [];
  let i = 2;

  while (i + 1 < b.length) {
    if (b[i] !== 0xff) {
      return null;
    }

    const marker = b[i + 1];

    // Fill bytes: any number of 0xFF may stand before a marker.
    if (marker === 0xff) {
      i++;
      continue;
    }

    // Markers that stand alone, with no length after them.
    if (marker === 0x01 || (marker >= 0xd0 && marker <= 0xd8)) {
      i += 2;
      continue;
    }

    if (i + 4 > b.length) {
      return null;
    }

    const length = (b[i + 2] << 8) | b[i + 3];
    if (length < 2 || i + 2 + length > b.length) {
      return null;
    }

    out.push({ marker, start: i, end: i + 2 + length });

    if (marker === 0xda) {
      return out;
    }

    i += 2 + length;
  }

  return null; // no SOS: not a whole JPEG
}

// jpegSize is the width and height the frame header gives, which is the size
// the pixels are stored at: before any EXIF rotation. null when there is none.
export function jpegSize(b) {
  const segs = segments(b);
  if (!segs) {
    return null;
  }

  for (const s of segs) {
    // SOF0 to SOF15, which are C0 to CF less the three that are not frames:
    // C4 (Huffman tables), C8 (reserved) and CC (arithmetic coding).
    const m = s.marker;
    if (m < 0xc0 || m > 0xcf || m === 0xc4 || m === 0xc8 || m === 0xcc) {
      continue;
    }

    if (s.end - s.start < 9) {
      return null;
    }

    const height = (b[s.start + 5] << 8) | b[s.start + 6];
    const width = (b[s.start + 7] << 8) | b[s.start + 8];

    return width > 0 && height > 0 ? { width, height } : null;
  }

  return null;
}

const exifHeader = [0x45, 0x78, 0x69, 0x66, 0x00, 0x00]; // "Exif\0\0"

function isExif(b, s) {
  if (s.marker !== 0xe1 || s.end - s.start < 4 + exifHeader.length + 8) {
    return false;
  }

  return exifHeader.every((c, k) => b[s.start + 4 + k] === c);
}

// exifSegment is a copy of the APP1 segment holding the EXIF, marker and
// length included, or null when there is none. Not the XMP, which is APP1
// too and starts with a namespace URL instead.
export function exifSegment(b) {
  const segs = segments(b);
  const s = segs && segs.find((s) => isExif(b, s));

  return s ? b.slice(s.start, s.end) : null;
}

// tiffStart is where the TIFF header sits in an EXIF segment: after the
// marker, the length and "Exif\0\0".
const tiffStart = 2 + 2 + exifHeader.length;

// orientationAt finds the Orientation tag in IFD0 of an EXIF segment: its
// value and the offset of that value in the segment, or null when the
// segment has no such tag or does not parse.
function orientationAt(app1) {
  const t = tiffStart;
  if (app1.length < t + 8) {
    return null;
  }

  let little;
  if (app1[t] === 0x49 && app1[t + 1] === 0x49 && app1[t + 2] === 0x2a && app1[t + 3] === 0x00) {
    little = true; // "II*\0"
  } else if (app1[t] === 0x4d && app1[t + 1] === 0x4d && app1[t + 2] === 0x00 && app1[t + 3] === 0x2a) {
    little = false; // "MM\0*"
  } else {
    return null;
  }

  const u16 = (at) => (little ? app1[at] | (app1[at + 1] << 8) : (app1[at] << 8) | app1[at + 1]);
  const u32 = (at) =>
    little
      ? (app1[at] | (app1[at + 1] << 8) | (app1[at + 2] << 16) | (app1[at + 3] << 24)) >>> 0
      : ((app1[at] << 24) | (app1[at + 1] << 16) | (app1[at + 2] << 8) | app1[at + 3]) >>> 0;

  const ifd0 = t + u32(t + 4);
  if (ifd0 + 2 > app1.length) {
    return null;
  }

  const count = u16(ifd0);
  for (let k = 0; k < count; k++) {
    const e = ifd0 + 2 + k * 12;
    if (e + 12 > app1.length) {
      return null;
    }

    // Orientation, a SHORT, held in the first two bytes of the value field.
    if (u16(e) === 0x0112 && u16(e + 2) === 3) {
      return { value: u16(e + 8), at: e + 8, little };
    }
  }

  return null;
}

// orientationOf is the EXIF orientation, 1 to 8; 1 when the segment does not
// say, which is what a viewer assumes too.
export function orientationOf(app1) {
  const o = app1 && orientationAt(app1);

  return o && o.value >= 1 && o.value <= 8 ? o.value : 1;
}

// withOrientation is a copy of an EXIF segment saying orientation value. The
// segment is copied whole, date and all; only those two bytes change.
export function withOrientation(app1, value) {
  const out = app1.slice();
  const o = orientationAt(out);

  if (o) {
    out[o.at] = o.little ? value & 0xff : value >> 8;
    out[o.at + 1] = o.little ? value >> 8 : value & 0xff;
  }

  return out;
}

// withExif is a copy of jpeg with app1 as its EXIF: after the JFIF header when
// it has one, as the specification asks, and otherwise straight after SOI.
// EXIF it had already is dropped rather than left to disagree. jpeg itself is
// returned when it does not parse.
//
// Only APP1 moves. The original's other segments stay behind: its colour
// profile (APP2) described pixels the browser has already converted to sRGB,
// and its multi-picture data (an HDR gain map, on some phones) described an
// image that is no longer there.
export function withExif(jpeg, app1) {
  const segs = segments(jpeg);
  if (!segs) {
    return jpeg;
  }

  const jfif = segs[0].marker === 0xe0 ? segs[0] : null;
  const insertAt = jfif ? jfif.end : 2;
  const drop = segs.filter((s) => isExif(jpeg, s));

  const parts = [];
  let from = 0;
  const keep = (to) => {
    if (to > from) {
      parts.push(jpeg.subarray(from, to));
    }
  };

  // The segments to drop come after insertAt, since a JFIF segment is not
  // EXIF, so this is one pass from the front.
  keep(insertAt);
  parts.push(app1);
  from = insertAt;
  for (const s of drop) {
    keep(s.start);
    from = s.end;
  }
  keep(jpeg.length);

  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }

  return out;
}
