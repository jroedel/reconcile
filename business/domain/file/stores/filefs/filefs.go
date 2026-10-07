// Package filefs keeps uploaded bytes in a directory, one file per content,
// named by its SHA-256.
//
// The directory sits beside the database, in the application's own directory
// and never under public_html, as stewards' photos do: the app is the only
// way to a file, and it asks who is reading first. deploy.sh snapshots it
// with every backup of the database.
package filefs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
)

// Store is a directory of files, implementing filebus.Bytes.
type Store struct {
	dir string
}

var _ filebus.Bytes = (*Store)(nil)

// NewStore opens the directory, making it if it is not there. 0700, as the
// database is: nobody else on a shared host has a reason to read it.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("making the file directory %s: %w", dir, err)
	}

	return &Store{dir: dir}, nil
}

// Put streams r into a temporary file while hashing it, and renames it to
// its hash once whole. Streamed rather than read into memory, because the
// host stops a process near 300 MB and a phone's photo of a receipt is not
// small. A crash leaves a stray temporary file, never a truncated one under
// a real name.
func (s *Store) Put(r io.Reader, limit int64) (string, int64, error) {
	tmp, err := os.CreateTemp(s.dir, ".put-*")
	if err != nil {
		return "", 0, fmt.Errorf("storing the upload: %w", err)
	}

	defer os.Remove(tmp.Name()) // a no-op once renamed

	h := sha256.New()

	// One byte past the limit, to tell "exactly the limit" from "over it".
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, limit+1))
	if err != nil {
		tmp.Close()

		return "", 0, fmt.Errorf("storing the upload: %w", err)
	}

	if n > limit {
		tmp.Close()

		return "", 0, filebus.ErrTooBig
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()

		return "", 0, fmt.Errorf("storing the upload: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("storing the upload: %w", err)
	}

	sha := hex.EncodeToString(h.Sum(nil))

	// The same content already kept is the same bytes: renaming over it
	// changes nothing anybody could read.
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, sha)); err != nil {
		return "", 0, fmt.Errorf("storing the upload: %w", err)
	}

	return sha, n, nil
}

var hexName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Open opens one file by its hash. A name that is not a hash is refused,
// whatever a later caller passes, so nothing can reach outside the directory.
func (s *Store) Open(sha string) (io.ReadSeekCloser, error) {
	if !hexName.MatchString(sha) {
		return nil, fmt.Errorf("%q is not a file's hash", sha)
	}

	f, err := os.Open(filepath.Join(s.dir, sha))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, filebus.ErrNotFound
	}

	return f, err
}
