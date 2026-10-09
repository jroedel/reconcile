// Package exportbus is the accountant's package (docs/plan.md, "Export"): a
// zip holding a spreadsheet with one row per part of a transaction, the
// receipts on those transactions under names an accountant can sort, and,
// for an account, the statements the transactions came from.
//
// It is its own domain because it reads several: the ledger for the
// transactions, receipts for what is attached to them, categories and
// tenancy for names, and files for the bytes. None of those may import the
// others in that direction, and a package belongs to none of them.
//
// Who may download is the Export permission on the account or the project:
// owners, bookkeepers and accountants. A project's package holds only the
// project's parts, with their receipts, and no statements: a role on a
// project is no window into the accounts it draws on (docs/plan.md,
// "Inheritance").
package exportbus

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// The errors a page tells apart.
var (
	ErrNotFound  = tenancybus.ErrNotFound
	ErrForbidden = tenancybus.ErrForbidden

	// ErrPeriod is a period that ends before it starts, or is missing an
	// end.
	ErrPeriod = errors.New("choose a period that starts before it ends")
)

// Columns is how many columns the spreadsheet has; the header a caller
// gives Write must have as many, in this order: date, account,
// description, amount, currency, the transaction's total, category, the
// category's kind, project, memo, receipts, statement, the day its
// period was reconciled, the transaction it is part of the explanation of
// (docs/clearing.md, 2), and the cardholder its file said made it (3).
//
// The kind is a column of its own (docs/plan.md, "Kinds of money") so that
// an accountant can map the categories onto their chart of accounts at a
// glance, and see at once which rows are transfers and pass-through rather
// than income or expenses.
const Columns = 15

// ExplanationColumns is how many columns explanations.csv has, in this
// order: the explained transaction's date, account, description and
// amount; one line's date, account, description and amount; the
// difference the lines leave; whether it was accepted; and the note.
const ExplanationColumns = 11

// Accounts is how this domain asks who may do what, and names projects
// (tenancybus).
type Accounts interface {
	Account(ctx context.Context, actor, id types.ID) (tenancybus.Account, tenancybus.Access, error)
	ProjectNames(ctx context.Context, ids []types.ID) (map[types.ID]string, error)
}

// Ledger is the transactions and statements (ledgerbus).
type Ledger interface {
	Between(ctx context.Context, actor, accountID types.ID, from, to types.Date) ([]ledgerbus.Transaction, error)
	Statements(ctx context.Context, actor, accountID types.ID) ([]ledgerbus.Statement, error)
	ProjectBook(ctx context.Context, actor, projectID types.ID) (ledgerbus.Book, error)
	LookupAll(ctx context.Context, ids []types.ID) ([]ledgerbus.Transaction, error)
	Reconciliations(ctx context.Context, accountID types.ID) ([]ledgerbus.Reconciliation, error)
	Clearing(ctx context.Context, actor types.ID, ids []types.ID) (ledgerbus.Clearing, error)
	Explain(ctx context.Context, actor, id types.ID) (ledgerbus.Explanation, error)
}

// Receipts is what is attached to the transactions (receiptbus).
type Receipts interface {
	OnTransactions(ctx context.Context, ids []types.ID) (map[types.ID][]receiptbus.Receipt, error)
}

// Categories names the parts of an account's transactions (categorybus).
type Categories interface {
	ForAccount(ctx context.Context, a tenancybus.Account) ([]categorybus.Category, error)
}

// Files is where the bytes are (filebus).
type Files interface {
	ByID(ctx context.Context, id types.ID) (filebus.File, error)
	Open(f filebus.File) (io.ReadSeekCloser, error)
}

// Business makes packages.
type Business struct {
	log        *slog.Logger
	accounts   Accounts
	ledger     Ledger
	receipts   Receipts
	categories Categories
	files      Files
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, accounts Accounts, ledger Ledger, receipts Receipts, categories Categories, files Files) *Business {
	return &Business{log: log, accounts: accounts, ledger: ledger, receipts: receipts, categories: categories, files: files}
}

