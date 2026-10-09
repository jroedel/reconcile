package imaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"runtime"
	"testing"
	"time"
)

// Fixtures are drawn here, never a real photo: a real one has a garden, a
// house or a person in it (CLAUDE.md).

// picture is a w×h image, red in its top-left quarter and blue elsewhere, so
// that which way up it came out is visible in one pixel.
func picture(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{0, 0, 255, 255}
			if x < w/2 && y < h/2 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}

	return img
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// withEXIF inserts an APP1 segment after the JPEG's start marker: a
// little-endian TIFF with the orientation, the camera's make (the kind of
// thing that must not survive), and an Exif directory holding the date and a
// made-up GPS-ish string.
func withEXIF(jpg []byte, orientation uint16, taken string) []byte {
	le := binary.LittleEndian
	var t []byte
	put16 := func(v uint16) { t = le.AppendUint16(t, v) }
	put32 := func(v uint32) { t = le.AppendUint32(t, v) }

	// An entry is a tag, a type, a count and a four-byte value field. A
	// SHORT sits in the first two bytes of the field, which in little-endian
	// is the same as the field read as a number.
	entry := func(tag, kind uint16, count, value uint32) {
		put16(tag)
		put16(kind)
		put32(count)
		put32(value)
	}

	t = append(t, "II*\x00"...)
	put32(8) // IFD0 at 8

	// IFD0: three entries, then the next-IFD offset.
	const ifd0Len = 2 + 3*12 + 4
	makeAt := uint32(8 + ifd0Len)
	makeStr := "SecretPhone\x00"
	exifAt := makeAt + uint32(len(makeStr))

	put16(3)
	entry(0x0112, 3, 1, uint32(orientation))
	entry(0x010F, 2, uint32(len(makeStr)), makeAt)
	entry(0x8769, 4, 1, exifAt)
	put32(0)
	t = append(t, makeStr...)

	// The Exif directory: the date, out of line.
	dateAt := exifAt + 2 + 12 + 4
	put16(1)
	entry(0x9003, 2, 20, dateAt)
	put32(0)
	t = append(t, taken+"\x00"...)

	seg := append([]byte("Exif\x00\x00"), t...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(seg)+2))
	app1 = append(app1, seg...)

	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)

	return append(out, jpg[2:]...)
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()

	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("the output is not a JPEG: %v", err)
	}

	return img
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xC000 && g < 0x4000 && b < 0x4000
}

// A phone held upright: the pixels are landscape and the tag says turn them
// a quarter clockwise. The result is portrait, red now top-right, and nothing
// of the EXIF is left in either size.
func TestAnUprightPhonePhotoComesOutUprightAndClean(t *testing.T) {
	src := withEXIF(jpegOf(t, picture(2000, 1000)), 6, "2027:04:18 09:30:00")

	p, err := Prepare(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}

	if p.Large.Width != 800 || p.Large.Height != 1600 {
		t.Errorf("large is %d×%d, want 800×1600", p.Large.Width, p.Large.Height)
	}

	if p.Small.Width != 400 || p.Small.Height != 800 {
		t.Errorf("small is %d×%d, want 400×800", p.Small.Width, p.Small.Height)
	}

	img := decode(t, p.Large.JPEG)
	if b := img.Bounds(); b.Dx() != 800 || b.Dy() != 1600 {
		t.Errorf("the large JPEG decodes as %v", b)
	}

	// The red corner was top-left of the landscape pixels; a quarter turn
	// clockwise puts it top-right.
	if !isRed(img.At(700, 100)) || isRed(img.At(100, 100)) {
		t.Error("the photo was not turned a quarter clockwise")
	}

	if p.Taken != (Taken{Year: 2027, Month: 4, Day: 18, Hour: 9, Minute: 30}) {
		t.Errorf("taken %+v, want 18 April 2027 at 9:30", p.Taken)
	}

	if p.Located {
		t.Errorf("a photo with no GPS directory was located at %+v", p.Where)
	}

	for name, b := range map[string][]byte{"large": p.Large.JPEG, "small": p.Small.JPEG} {
		if bytes.Contains(b, []byte("Exif")) || bytes.Contains(b, []byte("SecretPhone")) || bytes.Contains(b, []byte("2027:04")) {
			t.Errorf("the %s picture still carries the EXIF", name)
		}
	}
}

