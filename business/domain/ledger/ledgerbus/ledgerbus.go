// Package ledgerbus is an account's transactions and the statements they
// came from (docs/plan.md, "Importing a statement").
//
// A statement arrives as a file (filebus), is read by a source
// (importbus), is checked against its balances, and has the transactions an
// earlier statement already brought set aside; what is left is stored in one
// transaction with the statement that brought it and a line of history.
// A statement that does not balance imports nothing, and the page says which
// row broke: half a month imported is worse than none, because nobody goes
// looking for the other half.
//
// Who may do what is the account's: Bookkeep to import or remove a
// statement, Read to see them. Every lookup that misses -- an account,
// statement or file the actor cannot see, or that does not exist -- is
// ErrNotFound, so that a stranger cannot tell the two apart.
package ledgerbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/csvsource"
	"github.com/jroedel/reconcile/business/domain/importing/sources/ofxsource"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/pdftext"
)

// The errors a page tells apart.
var (
	// ErrNotFound and ErrForbidden are the tenancy domain's, so that a page
	// answers them the same way wherever they come from.
	ErrNotFound  = tenancybus.ErrNotFound
	ErrForbidden = tenancybus.ErrForbidden

	// The reasons a PDF cannot be read, each said differently on the page
	// and each ending in the same advice: download CSV or OFX instead.
	ErrPDFUnavailable = errors.New("PDFs cannot be read on this server")
	ErrPDFScan        = errors.New("the PDF is a picture, with no text to read")
	ErrPDFPassword    = errors.New("the PDF is locked with a password")
	ErrPDFUnreadable  = errors.New("the PDF could not be opened")
	ErrPDFNoRows      = errors.New("no transactions could be found in the PDF")

	// ErrUnreadable is a file that is neither OFX nor a CSV export with a
	// date and an amount column.
	ErrUnreadable = errors.New("that file is not a statement this can read")

	// ErrEmpty is a file in which no transaction could be read.
	ErrEmpty = errors.New("no transactions could be read from that file")

	// ErrUnbalanced is a statement that does not agree with its balances.
	ErrUnbalanced = errors.New("the statement does not balance")

	// ErrSameFile is a file already imported into this account.
	ErrSameFile = errors.New("that file was already imported here")

	// ErrUnstable is a file that importing a second time, inside the
	// preview's rollback, would have changed again: some rule of the
	// import depends on something it should not (docs/duplicates.md). It is
	// the developers' to fix, and nothing is imported until they do.
	ErrUnstable = errors.New("importing the file twice did not give the same answer")
)

// The actions this domain writes into the history.
const (
	StatementImported eventbus.Action = "statement.imported"
	StatementRemoved  eventbus.Action = "statement.removed"
)

// MaxFile is the largest statement accepted. A year of a busy account's CSV
// is a few hundred kilobytes; this is room for a bank that pads.
const MaxFile = 10 << 20

// Accounts is how this domain asks who may do what, and names projects
// (tenancybus).
type Accounts interface {
	Account(ctx context.Context, actor, id types.ID) (tenancybus.Account, tenancybus.Access, error)
	Project(ctx context.Context, actor, id types.ID) (tenancybus.Project, tenancybus.Access, error)
	AccessTo(ctx context.Context, actor types.ID, scope types.Scope) (tenancybus.Access, error)
	ProjectsFor(ctx context.Context, actor types.ID, p tenancybus.Permission) ([]tenancybus.Project, error)
	ProjectNames(ctx context.Context, ids []types.ID) (map[types.ID]string, error)
	Overview(ctx context.Context, actor types.ID) (tenancybus.Overview, error)
}

// Files is where statements' bytes are (filebus).
type Files interface {
	ByID(ctx context.Context, id types.ID) (filebus.File, error)
	ReadAll(f filebus.File) ([]byte, error)
}

