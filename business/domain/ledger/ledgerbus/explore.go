package ledgerbus

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Looking through transactions (docs/explorer.md): one set of choices over
// an account's transactions, an organization's, or a project's parts,
// with the totals of what fits. The account's month list is this with only
// a month chosen; Find, the API's search, is this over every account a
// key may read.
//
// Only the accounts, the project and the days are asked of the database
// (ledgerdb/explore.go). The rest is decided here, part by part: a
// category and a project chosen together are one part in both, never one
// part in each, and words are folded exactly as a sorting rule folds them,
// so that the search and the rules agree on what a charge says.

// Mark is one of the month list's marks to look over.
type Mark string

// The marks: a part a sorting rule sorted, and anything a person's program
// changed that no person has saved since (Transaction.ByProgram).
const (
	MarkRule    Mark = "byrule"
	MarkProgram Mark = "through"
)

// HasReceipts is whether a transaction has receipts.
type HasReceipts string

// With receipts, or without any.
const (
	WithReceipts    HasReceipts = "with"
	WithoutReceipts HasReceipts = "without"
)

// Criteria is what a person chose. Every part is optional; the zero
// Criteria is every transaction in the scope, newest first.
type Criteria struct {
	// Text is words to look for, as a rule looks for them: in the payee
	// and the description, the transaction's own description, a check's
	// memo and each part's memo.
	Text string

	// Min and Max bound the amount without its sign, when Has says they
	// are set; Direction is "in", "out" or "". Both are compared with the
	// amount a row shows: the matching parts' when a part is chosen below.
	Min, Max       money.Amount
	HasMin, HasMax bool
	Direction      rulebus.Direction

	// From and To bound the date, both days included, when not zero. A
	// whole month, first day to last, lists as its statement reads.
	From, To types.Date

	// Account narrows an organization's or a project's list to one of its
	// accounts. On an organization it is also the one way to an account
	// for reference, which is otherwise left out.
	Account types.ID

	// What a part is sorted into: a category, a whole kind, a category
	// whose kind is not said, or no category at all. One is chosen at a
	// time; the form offers them in one list.
	Category   types.ID
	Kind       categorybus.Kind
	KindUnsaid bool
	Unsorted   bool

	// Project is a part in that project, NoProject a part in none. Not
	// asked on a project's own list, which is its parts.
	Project   types.ID
	NoProject bool

	Mark     Mark
	Receipts HasReceipts

	// Accountant is one of the two lists for the accountant
	// (accountant.go): important transactions with no context given yet,
	// or those with some.
	Accountant ForAccountant

	// Skip and Limit page anything wider than a month: Limit is
	// DefaultExplore when zero.
	Skip, Limit int
}

// DefaultExplore is how many rows a page of a search shows.
const DefaultExplore = 100

// parted reports whether a part is chosen: then a row's amount is the
// parts that are.
func (c Criteria) parted() bool {
	return !c.Category.Zero() || c.Kind != "" || c.KindUnsaid || c.Unsorted || !c.Project.Zero() || c.NoProject
}

// Statement reports whether the days are one whole month, which lists in
// the order the statement reads rather than newest first, all of it.
func (c Criteria) Statement() bool {
	if c.From.Zero() || c.To.Zero() {
		return false
	}

	from, to, err := MonthRange(c.From.String()[:7])

	return err == nil && from == c.From && to.AddDays(-1) == c.To
}

// Row is a transaction that fits, in its account.
type Row struct {
	Transaction Transaction
	Account     tenancybus.Account

	// Amount is what the row shows: the transaction's, or the parts that
	// are what was chosen, which Part says when they are not all of it.
	// Parts is those parts, or all of them: on a project's list, what the
	// row says the money was for is only the project's, never the rest of
	// a charge a project's role does not reach.
	Amount money.Amount
	Part   bool
	Parts  []Split

	// Readable is whether the reader may open the transaction: on a
	// project's list, a part in an account they are given nothing on is
	// shown, and not linked (docs/plan.md, "Inheritance").
	Readable bool

	// Why is what makes the transaction important to the accountant, and
	// Given the context somebody gave it, when one of the accountant's
	// lists was chosen; the page says them, a word for each.
	Why   []Reason
	Given []Given
}

// Sorted reports whether every part the row shows has a category: of a
// project's row, only the project's parts, which are all its reader may
// know of.
func (r Row) Sorted() bool {
	if r.Transaction.Cleared() {
		return true
	}

	return len(r.Parts) > 0 && !slices.ContainsFunc(r.Parts, func(s Split) bool { return s.CategoryID.Zero() })
}