// Row is one part of one transaction: a line of the spreadsheet.
type Row struct {
	PostedOn    types.Date
	Account     string
	Description string

	// Amount is the part's; Total is the whole transaction's, so that a
	// $30.00 row is seen to be part of a $33.99 charge.
	Amount, Total money.Amount
	Currency      string

	Category, Project, Memo string

	// Kind is the category's kind; Unsaid for a part with no category, or
	// one whose kind is not said.
	Kind categorybus.Kind

	// Receipts is the paths in the zip of the transaction's receipts, a
	// file per page.
	Receipts []string

	// Statement is the file the transaction came from, in an account's
	// package.
	Statement string

	// Reconciled is the day the period holding the transaction was
	// reconciled, or zero.
	Reconciled types.Date

	// ClearedBy is the transaction this one is part of the explanation
	// of, when the reader may read its account.
	ClearedBy ledgerbus.Transaction

	// Holder is the cardholder the transaction's file said made it.
	Holder string
}

// Entry is a file in the zip beside the spreadsheet.
type Entry struct {
	Path string
	File filebus.File
}

// Package is everything a download holds, decided before a byte is
// written: once the zip has started, a failure can only cut it short.
type Package struct {
	// Name is the download's file name.
	Name    string
	Rows    []Row
	Entries []Entry

	// Explanations is the explained transactions of an account's package,
	// for explanations.csv; none, and there is no such file.
	Explanations []ledgerbus.Explanation
}

// Words is what a package's files say in the reader's language: the
// header rows, and the words for each kind and for an accepted
// difference.
type Words struct {
	Columns      []string
	Kinds        map[categorybus.Kind]string
	Explanations []string
	Accepted     string
}

// Account is an account's package for a period, both days included.
func (b *Business) Account(ctx context.Context, actor, accountID types.ID, from, to types.Date) (Package, error) {
	account, access, err := b.accounts.Account(ctx, actor, accountID)
	if err != nil {
		return Package{}, err
	}

	if !access.Can(tenancybus.Export) {
		return Package{}, ErrForbidden
	}

	if from.Zero() || to.Zero() || to.Before(from) {
		return Package{}, ErrPeriod
	}

	txs, err := b.ledger.Between(ctx, actor, accountID, from, to)
	if err != nil {
		return Package{}, err
	}

	statements, err := b.ledger.Statements(ctx, actor, accountID)
	if err != nil {
		return Package{}, err
	}

	recs, err := b.ledger.Reconciliations(ctx, accountID)
	if err != nil {
		return Package{}, err
	}

	cats, err := b.categories.ForAccount(ctx, account)
	if err != nil {
		return Package{}, err
	}

	categories := make(map[types.ID]categorybus.Category, len(cats))
	for _, c := range cats {
		categories[c.ID] = c
	}

	var projectIDs []types.ID

	for _, t := range txs {
		for _, s := range t.Splits {
			if !s.ProjectID.Zero() {
				projectIDs = append(projectIDs, s.ProjectID)
			}
		}
	}

	projects, err := b.accounts.ProjectNames(ctx, projectIDs)
	if err != nil {
		return Package{}, err
	}

	p := Package{Name: slug(account.Name, "account") + "_" + from.String() + "_" + to.String() + ".zip"}
	n := newNamer()

	// The statements first, so that their files lead the zip's listing
	// after the spreadsheet: they are what the rest is checked against.
	fileNames := make(map[types.ID]string, len(statements))

	for _, st := range slices.Backward(statements) {
		fileNames[st.ID] = st.FileName

		start, end := st.Period()
		if end.Before(from) || to.Before(start) {
			continue
		}

		f, err := b.files.ByID(ctx, st.FileID)
		if err != nil {
			return Package{}, err
		}

		p.Entries = append(p.Entries, Entry{Path: n.free("statements/" + start.String() + "_" + end.String() + "_" + safeName(st.FileName)), File: f})
	}

	attached, err := b.attached(ctx, txs, n, &p)
	if err != nil {
		return Package{}, err
	}

	cleared, err := b.clearing(ctx, actor, txs, &p)
	if err != nil {
		return Package{}, err
	}

	for _, t := range txs {
		for _, s := range t.Splits {
			p.Rows = append(p.Rows, Row{
				PostedOn: t.PostedOn, Account: account.Name, Description: t.Description,
				Amount: s.Amount, Total: t.Amount, Currency: account.Currency,
				Category: categories[s.CategoryID].Name, Kind: categories[s.CategoryID].Kind, Project: projects[s.ProjectID], Memo: s.Memo,
				Receipts: attached[t.ID], Statement: fileNames[t.StatementID], Reconciled: reconciledOn(recs, t.PostedOn),
				ClearedBy: cleared.By[t.ID].Transaction, Holder: t.Holder,
			})
		}
	}

	return p, nil
}