// Storer keeps statements, transactions and the CSV mappings.
type Storer interface {
	// Import stores a statement and those of its transactions not already
	// in the account, and returns the statement with Added, Already and
	// Locked counted. With commit false it does all of it and rolls back, so a
	// preview's counts are the import's, by construction.
	Import(ctx context.Context, st Statement, txs []Transaction, also map[types.ID][]eventbus.Event, mapping *SavedMapping, ev eventbus.Event, commit bool) (Statement, error)

	StatementByID(ctx context.Context, id types.ID) (Statement, error)
	StatementWithFile(ctx context.Context, account types.ID, sha string) (Statement, bool, error)
	Statements(ctx context.Context, account types.ID) ([]Statement, error)
	// RemoveStatement deletes a statement and the transactions it brought
	// in, or answers ErrLocked if any of them is in a reconciled period.
	RemoveStatement(ctx context.Context, st Statement, ev eventbus.Event) (int, error)

	Months(ctx context.Context, account types.ID) ([]Month, error)
	Transactions(ctx context.Context, account types.ID, from, to types.Date) ([]Transaction, error)

	Mappings(ctx context.Context, account types.ID) (map[string]csvsource.Mapping, error)

	// TransactionByID is one transaction with its parts.
	TransactionByID(ctx context.Context, id types.ID) (Transaction, error)

	// ReplaceSplits writes a transaction's new parts in place of the old,
	// with the history, in one transaction.
	ReplaceSplits(ctx context.Context, transactionID types.ID, splits []Split, events []eventbus.Event) error

	// ProjectLines is every part in a project, oldest first.
	ProjectLines(ctx context.Context, projectID types.ID) ([]ProjectLine, error)

	// OrgLines is the same of an organization's accounts, for a period.
	OrgLines(ctx context.Context, orgID types.ID, from, to types.Date) ([]ProjectLine, error)

	// Settling is the sums of a list's transfer and pass-through
	// categories, by category, currency and month.
	Settling(ctx context.Context, owner types.Scope) ([]SettlingRow, error)

	// TransactionsByID is several transactions with their parts.
	TransactionsByID(ctx context.Context, ids []types.ID) ([]Transaction, error)

	// Matching is the transactions in any of the accounts for exactly the
	// amount, either way round, posted between from and to inclusive.
	Matching(ctx context.Context, accounts []types.ID, amount money.Amount, from, to types.Date) ([]Transaction, error)

	// Reconcile stores a reconciliation and its history if the statement
	// has none, and reports whether it did: one statement, so that two
	// people marking at once cannot both.
	Reconcile(ctx context.Context, r Reconciliation, ev eventbus.Event) (bool, error)

	// Reopen deletes a statement's reconciliation and writes the history,
	// and reports whether there was one.
	Reopen(ctx context.Context, statementID types.ID, ev eventbus.Event) (bool, error)

	// Reconciliations is an account's, by the start of their periods.
	Reconciliations(ctx context.Context, account types.ID) ([]Reconciliation, error)

	// Blank is the account's transactions a sorting rule may still sort:
	// one part, nothing chosen, outside a reconciled period.
	Blank(ctx context.Context, account types.ID) ([]Transaction, error)

	// SortByRule writes the parts rules sorted, each only if it is still
	// blank, and ev if any was; it returns how many were.
	SortByRule(ctx context.Context, sorted []SortedPart, ev eventbus.Event) (int, error)

	// RuleCounts is how many parts each of the account's rules sorted.
	RuleCounts(ctx context.Context, account types.ID) (map[types.ID]int, error)

	// Examples is what a person sorted in the account, for suggestions.
	Examples(ctx context.Context, account types.ID) ([]rulebus.Example, error)

	// ByHolder is whether the account's statements arrive one file per
	// cardholder (holders.go), and SetByHolder turns it on or off, giving
	// every stored row its identity again in the same transaction.
	ByHolder(ctx context.Context, account types.ID) (bool, error)
	SetByHolder(ctx context.Context, account types.ID, on bool, by types.ID, ev eventbus.Event) error

	// Holders is the cardholders the account's rows have named, by name;
	// HolderTotals what each one's rows come to from start up to end.
	Holders(ctx context.Context, account types.ID) ([]string, error)
	HolderTotals(ctx context.Context, account types.ID, start, end types.Date) ([]HolderTotal, error)

	// Explanation is a transaction's explanation (explain.go), if any.
	Explanation(ctx context.Context, transactionID types.ID) (Stored, bool, error)

	// Explained is how an account's explanations were gathered, newest
	// first, with each explained transaction's description.
	Explained(ctx context.Context, account types.ID) ([]Remembered, error)

	// Clearing is which explanation each of some transactions is a line
	// of, and which are explained themselves, with whether each of those
	// is settled.
	Clearing(ctx context.Context, txs []types.ID) (map[types.ID]types.ID, map[types.ID]bool, error)

	// SaveExplanation writes how it was gathered and its lines; a line
	// another explanation has is ErrExplained, and nothing is saved.
	SaveExplanation(ctx context.Context, x Stored, add, remove []types.ID, by types.ID, ev eventbus.Event) error

	// Settle writes the note and whether the difference is accepted.
	Settle(ctx context.Context, transactionID types.ID, note string, accepted bool, by types.ID, ev eventbus.Event) error
}