// Sum is money in and out of the rows in one currency. A total across
// currencies would mean nothing.
type Sum struct {
	Currency string
	In, Out  money.Amount
}

// Explored is a search's answer.
type Explored struct {
	// Rows is the page: oldest first and all of it for one month, newest
	// first otherwise.
	Rows []Row

	// Total is how many fit, and Sums their money, over every row and not
	// only the page. A pending charge since posted is a row and is not in
	// the sums: it is the posted charge that counts.
	Total int
	Sums  []Sum

	// Skip is where the page starts; Next where the next one would, zero
	// when this is the last.
	Skip, Next int

	// Accounts is what an organization's or a project's list may be
	// narrowed to, and LeftOut an organization's accounts for reference,
	// which a search reads only when one is chosen.
	Accounts []tenancybus.Account
	LeftOut  []tenancybus.Account

	// Categories and Projects are what the form offers: the lists the
	// scope's parts are sorted with, by name, and the projects its parts
	// may be in -- none on a project's own list, which is its parts.
	Categories []categorybus.Category
	Projects   []tenancybus.Project
}

// ReceiptCounts is how many receipts each of some transactions has.
type ReceiptCounts func(ctx context.Context, ids []types.ID) (map[types.ID]int, error)

// OnReceipts sets how receipts are counted, for the choice of with or
// without: receipts are made from the ledger, so the ledger is told how to
// ask them rather than made from them (as OnImport). main sets it once,
// before anything is served; without it, no transaction has any.
func (b *Business) OnReceipts(fn ReceiptCounts) { b.receipted = fn }

// Within is what the store reads for a search: the accounts', or with a
// project its parts' transactions, in any of its accounts not for
// reference or in the accounts given; posted from From up to but not
// including To. Facts asks for each transaction's Facts too, which only
// the accountant's lists need.
type Within struct {
	Accounts []types.ID
	Project  types.ID
	From, To types.Date
	Facts    bool
}

// Explorable is what the store read: the transactions in the order
// posted, with their parts, the kind of each category the parts are in,
// and when asked the facts of those transactions that have any.
type Explorable struct {
	Transactions []Transaction
	Kinds        map[types.ID]categorybus.Kind
	Facts        map[types.ID]Facts
}

// Explore is the transactions in a scope -- an account, an organization or
// a project -- that fit what was chosen, for anyone who may read it.
func (b *Business) Explore(ctx context.Context, actor types.ID, scope types.Scope, c Criteria) (Explored, error) {
	var (
		ex       Explored
		within   Within
		searched []tenancybus.Account
		readable = func(types.ID) bool { return true }
	)

	switch scope.Kind {
	case types.ScopeAccount:
		a, _, err := b.accounts.Account(ctx, actor, scope.ID)
		if err != nil {
			return Explored{}, err
		}

		searched = []tenancybus.Account{a}
		c.Account = types.ID{}

		mine, err := b.accounts.ProjectsFor(ctx, actor, tenancybus.Read)
		if err != nil {
			return Explored{}, err
		}

		ex.Projects = slices.DeleteFunc(mine, func(p tenancybus.Project) bool { return p.OrgID != a.OrgID })

	case types.ScopeOrg:
		access, err := b.accounts.AccessTo(ctx, actor, scope)
		if err != nil {
			return Explored{}, err
		}

		if !access.Can(tenancybus.Read) {
			return Explored{}, ErrNotFound
		}

		if ex.Accounts, ex.Projects, err = b.accounts.Contents(ctx, actor, scope.ID); err != nil {
			return Explored{}, err
		}

		// The account chosen, if it is one of the organization's: an
		// address with an account since moved shows everything rather
		// than nothing, as a report's does.
		for _, a := range ex.Accounts {
			switch {
			case a.ID == c.Account:
				searched = []tenancybus.Account{a}
			case a.Reference:
				ex.LeftOut = append(ex.LeftOut, a)
			}
		}

		if len(searched) == 0 {
			searched = slices.DeleteFunc(slices.Clone(ex.Accounts), func(a tenancybus.Account) bool { return a.Reference })
		} else {
			ex.LeftOut = nil
		}

	case types.ScopeProject:
		if _, _, err := b.accounts.Project(ctx, actor, scope.ID); err != nil {
			return Explored{}, err
		}

		var err error
		if ex.Accounts, err = b.store.ProjectAccounts(ctx, scope.ID); err != nil {
			return Explored{}, err
		}

		mine := map[types.ID]bool{}

		for _, a := range ex.Accounts {
			switch _, _, err := b.accounts.Account(ctx, actor, a.ID); {
			case err == nil:
				mine[a.ID] = true
			case !errors.Is(err, ErrNotFound):
				return Explored{}, err
			}

			if a.ID == c.Account {
				within.Accounts = []types.ID{a.ID}
			}
		}

		searched, within.Project = ex.Accounts, scope.ID
		readable = func(id types.ID) bool { return mine[id] }

		// The scope is the project: a part in it, whatever else.
		c.Project, c.NoProject = scope.ID, false

	default:
		return Explored{}, ErrNotFound
	}

	var err error
	if ex.Categories, err = b.lists(ctx, searched); err != nil {
		return Explored{}, err
	}

	if scope.Kind != types.ScopeProject {
		for _, a := range searched {
			within.Accounts = append(within.Accounts, a.ID)
		}
	}

	rows, err := b.look(ctx, within, searched, c)
	if err != nil {
		return Explored{}, err
	}

	for i := range rows {
		rows[i].Readable = readable(rows[i].Account.ID)
	}

	ex.Total = len(rows)
	ex.Sums = sums(rows)

	if c.Statement() {
		ex.Rows = rows

		return ex, nil
	}

	newestFirst(rows)

	limit := c.Limit
	if limit <= 0 {
		limit = DefaultExplore
	}

	ex.Skip = min(max(c.Skip, 0), len(rows))
	end := min(ex.Skip+limit, len(rows))
	ex.Rows = rows[ex.Skip:end]

	if end < len(rows) {
		ex.Next = end
	}

	return ex, nil
}