// Project is a project's package: every part in it, from every account,
// with the receipts on their transactions.
func (b *Business) Project(ctx context.Context, actor, projectID types.ID) (Package, error) {
	book, err := b.ledger.ProjectBook(ctx, actor, projectID)
	if err != nil {
		return Package{}, err
	}

	if !book.Access.Can(tenancybus.Export) {
		return Package{}, ErrForbidden
	}

	var ids []types.ID
	for _, l := range book.Lines {
		ids = append(ids, l.Split.TransactionID)
	}

	ids = slices.Compact(ids)

	txs, err := b.ledger.LookupAll(ctx, ids)
	if err != nil {
		return Package{}, err
	}

	// LookupAll promises no order; the rows follow the book's.
	byID := make(map[types.ID]ledgerbus.Transaction, len(txs))
	for _, t := range txs {
		byID[t.ID] = t
	}

	ordered := make([]ledgerbus.Transaction, 0, len(ids))
	for _, id := range ids {
		if t, ok := byID[id]; ok {
			ordered = append(ordered, t)
		}
	}

	recs := map[types.ID][]ledgerbus.Reconciliation{}

	for _, l := range book.Lines {
		if _, ok := recs[l.AccountID]; ok {
			continue
		}

		if recs[l.AccountID], err = b.ledger.Reconciliations(ctx, l.AccountID); err != nil {
			return Package{}, err
		}
	}

	p := Package{Name: slug(book.Project.Name, "project") + ".zip"}

	attached, err := b.attached(ctx, ordered, newNamer(), &p)
	if err != nil {
		return Package{}, err
	}

	// What its lines are cleared by, but not the explanations themselves:
	// a role on a project is no window into the accounts it draws on.
	cleared, err := b.ledger.Clearing(ctx, actor, ids)
	if err != nil {
		return Package{}, err
	}

	for _, l := range book.Lines {
		p.Rows = append(p.Rows, Row{
			PostedOn: l.PostedOn, Account: l.AccountName, Description: l.Description,
			Amount: l.Split.Amount, Total: byID[l.Split.TransactionID].Amount, Currency: l.Currency,
			Category: l.CategoryName, Kind: l.CategoryKind, Project: book.Project.Name, Memo: l.Split.Memo,
			Receipts: attached[l.Split.TransactionID], Reconciled: reconciledOn(recs[l.AccountID], l.PostedOn),
			ClearedBy: cleared.By[l.Split.TransactionID].Transaction, Holder: byID[l.Split.TransactionID].Holder,
		})
	}

	return p, nil
}

// attached names the receipts on the transactions, adds their files to the
// package once each -- a receipt for two charges is one receipt -- and
// answers each transaction's paths.
func (b *Business) attached(ctx context.Context, txs []ledgerbus.Transaction, n *namer, p *Package) (map[types.ID][]string, error) {
	ids := make([]types.ID, len(txs))
	for i, t := range txs {
		ids[i] = t.ID
	}

	on, err := b.receipts.OnTransactions(ctx, ids)
	if err != nil {
		return nil, err
	}

	named := map[types.ID][]string{}
	out := make(map[types.ID][]string, len(on))

	for _, t := range txs {
		for _, rc := range on[t.ID] {
			paths, ok := named[rc.ID]
			if !ok {
				for i, f := range rc.Files {
					path := n.free(receiptPath(rc, t, i+1, f.ContentType))
					paths = append(paths, path)
					p.Entries = append(p.Entries, Entry{Path: path, File: f})
				}

				named[rc.ID] = paths
			}

			out[t.ID] = append(out[t.ID], paths...)
		}
	}

	return out, nil
}

// clearing is what each transaction is cleared by, and puts the
// explanations of those that are explained into the package.
func (b *Business) clearing(ctx context.Context, actor types.ID, txs []ledgerbus.Transaction, p *Package) (ledgerbus.Clearing, error) {
	ids := make([]types.ID, len(txs))
	for i, t := range txs {
		ids[i] = t.ID
	}

	c, err := b.ledger.Clearing(ctx, actor, ids)
	if err != nil {
		return ledgerbus.Clearing{}, err
	}

	for _, t := range txs {
		if c.State(t.ID) == "" {
			continue
		}

		x, err := b.ledger.Explain(ctx, actor, t.ID)
		if err != nil {
			return ledgerbus.Clearing{}, err
		}

		p.Explanations = append(p.Explanations, x)
	}

	return c, nil
}