// SavedMapping is a CSV mapping kept for the next file with the same
// header.
type SavedMapping struct {
	Fingerprint string
	Mapping     csvsource.Mapping
	By          types.ID
	At          time.Time
}

// Business is the set of operations on the ledger.
type Business struct {
	log        *slog.Logger
	store      Storer
	accounts   Accounts
	files      Files
	categories Categories
	rules      Rules
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer, accounts Accounts, files Files, categories Categories, rules Rules) *Business {
	return &Business{log: log, store: store, accounts: accounts, files: files, categories: categories, rules: rules}
}

// --- reading a file ---------------------------------------------------------

// Options is what a person chose on the preview page.
type Options struct {
	// Mapping is the CSV's columns. Ignored for OFX.
	Mapping csvsource.Mapping

	// Opening and Closing are balances typed from the paper statement, for
	// a file that prints none.
	Opening, Closing importbus.Balance

	// Invert turns a PDF's amounts round: a card issuer prints a purchase
	// as a positive amount it is owed. Without Options it is on for a card
	// account and off for any other. A CSV's is its Mapping's.
	Invert bool

	// Import is the rows, by their place among the file's rows, to import
	// although the count rule sets them aside (Doubt).
	Import []int

	// Holder is whose a file is that names no cardholder, on an account
	// whose statements arrive one file per cardholder: a person's answer
	// on the preview (Draft.Unnamed).
	Holder string
}

// Draft is a file read for an account and not yet imported: everything the
// preview page shows.
type Draft struct {
	Account tenancybus.Account
	File    filebus.File
	Format  Format

	// For a CSV: its header and first rows, the mapping in use, and
	// whether it was remembered from an earlier file with the same header.
	Inspection csvsource.Inspection
	Mapping    csvsource.Mapping
	Remembered bool

	// Unmapped is a mapping that names no date or no amount column in this
	// file; nothing else below is filled in.
	Unmapped bool

	Result           importbus.Result
	Opening, Closing importbus.Balance
	Check            Check

	// Invert is whether a PDF's amounts were turned round (Options).
	Invert bool

	// ByHolder is whether the account's statements arrive one file per
	// cardholder (holders.go). Holders is the cardholders its rows have
	// named before, Unnamed how many of this file's rows name none, and
	// Holder whose a person said they are.
	ByHolder bool
	Holders  []string
	Unnamed  int
	Holder   string

	// Statement is what importing would make, with Added and Already
	// counted.
	Statement Statement

	// Earlier is the statement this same file already made, if it did.
	Earlier    Statement
	HasEarlier bool
}

