package imaging

import (
	"encoding/binary"
	"math"
	"strconv"
	"time"
)

// exifMeta is what is read from a photo's EXIF.
type exifMeta struct {
	orientation int // 1 to 8; 0 when the photo does not say
	taken       Taken
	where       Position
	located     bool
}

// readEXIF finds a JPEG's EXIF and reads the orientation, the date taken and
// the GPS position from it. A PNG, a JPEG with no EXIF, and EXIF that does not parse all give
// the zero value: a photo shown upright with no date, which is what a browser
// would have done with it.
//
// Written here rather than imported. The few tags needed are a few dozen
// lines of TIFF directory walking, and every EXIF library is far more code
// than that facing bytes a stranger chose. Every offset below is checked
// against the length before it is used; a crafted file can make this return
// nothing, never read outside the slice.
func readEXIF(data []byte, format string) exifMeta {
	if format != "jpeg" {
		return exifMeta{}
	}

	tiff := app1(data)
	if tiff == nil {
		return exifMeta{}
	}

	var order binary.ByteOrder

	switch string(tiff[:4]) {
	case "II*\x00":
		order = binary.LittleEndian
	case "MM\x00*":
		order = binary.BigEndian
	default:
		return exifMeta{}
	}

	t := tiffReader{b: tiff, order: order}

	var meta exifMeta

	ifd0 := t.u32(4)
	exifIFD, gpsIFD := uint32(0), uint32(0)

	t.entries(ifd0, func(tag, kind uint16, count, value, at uint32) {
		switch {
		case tag == 0x0112 && kind == 3: // Orientation, a SHORT
			meta.orientation = int(t.u16(at))
		case tag == 0x8769 && kind == 4: // the Exif sub-directory, a LONG offset
			exifIFD = value
		case tag == 0x8825 && kind == 4: // the GPS sub-directory, likewise
			gpsIFD = value
		}
	})

	if exifIFD != 0 {
		offset := []byte(nil)

		t.entries(exifIFD, func(tag, kind uint16, count, value, _ uint32) {
			switch {
			// DateTimeOriginal, ASCII "2006:01:02 15:04:05" and a NUL:
			// twenty bytes, so never inline.
			case tag == 0x9003 && kind == 2 && count >= 20:
				meta.taken = parseTaken(t.bytes(value, 19))
			case tag == 0x9003 && kind == 2 && count >= 8:
				meta.taken = parseTaken(t.bytes(value, 7))

			// OffsetTimeOriginal, ASCII "+05:00" and a NUL: seven
			// bytes, also out of line.
			case tag == 0x9011 && kind == 2 && count >= 7:
				offset = t.bytes(value, 6)
			}
		})

		if meta.taken.Year != 0 {
			meta.taken.Offset, meta.taken.Zoned = parseOffset(offset)
		}
	}

	if gpsIFD != 0 {
		meta.where, meta.located = t.gps(gpsIFD)
	}

	return meta
}

// gps reads a GPS directory's latitude and longitude. Each is three RATIONALs
// -- degrees, minutes, seconds -- out of line, and a reference, "N" or "S",
// "E" or "W", which is two ASCII bytes and so sits in the entry itself.
//
// Not located for anything missing or impossible, and not for exactly 0, 0:
// a phone without a fix writes that rather than nothing, and nobody is
// planting in the Gulf of Guinea.
func (t tiffReader) gps(off uint32) (Position, bool) {
	var (
		latRef, lonRef   byte
		lat, lon         float64
		haveLat, haveLon bool
	)

	t.entries(off, func(tag, kind uint16, count, value, at uint32) {
		switch {
		case tag == 1 && kind == 2 && count >= 1:
			latRef = t.u8(at)
		case tag == 2 && kind == 5 && count == 3:
			lat, haveLat = t.degrees(value)
		case tag == 3 && kind == 2 && count >= 1:
			lonRef = t.u8(at)
		case tag == 4 && kind == 5 && count == 3:
			lon, haveLon = t.degrees(value)
		}
	})

	switch latRef {
	case 'S':
		lat = -lat
	case 'N':
	default:
		return Position{}, false
	}

	switch lonRef {
	case 'W':
		lon = -lon
	case 'E':
	default:
		return Position{}, false
	}

	if !haveLat || !haveLon || lat < -90 || lat > 90 || lon < -180 || lon > 180 || (lat == 0 && lon == 0) {
		return Position{}, false
	}

	return Position{Lat: lat, Lon: lon}, true
}

