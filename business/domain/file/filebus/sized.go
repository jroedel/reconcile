package filebus

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jroedel/reconcile/foundation/imaging"
)

// Smaller pictures of a photo, for pages (docs/plan.md, step 9).
//
// A receipt is kept as it was taken, because it is the receipt: the
// accountant's package has the original. But a phone that shows a list of
// twenty receipts should not fetch twenty photos of eight megabytes each,
// and the receipt's own page needs no more than a screen's worth. So a
// photo has two smaller pictures (imaging.Prepare: upright, JPEG, without
// its EXIF), made the first time a page asks and kept beside it by its
// hash, so that the same photo uploaded twice is made smaller once.
//
// Made when first asked rather than at upload, deliberately. Uploading is
// what a volunteer does at a shop counter, three receipts in under a
// minute (docs/design.md); making the pictures there would add seconds to
// every one of them, on a host that makes one picture at a time. Asked for
// later, the first reader of a page waits once, and every receipt already
// uploaded gets its pictures without a step to backfill them.

// Size is which smaller picture.
type Size string

const (
	// Small is a list's thumbnail, imaging.SmallSide on its longer side.
	Small Size = "small"

	// Large is the receipt's own page, imaging.LargeSide.
	Large Size = "large"
)

// Sizes is every size, for checking one a request names.
var Sizes = []Size{Small, Large}

// ErrNoPicture is a file that has no smaller pictures: not a JPEG or a PNG,
// or a photo that imaging refuses -- too small to need them, or claiming
// more pixels than any camera takes. The caller shows the original.
var ErrNoPicture = errors.New("there is no smaller picture of that file")

// Sizable reports whether a file of this content type has smaller
// pictures. The others -- WebP, HEIC, PDF -- are shown or offered as they
// are; imaging decodes JPEG and PNG only, and why is said there.
func Sizable(contentType string) bool { return contentType == JPEG || contentType == PNG }

// maxPhoto is the most a photo may weigh for its pictures to be made. It
// is read whole into memory to be decoded, so it is bounded here and not
// only by what an upload was allowed: a receipt's limit is 20 MB.
const maxPhoto = 32 << 20

// Picture is the file's picture at a size, made and kept the first time it
// is asked for. ErrNoPicture means the original is what there is.
//
// Who may see it is the caller's to have asked, as for Open.
func (b *Business) Picture(ctx context.Context, f File, size Size) (io.ReadSeekCloser, error) {
	if !Sizable(f.ContentType) || (size != Small && size != Large) {
		return nil, ErrNoPicture
	}

	rc, err := b.bytes.OpenSized(f.SHA256, size)
	if !errors.Is(err, ErrNotFound) {
		return rc, err
	}

	if f.Size > maxPhoto {
		return nil, ErrNoPicture
	}

	data, err := b.ReadAll(f)
	if err != nil {
		return nil, err
	}

	p, err := imaging.Prepare(ctx, data)

	switch {
	case errors.Is(err, imaging.ErrNotAPhoto), errors.Is(err, imaging.ErrTooSmall), errors.Is(err, imaging.ErrTooLarge):
		return nil, ErrNoPicture
	case err != nil:
		return nil, fmt.Errorf("making the smaller pictures: %w", err)
	}

	// Both at once, since both were made: the next page to ask for the
	// other size finds it.
	for s, pic := range map[Size]imaging.Picture{Small: p.Small, Large: p.Large} {
		if err := b.bytes.PutSized(f.SHA256, s, pic.JPEG); err != nil {
			return nil, err
		}
	}

	b.log.Info("smaller pictures were made", "file_id", f.ID.String(), "size", f.Size)

	return b.bytes.OpenSized(f.SHA256, size)
}
