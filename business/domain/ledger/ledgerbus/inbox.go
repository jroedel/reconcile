package ledgerbus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/types"
)

// The statement inbox (docs/books-api.md, "The inbox"): statement files a
// program sent in a person's name -- a script in their Google account,
// posting what their email brings -- waiting for that person, or their
// Claude, to import them.
//
// An inbox file belongs to a person, not to an account. Which account a
// file is for is exactly what a bulk import works out (Propose), among the
// accounts the person keeps the books of, so the inbox is a list of files
// to propose and nothing more: /imports shows them as its list, and
// importing them is importing that list. A file leaves the inbox when
// everything in it is imported (ImportProposals closes it) or when a
// person dismisses it.
//
// The same file sent again is recognised by its content, not its name: a
// script that sends one attachment twice, or two emails with the same
// statement, adds one file to the inbox.

// Received is what became of a file sent to the inbox.
type Received string

const (
	// Arrived is a file new to the person's inbox.
	Arrived Received = "received"

	// AlreadyWaiting is a file that was sent before and is still in the
	// inbox, or was dismissed from it.
	AlreadyWaiting Received = "already_waiting"

	// AlreadyImported is a file that was sent before and imported.
	AlreadyImported Received = "already_imported"
)

// MaxSource is the longest note a program may keep with a file it sends,
// for its own reference ("gmail:<message id>").
const MaxSource = 200

// InboxFile is one file in a person's statement inbox.
type InboxFile struct {
	ID         types.ID
	UserID     types.ID
	FileID     types.ID
	SHA256     string
	Name       string
	Source     string
	ReceivedAt time.Time

	// ClosedAt is when everything in it was imported, DismissedAt when a
	// person took it off the list; zero while it waits.
	ClosedAt    time.Time
	DismissedAt time.Time
}

// Receive keeps a file a program sent to the actor's inbox: the file
// itself, up to MaxFile, and a line in the inbox unless the same content
// is there already. The answer says which.
//
// The content is read and hashed before anything is kept, so that a file
// sent a second time leaves nothing behind: not a second upload, not a
// second line.
func (b *Business) Receive(ctx context.Context, now time.Time, actor types.ID, name, source string, r io.Reader) (Received, error) {
	content, err := io.ReadAll(io.LimitReader(r, MaxFile+1))

	switch {
	case err != nil:
		return "", fmt.Errorf("reading the file: %w", err)
	case len(content) > MaxFile:
		return "", filebus.ErrTooBig
	case len(content) == 0:
		return "", ErrEmpty
	}

	source = strings.Join(strings.Fields(source), " ")
	if utf8.RuneCountInString(source) > MaxSource {
		source = string([]rune(source)[:MaxSource])
	}

	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])

	if got, err := b.sent(ctx, actor, sha); err != nil || got != "" {
		return got, err
	}

	f, err := b.files.Save(ctx, now, actor, name, bytes.NewReader(content), MaxFile, nil)
	if err != nil {
		return "", fmt.Errorf("keeping the file: %w", err)
	}

	added, err := b.store.AddToInbox(ctx, InboxFile{
		ID: types.NewID(), UserID: actor, FileID: f.ID, SHA256: sha,
		Name: f.Name, Source: source, ReceivedAt: now,
	})

	switch {
	case err != nil:
		return "", err
	case !added:
		// The same file, sent twice at once: the other request's line
		// stands, and this upload is one more row of the files table,
		// which is what any upload is.
		return b.sent(ctx, actor, sha)
	}

	b.log.Info("a statement arrived in an inbox", "user_id", actor.String(), "file_id", f.ID.String())

	return Arrived, nil
}

// sent is what Receive answers for content the actor's inbox has had
// before, or "" for content new to it.
func (b *Business) sent(ctx context.Context, actor types.ID, sha string) (Received, error) {
	in, err := b.store.InboxFileBySHA(ctx, actor, sha)

	switch {
	case errors.Is(err, ErrNotFound):
		return "", nil
	case err != nil:
		return "", err
	case !in.ClosedAt.IsZero():
		return AlreadyImported, nil
	}

	return AlreadyWaiting, nil
}

// Inbox is the files waiting in the actor's inbox, oldest first. Nobody
// else's: an inbox is its person's, as the files in it are.
func (b *Business) Inbox(ctx context.Context, actor types.ID) ([]InboxFile, error) {
	return b.store.Inbox(ctx, actor)
}

// Dismiss takes a file off the actor's inbox without importing it. The
// file is kept, as every upload is, and the same content sent again stays
// off the list. A file that is not in the actor's inbox is ErrNotFound.
func (b *Business) Dismiss(ctx context.Context, now time.Time, actor, id types.ID) error {
	return b.store.DismissInboxFile(ctx, actor, id, now)
}

// closeImported closes the inbox lines of the files whose every proposal
// is now in its account: imported before (Imported), or just now
// (Unattended, which ImportProposals imported). A file with one part
// still needing a person stays in the inbox, so that the part is not
// forgotten.
func (b *Business) closeImported(ctx context.Context, now time.Time, actor types.ID, props []Proposal, failed map[types.ID]bool) error {
	done := map[types.ID]bool{}

	for _, p := range props {
		in := (p.Imported() || p.Unattended()) && !failed[p.File.ID]

		if prev, seen := done[p.File.ID]; seen {
			done[p.File.ID] = prev && in
		} else {
			done[p.File.ID] = in
		}
	}

	var files []types.ID

	for id, ok := range done {
		if ok {
			files = append(files, id)
		}
	}

	if len(files) == 0 {
		return nil
	}

	return b.store.CloseInboxFiles(ctx, actor, files, now)
}