// Ready reports whether importing would go ahead.
func (d Draft) Ready() bool {
	return !d.Unmapped && !d.HasEarlier && !d.Check.Failed() && len(d.Result.Records) > 0 && d.Statement.Locked == 0
}

// Prepare reads an uploaded file for an account. With opts nil it uses the
// mapping remembered for the file's header, or a guessed one, and the
// balances the file states.
//
// The file must be one the actor uploaded. A file's identifier reaching
// somebody else -- in a log line, say -- is no way into its contents.
func (b *Business) Prepare(ctx context.Context, actor, accountID, fileID types.ID, opts *Options) (Draft, error) {
	account, access, err := b.accounts.Account(ctx, actor, accountID)
	if err != nil {
		return Draft{}, err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return Draft{}, ErrForbidden
	}

	f, err := b.files.ByID(ctx, fileID)
	if errors.Is(err, filebus.ErrNotFound) || err == nil && f.UploadedBy != actor {
		return Draft{}, ErrNotFound
	}

	if err != nil {
		return Draft{}, err
	}

	data, err := b.files.ReadAll(f)
	if err != nil {
		return Draft{}, fmt.Errorf("reading the statement: %w", err)
	}

	d := Draft{Account: account, File: f}

	if err := b.read(ctx, &d, data, opts); err != nil {
		return Draft{}, err
	}

	if d.Unmapped {
		return d, nil
	}

	if err := b.holders(ctx, &d, opts); err != nil {
		return Draft{}, err
	}

	if d.Earlier, d.HasEarlier, err = b.store.StatementWithFile(ctx, accountID, f.SHA256); err != nil {
		return Draft{}, err
	}

	d.Check = verify(d.Result.Records, d.Opening, d.Closing, d.Result.Total, account.Kind == tenancybus.Card)

	if len(d.Result.Records) == 0 || d.Check.Failed() {
		return d, nil
	}

	// The counts come from doing the import and rolling it back, with the
	// rules applied, so that the preview can say how many they would sort.
	now := time.Now()
	st, txs := b.statement(d, actor, now)
	insist(txs, opts)

	if _, err := b.sortNew(ctx, now, actor, account, txs); err != nil {
		return Draft{}, err
	}

	if d.Statement, err = b.store.Import(ctx, st, txs, nil, nil, eventbus.Event{}, false); err != nil {
		if errors.Is(err, ErrUnstable) {
			b.log.Error("a statement was refused: importing it twice did not give the same answer",
				"account_id", accountID.String(), "file_id", fileID.String(), "error", err)
		}

		return Draft{}, err
	}

	return d, nil
}

// read sniffs the format and reads the file into the draft.
func (b *Business) read(ctx context.Context, d *Draft, data []byte, opts *Options) error {
	head := data[:min(len(data), 1024)]

	switch {
	case ofxsource.Looks(head):
		d.Format = OFX

		res, err := ofxsource.Read(data)
		if err != nil {
			return ErrUnreadable
		}

		d.Result = res
		d.Closing = res.Closing

		if opts != nil {
			d.Opening = opts.Opening

			if opts.Closing.Known {
				d.Closing = opts.Closing
			}
		}

		return nil

	case strings.HasPrefix(http.DetectContentType(head), "application/pdf"):
		return b.readPDF(ctx, d, data, opts)

	case !strings.HasPrefix(http.DetectContentType(head), "text/"):
		return ErrUnreadable
	}

	d.Format = CSV

	if err := b.mapping(ctx, d, data, opts); err != nil {
		return err
	}

	res, err := csvsource.Read(data, d.Mapping)
	if errors.Is(err, csvsource.ErrNoHeader) {
		d.Unmapped = true

		return nil
	}

	if err != nil {
		return ErrUnreadable
	}

	d.Result = res

	if opts != nil {
		d.Opening, d.Closing = opts.Opening, opts.Closing
	}

	return nil
}