// reconciledOn is the day a reconciliation covering the day was made.
func reconciledOn(recs []ledgerbus.Reconciliation, day types.Date) types.Date {
	for _, r := range recs {
		if r.Covers(day) {
			return types.DateOf(r.At)
		}
	}

	return types.Date{}
}

// --- writing it -----------------------------------------------------------------

// Write streams a package as a zip: the spreadsheet first, then every file,
// read from disk one at a time and never held whole. words.Columns is the
// spreadsheet's first row in the reader's language, Columns long, and
// words.Explanations explanations.csv's, ExplanationColumns long.
//
// Photos and PDFs are stored rather than compressed: they are compressed
// already, and deflating them again costs the server time and saves the
// accountant nothing.
func (b *Business) Write(ctx context.Context, now time.Time, w io.Writer, p Package, words Words) error {
	if len(words.Columns) != Columns {
		return fmt.Errorf("the header has %d columns, not %d", len(words.Columns), Columns)
	}

	if len(words.Explanations) != ExplanationColumns {
		return fmt.Errorf("the explanations' header has %d columns, not %d", len(words.Explanations), ExplanationColumns)
	}

	zw := zip.NewWriter(w)

	sheet, err := spreadsheet(p.Rows, words)
	if err != nil {
		return err
	}

	if err := deflated(zw, "transactions.csv", now, sheet); err != nil {
		return err
	}

	if len(p.Explanations) > 0 {
		sheet, err := explanations(p.Explanations, words)
		if err != nil {
			return err
		}

		if err := deflated(zw, "explanations.csv", now, sheet); err != nil {
			return err
		}
	}

	for _, e := range p.Entries {
		// A person who gave up on a large download leaves nothing to write
		// to; stop rather than read the rest of the files from disk.
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := b.copy(zw, e); err != nil {
			return err
		}
	}

	return zw.Close()
}

// deflated writes a file the package makes, compressed.
func deflated(zw *zip.Writer, name string, now time.Time, data []byte) error {
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}

	_, err = fw.Write(data)

	return err
}

func (b *Business) copy(zw *zip.Writer, e Entry) error {
	rc, err := b.files.Open(e.File)
	if err != nil {
		return fmt.Errorf("opening %s: %w", e.Path, err)
	}
	defer rc.Close()

	fw, err := zw.CreateHeader(&zip.FileHeader{Name: e.Path, Method: zip.Store, Modified: e.File.UploadedAt})
	if err != nil {
		return err
	}

	if _, err := io.Copy(fw, rc); err != nil {
		return fmt.Errorf("writing %s: %w", e.Path, err)
	}

	return nil
}

// spreadsheet is the rows as CSV, with a byte-order mark: without one,
// Excel reads UTF-8 as the Windows code page, and every accented name and
// every "€" arrives as two wrong letters.
func spreadsheet(rows []Row, words Words) ([]byte, error) {
	buf, cw := sheet()

	if err := cw.Write(words.Columns); err != nil {
		return nil, err
	}

	for _, r := range rows {
		reconciled := ""
		if !r.Reconciled.Zero() {
			reconciled = r.Reconciled.String()
		}

		if err := cw.Write([]string{
			r.PostedOn.String(), cell(r.Account), cell(r.Description),
			r.Amount.String(), r.Currency, r.Total.String(),
			cell(r.Category), cell(words.Kinds[r.Kind]), cell(r.Project), cell(r.Memo),
			strings.Join(r.Receipts, "; "), cell(r.Statement), reconciled, clearer(r.ClearedBy), cell(r.Holder),
		}); err != nil {
			return nil, err
		}
	}

	cw.Flush()

	return buf.Bytes(), cw.Error()
}

// sheet is a CSV to write, starting with the byte-order mark.
func sheet() (*bytes.Buffer, *csv.Writer) {
	var buf bytes.Buffer

	buf.WriteString("\ufeff")

	cw := csv.NewWriter(&buf)
	cw.UseCRLF = true

	return &buf, cw
}

// clearer is the "Cleared by" cell: the explained transaction's date,
// amount and description, or nothing.
func clearer(t ledgerbus.Transaction) string {
	if t.ID.Zero() {
		return ""
	}

	return cell(t.PostedOn.String() + " " + t.Amount.String() + " " + t.Description)
}