// Every orientation, on a 2×3 image whose six pixels are all different.
func TestEachOrientationIsTheSpecificationsPicture(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for i := range 6 {
		src.Set(i%2, i/2, color.RGBA{uint8(i + 1), 0, 0, 255})
	}

	// Each row is the output read left to right, top to bottom, by the
	// number of the source pixel (1 is top-left, 2 top-right, 6 bottom-right).
	for o, want := range map[int][]uint8{
		1: {1, 2, 3, 4, 5, 6},
		2: {2, 1, 4, 3, 6, 5},
		3: {6, 5, 4, 3, 2, 1},
		4: {5, 6, 3, 4, 1, 2},
		5: {1, 3, 5, 2, 4, 6},
		6: {5, 3, 1, 6, 4, 2},
		7: {6, 4, 2, 5, 3, 1},
		8: {2, 4, 6, 1, 3, 5},
	} {
		out := orient(src, o)

		var got []uint8
		for y := range out.Bounds().Dy() {
			for x := range out.Bounds().Dx() {
				got = append(got, out.RGBAAt(x, y).R)
			}
		}

		if !bytes.Equal(got, want) {
			t.Errorf("orientation %d: %v, want %v", o, got, want)
		}
	}
}

// A small borrowed photo keeps its size rather than being blown up, and a
// PNG's transparency becomes white rather than black.
func TestASmallTransparentPNGIsNotEnlargedOrBlackened(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 500, 400))

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	p, err := Prepare(t.Context(), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	if p.Format != "png" || p.Large.Width != 500 || p.Large.Height != 400 || p.Small.Width != 500 {
		t.Errorf("got %s %d×%d, small %d", p.Format, p.Large.Width, p.Large.Height, p.Small.Width)
	}

	if r, g, b, _ := decode(t, p.Large.JPEG).At(10, 10).RGBA(); r < 0xF000 || g < 0xF000 || b < 0xF000 {
		t.Error("the transparent part did not come out white")
	}

	if p.Taken != (Taken{}) {
		t.Error("a PNG was given a date")
	}
}

func TestWhatIsNotAPhotoIsRefused(t *testing.T) {
	var gif bytes.Buffer
	gif.WriteString("GIF89a")

	tiny := jpegOf(t, picture(200, 100))

	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"text":           {[]byte("hello, this is not a photo"), ErrNotAPhoto},
		"empty":          {nil, ErrNotAPhoto},
		"a gif":          {gif.Bytes(), ErrNotAPhoto},
		"a cut-off jpeg": {jpegOf(t, picture(600, 600))[:400], ErrNotAPhoto},
		"too small":      {tiny, ErrTooSmall},
		"a pixel bomb":   {pngClaiming(t, 100_000, 100_000), ErrTooLarge},
	} {
		if _, err := Prepare(t.Context(), tc.data); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

// pngClaiming is a tiny PNG whose header says it is w×h, with a correct
// checksum, so only the size gives it away.
func pngClaiming(t *testing.T, w, h uint32) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}

	b := buf.Bytes()

	// The IHDR chunk: 8-byte signature, 4 length, "IHDR", then width and
	// height, and the CRC over the type and data 13 bytes later.
	binary.BigEndian.PutUint32(b[16:], w)
	binary.BigEndian.PutUint32(b[20:], h)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))

	return b
}

// Malformed EXIF never panics and never turns the photo: every way of
// cutting the segment short gives the zero answer.
func TestBrokenEXIFIsIgnored(t *testing.T) {
	good := withEXIF(jpegOf(t, picture(400, 300)), 6, "2027:04:18 09:30:00")

	for n := range 120 {
		cut := append([]byte{}, good...)
		// Shorten the APP1 segment's declared length; the walk must stop
		// at the bounds, not past them.
		binary.BigEndian.PutUint16(cut[4:], uint16(n))
		_ = readEXIF(cut, "jpeg")
	}

	if m := readEXIF(withEXIF(jpegOf(t, picture(400, 300)), 6, "0000:00:00 00:00:00"), "jpeg"); m.taken != (Taken{}) || m.orientation != 6 {
		t.Errorf("an unset camera clock: %+v", m)
	}
}

// A phone's photo must not cost memory in proportion to its size. The host
// is shared and the account's memory is capped: CatmullRom's scaler, which
// this replaced, allocated about 230 MB for a photo like this one, and five
// uploads in a row took the process down. Halving needs the decoded photo
// (18 MB here), the half (12 MB) and the pictures it keeps.
func TestAPhonePhotoIsShrunkWithoutCostingItsSizeInMemory(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4032, 3024))
	fill(img, img.Bounds(), color.RGBA{0, 0, 255, 255})
	fill(img, image.Rect(0, 0, 2016, 1512), color.RGBA{255, 0, 0, 255})
	src := jpegOf(t, img)

	runtime.GC()

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	p, err := Prepare(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}

	runtime.ReadMemStats(&after)

	if p.Large.Width != 1600 || p.Large.Height != 1200 || p.Small.Width != 800 || p.Small.Height != 600 {
		t.Errorf("large %d×%d, small %d×%d", p.Large.Width, p.Large.Height, p.Small.Width, p.Small.Height)
	}

	if used := (after.TotalAlloc - before.TotalAlloc) >> 20; used > 80 {
		t.Errorf("preparing a 12-megapixel photo allocated %d MB; it should need well under 80", used)
	}
}

