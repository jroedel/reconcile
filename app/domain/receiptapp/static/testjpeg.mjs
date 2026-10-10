// Test JPEGs, built byte by byte, for jpeg_test.mjs under Node and for the
// browser tests, which load this module into a page. Never served by the app:
// receiptapp embeds its modules by name, and this is not one of them.
//
// Built here rather than taken from a camera because the tests need to know
// exactly what is in them, and because test fixtures are invented data,
// never a real photo (CLAUDE.md).

export const bytes = (...parts) => {
  const flat = parts.flatMap((p) => (typeof p === "string" ? [...p].map((c) => c.charCodeAt(0)) : Array.from(p)));
  return Uint8Array.from(flat);
};

const u16be = (n) => [n >> 8, n & 0xff];

// segment is a marker segment: 0xFF, the marker, a length that counts itself,
// and the payload.
export const segment = (marker, ...payload) => {
  const body = bytes(...payload);
  return bytes([0xff, marker], u16be(body.length + 2), body);
};

export const jfif = () => segment(0xe0, "JFIF\0", [1, 1, 0, 0, 1, 0, 1, 0, 0]);
export const xmp = () => segment(0xe1, "http://ns.adobe.com/xap/1.0/\0", "<x:xmpmeta/>");
export const dqt = () => segment(0xdb, [0], new Array(64).fill(1));
export const dht = () => segment(0xc4, [0], new Array(16).fill(0));
export const sof = (marker, width, height) =>
  segment(marker, [8], u16be(height), u16be(width), [3, 1, 0x11, 0, 2, 0x11, 1, 3, 0x11, 1]);
export const sos = () => segment(0xda, [3, 1, 0, 2, 0x11, 3, 0x11, 0, 0x3f, 0]);

// scan is image data with 0xFF bytes in it, stuffed as a real scan stuffs
// them, so a walk that kept going past SOS would trip over it.
export const scan = () => bytes([0x12, 0xff, 0x00, 0x34, 0xff, 0x00, 0xff, 0xd0, 0x56], [0xff, 0xd9]);

// jpeg is the headers of a JPEG of the given size, with a scan no decoder
// would accept: enough for everything that reads markers.
export const jpeg = ({ app = [jfif()], width = 4080, height = 3072, frame = 0xc0, tablesFirst = false } = {}) =>
  tablesFirst
    ? bytes([0xff, 0xd8], ...app, dqt(), dht(), sof(frame, width, height), sos(), scan())
    : bytes([0xff, 0xd8], ...app, dqt(), sof(frame, width, height), dht(), sos(), scan());

// exif is an APP1 EXIF segment laid out as a phone lays one out: IFD0 with the
// Orientation (when given) and a pointer to the Exif sub-directory, which
// holds DateTimeOriginal and OffsetTimeOriginal -- the two the server reads
// the date from (foundation/imaging/exif.go).
export const exif = ({ orientation, little = true, date = "2026:05:14 09:30:00", offset = "-05:00" } = {}) => {
  const u16 = (n) => (little ? [n & 0xff, n >> 8] : [n >> 8, n & 0xff]);
  const u32 = (n) =>
    little ? [n & 0xff, (n >> 8) & 0xff, (n >> 16) & 0xff, n >>> 24] : [n >>> 24, (n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff];
  const entry = (tag, kind, count, value) => [...u16(tag), ...u16(kind), ...u32(count), ...value];

  const ifd0Count = orientation === undefined ? 1 : 2;
  const sub = 8 + 2 + ifd0Count * 12 + 4;
  const data = sub + 2 + 2 * 12 + 4;

  const ifd0 = [];
  if (orientation !== undefined) {
    ifd0.push(entry(0x0112, 3, 1, [...u16(orientation), 0, 0]));
  }
  ifd0.push(entry(0x8769, 4, 1, u32(sub)));

  const tiff = bytes(
    little ? "II" : "MM",
    u16(42),
    u32(8),
    u16(ifd0.length),
    ...ifd0,
    u32(0),
    u16(2),
    entry(0x9003, 2, 20, u32(data)),
    entry(0x9011, 2, 7, u32(data + 20)),
    u32(0),
    date + "\0",
    offset + "\0",
  );

  return segment(0xe1, "Exif\0\0", tiff);
};

// afterSOI is jpeg with seg put straight after its SOI: how a test gives a
// canvas's JPEG an EXIF without using the code under test to do it.
export const afterSOI = (jpeg, seg) => bytes(jpeg.subarray(0, 2), seg, jpeg.subarray(2));

// has is whether needle, a string or bytes, occurs in haystack.
export const has = (haystack, needle) => {
  const n = bytes(needle);
  outer: for (let i = 0; i + n.length <= haystack.length; i++) {
    for (let k = 0; k < n.length; k++) {
      if (haystack[i + k] !== n[k]) continue outer;
    }
    return true;
  }
  return false;
};