// Latest is the n newest transactions of an organization's accounts, not
// those for reference, newest first: the card on the organization's page.
// Read newest first with a limit, not as a search is, because the page is
// opened far more often than anything is looked for.
func (b *Business) Latest(ctx context.Context, actor, orgID types.ID, n int) ([]Row, error) {
	access, err := b.accounts.AccessTo(ctx, actor, types.OrgScope(orgID))
	if err != nil {
		return nil, err
	}

	if !access.Can(tenancybus.Read) {
		return nil, ErrNotFound
	}

	accounts, _, err := b.accounts.Contents(ctx, actor, orgID)
	if err != nil {
		return nil, err
	}

	named := map[types.ID]tenancybus.Account{}

	var ids []types.ID

	for _, a := range accounts {
		if !a.Reference {
			named[a.ID] = a
			ids = append(ids, a.ID)
		}
	}

	if len(ids) == 0 {
		return nil, nil
	}

	txs, err := b.store.Latest(ctx, ids, n)
	if err != nil {
		return nil, err
	}

	rows := make([]Row, len(txs))
	for i, t := range txs {
		rows[i] = Row{Transaction: t, Account: named[t.AccountID], Amount: t.Amount, Parts: t.Splits, Readable: true}
	}

	return rows, nil
}

// lists is the categories the accounts' parts are sorted with, each list
// once, by name.
func (b *Business) lists(ctx context.Context, accounts []tenancybus.Account) ([]categorybus.Category, error) {
	var (
		out  []categorybus.Category
		seen = map[types.Scope]bool{}
	)

	for _, a := range accounts {
		owner := categorybus.OwnerOf(a)
		if seen[owner] {
			continue
		}

		seen[owner] = true

		list, err := b.categories.Of(ctx, owner)
		if err != nil {
			return nil, err
		}

		out = append(out, list...)
	}

	slices.SortFunc(out, func(x, y categorybus.Category) int {
		return cmp.Or(strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), strings.Compare(x.ID.String(), y.ID.String()))
	})

	return out, nil
}

