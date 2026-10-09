// Package imaging turns a photo as a phone sends it into the two pictures a
// page shows: a large one and a small one, both JPEG, both the right way up,
// and neither carrying anything the original said about where or how it was
// taken.
//
// Lifted from /opt/projects/stewards, where it was paid for: the memory
// figures below are that host's, which is this one's too.
//
// Here the smaller pictures are mostly about weight -- a phone should not
// fetch eight megabytes to show a thumbnail -- but the last part holds as
// well. A phone photo's EXIF holds the GPS position to a few metres, the
// phone's make and serial-ish identifiers, and the time to the second.
// Re-encoding from decoded pixels is the one way to be sure none of it
// survives: there is no list of tags to strip that can be incomplete. The
// original is the caller's to keep or not, privately.
//
// Three things are read from the EXIF before it is dropped, because the
// picture is wrong without them or the form is tedious without them: the
// orientation (a phone held upright saves the pixels sideways and a tag saying
// so), the date it was taken, which the caller can offer as the month, and the
// GPS position, which the caller may keep beside the private original and
// never in a picture it shows.
//
// JPEG and PNG only. iPhones store HEIC, but Safari converts to JPEG on upload
// unless the form asks for HEIC by name, and decoding HEIC in Go would mean a
// C library on a shared host that has none. WebP decodes with x/image, but
// nobody's phone camera produces it, and every format accepted is one more
// decoder facing the internet.
package imaging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"time"

	// The standard library decodes and encodes, but has no scaler: image/draw
	// only composites at 1:1. x/image/draw is the Go team's own answer, the
	// same Draw API with interpolating scalers added.
	xdraw "golang.org/x/image/draw"
)

// The sizes, as the longest side in pixels.
//
// Large is for a phone held upright at three device pixels per CSS pixel,
// with room for a tablet; past that the file grows and nobody can see the
// difference on a card. Small is the card's own column, 390 CSS pixels at
// two device pixels each, with a little to spare.
const (
	LargeSide = 1600
	SmallSide = 800

	// MinSide is the smallest photo worth making smaller pictures of.
	// Below it the caller has the original, which is small already.
	MinSide = 300

	// MaxPixels refuses a decompression bomb: a few kilobytes that claim
	// to be a 100,000-pixel square and would take gigabytes to decode. The
	// largest phone cameras in common use are 50 megapixels; this allows
	// those and a little more.
	MaxPixels = 60_000_000

	quality = 82
)

// The reasons a photo is refused. Each is a sentence continuation, for the
// caller to put in front of a person.
var (
	ErrNotAPhoto = errors.New("that file is not a photo this site can read. Send a JPEG or PNG; a phone's camera roll gives one")
	ErrTooSmall  = fmt.Errorf("that photo is too small to show anything. It needs to be at least %d pixels on its longer side", MinSide)
	ErrTooLarge  = errors.New("that photo has more pixels than any phone camera takes. Send the photo as the camera saved it")
)

// Picture is one encoded size.
type Picture struct {
	JPEG          []byte
	Width, Height int
}

// Prepared is what Prepare makes of a photo.
type Prepared struct {
	Large, Small Picture

	// Format is the original's, "jpeg" or "png", for naming the original
	// if the caller keeps it.
	Format string

	// Taken is when the camera says the photo was taken, by the camera's
	// own clock.
	Taken Taken

	// Where is the camera's GPS position, and Located whether it gave one.
	// Often it gives none even when the phone knew: a phone may strip the
	// location from a photo chosen in a browser, for the person's privacy,
	// unless they asked for it to be kept. Nothing may depend on it.
	Where   Position
	Located bool
}

// Taken is the date from the EXIF. Year and Month are zero when there was
// none; Day and the clock are zero when the camera wrote only part of it,
// which a camera whose clock was half set does.
type Taken struct {
	Year, Month               int
	Day, Hour, Minute, Second int

	// Offset is the camera's offset from UTC in seconds, from the
	// OffsetTimeOriginal tag phones have written since about 2018, and
	// Zoned whether there was one. Without it the clock is only a wall
	// clock, and Time needs to be told whose.
	Offset int
	Zoned  bool
}

// Time is the moment the photo was taken, read in the camera's own zone when
// it said one and in fallback when it did not. ok is false when the camera
// did not say the day: a month is not a moment.
func (t Taken) Time(fallback *time.Location) (time.Time, bool) {
	if t.Year == 0 || t.Day == 0 {
		return time.Time{}, false
	}

	loc := fallback
	if t.Zoned {
		loc = time.FixedZone("", t.Offset)
	}

	return time.Date(t.Year, time.Month(t.Month), t.Day, t.Hour, t.Minute, t.Second, 0, loc), true
}

// Position is a point on the earth, in decimal degrees: north and east are
// positive.
type Position struct {
	Lat, Lon float64
}

// one is held while a photo is decoded and scaled, by whichever caller is
// doing it. A 12-megapixel photo is about 50 MB of pixels while it is worked
// on, and a shared host's memory is not ours to spend twice over because two
// people pressed Upload in the same second -- or because one page asked for
// twenty pictures at once. Package-wide rather than
// each caller's own, which is what it was until there were two callers: two
// limits of one each are a limit of two.
var one = make(chan struct{}, 1)

