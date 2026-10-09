package ledgerbus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Entries by hand (docs/clearing.md, 1): a transaction no file lists, such
// as a ticket somebody else paid on their own card and added to this
// account's month, entered by a bookkeeper with the document that
// supports it.
//
// It is stored as a statement of its own, format Hand, whose file is that
// document, with one transaction. So it has the identity, the parts, the
// sorting, the receipts, the history and the removal every transaction
// has, the accountant's package carries the document with the statements,
// and the statements table needed no change. The document is required: an
// entry nobody can show the paper for is the one an accountant asks about.

// Hand is the format of an entry by hand, and ByHand how it was "checked":
// by the person who entered it, against its document.
const (
	Hand   Format = "hand"
	ByHand Method = "hand"
)

// TransactionEntered is the history's action for an entry by hand.
const TransactionEntered eventbus.Action = "transaction.entered"

// HandAccept is what an entry's document may be: what a receipt may be.
var HandAccept = []string{filebus.JPEG, filebus.PNG, filebus.WebP, filebus.HEIC, filebus.PDF}

// MaxDescription is the longest description typed for an entry.
const MaxDescription = 200

// ErrEntered is an entry identical to a transaction already in the account
// -- the same day, description and amount -- which is far likelier to be
// the same one entered twice than a second. Saying something to tell them
// apart enters it.
var ErrEntered = errors.New("that transaction is in the account already")

// ErrHand is reconciling an entry by hand, which is not a bank's statement.
// Its day is locked by the statement that covers it.
var ErrHand = errors.New("an entry by hand is not a statement to reconcile")

// Entry is what a person typed.
type Entry struct {
	Date        types.Date
	Description string

	// Amount is signed: money into the account is positive.
	Amount money.Amount

	// File is the document, uploaded by the person entering it.
	File types.ID
}

// EntryInvalid names the field of an Entry that will not do: "date",
// "description", "amount" or "file".
type EntryInvalid struct{ Field string }

func (e EntryInvalid) Error() string { return fmt.Sprintf("the entry's %s will not do", e.Field) }

// Enter stores an entry by hand in an account, and returns its
// transaction. The account's sorting rules sort it as they would a row of
// a statement.
func (b *Business) Enter(ctx context.Context, now time.Time, actor, accountID types.ID, e Entry) (Transaction, error) {
	account, access, err := b.accounts.Account(ctx, actor, accountID)
	if err != nil {
		return Transaction{}, err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return Transaction{}, ErrForbidden
	}

	e.Description = strings.Join(strings.Fields(e.Description), " ")

	day, err := time.Parse("2006-01-02", e.Date.String())

	switch {
	case e.Date.Zero() || err != nil:
		return Transaction{}, EntryInvalid{"date"}
	case e.Description == "" || utf8.RuneCountInString(e.Description) > MaxDescription:
		return Transaction{}, EntryInvalid{"description"}
	case e.Amount == 0:
		return Transaction{}, EntryInvalid{"amount"}
	}

	f, err := b.files.ByID(ctx, e.File)
	if errors.Is(err, filebus.ErrNotFound) || err == nil && (f.UploadedBy != actor || !slices.Contains(HandAccept, f.ContentType)) {
		return Transaction{}, EntryInvalid{"file"}
	}

	if err != nil {
		return Transaction{}, err
	}

	st := Statement{
		ID:         types.NewID(),
		AccountID:  account.ID,
		FileID:     f.ID,
		FileName:   f.Name,
		Format:     Hand,
		Start:      e.Date,
		End:        e.Date,
		Checked:    ByHand,
		ImportedBy: actor,
		ImportedAt: now,
	}

	txs := transactions(account.ID, []importbus.Record{{Line: 1, Date: day, Description: e.Description, Amount: e.Amount}})
	txs[0].StatementID = st.ID

	// A person's word that it is another charge: the count rule, which
	// would take it for a row of another statement on the same day for the
	// same amount, never sets it aside.
	txs[0].Insist = true

	also, err := b.sortNew(ctx, now, actor, account, txs)
	if err != nil {
		return Transaction{}, err
	}

	// Looked at first and rolled back, for what it would do: an entry
	// inside a reconciled period is refused, as a statement's row is.
	preview, err := b.store.Import(ctx, st, txs, nil, nil, eventbus.Event{}, false)
	if err != nil {
		return Transaction{}, err
	}

	switch {
	case preview.Locked > 0:
		return Transaction{}, ErrLocked
	case preview.Added == 0:
		return Transaction{}, ErrEntered
	}

	ev := eventbus.New(now, actor, account.Scope(), TransactionEntered, map[string]string{
		"description": e.Description,
		"amount":      e.Amount.String(),
		"currency":    account.Currency,
		"date":        e.Date.String(),
	})

	if _, err := b.store.Import(ctx, st, txs, also, nil, ev, true); err != nil {
		return Transaction{}, err
	}

	b.log.Info("a transaction was entered by hand", "account_id", accountID.String(), "transaction_id", txs[0].ID.String())

	return txs[0], nil
}