// look reads the transactions within and keeps those that fit, in the
// order posted, each named by its account among those given.
func (b *Business) look(ctx context.Context, within Within, accounts []tenancybus.Account, c Criteria) ([]Row, error) {
	from, to := c.From, c.To
	if to.Zero() {
		to = types.DateOf(time.Now().AddDate(1, 0, 0))
	}

	within.From, within.To = from, to.AddDays(1)
	within.Facts = c.Accountant != ""

	read, err := b.store.Explore(ctx, within)
	if err != nil {
		return nil, err
	}

	named := make(map[types.ID]tenancybus.Account, len(accounts))
	for _, a := range accounts {
		named[a.ID] = a
	}

	text := rulebus.Key(c.Text)

	var rows []Row

	for _, t := range read.Transactions {
		a, ok := named[t.AccountID]
		if !ok {
			continue
		}

		amount, parts, ok := c.parts(t, read.Kinds)
		if !ok || !c.fits(t, text, amount) {
			continue
		}

		rows = append(rows, Row{Transaction: t, Account: a, Amount: amount, Part: len(parts) < len(t.Splits), Parts: parts})
	}

	if c.Receipts == "" && c.Accountant == "" || len(rows) == 0 {
		return rows, nil
	}

	counts := map[types.ID]int{}

	if b.receipted != nil {
		ids := make([]types.ID, len(rows))
		for i, r := range rows {
			ids[i] = r.Transaction.ID
		}

		if counts, err = b.receipted(ctx, ids); err != nil {
			return nil, err
		}
	}

	if c.Receipts != "" {
		rows = slices.DeleteFunc(rows, func(r Row) bool {
			return (counts[r.Transaction.ID] > 0) != (c.Receipts == WithReceipts)
		})
	}

	if c.Accountant == "" {
		return rows, nil
	}

	return b.forAccountant(ctx, rows, c.Accountant, within.Project, read.Kinds, read.Facts, counts)
}

// parts is the parts of a transaction that are what was chosen, their
// amount, and whether any is. With no part chosen it is the whole
// transaction.
func (c Criteria) parts(t Transaction, kinds map[types.ID]categorybus.Kind) (money.Amount, []Split, bool) {
	if !c.parted() {
		return t.Amount, t.Splits, true
	}

	var (
		sum   money.Amount
		parts []Split
	)

	for _, s := range t.Splits {
		kind, sorted := kinds[s.CategoryID], !s.CategoryID.Zero()

		switch {
		case !c.Category.Zero() && s.CategoryID != c.Category,
			c.Kind != "" && (!sorted || kind != c.Kind),
			c.KindUnsaid && (!sorted || kind != categorybus.Unsaid),
			c.Unsorted && sorted,
			!c.Project.Zero() && s.ProjectID != c.Project,
			c.NoProject && !s.ProjectID.Zero():
			continue
		}

		sum += s.Amount
		parts = append(parts, s)
	}

	return sum, parts, len(parts) > 0
}

// fits reports whether a transaction, showing amount, is one the rest of
// what was chosen asks for.
func (c Criteria) fits(t Transaction, text string, amount money.Amount) bool {
	abs := amount.Abs()

	switch {
	case text != "" && !says(t, text):
		return false
	case c.HasMin && abs < c.Min, c.HasMax && abs > c.Max:
		return false
	case c.Direction == rulebus.In && amount < 0, c.Direction == rulebus.Out && amount > 0:
		return false
	case c.Mark == MarkRule && !t.ByRule(), c.Mark == MarkProgram && t.ByProgram() == "":
		return false
	}

	return true
}

// says reports whether a transaction says the words, folded as a rule's.
func says(t Transaction, text string) bool {
	for _, s := range []string{t.Words(), t.OwnDescription, t.CheckMemo} {
		if strings.Contains(rulebus.Key(s), text) {
			return true
		}
	}

	return slices.ContainsFunc(t.Splits, func(s Split) bool { return strings.Contains(rulebus.Key(s.Memo), text) })
}

// sums is the rows' money in and out by currency, in currency order,
// without a pending charge since posted.
func sums(rows []Row) []Sum {
	by := map[string]*Sum{}

	for _, r := range rows {
		if r.Transaction.Cleared() {
			continue
		}

		s, ok := by[r.Account.Currency]
		if !ok {
			s = &Sum{Currency: r.Account.Currency}
			by[r.Account.Currency] = s
		}

		if r.Amount < 0 {
			s.Out += r.Amount
		} else {
			s.In += r.Amount
		}
	}

	out := make([]Sum, 0, len(by))
	for _, s := range by {
		out = append(out, *s)
	}

	slices.SortFunc(out, func(x, y Sum) int { return cmp.Compare(x.Currency, y.Currency) })

	return out
}

// newestFirst orders rows by date, newest first, keeping the order posted
// within a day: a day's charges read as the statement has them.
func newestFirst(rows []Row) {
	slices.SortStableFunc(rows, func(x, y Row) int {
		return cmp.Compare(y.Transaction.PostedOn.String(), x.Transaction.PostedOn.String())
	})
}