// readPDF reads a PDF's text into the draft (docs/pdf-statements.md). The
// balances and the total it states are what it is checked by, unless a
// person typed balances of their own.
func (b *Business) readPDF(ctx context.Context, d *Draft, data []byte, opts *Options) error {
	d.Format = PDF

	text, err := pdftext.Extract(ctx, data)

	switch {
	case errors.Is(err, pdftext.ErrUnavailable):
		b.log.Error("a PDF statement arrived and pdftotext is not installed")

		return ErrPDFUnavailable
	case errors.Is(err, pdftext.ErrNoText):
		return ErrPDFScan
	case errors.Is(err, pdftext.ErrPassword):
		return ErrPDFPassword
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		b.log.Info("a PDF statement could not be read", "file_id", d.File.ID.String(), "error", err)

		return ErrPDFUnreadable
	}

	res, err := pdfsource.Read(text)
	if err != nil {
		return ErrPDFNoRows
	}

	d.Invert = d.Account.Kind == tenancybus.Card
	d.Opening, d.Closing = res.Opening, res.Closing

	if opts != nil {
		d.Invert = opts.Invert

		if opts.Opening.Known {
			d.Opening = opts.Opening
		}

		if opts.Closing.Known {
			d.Closing = opts.Closing
		}
	}

	// Turned round as a whole, rows, balances and total alike, so that the
	// check below holds or fails the same either way.
	if d.Invert {
		for i := range res.Records {
			res.Records[i].Amount = -res.Records[i].Amount
		}
	}

	d.Result = res

	return nil
}

// mapping chooses the columns for a CSV: the person's, else one remembered
// for this header, else a guess.
//
// A remembered mapping is tried with its own skipped lines, because the
// header of a bank that prefaces its export with a title is only found
// after the title.
func (b *Business) mapping(ctx context.Context, d *Draft, data []byte, opts *Options) error {
	if opts != nil {
		in, err := csvsource.Inspect(data, opts.Mapping.SkipLines)
		if err != nil {
			return ErrUnreadable
		}

		d.Inspection, d.Mapping = in, opts.Mapping
		if d.Mapping.Separator == "" {
			d.Mapping.Separator = in.Detected.Separator
		}

		return nil
	}

	saved, err := b.store.Mappings(ctx, d.Account.ID)
	if err != nil {
		return err
	}

	for fingerprint, m := range saved {
		if in, err := csvsource.Inspect(data, m.SkipLines); err == nil && in.Fingerprint == fingerprint {
			d.Inspection, d.Mapping, d.Remembered = in, m, true

			return nil
		}
	}

	in, err := csvsource.Inspect(data, 0)
	if err != nil {
		return ErrUnreadable
	}

	d.Inspection, d.Mapping = in, in.Detected

	return nil
}

// statement is the rows a draft would store.
func (b *Business) statement(d Draft, actor types.ID, now time.Time) (Statement, []Transaction) {
	st := Statement{
		ID:         types.NewID(),
		AccountID:  d.Account.ID,
		FileID:     d.File.ID,
		FileName:   d.File.Name,
		Format:     d.Format,
		Checked:    d.Check.Method,
		ImportedBy: actor,
		ImportedAt: now,
	}

	switch d.Check.Method {
	case ByTotals:
		st.Opening, st.HasOpening = d.Opening.Amount, true
		st.Closing, st.HasClosing = d.Closing.Amount, true
	case ByBalances:
		st.Opening, st.HasOpening = d.Check.Opening, true
		st.Closing, st.HasClosing = d.Check.Closing, true
	}

	// A total says nothing of the balance before or after, so a statement
	// checked by one keeps none (BySum).

	st.ByHolder = d.ByHolder
	txs := transactions(d.Account.ID, d.Result.Records, d.ByHolder)

	// The period the file states, or else the one its rows span.
	if !d.Result.Start.IsZero() {
		st.Start = types.DateOf(d.Result.Start)
	}

	if !d.Result.End.IsZero() {
		st.End = types.DateOf(d.Result.End)
	}

	for _, t := range txs {
		if st.Start.Zero() || t.PostedOn.Before(st.Start) {
			st.Start = t.PostedOn
		}

		if st.End.Zero() || st.End.Before(t.PostedOn) {
			st.End = t.PostedOn
		}
	}

	for i := range txs {
		txs[i].StatementID = st.ID
	}

	return st, txs
}