// degrees reads three RATIONALs at off as degrees, minutes and seconds.
func (t tiffReader) degrees(off uint32) (float64, bool) {
	var parts [3]float64

	for i := range parts {
		at := off + uint32(i)*8
		num, den := t.u32(at), t.u32(at+4)

		if den == 0 || uint64(at)+8 > uint64(len(t.b)) {
			return 0, false
		}

		parts[i] = float64(num) / float64(den)
	}

	d := parts[0] + parts[1]/60 + parts[2]/3600
	if math.IsNaN(d) || math.IsInf(d, 0) {
		return 0, false
	}

	return d, true
}

// app1 returns the TIFF data in a JPEG's EXIF segment, or nil.
//
// A JPEG is a series of marker segments, each 0xFF, a marker byte and a
// two-byte length that counts itself. EXIF is the APP1 segment that starts
// "Exif\0\0"; it comes before the image data, so the walk stops at the start
// of scan.
func app1(data []byte) []byte {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil
	}

	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return nil
		}

		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			return nil
		}

		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return nil
		}

		seg := data[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) >= 14 && string(seg[:6]) == "Exif\x00\x00" {
			return seg[6:]
		}

		i += 2 + n
	}

	return nil
}

// tiffReader reads a TIFF structure, answering zero for anything out of
// range instead of panicking.
type tiffReader struct {
	b     []byte
	order binary.ByteOrder
}

func (t tiffReader) u8(at uint32) byte {
	if uint64(at)+1 > uint64(len(t.b)) {
		return 0
	}

	return t.b[at]
}

func (t tiffReader) u16(at uint32) uint16 {
	if uint64(at)+2 > uint64(len(t.b)) {
		return 0
	}

	return t.order.Uint16(t.b[at:])
}

func (t tiffReader) u32(at uint32) uint32 {
	if uint64(at)+4 > uint64(len(t.b)) {
		return 0
	}

	return t.order.Uint32(t.b[at:])
}

func (t tiffReader) bytes(at, n uint32) []byte {
	if uint64(at)+uint64(n) > uint64(len(t.b)) {
		return nil
	}

	return t.b[at : at+n]
}

// entries calls fn for each entry of the directory at off: its tag, type,
// count, the value field read as a LONG, and where the value field sits.
//
// At most 512 entries, which is more than any camera writes and stops a
// directory that claims 65,535 from being walked in a loop of reads that
// each answer zero.
func (t tiffReader) entries(off uint32, fn func(tag, kind uint16, count, value, at uint32)) {
	if off == 0 {
		return
	}

	n := min(uint32(t.u16(off)), 512)

	for i := range n {
		e := off + 2 + i*12
		if uint64(e)+12 > uint64(len(t.b)) {
			return
		}

		fn(t.u16(e), t.u16(e+2), t.u32(e+4), t.u32(e+8), e+8)
	}
}

// parseTaken reads an EXIF date, "2006:01:02 15:04:05". A camera whose clock
// was never set writes zeros or spaces, which parse to nothing; one that gives
// a month and nonsense after it is kept to the month, which is all a species
// photo has ever needed.
func parseTaken(b []byte) Taken {
	if len(b) < 7 || b[4] != ':' {
		return Taken{}
	}

	y, err1 := strconv.Atoi(string(b[:4]))
	m, err2 := strconv.Atoi(string(b[5:7]))

	if err1 != nil || err2 != nil || y < 1990 || m < 1 || m > 12 {
		return Taken{}
	}

	t := Taken{Year: y, Month: m}

	if len(b) < 19 || b[7] != ':' || b[10] != ' ' || b[13] != ':' || b[16] != ':' {
		return t
	}

	var n [4]int

	for i, at := range []int{8, 11, 14, 17} {
		v, err := strconv.Atoi(string(b[at : at+2]))
		if err != nil {
			return t
		}
		n[i] = v
	}

	// The day against the month's own length: time.Date would quietly
	// make 31 September into 1 October.
	last := time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if n[0] < 1 || n[0] > last || n[1] > 23 || n[2] > 59 || n[3] > 59 {
		return t
	}

	t.Day, t.Hour, t.Minute, t.Second = n[0], n[1], n[2], n[3]

	return t
}

// parseOffset reads an EXIF offset, "+05:00" or "-06:00", as seconds east of
// UTC.
func parseOffset(b []byte) (int, bool) {
	if len(b) != 6 || (b[0] != '+' && b[0] != '-') || b[3] != ':' {
		return 0, false
	}

	h, err1 := strconv.Atoi(string(b[1:3]))
	m, err2 := strconv.Atoi(string(b[4:6]))

	if err1 != nil || err2 != nil || h > 14 || m > 59 {
		return 0, false
	}

	secs := (h*60 + m) * 60
	if b[0] == '-' {
		secs = -secs
	}

	return secs, true
}