// explanations is explanations.csv: a row for each line of each
// explanation, with the explained transaction and the difference repeated
// on each, so that a filter on any column keeps whole rows. Lines in
// accounts the reader may not see are one row with their sum and no
// description.
func explanations(xs []ledgerbus.Explanation, words Words) ([]byte, error) {
	buf, cw := sheet()

	if err := cw.Write(words.Explanations); err != nil {
		return nil, err
	}

	for _, x := range xs {
		t := x.Transaction

		accepted := ""
		if x.Accepted && x.Difference() != 0 {
			accepted = words.Accepted
		}

		row := func(day, account, description, amount string) error {
			return cw.Write([]string{
				t.PostedOn.String(), cell(x.Account.Name), cell(t.Description), t.Amount.String(),
				day, cell(account), cell(description), amount,
				x.Difference().String(), accepted, cell(x.Note),
			})
		}

		for _, l := range x.Lines {
			lt := l.Transaction
			if err := row(lt.PostedOn.String(), l.Account.Name, lt.Description, lt.Amount.String()); err != nil {
				return nil, err
			}
		}

		if x.Hidden > 0 {
			if err := row("", "", "", x.HiddenSum.String()); err != nil {
				return nil, err
			}
		}

		if x.Count() == 0 {
			if err := row("", "", "", ""); err != nil {
				return nil, err
			}
		}
	}

	cw.Flush()

	return buf.Bytes(), cw.Error()
}

// cell keeps a spreadsheet from running text as a formula. A description
// comes from a bank's file and a memo from whoever typed it, and a cell
// that starts with = (or +, -, @) is a formula to Excel -- one that can
// fetch from the network when the accountant opens the file. A leading
// apostrophe makes it text, and is how Excel itself writes such a cell.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}

	return s
}

// --- names ----------------------------------------------------------------------

// namer hands out paths in the zip, each once.
type namer struct {
	taken map[string]bool
}

func newNamer() *namer { return &namer{taken: map[string]bool{}} }

// free is the path, or the path with -2, -3 before its extension when it
// is taken: two receipts from one shop for one amount on one day.
func (n *namer) free(path string) string {
	base, ext := path, ""
	if i := strings.LastIndex(path, "."); i > strings.LastIndex(path, "/") {
		base, ext = path[:i], path[i:]
	}

	try := path
	for i := 2; n.taken[try]; i++ {
		try = base + "-" + strconv.Itoa(i) + ext
	}

	n.taken[try] = true

	return try
}

// receiptPath is where one page of a receipt goes:
// receipts/YYYY-MM-DD_amount_merchant_n.ext, from what was typed on the
// receipt, or else from the transaction it is on. Named so that the
// folder sorts by date and the accountant can find a charge's receipt by
// its amount without opening the spreadsheet.
func receiptPath(rc receiptbus.Receipt, t ledgerbus.Transaction, page int, contentType string) string {
	day := rc.SpentOn
	if day.Zero() {
		day = t.PostedOn
	}

	amount := t.Amount.Abs()
	if rc.HasAmount {
		amount = rc.Amount
	}

	who := rc.Merchant
	if who == "" {
		who = t.Description
	}

	return fmt.Sprintf("receipts/%s_%s_%s_%d%s", day, amount, slug(who, "receipt"), page, extension(contentType))
}

func extension(contentType string) string {
	switch contentType {
	case filebus.JPEG:
		return ".jpg"
	case filebus.PNG:
		return ".png"
	case filebus.WebP:
		return ".webp"
	case filebus.HEIC:
		return ".heic"
	case filebus.PDF:
		return ".pdf"
	}

	return ""
}

// MaxSlug is the most of a name a path keeps.
const MaxSlug = 40

// slug is a name as part of a path: letters and digits in lower case, in
// any alphabet, and a dash for every run of anything else. "Café São
// José" is "café-são-josé"; a zip says its names are UTF-8, and every
// unzipper an accountant has reads them so.
func slug(s, otherwise string) string {
	var b strings.Builder

	dash := false

	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}

			b.WriteRune(r)

			dash = false
		default:
			dash = true
		}

		if utf8.RuneCountInString(b.String()) >= MaxSlug {
			break
		}
	}

	if b.Len() == 0 {
		return otherwise
	}

	return b.String()
}

// safeName is a statement's file name as the last part of a path: what an
// uploader's device called it, which may hold a slash or a dot-dot that a
// careless unzipper would follow.
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < ' ' {
			return '_'
		}

		return r
	}, name)

	name = strings.TrimLeft(name, ".")
	if name == "" {
		return "statement"
	}

	return name
}