// Import reads the file as Prepare does and stores it, remembering a CSV's
// mapping for the next file with the same header. A draft that is not Ready
// imports nothing, and the error says why.
func (b *Business) Import(ctx context.Context, now time.Time, actor, accountID, fileID types.ID, opts Options) (Statement, error) {
	d, err := b.Prepare(ctx, actor, accountID, fileID, &opts)
	if err != nil {
		return Statement{}, err
	}

	switch {
	case d.Unmapped:
		return Statement{}, ErrUnreadable
	case d.HasEarlier:
		return Statement{}, ErrSameFile
	case len(d.Result.Records) == 0:
		return Statement{}, ErrEmpty
	case d.Check.Failed():
		return Statement{}, ErrUnbalanced
	case d.Statement.Locked > 0:
		return Statement{}, ErrLocked
	}

	st, txs := b.statement(d, actor, now)
	insist(txs, &opts)

	also, err := b.sortNew(ctx, now, actor, d.Account, txs)
	if err != nil {
		return Statement{}, err
	}

	var saved *SavedMapping
	if d.Format == CSV {
		saved = &SavedMapping{Fingerprint: d.Inspection.Fingerprint, Mapping: d.Mapping, By: actor, At: now}
	}

	ev := eventbus.New(now, actor, d.Account.Scope(), StatementImported, nil)

	st, err = b.store.Import(ctx, st, txs, also, saved, ev, true)
	if err != nil {
		return Statement{}, err
	}

	b.log.Info("a statement was imported", "account_id", accountID.String(), "statement_id", st.ID.String(),
		"added", st.Added, "already", st.Already, "by_rule", st.ByRule, "checked", string(st.Checked))

	return st, nil
}

// insist marks the rows a person chose to import although the count rule
// set them aside.
func insist(txs []Transaction, opts *Options) {
	if opts == nil {
		return
	}

	for _, i := range opts.Import {
		if i >= 0 && i < len(txs) {
			txs[i].Insist = true
		}
	}
}

// EventDetail is what the history says about a statement. The store fills
// it in on import, because only the store knows the counts.
func EventDetail(st Statement) map[string]string {
	return map[string]string{
		"name":     st.FileName,
		"start":    st.Start.String(),
		"end":      st.End.String(),
		"added":    strconv.Itoa(st.Added),
		"already":  strconv.Itoa(st.Already),
		"byrule":   strconv.Itoa(st.ByRule),
		"setaside": strconv.Itoa(st.SetAside()),
	}
}

// --- reading the ledger -----------------------------------------------------

// Statements is an account's statements, newest period first.
func (b *Business) Statements(ctx context.Context, actor, accountID types.ID) ([]Statement, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	return b.store.Statements(ctx, accountID)
}

// Statement is one statement and what the actor holds on its account.
func (b *Business) Statement(ctx context.Context, actor, id types.ID) (Statement, tenancybus.Access, error) {
	st, err := b.store.StatementByID(ctx, id)
	if err != nil {
		return Statement{}, tenancybus.Access{}, err
	}

	_, access, err := b.accounts.Account(ctx, actor, st.AccountID)
	if err != nil {
		return Statement{}, tenancybus.Access{}, err
	}

	return st, access, nil
}

// StatementFile is the file a statement was read from, for downloading.
// Anyone who may read the account may read it: it holds nothing the
// transactions do not.
func (b *Business) StatementFile(ctx context.Context, actor, id types.ID) (filebus.File, error) {
	st, _, err := b.Statement(ctx, actor, id)
	if err != nil {
		return filebus.File{}, err
	}

	return b.files.ByID(ctx, st.FileID)
}