// Turn waits for that one turn, for as long as ctx allows, and gives back
// the function that hands it on. Prepare takes it itself; it is exported for
// work that is not a decode but holds a photo's bytes twice over all the
// same, such as reading an original and writing it out again Stripped.
func Turn(ctx context.Context) (release func(), err error) {
	select {
	case one <- struct{}{}:
		return func() { <-one }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Prepare decodes a photo and makes its two sizes, one photo at a time
// across the whole process. It waits its turn for as long as ctx allows.
func Prepare(ctx context.Context, data []byte) (Prepared, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return Prepared{}, ErrNotAPhoto
	}

	// Checked before decoding, which is the whole point: the header is
	// read for free, the pixels are not.
	switch {
	case cfg.Width*cfg.Height > MaxPixels || cfg.Width <= 0 || cfg.Height <= 0:
		return Prepared{}, ErrTooLarge
	case max(cfg.Width, cfg.Height) < MinSide:
		return Prepared{}, ErrTooSmall
	}

	// After the header, so a refusal never waits behind somebody else's
	// photo.
	release, err := Turn(ctx)
	if err != nil {
		return Prepared{}, err
	}
	defer release()

	var src image.Image
	if format == "jpeg" {
		src, err = jpeg.Decode(bytes.NewReader(data))
	} else {
		src, err = png.Decode(bytes.NewReader(data))
	}

	if err != nil {
		return Prepared{}, ErrNotAPhoto
	}

	meta := readEXIF(data, format)

	p := Prepared{Format: format, Taken: meta.taken, Where: meta.where, Located: meta.located}

	// The small picture is made from the large one rather than from the
	// original: the large has already done the expensive part, and from
	// 1600 to 800 is one exact halving.
	large := shrink(src, LargeSide)

	if p.Large, err = encoded(large, meta.orientation); err != nil {
		return Prepared{}, err
	}

	if p.Small, err = encoded(shrink(large, SmallSide), meta.orientation); err != nil {
		return Prepared{}, err
	}

	return p, nil
}

// shrink scales src so its longer side is at most side, onto white. Never
// larger than the original: a small borrowed photo is re-encoded at its own
// size rather than blown up.
//
// It halves the photo exactly while it is at least twice the size wanted,
// and only then makes the one last step, of less than half, bilinearly. An
// exact halving with ApproxBiLinear samples halfway between each pair of
// source pixels in both directions, so every output pixel is the plain
// average of a 2×2 block: a box filter, which is what a large reduction
// needs to keep fine detail, such as small print, from turning into noise. The last step is too
// small for bilinear's weakness at large reductions -- skipping pixels --
// to show.
//
// This replaced x/image/draw's CatmullRom, which is sharper by a margin
// nobody can see at a card's size and costs memory in proportion to the
// original. Its two-pass scaler keeps 32 bytes per (output width × source
// height): 79 MB for the large picture of a 3-megapixel photo and 150 MB of
// a 12-megapixel one, and it was being paid twice, once per size. Five
// uploads in a row on the shared host (2026-10-01) and the process died
// with nothing in its log -- no panic, no "out of memory" from the runtime
// -- which is what the account's memory limit looks like from inside.
// Halving holds nothing but the halved picture itself: the same five photos
// peak at 40 MB rather than 319 MB.
func shrink(src image.Image, side int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	for max(w, h) >= 2*side {
		w, h = max(1, w/2), max(1, h/2)

		half := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.ApproxBiLinear.Scale(half, half.Bounds(), src, src.Bounds(), xdraw.Src, nil)
		src = half
	}

	if long := max(w, h); long > side {
		w, h = max(1, w*side/long), max(1, h*side/long)
	}

	// Onto white, so a PNG's transparent parts are paper rather than the
	// black a JPEG would otherwise make of them. The halvings above copy
	// with Src, which keeps the alpha for this step to composite.
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, xdraw.Src)
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)

	return dst
}

// encoded turns a scaled picture upright and encodes it.
//
// Turned after it is scaled, because turning is a pixel-by-pixel copy and
// the scaled image has a fraction of the pixels.
func encoded(img *image.RGBA, orientation int) (Picture, error) {
	out := orient(img, orientation)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: quality}); err != nil {
		return Picture{}, fmt.Errorf("encoding the photo: %w", err)
	}

	return Picture{JPEG: buf.Bytes(), Width: out.Bounds().Dx(), Height: out.Bounds().Dy()}, nil
}

// orient applies an EXIF orientation, 1 to 8, so the pixels are the way the
// photo was meant to be seen. Anything else is treated as 1, upright, which is
// what a browser does with a tag it does not understand.
//
// The eight are the four rotations, each with and without a mirror. Written
// as where each output pixel comes from, rather than as rotations composed,
// so each case can be checked against the specification's own picture of it
// in one line.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}

	w, h := src.Bounds().Dx(), src.Bounds().Dy()

	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	for y := range dh {
		for x := range dw {
			var sx, sy int

			switch o {
			case 2: // mirrored
				sx, sy = w-1-x, y
			case 3: // upside down
				sx, sy = w-1-x, h-1-y
			case 4: // upside down, mirrored
				sx, sy = x, h-1-y
			case 5: // on its side, mirrored
				sx, sy = y, x
			case 6: // a phone held upright: turn a quarter clockwise
				sx, sy = y, h-1-x
			case 7: // on its other side, mirrored
				sx, sy = w-1-y, h-1-x
			case 8: // a quarter anticlockwise
				sx, sy = w-1-y, x
			}

			i, j := dst.PixOffset(x, y), src.PixOffset(sx, sy)
			copy(dst.Pix[i:i+4], src.Pix[j:j+4])
		}
	}

	return dst
}
