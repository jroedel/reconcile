package filebus_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/types"
)

// photo is a drawn JPEG, never a real one.
func photo(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// A photo's smaller pictures are made when first asked for, both at once,
// and kept; what has none says so, for the caller to show the original.
func TestSmallerPictures(t *testing.T) {
	b, dir := newBus(t)
	ctx := t.Context()

	f, err := b.Save(ctx, time.Now(), types.NewID(), "receipt.jpg", bytes.NewReader(photo(t, 2000, 1000)), 20<<20, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		size filebus.Size
		w, h int
	}{{filebus.Small, 800, 400}, {filebus.Large, 1600, 800}} {
		rc, err := b.Picture(ctx, f, c.size)
		if err != nil {
			t.Fatalf("%s: %v", c.size, err)
		}

		cfg, err := jpeg.DecodeConfig(rc)
		rc.Close()

		if err != nil || cfg.Width != c.w || cfg.Height != c.h {
			t.Errorf("%s: %dx%d, %v", c.size, cfg.Width, cfg.Height, err)
		}
	}

	kept, _ := filepath.Glob(filepath.Join(dir, "sized", f.SHA256+"-*.jpg"))
	if len(kept) != 2 {
		t.Errorf("kept %v", kept)
	}

	// Asked again, it is what was kept: made once.
	for _, p := range kept {
		os.WriteFile(p, []byte("kept"), 0o600)
	}

	rc, _ := b.Picture(ctx, f, filebus.Small)
	var got bytes.Buffer
	got.ReadFrom(rc)
	rc.Close()

	if got.String() != "kept" {
		t.Error("the picture was made again")
	}

	// Too small to need one, not a photo at all, or no such size.
	small, _ := b.Save(ctx, time.Now(), types.NewID(), "tiny.jpg", bytes.NewReader(photo(t, 100, 80)), 20<<20, nil)
	text, _ := b.Save(ctx, time.Now(), types.NewID(), "notes.txt", strings.NewReader("not a photo"), 1<<20, nil)

	for name, c := range map[string]struct {
		f    filebus.File
		size filebus.Size
	}{"a small photo": {small, filebus.Small}, "a text": {text, filebus.Small}, "a size that is not one": {f, "huge"}} {
		if _, err := b.Picture(ctx, c.f, c.size); !errors.Is(err, filebus.ErrNoPicture) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
