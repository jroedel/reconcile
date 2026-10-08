// Package filebus keeps what people upload: statements now, receipts in step
// 7 (docs/plan.md, "Receipts and files").
//
// A File is one upload -- who, when, what it was called -- and its bytes are
// kept once per content, named by their SHA-256, so that the same statement
// uploaded twice or the same receipt attached to two charges costs the disk
// once. The row is per upload rather than per content because who uploaded
// it is part of what it is: the second person to upload a file did upload
// it.
//
// # Who may see one
//
// Nobody, through this package. A file has no scope of its own; the domain
// that links it to one -- a statement to its account, a receipt to its
// charge -- decides who may read it, and serves it only after asking. The
// bytes live outside anything Apache serves (filefs), so the app is the only
// way to them.
package filebus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jroedel/reconcile/business/types"
)

// ErrTooBig is an upload over the limit the caller set.
var ErrTooBig = errors.New("that file is too big")

// ErrNotFound is no such file.
var ErrNotFound = errors.New("no such file")

// ErrType is a file whose content is not one of the kinds the caller
// accepts. Nothing of it is kept.
var ErrType = errors.New("that kind of file is not accepted here")

// The content types a receipt may be: what a phone's camera and a shop's
// emailed invoice produce. HEIC is what an iPhone takes photos in.
const (
	JPEG = "image/jpeg"
	PNG  = "image/png"
	WebP = "image/webp"
	HEIC = "image/heic"
	PDF  = "application/pdf"
)

// File is one upload.
type File struct {
	ID          types.ID
	SHA256      string
	Size        int64
	ContentType string
	Name        string // as the uploader's device called it, tidied
	UploadedBy  types.ID
	UploadedAt  time.Time
}

// Storer keeps the rows.
type Storer interface {
	Create(ctx context.Context, f File) error
	ByID(ctx context.Context, id types.ID) (File, error)
}

// Bytes keeps the content, by its hash.
type Bytes interface {
	// Put stores at most limit bytes from r and returns their hash and size,
	// or ErrTooBig without keeping anything.
	Put(r io.Reader, limit int64) (sha string, size int64, err error)
	Open(sha string) (io.ReadSeekCloser, error)
}

// Business is the set of operations on files.
type Business struct {
	log   *slog.Logger
	store Storer
	bytes Bytes
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer, bytes Bytes) *Business {
	return &Business{log: log, store: store, bytes: bytes}
}

// Save keeps an upload. The content type is sniffed from the bytes, never
// taken from the browser: what a file says it is, is whatever the sender
// liked. With accept, a file of any other type is refused with ErrType
// before a byte of it is written.
func (b *Business) Save(ctx context.Context, now time.Time, actor types.ID, name string, r io.Reader, limit int64, accept []string) (File, error) {
	head := make([]byte, 512)

	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return File{}, fmt.Errorf("reading the upload: %w", err)
	}

	head = head[:n]
	kind := Sniff(head)

	if accept != nil && !slices.Contains(accept, strings.SplitN(kind, ";", 2)[0]) {
		return File{}, ErrType
	}

	sha, size, err := b.bytes.Put(io.MultiReader(strings.NewReader(string(head)), r), limit)
	if err != nil {
		return File{}, err
	}

	f := File{
		ID:          types.NewID(),
		SHA256:      sha,
		Size:        size,
		ContentType: kind,
		Name:        tidyName(name),
		UploadedBy:  actor,
		UploadedAt:  now,
	}

	if err := b.store.Create(ctx, f); err != nil {
		return File{}, fmt.Errorf("recording the upload: %w", err)
	}

	b.log.Info("a file was uploaded", "file_id", f.ID.String(), "size", size, "user_id", actor.String())

	return f, nil
}

// ByID is one file's row.
func (b *Business) ByID(ctx context.Context, id types.ID) (File, error) {
	return b.store.ByID(ctx, id)
}

// Open is a file's content, for serving or parsing.
func (b *Business) Open(f File) (io.ReadSeekCloser, error) {
	return b.bytes.Open(f.SHA256)
}

// ReadAll is a file's whole content, for a parser. Only for files whose size
// was limited when they were saved: a statement, never a photo.
func (b *Business) ReadAll(f File) ([]byte, error) {
	rc, err := b.Open(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	return io.ReadAll(rc)
}

// Sniff is the content type of a file from its first bytes:
// http.DetectContentType, which knows JPEG, PNG, WebP and PDF, and HEIC,
// which it does not. HEIC is an ISO media file -- a box named "ftyp" four
// bytes in -- whose brand says it holds a still image rather than a film.
func Sniff(head []byte) string {
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		switch string(head[8:12]) {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
			return HEIC
		}
	}

	return http.DetectContentType(head)
}

// maxName is the longest name kept for a file.
const maxName = 200

// tidyName is the base of the name the device gave, without control
// characters, and not too long. It is shown on pages and used in a
// download's file name, so nothing in it may end a header or change a path.
func tidyName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))

	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' || r == '/' {
			return -1
		}

		return r
	}, name)

	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		return "upload"
	}

	if r := []rune(name); len(r) > maxName {
		name = string(r[:maxName])
	}

	return name
}
