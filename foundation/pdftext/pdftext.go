// Package pdftext reads the text out of a PDF, laid out as it was printed.
//
// Lifted from eumaeus, where it reads bank statements on one person's
// machine. Here it reads a file somebody uploaded to a web server, which
// changes what it has to guard against more than what it does; the limits
// below are that difference.
//
// # Why this shells out
//
// A PDF written by software -- a statement from a bank's composition engine,
// or a web page a browser printed -- is not a picture of a page. It is text
// with coordinates, and nothing needs recognizing; it needs reading back in
// the arrangement it was laid down in. Turning glyphs with coordinates back
// into columns is the whole problem, and poppler's pdftotext has solved it
// carefully for twenty years. A pure-Go PDF library hands back text and
// coordinates and leaves the columns to the caller.
//
// The cost is honest: the server needs poppler installed, which the shared
// host has. Nothing but reading an uploaded PDF needs it, so a machine
// without it runs everything else, and says so when a PDF arrives.
//
// # Why a separate process is the safer choice too
//
// poppler is a large parser of a famously loose format, facing whatever
// anybody uploads. In its own process, a file that makes it spin is stopped
// by the deadline, and one that makes it crash or grow until the host kills
// it costs that process and not this one. The bytes go in on stdin and the
// text comes out on stdout, so no file is named, written or left behind.
package pdftext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The ways reading a PDF fails, for the caller to say in its own words.
var (
	// ErrUnavailable is a machine without pdftotext.
	ErrUnavailable = errors.New("pdftext: pdftotext is not installed")

	// ErrNoText is a PDF with no text in it worth reading: a scan, almost
	// always.
	ErrNoText = errors.New("pdftext: the PDF has no text in it")

	// ErrPassword is a PDF that needs a password to open.
	ErrPassword = errors.New("pdftext: the PDF is locked with a password")

	// ErrUnreadable is anything else pdftotext refused or gave up on: a
	// damaged file, one that is not a PDF, or one that took too long.
	ErrUnreadable = errors.New("pdftext: the PDF could not be read")
)

// The limits on one file.
const (
	// MaxPages is how many pages are read. A month of a busy account is a
	// dozen; a year's printout is not a statement to import in one go.
	MaxPages = 50

	// MaxText is how much text is kept, about fifty full pages of it. Past
	// it the output is cut off rather than held: a file that unpacks into
	// more is not a statement.
	MaxText = 4 << 20

	// Timeout is how long one file may take. A statement takes well under a
	// second.
	Timeout = 20 * time.Second

	// minText is the length below which the output is taken for no text at
	// all. A scan is not an empty file to pdftotext: it still returns page
	// breaks and the odd stray mark, and testing for emptiness would let a
	// scan through to a reader that then reports no transactions rather
	// than the real problem, which is that the file is a photograph.
	minText = 200
)

// candidates is where pdftotext is looked for when it is not on the PATH. A
// server process may be started with a PATH of almost nothing, and poppler
// is in /usr/bin wherever a distribution installed it.
var candidates = []string{"/usr/bin/pdftotext", "/usr/local/bin/pdftotext", "/opt/homebrew/bin/pdftotext"}

// binary finds pdftotext.
func binary() (string, error) {
	if path, err := exec.LookPath("pdftotext"); err == nil {
		return path, nil
	}

	for _, path := range candidates {
		if p, err := exec.LookPath(path); err == nil {
			return p, nil
		}
	}

	return "", ErrUnavailable
}

// Available reports whether text can be read on this machine, so a test can
// skip and a page can say so before a person waits.
func Available() bool {
	_, err := binary()

	return err == nil
}

// one is held while pdftotext runs. It takes a few tens of megabytes for a
// statement, and a shared host's memory is not ours to spend several times
// over because several people pressed Upload in the same second -- the same
// reasoning as imaging's photos, with a turn of its own because the two do
// not hold memory in the same process.
var one = make(chan struct{}, 1)

// Extract returns the text of a PDF with its layout kept: each line as it was
// printed, its columns apart by runs of spaces, and its pages separated by
// form feeds.
//
// The -layout flag is not a nicety. Without it poppler gives the text in the
// order the PDF happens to draw it, which for a statement interleaves the
// columns and loses which figure was the amount and which the balance.
func Extract(ctx context.Context, pdf []byte) (string, error) {
	path, err := binary()
	if err != nil {
		return "", err
	}

	select {
	case one <- struct{}{}:
		defer func() { <-one }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	out, errOut := &capped{max: MaxText}, &capped{max: 4096}

	// "-" for both: the PDF from stdin and the text to stdout.
	cmd := exec.CommandContext(ctx, path, "-layout", "-enc", "UTF-8", "-l", strconv.Itoa(MaxPages), "-", "-")
	cmd.Stdin = bytes.NewReader(pdf)
	cmd.Stdout = out
	cmd.Stderr = errOut

	if err := cmd.Run(); err != nil {
		if strings.Contains(strings.ToLower(errOut.buf.String()), "password") {
			return "", ErrPassword
		}

		return "", fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	text := out.buf.String()

	if len(strings.TrimSpace(text)) < minText {
		return "", ErrNoText
	}

	return text, nil
}

// capped keeps the first max bytes written to it and drops the rest, so
// that a process writing without end fills nothing but its own pipe.
type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}

	return len(p), nil
}