// A pattern finer than the picture can show must come out as its average,
// not as one of its colours. One-pixel stripes of black and white, shrunk to
// less than a third, are grey everywhere if every pixel was counted. Bilinear
// in one step reads two neighbours per output pixel and skips the rest, so
// across the row it lands on black pairs, white pairs and mixed ones in turn.
// 5000 rather than a power of two, because at exactly 4:1 bilinear happens to
// sample a black and a white stripe each time and comes out grey by luck.
func TestFineDetailIsAveragedNotSkipped(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 5000, 400))
	fill(img, img.Bounds(), color.RGBA{255, 255, 255, 255})

	for x := 0; x < 5000; x += 2 {
		fill(img, image.Rect(x, 0, x+1, 400), color.RGBA{0, 0, 0, 255})
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	p, err := Prepare(t.Context(), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	for name, b := range map[string][]byte{"large": p.Large.JPEG, "small": p.Small.JPEG} {
		out := decode(t, b)
		y := out.Bounds().Dy() / 2

		// The edges are left out: there the filter has only one side.
		for x := 2; x < out.Bounds().Dx()-2; x++ {
			if r, _, _, _ := out.At(x, y).RGBA(); r < 0x6000 || r > 0xA000 {
				t.Errorf("the %s picture's stripes came out %#x at x=%d, not grey", name, r, x)

				break
			}
		}
	}
}

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// gpsEXIF is a JPEG's APP1 with what a phone writes about where and when, in
// big-endian, which is the other byte order withEXIF does not cover: the
// date, its offset from UTC, and a GPS directory with a latitude and a
// longitude as degrees, minutes and seconds. The position is made up, a
// field west of Austin, rather than anybody's garden.
func gpsEXIF(jpg []byte, latRef byte, lat [3][2]uint32, lonRef byte, lon [3][2]uint32, taken, offset string) []byte {
	be := binary.BigEndian
	var t []byte
	put16 := func(v uint16) { t = be.AppendUint16(t, v) }
	put32 := func(v uint32) { t = be.AppendUint32(t, v) }
	entry := func(tag, kind uint16, count, value uint32) {
		put16(tag)
		put16(kind)
		put32(count)
		put32(value)
	}

	// In big-endian an inline ASCII or SHORT value sits at the front of
	// the field, so a one-letter reference is the field's top byte.
	inline := func(b byte) uint32 { return uint32(b) << 24 }

	dir := func(n int) uint32 { return uint32(2 + n*12 + 4) }

	const ifd0At = 8
	exifAt := ifd0At + dir(2)
	dateAt := exifAt + dir(2)
	offsetAt := dateAt + 20
	gpsAt := offsetAt + 7
	latAt := gpsAt + dir(4)
	lonAt := latAt + 24

	t = append(t, "MM\x00*"...)
	put32(ifd0At)

	put16(2)
	entry(0x8769, 4, 1, exifAt)
	entry(0x8825, 4, 1, gpsAt)
	put32(0)

	put16(2)
	entry(0x9003, 2, 20, dateAt)
	entry(0x9011, 2, 7, offsetAt)
	put32(0)
	t = append(t, taken+"\x00"...)
	t = append(t, offset+"\x00"...)

	put16(4)
	entry(1, 2, 2, inline(latRef))
	entry(2, 5, 3, latAt)
	entry(3, 2, 2, inline(lonRef))
	entry(4, 5, 3, lonAt)
	put32(0)

	for _, r := range append(lat[:], lon[:]...) {
		put32(r[0])
		put32(r[1])
	}

	seg := append([]byte("Exif\x00\x00"), t...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(seg)+2))
	app1 = append(app1, seg...)

	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)

	return append(out, jpg[2:]...)
}

// 30° 18' 36.72" N, 97° 54' 0" W, as a phone writes it: whole degrees and
// minutes, and seconds as hundredths.
var (
	fieldLat = [3][2]uint32{{30, 1}, {18, 1}, {3672, 100}}
	fieldLon = [3][2]uint32{{97, 1}, {54, 1}, {0, 1}}
)