// RemoveStatement takes a statement out, with the transactions it brought
// in and how they were split and sorted. Transactions a later statement also listed go with it: they were this
// one's, and the later one counted them as already here. Importing this
// file again brings them back; that is what removing is for, a statement
// read with the wrong columns.
func (b *Business) RemoveStatement(ctx context.Context, now time.Time, actor, id types.ID) (Statement, error) {
	st, access, err := b.Statement(ctx, actor, id)
	if err != nil {
		return Statement{}, err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return Statement{}, ErrForbidden
	}

	// A reconciled statement is reopened first, with a reason; the store
	// refuses the rest of what removing would unlock (Storer).
	if st.Reconciled() {
		return st, ErrLocked
	}

	ev := eventbus.New(now, actor, types.AccountScope(st.AccountID), StatementRemoved, EventDetail(st))

	n, err := b.store.RemoveStatement(ctx, st, ev)
	if err != nil {
		return Statement{}, err
	}

	b.log.Info("a statement was removed", "account_id", st.AccountID.String(), "statement_id", id.String(), "transactions", n)

	return st, nil
}

// Months is an account's months that have transactions, newest first.
func (b *Business) Months(ctx context.Context, actor, accountID types.ID) ([]Month, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	return b.store.Months(ctx, accountID)
}

// Transactions is one month of an account, "2026-07", oldest first.
func (b *Business) Transactions(ctx context.Context, actor, accountID types.ID, month string) ([]Transaction, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	from, to, err := MonthRange(month)
	if err != nil {
		return nil, ErrNotFound
	}

	return b.store.Transactions(ctx, accountID, from, to)
}

// Between is an account's transactions posted from one day to another, both
// included, oldest first.
func (b *Business) Between(ctx context.Context, actor, accountID types.ID, from, to types.Date) ([]Transaction, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	return b.store.Transactions(ctx, accountID, from, to.AddDays(1))
}

// --- for other domains -------------------------------------------------------

// Reconciliations is an account's, by the start of their periods, asking
// nobody's permission: for the export, which has asked for the account or
// the project already, and says beside each line whether its period was
// reconciled.
func (b *Business) Reconciliations(ctx context.Context, accountID types.ID) ([]Reconciliation, error) {
	return b.store.Reconciliations(ctx, accountID)
}

// Lookup is one transaction with its parts, asking nobody's permission: for
// the receipt domain, which decides who may see a receipt by what it is
// attached to and must read the transaction to know.
func (b *Business) Lookup(ctx context.Context, id types.ID) (Transaction, error) {
	return b.store.TransactionByID(ctx, id)
}

// LookupAll is Lookup for several, in no promised order.
func (b *Business) LookupAll(ctx context.Context, ids []types.ID) ([]Transaction, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	return b.store.TransactionsByID(ctx, ids)
}

// Matching is the transactions a receipt for amount, dated on, might be: in
// the accounts given, for exactly the amount, either way round, within
// days of it. Asks nobody's permission; the caller chose the accounts.
func (b *Business) Matching(ctx context.Context, accounts []types.ID, amount money.Amount, on types.Date, days int) ([]Transaction, error) {
	if len(accounts) == 0 || amount == 0 || on.Zero() {
		return nil, nil
	}

	t, err := time.Parse("2006-01-02", on.String())
	if err != nil {
		return nil, err
	}

	return b.store.Matching(ctx, accounts, amount.Abs(), types.DateOf(t.AddDate(0, 0, -days)), types.DateOf(t.AddDate(0, 0, days)))
}

// MonthRange is the first day of a month, "2026-07", and the first day of
// the next.
func MonthRange(month string) (types.Date, types.Date, error) {
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return types.Date{}, types.Date{}, fmt.Errorf("%q is not a month", month)
	}

	return types.DateOf(start), types.DateOf(start.AddDate(0, 1, 0)), nil
}