func TestAPhonesPositionAndMomentAreRead(t *testing.T) {
	src := gpsEXIF(jpegOf(t, picture(400, 300)), 'N', fieldLat, 'W', fieldLon, "2026:10:03 16:45:09", "-05:00")

	p, err := Prepare(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}

	if !p.Located {
		t.Fatal("the GPS position was not read")
	}

	if math.Abs(p.Where.Lat-30.3102) > 1e-6 || math.Abs(p.Where.Lon-(-97.9)) > 1e-6 {
		t.Errorf("located at %+v, want 30.3102, -97.9", p.Where)
	}

	// The camera's own zone wins over the fallback: 4:45 pm at UTC-5 is
	// 9:45 pm UTC, whatever zone the caller would have guessed.
	at, ok := p.Taken.Time(time.UTC)
	if !ok || !at.Equal(time.Date(2026, 10, 3, 21, 45, 9, 0, time.UTC)) {
		t.Errorf("taken at %v (%v), want 21:45:09 UTC on 3 October 2026", at, ok)
	}

	for name, b := range map[string][]byte{"large": p.Large.JPEG, "small": p.Small.JPEG} {
		if bytes.Contains(b, []byte("Exif")) {
			t.Errorf("the %s picture still carries the EXIF", name)
		}
	}
}

func TestAPositionThatCannotBeRightIsNotOne(t *testing.T) {
	zero := [3][2]uint32{{0, 1}, {0, 1}, {0, 1}}

	for name, src := range map[string][]byte{
		"no fix, written as zeros": gpsEXIF(jpegOf(t, picture(400, 300)), 'N', zero, 'E', zero, "2026:10:03 16:45:09", "-05:00"),
		"a zero denominator":       gpsEXIF(jpegOf(t, picture(400, 300)), 'N', [3][2]uint32{{30, 0}, {0, 1}, {0, 1}}, 'W', fieldLon, "2026:10:03 16:45:09", "-05:00"),
		"no reference":             gpsEXIF(jpegOf(t, picture(400, 300)), 0, fieldLat, 'W', fieldLon, "2026:10:03 16:45:09", "-05:00"),
		"past the pole":            gpsEXIF(jpegOf(t, picture(400, 300)), 'N', [3][2]uint32{{91, 1}, {0, 1}, {0, 1}}, 'W', fieldLon, "2026:10:03 16:45:09", "-05:00"),
	} {
		if m := readEXIF(src, "jpeg"); m.located {
			t.Errorf("%s: located at %+v", name, m.where)
		}
	}
}

// A camera that wrote no zone, or one that wrote nonsense, gives a wall
// clock, which the caller reads in the zone it knows the garden is in.
func TestAClockWithNoZoneIsReadInTheCallersZone(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("no zone database here:", err)
	}

	src := gpsEXIF(jpegOf(t, picture(400, 300)), 'N', fieldLat, 'W', fieldLon, "2026:10:03 16:45:09", "later")
	m := readEXIF(src, "jpeg")

	if m.taken.Zoned {
		t.Errorf("an offset of %q was believed", "later")
	}

	at, ok := m.taken.Time(chicago)
	if !ok || !at.Equal(time.Date(2026, 10, 3, 16, 45, 9, 0, chicago)) {
		t.Errorf("taken at %v, want 4:45 pm in Chicago", at)
	}
}

func TestAPartDateIsKeptToTheMonth(t *testing.T) {
	for raw, want := range map[string]Taken{
		"2026:09:31 10:00:00": {Year: 2026, Month: 9}, // no 31 September
		"2026:10:03 25:00:00": {Year: 2026, Month: 10},
		"2026:10:03         ": {Year: 2026, Month: 10},
		"2026:02:29 10:00:00": {Year: 2026, Month: 2}, // not a leap year
		"2028:02:29 10:00:00": {Year: 2028, Month: 2, Day: 29, Hour: 10},
	} {
		got := parseTaken([]byte(raw))
		if got != want {
			t.Errorf("%q: %+v, want %+v", raw, got, want)
		}

		if _, ok := got.Time(time.UTC); ok != (want.Day != 0) {
			t.Errorf("%q: Time says ok=%v", raw, ok)
		}
	}
}

// Only one photo is decoded at a time in the whole process, and waiting for
// the turn gives up when the request does. The turn is taken here by hand,
// as another caller's photo would take it.
func TestAPhotoWaitsItsTurnAndGivesUpWithItsRequest(t *testing.T) {
	src := jpegOf(t, picture(400, 300))

	one <- struct{}{}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := Prepare(ctx, src)
	<-one

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Prepare while another photo was being decoded: %v, want it to wait and then give up", err)
	}

	// And a photo that is refused from its header does not wait at all.
	one <- struct{}{}
	_, err = Prepare(t.Context(), []byte("not a photo"))
	<-one

	if !errors.Is(err, ErrNotAPhoto) {
		t.Errorf("a non-photo while another was being decoded: %v", err)
	}

	if _, err := Prepare(t.Context(), src); err != nil {
		t.Errorf("after the turn was given back: %v", err)
	}
}
