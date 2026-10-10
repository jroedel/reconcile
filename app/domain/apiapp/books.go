package apiapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/web"
)

// Reading the books (docs/books-api.md, "Reading"): what a key with
// books:read reaches, which is what its person may read on the pages and
// nothing more. Every answer is the business layer's, asked as the person,
// so a statement of somebody else's is a 404 here exactly as it is on its
// page.
//
// The answers are made to sit in a conversation: amounts as signed strings
// beside their currency, dates as YYYY-MM-DD, names beside identifiers so
// that nobody has to look a category up to read a line, and a url on every
// statement, transaction, rule, receipt and project, so that Claude can send
// its person to the page where every number says where it came from.

// Ledger is the slice of ledgerbus the books endpoints read.
type Ledger interface {
	Inbox(ctx context.Context, actor types.ID) ([]ledgerbus.InboxFile, error)
	Propose(ctx context.Context, actor types.ID, fileIDs []types.ID, chosen map[string]types.ID) ([]ledgerbus.Proposal, error)
	Coverage(ctx context.Context, actor, accountID types.ID, today types.Date) ([]ledgerbus.MonthCover, error)
	Statements(ctx context.Context, actor, accountID types.ID) ([]ledgerbus.Statement, error)
	Review(ctx context.Context, actor, id types.ID) (ledgerbus.Review, error)
	Transactions(ctx context.Context, actor, accountID types.ID, month string) ([]ledgerbus.Transaction, error)
	Transaction(ctx context.Context, actor, id types.ID) (ledgerbus.Editor, error)
	Clearing(ctx context.Context, actor types.ID, ids []types.ID) (ledgerbus.Clearing, error)
	ToSort(ctx context.Context, now time.Time, actor, accountID types.ID, month string) (ledgerbus.MonthToSort, error)
	Rulebook(ctx context.Context, now time.Time, actor, accountID types.ID) (ledgerbus.Rulebook, error)
	TryRule(ctx context.Context, now time.Time, actor, accountID types.ID, f rulebus.Fields) (ledgerbus.Trial, error)
	Holders(ctx context.Context, actor, accountID types.ID, month string) (ledgerbus.HolderMonth, error)
	Find(ctx context.Context, actor types.ID, q ledgerbus.Query) (ledgerbus.Finding, error)
	Explain(ctx context.Context, actor, id types.ID) (ledgerbus.Explanation, error)
	Candidates(ctx context.Context, actor, id types.ID, accounts []types.ID, from, to string) ([]ledgerbus.Line, error)
	ProjectBook(ctx context.Context, actor, projectID types.ID) (ledgerbus.Book, error)

	// What keeping the books changes (keeping.go).
	ImportProposals(ctx context.Context, now time.Time, actor types.ID, props []ledgerbus.Proposal) (int, error)
	SortMany(ctx context.Context, now time.Time, actor, accountID types.ID, choices []ledgerbus.Choice) (int, map[types.ID]error, error)
	SetSplits(ctx context.Context, now time.Time, actor, id types.ID, parts []ledgerbus.Part) (ledgerbus.Transaction, error)
	Describe(ctx context.Context, now time.Time, actor, id types.ID, description string) (ledgerbus.Transaction, error)
	SortUnsorted(ctx context.Context, now time.Time, actor, accountID types.ID) (int, error)
	Gather(ctx context.Context, now time.Time, actor, id types.ID, add, remove, accounts []types.ID, from, to string) error
}

// Tenancy is the slice of tenancybus the books endpoints read.
type Tenancy interface {
	Overview(ctx context.Context, actor types.ID) (tenancybus.Overview, error)
	Account(ctx context.Context, actor, id types.ID) (tenancybus.Account, tenancybus.Access, error)
	ProjectNames(ctx context.Context, ids []types.ID) (map[types.ID]string, error)
}

// Categories is an account's list, for the names of what a part is in.
type Categories interface {
	ForAccount(ctx context.Context, a tenancybus.Account) ([]categorybus.Category, error)
}

// Receipts is the slice of receiptbus the books endpoints read.
type Receipts interface {
	Waiting(ctx context.Context, actor types.ID) ([]receiptbus.Receipt, error)
	Suggestions(ctx context.Context, actor types.ID, receipts []receiptbus.Receipt) (map[types.ID][]ledgerbus.Transaction, error)
	OnTransactions(ctx context.Context, ids []types.ID) (map[types.ID][]receiptbus.Receipt, error)
	Attach(ctx context.Context, now time.Time, actor, receiptID, transactionID types.ID) (receiptbus.Receipt, error)
	File(ctx context.Context, actor, id types.ID, n int) (filebus.File, error)
	ReadCheck(ctx context.Context, now time.Time, actor, id types.ID, rd receiptbus.CheckReading) (receiptbus.Receipt, error)
	Detach(ctx context.Context, actor, receiptID, transactionID types.ID) (receiptbus.Receipt, error)
	OutstandingChecks(ctx context.Context, actor, accountID types.ID, by types.Date) (receiptbus.Outstanding, error)

	// The person's own check inbox (receiptbus, inbox.go): its checks are
	// among the waiting ones, by an id that get_receipt_image and
	// read_check take as they take a receipt's.
	CheckInbox(ctx context.Context, actor types.ID) (receiptbus.CheckInbox, error)
	InboxCheckFile(ctx context.Context, actor, id types.ID, n int) (filebus.File, error)
	ReadInboxCheck(ctx context.Context, now time.Time, actor, id types.ID, rd receiptbus.CheckReading, face receiptbus.OnFace) (receiptbus.Receipt, error)
}

// History is what a person changed through a key.
type History interface {
	Through(ctx context.Context, actor types.ID, since time.Time, limit int) ([]eventbus.Event, error)
}

// Pictures is the files' bytes, for showing a receipt's page (filebus).
type Pictures interface {
	Picture(ctx context.Context, f filebus.File, size filebus.Size) (io.ReadSeekCloser, error)
	Open(f filebus.File) (io.ReadSeekCloser, error)
}

// Books is everything the books endpoints read through. Nil fields leave
// those endpoints out of the API, so that a test of the translations need
// not build the books.
type Books struct {
	Ledger     Ledger
	Tenancy    Tenancy
	Categories Categories
	Receipts   Receipts
	History    History
	Rules      Rules
	Pictures   Pictures
}

func (b Books) complete() bool {
	return b.Ledger != nil && b.Tenancy != nil && b.Categories != nil && b.Receipts != nil && b.History != nil && b.Rules != nil && b.Pictures != nil
}

// --- the endpoints ----------------------------------------------------------------

func (a app) bookEndpoints() []Endpoint {
	if !a.books.complete() {
		return nil
	}

	read := userbus.BooksRead
	month := func(what string) Field {
		return Field{Name: "month", Type: "string", Description: "The month, as YYYY-MM. " + what}
	}

	return []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix + "/me", Scope: read, Tool: "whoami",
			Summary: "Who this key acts as, what it may do, and when it ends.",
			Returns: "{name, email, scopes, key: {name, ends}}", handler: a.whoami,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/overview", Scope: read, Tool: "get_overview",
			Summary: "Start here: every organization, account and project you may see, with your role on each, and for each account the end of its last statement, how many of its transactions are not sorted yet, and how many months have no statement.",
			Returns: "{organizations: [{id, name, accounts, projects}], accounts (personal), projects (personal)}; an account is {id, name, kind, currency, last4, roles, last_statement_ends, unsorted, months_missing, url}.",
			handler: a.overview,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/inbox", Scope: read, Tool: "list_inbox",
			Summary: "The statement files waiting in your inbox, sent by your Gmail script, each read for the account proposed for it: its period, how it checked, how many transactions are new, and whether it can be imported with no person looking at it, or what needs a person.",
			Returns: "{files: [{inbox_id, file, name, received, source, parts: [{part, account, account_name, by, period, transactions, new, check, ready, imported, needs}]}], imports_url}",
			handler: a.listInbox,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/months", Scope: read, Tool: "get_account_months",
			Summary: "An account month by month: reconciled, imported (every day in a statement), partial (and which days are in none), missing (no statement), or still going; with each month's statements, transactions and how many are not sorted yet. And its checks outstanding: images of checks read, with their number, that no statement has paid yet, and their total.",
			Returns: "{account, months: [{month, state, gaps: [{from, to}], statements: [{id, name}], transactions, unsorted}], outstanding_checks: {checks: [receipt], total, unpriced}}, months newest first; unpriced is how many checks have no amount yet, which the total leaves out.",
			handler: a.accountMonths,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/statements", Scope: read, Tool: "list_statements",
			Summary: "An account's statements, newest first: each one's period, balances, how it was checked, and whether its period is reconciled.",
			Returns: "{account, statements: [statement]}", handler: a.listStatements,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/statements/{statement}", Scope: read, Tool: "get_statement",
			Summary: "One statement as its page shows it: how it checked against its balances, its period's transactions, money in and out, and those not sorted yet. Reconciling it is its person's, on its page.",
			Returns: "{statement, in, out, net, transactions: [transaction], unsorted: [transaction], overlapping_reconciled: [{from, to}]}",
			handler: a.getStatement,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/transactions", Scope: read, Tool: "list_transactions",
			Summary: "An account's transactions for one month, oldest first, each with its parts (category, project, memo), whether a rule sorted it, and how many receipts it has.",
			Query: []Field{
				month("Required."),
				{Name: "show", Type: "string", Values: []string{"all", "unsorted", "by_rule", "no_receipt"}, Description: "Which: every one (all, the default), those not sorted yet, those a rule sorted, or those with no receipt."},
			},
			Returns: "{account, month, transactions: [transaction]}", handler: a.listTransactions,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/transactions", Scope: read, Tool: "find_transactions",
			Summary: "Find transactions across every account you may read, or the ones named: by words in the description or payee (as a sorting rule looks for them), an amount without its sign, a range of amounts, dates, and money in or out. Newest first. Use it to hunt down an amount, or the same payment in earlier months.",
			Query: []Field{
				{Name: "text", Type: "string", Description: "Words to look for, without case."},
				{Name: "amount", Type: "string", Description: "An exact amount, without its sign, such as 1240.00."},
				{Name: "min", Type: "string", Description: "At least this amount, without its sign."},
				{Name: "max", Type: "string", Description: "At most this amount, without its sign."},
				{Name: "direction", Type: "string", Values: []string{"in", "out"}, Description: "Money in or money out only."},
				{Name: "from", Type: "string", Description: "From this day, YYYY-MM-DD, included."},
				{Name: "to", Type: "string", Description: "To this day, YYYY-MM-DD, included."},
				{Name: "account", Type: "string", Description: "One account's id, or several separated by commas. Every account you may read when left out."},
				{Name: "limit", Type: "integer", Description: fmt.Sprintf("How many, %d by default and %d at most.", ledgerbus.DefaultFind, ledgerbus.MaxFind)},
			},
			Returns: "{transactions: [transaction, with account_name], more}: more is how many else matched.",
			handler: a.findTransactions,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/transactions/{transaction}", Scope: read, Tool: "get_transaction",
			Summary: "One transaction as its page shows it: its parts, the statement it came from, its receipts, whether a period it is in is reconciled, and what explains it or what it is cleared by.",
			Returns: "{transaction, statement, reconciled, explained: {state}, cleared_by: transaction}", handler: a.getTransaction,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/to-sort", Scope: read, Tool: "get_month_to_sort",
			Summary: "A month's transactions not sorted yet, each with what the app would suggest -- a rule's choice, or a likely category from what was sorted before -- and the categories and projects it may be sorted into. Bookkeepers and owners only.",
			Query:   []Field{month("Required.")},
			Returns: "{account, month, rows: [{transaction, guess: {by, category, category_name, project, project_name, rule}}], more, categories: [{id, name, kind}], projects: [{id, name}]}",
			handler: a.monthToSort,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/rules", Scope: read, Tool: "list_rules",
			Summary: "An account's sorting rules: each one's text, direction and choice, how many parts it has sorted, how many unsorted transactions it would sort now, and any problem; rules that disagree about a transaction; and how many the rules would sort now. Bookkeepers and owners only.",
			Returns: "{account, rules: [rule], waiting, torn: [{transaction, rules}], url}", handler: a.listRules,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/rules/try", Scope: read, Tool: "try_rule",
			Summary: "What a sorting rule would do if it were saved now, and nothing is saved: the transactions it would sort, those a longer rule sorts instead, those where a rule as long disagrees (and which), and those with its text that are sorted already, which no rule changes. Try a rule before proposing it. Trying a rule's own text tries a correction of it.",
			Query: []Field{
				{Name: "match", Type: "string", Required: true, Description: "The text to look for in the description, 3 to 100 characters, without case."},
				{Name: "direction", Type: "string", Values: []string{"out", "in", "any"}, Description: "Money out, money in, or either (any, the default)."},
				{Name: "category", Type: "string", Description: "The category's id it would set, if chosen: it tells agreement from disagreement with a rule as long."},
				{Name: "project", Type: "string", Description: "The project's id it would set, if chosen."},
			},
			Returns: "{rule, would: {count, transactions}, elsewhere: {count, transactions}, torn: {count, transactions, with: [rule]}, already: {count, transactions}}: at most 25 transactions of each.",
			handler: a.tryRule,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/holders", Scope: read, Tool: "get_holders",
			Summary: "On an account whose statements arrive one file per holder (a person or card on the account), each holder's total for a month, and who has no file yet. A missing holder or month is the usual reason a payment and its charges differ.",
			Query:   []Field{month("Required.")},
			Returns: "{account, month, by_holder: bool, holders: [{holder, transactions, sum}], missing: [name], present, expected}",
			handler: a.holders,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/transactions/{transaction}/explanation", Scope: read, Tool: "get_explanation",
			Summary: "What explains a transaction's amount: the lines that make it up, from any accounts of the same organization, grouped by account, their sum, and the difference, as the app counts them. Also the accounts and months to gather from, as last time or as for the last transaction like it.",
			Returns: "{transaction, lines: [transaction, with account_name], hidden: {count, sum}, sum, difference, state: explained|open|accepted, note, gather: {accounts: [{id, name}], from, to}, may_draw_on: [{id, name}], url}",
			handler: a.getExplanation,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/transactions/{transaction}/explanation/candidates", Scope: read, Tool: "list_explanation_candidates",
			Summary: "What may be added to a transaction's explanation from some accounts and months: their transactions not already in an explanation. By account and date.",
			Query: []Field{
				{Name: "account", Type: "string", Description: "Account ids, separated by commas; those to gather from as last time when left out."},
				{Name: "from", Type: "string", Description: "The first month, YYYY-MM; as last time when left out."},
				{Name: "to", Type: "string", Description: "The last month, YYYY-MM; as last time when left out."},
			},
			Returns: "{lines: [transaction, with account_name]}", handler: a.explanationCandidates,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/receipts/waiting", Scope: read, Tool: "list_waiting_receipts",
			Summary: "Receipts waiting for a match in the inboxes you may see, each with the transactions it may belong to: the same amount within a few days, in accounts you may attach receipts on. Also the images of checks in your own checks (your_checks true), shared from any account and waiting to be filed under the one they are drawn on: read each with read_check and the account number printed on it.",
			Returns: "{receipts: [{id, spent_on, amount, merchant, note, pages, uploaded, url, check_image, check, your_checks, suggestions: [transaction]}]}; check_image is true for the image of a check, and check its number once somebody has said it; your_checks is true for one in your own checks, which is in no account yet",
			handler: a.waitingReceipts,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/receipts/{receipt}/image", Scope: read, Tool: "get_receipt_image",
			Summary: "One page of a receipt, as an image to look at: a photo or a screenshot, at most 1600 pixels on its longer side. For the image of a check, read its number, whom it was paid to, its amount and its date, and say them with read_check.",
			Query:   []Field{{Name: "page", Type: "integer", Description: "Which page, counting from 1; 1 when left out. list_waiting_receipts says how many pages each receipt has."}},
			Returns: "The page as an image (JPEG, PNG or WebP). A PDF or HEIC page is refused, with the receipt's url, where a person can look at it.",
			handler: a.receiptImage,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/projects/{project}", Scope: read, Tool: "get_project_book",
			Summary: "A project's book: its totals per currency (income, expenses, transfers, pass-through, not sorted yet), by category and by month, and every part in it.",
			Returns: "{project, totals, by_category, by_month, lines: [{date, description, account, amount, currency, category, kind, memo}], more, url}",
			handler: a.projectBook,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/changes", Scope: read, Tool: "list_changes",
			Summary: "What you changed through an API key -- this one or another, such as Claude -- since a moment, newest first, from the site's own history. For checking at the end of a session what was done.",
			Query: []Field{
				{Name: "since", Type: "string", Description: "From this moment, as YYYY-MM-DD or an RFC 3339 time; the last 24 hours when left out."},
				{Name: "limit", Type: "integer", Description: fmt.Sprintf("How many, at most %d.", eventbus.MaxThrough)},
			},
			Returns: "{changes: [{at, via, action, on: {kind, id}, detail}]}", handler: a.changes,
		},
	}
}

// --- what answers are made of -----------------------------------------------

type idName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type accountOut struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Currency string   `json:"currency"`
	Last4    string   `json:"last4,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Archived bool     `json:"archived,omitempty"`

	LastStatementEnds string `json:"last_statement_ends,omitempty"`
	Unsorted          *int   `json:"unsorted,omitempty"`
	MonthsMissing     *int   `json:"months_missing,omitempty"`

	URL string `json:"url"`
}

type partOut struct {
	Amount       money.Amount `json:"amount"`
	Category     string       `json:"category,omitempty"`
	CategoryName string       `json:"category_name,omitempty"`
	Kind         string       `json:"kind,omitempty"`
	Project      string       `json:"project,omitempty"`
	ProjectName  string       `json:"project_name,omitempty"`
	Memo         string       `json:"memo,omitempty"`
	ByRule       bool         `json:"by_rule,omitempty"`

	// Through is the API key a program sorted the part through, until a
	// person saves it on the web.
	Through string `json:"through,omitempty"`
}

type txOut struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	AccountName string `json:"account_name,omitempty"`
	Date        string `json:"date"`
	Description string `json:"description"`

	// OwnDescription is what a person, or a program as them, said it was,
	// shown on the site in place of the bank's Description; Described is
	// the key a program wrote it through, until a person saves it on the
	// web. Description stays the bank's, since it is what rules read.
	OwnDescription string `json:"own_description,omitempty"`
	Described      string `json:"own_description_through,omitempty"`
	Payee          string `json:"payee,omitempty"`

	// CheckMemo and Written are what a check's image says it was for and
	// the day it was written; Date stays the day it cleared.
	CheckMemo string        `json:"check_memo,omitempty"`
	Written   string        `json:"written,omitempty"`
	Amount    money.Amount  `json:"amount"`
	Currency  string        `json:"currency"`
	Balance   *money.Amount `json:"balance,omitempty"`
	Holder    string        `json:"holder,omitempty"`
	Check     string        `json:"check,omitempty"`
	Pending   bool          `json:"pending,omitempty"`
	Sorted    bool          `json:"sorted"`
	Parts     []partOut     `json:"parts"`
	Receipts  *int          `json:"receipts,omitempty"`
	URL       string        `json:"url"`
}

type spanOut struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type reconciledOut struct {
	From string `json:"from"`
	To   string `json:"to"`
	At   string `json:"at"`
	Note string `json:"note,omitempty"`
}

type statementOut struct {
	ID         string         `json:"id"`
	Account    string         `json:"account"`
	File       string         `json:"file"`
	Format     string         `json:"format"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	Opening    *money.Amount  `json:"opening,omitempty"`
	Closing    *money.Amount  `json:"closing,omitempty"`
	Checked    string         `json:"checked"`
	Added      int            `json:"added"`
	Already    int            `json:"already"`
	ByRule     int            `json:"by_rule"`
	ImportedAt string         `json:"imported_at"`
	Reconciled *reconciledOut `json:"reconciled,omitempty"`
	URL        string         `json:"url"`
}

type ruleOut struct {
	ID           string `json:"id,omitempty"`
	Match        string `json:"match"`
	Direction    string `json:"direction"`
	Category     string `json:"category,omitempty"`
	CategoryName string `json:"category_name,omitempty"`
	Project      string `json:"project,omitempty"`
	ProjectName  string `json:"project_name,omitempty"`
	Parts        *int   `json:"parts_sorted,omitempty"`
	Waiting      *int   `json:"would_sort_now,omitempty"`
	Problem      string `json:"problem,omitempty"`
	Through      string `json:"through,omitempty"`
	URL          string `json:"url,omitempty"`
}

// --- reading, as one person, in one request ----------------------------------

// reader turns the business layer's answers into the API's for one request:
// the person, and the names it has looked up so far, each once.
type reader struct {
	a      app
	ctx    context.Context
	me     types.ID
	cats   map[types.ID]map[types.ID]categorybus.Category
	names  map[types.ID]string
	accts  map[types.ID]tenancybus.Account
	failed error
}

func (a app) reader(r *http.Request) *reader {
	u, _ := mid.UserFrom(r.Context())

	return &reader{
		a: a, ctx: r.Context(), me: u.ID,
		cats:  map[types.ID]map[types.ID]categorybus.Category{},
		names: map[types.ID]string{},
		accts: map[types.ID]tenancybus.Account{},
	}
}

func (rd *reader) url(path string) string { return rd.a.base + path }

// account is an account the person may read, looked up once.
func (rd *reader) account(id types.ID) tenancybus.Account {
	if a, ok := rd.accts[id]; ok {
		return a
	}

	a, _, err := rd.a.books.Tenancy.Account(rd.ctx, rd.me, id)
	if err != nil && !errors.Is(err, tenancybus.ErrNotFound) && rd.failed == nil {
		rd.failed = err
	}

	if err != nil {
		a = tenancybus.Account{ID: id}
	}

	rd.accts[id] = a

	return a
}

func (rd *reader) category(a tenancybus.Account, id types.ID) categorybus.Category {
	if id.Zero() || a.Name == "" {
		return categorybus.Category{}
	}

	list, ok := rd.cats[a.ID]
	if !ok {
		cats, err := rd.a.books.Categories.ForAccount(rd.ctx, a)
		if err != nil && rd.failed == nil {
			rd.failed = err
		}

		list = make(map[types.ID]categorybus.Category, len(cats))
		for _, c := range cats {
			list[c.ID] = c
		}

		rd.cats[a.ID] = list
	}

	return list[id]
}

func (rd *reader) project(id types.ID) string {
	if id.Zero() {
		return ""
	}

	if n, ok := rd.names[id]; ok {
		return n
	}

	names, err := rd.a.books.Tenancy.ProjectNames(rd.ctx, []types.ID{id})
	if err != nil && rd.failed == nil {
		rd.failed = err
	}

	rd.names[id] = names[id]

	return names[id]
}

func (rd *reader) accountOut(a tenancybus.Account, access tenancybus.Access) accountOut {
	out := accountOut{
		ID: a.ID.String(), Name: a.Name, Kind: string(a.Kind), Currency: a.Currency, Last4: a.Last4,
		Archived: !a.ArchivedAt.IsZero(), URL: rd.url("/accounts/" + a.ID.String() + "/transactions"),
	}

	for _, r := range access.Roles {
		out.Roles = append(out.Roles, string(r))
	}

	return out
}

func (rd *reader) tx(t ledgerbus.Transaction, a tenancybus.Account) txOut {
	if a.ID.Zero() || a.ID != t.AccountID {
		a = rd.account(t.AccountID)
	}

	out := txOut{
		ID: t.ID.String(), Account: t.AccountID.String(), Date: t.PostedOn.String(), Description: t.Description,
		OwnDescription: t.OwnDescription, Described: t.OwnVia, Payee: t.Payee, CheckMemo: t.CheckMemo, Written: t.WrittenOn.String(), Amount: t.Amount, Currency: a.Currency, Holder: t.Holder, Check: t.CheckNumber,
		Pending: t.Pending, Sorted: t.Sorted(), Parts: []partOut{}, URL: rd.url("/transactions/" + t.ID.String()),
	}

	if t.HasBalance {
		out.Balance = &t.Balance
	}

	for _, s := range t.Splits {
		p := partOut{Amount: s.Amount, Memo: s.Memo, ByRule: !s.RuleID.Zero(), Through: s.Via}

		if !s.CategoryID.Zero() {
			c := rd.category(a, s.CategoryID)
			p.Category, p.CategoryName, p.Kind = s.CategoryID.String(), c.Name, string(c.Kind)
		}

		if !s.ProjectID.Zero() {
			p.Project, p.ProjectName = s.ProjectID.String(), rd.project(s.ProjectID)
		}

		out.Parts = append(out.Parts, p)
	}

	return out
}

func (rd *reader) txs(list []ledgerbus.Transaction, a tenancybus.Account) []txOut {
	out := make([]txOut, 0, len(list))
	for _, t := range list {
		out = append(out, rd.tx(t, a))
	}

	return out
}

// named is a transaction with its account's name, for answers that mix
// accounts.
func (rd *reader) named(t ledgerbus.Transaction, a tenancybus.Account) txOut {
	out := rd.tx(t, a)
	out.AccountName = rd.account(t.AccountID).Name

	return out
}

func (rd *reader) statement(st ledgerbus.Statement) statementOut {
	out := statementOut{
		ID: st.ID.String(), Account: st.AccountID.String(), File: st.FileName, Format: string(st.Format),
		From: st.Start.String(), To: st.End.String(), Checked: string(st.Checked),
		Added: st.Added, Already: st.Already, ByRule: st.ByRule,
		ImportedAt: st.ImportedAt.UTC().Format(time.RFC3339), URL: rd.url("/statements/" + st.ID.String()),
	}

	if st.HasOpening {
		out.Opening = &st.Opening
	}

	if st.HasClosing {
		out.Closing = &st.Closing
	}

	if r := st.Reconciliation; !r.At.IsZero() {
		out.Reconciled = &reconciledOut{From: r.Start.String(), To: r.End.String(), At: r.At.UTC().Format(time.RFC3339), Note: r.Note}
	}

	return out
}

func (rd *reader) rule(r rulebus.Rule, a tenancybus.Account) ruleOut {
	out := ruleOut{Match: r.Match, Direction: string(r.Direction), Through: r.Via}

	if !r.ID.Zero() {
		out.ID, out.URL = r.ID.String(), rd.url("/accounts/"+a.ID.String()+"/rules")
	}

	if !r.CategoryID.Zero() {
		out.Category, out.CategoryName = r.CategoryID.String(), rd.category(a, r.CategoryID).Name
	}

	if !r.ProjectID.Zero() {
		out.Project, out.ProjectName = r.ProjectID.String(), rd.project(r.ProjectID)
	}

	return out
}

// done writes the answer, unless a name could not be looked up on the way.
func (rd *reader) done(w http.ResponseWriter, r *http.Request, v any) {
	if rd.failed != nil {
		rd.a.fail(w, r, "reading the books", rd.failed)

		return
	}

	web.WriteJSON(w, http.StatusOK, v)
}

// --- refusals ---------------------------------------------------------------------

// bookRefused answers a business refusal in the API's words, and reports
// whether err was one: 404 for what is not there or not the person's, 403
// for what their role does not allow, 422 for a field that will not do.
func (a app) bookRefused(w http.ResponseWriter, r *http.Request, err error, what string) bool {
	invalid, isInvalid := errors.AsType[rulebus.Invalid](err)
	part, isPart := errors.AsType[ledgerbus.Invalid](err)
	receipt, isReceipt := errors.AsType[receiptbus.Invalid](err)
	differs, isDiffers := errors.AsType[receiptbus.AmountDiffers](err)
	unknown, isUnknown := errors.AsType[receiptbus.AccountUnknown](err)

	switch {
	case err == nil:
		return false
	case errors.Is(err, ledgerbus.ErrLocked):
		web.WriteJSON(w, http.StatusConflict, web.Problem("", "That is in a reconciled period, so it cannot change. Reopening the period is its person's, on the statement's page; tell them rather than asking again."))
	case errors.Is(err, ledgerbus.ErrExplained):
		web.WriteJSON(w, http.StatusConflict, web.Problem("", "That transaction is part of another explanation, or is not one list_explanation_candidates offers for these accounts and months. Ask for the candidates again and add only those."))
	case errors.Is(err, rulebus.ErrDuplicate):
		web.WriteJSON(w, http.StatusConflict, web.Problem("match", "The account already has a rule with that text. Change that rule instead, or save the rule again with save_rule, which corrects the rule with the same text."))
	case errors.Is(err, receiptbus.ErrRemoved):
		web.WriteJSON(w, http.StatusConflict, web.Problem("", "That receipt was removed. Restoring it is its person's, on its page."))
	case errors.Is(err, receiptbus.ErrAttached):
		web.WriteJSON(w, http.StatusConflict, web.Problem("", "That check's image is attached to a transaction already. If it is on the wrong one, detach_receipt it first, then read it again."))
	case isDiffers:
		web.WriteJSON(w, http.StatusConflict, web.Problem("amount", fmt.Sprintf("The bank paid check %s for %s, and you read %s. Nothing was changed. Look at the image again: one of the number's digits or the amount's is misread. If the image really says %s, tell the person rather than reading it again.", differs.Number, differs.Bank, differs.Read, differs.Read)))
	case errors.Is(err, receiptbus.ErrFiled):
		web.WriteJSON(w, http.StatusConflict, web.Problem("", "That check was filed under its account already, or the person removed it from their checks. list_waiting_receipts says what is still waiting."))
	case isUnknown:
		web.WriteJSON(w, http.StatusConflict, web.Problem("account_number", accountUnknown(unknown)))
	case isReceipt:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(receipt.Field, "The "+receipt.Field+" will not do: "+receipt.Err.Error()+"."))
	case isPart:
		field := "parts"
		if part.Index >= 0 {
			field = fmt.Sprintf("parts[%d].%s", part.Index, part.Field)
		}

		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(field, "That will not do: "+part.Err.Error()+"."))
	case errors.Is(err, ledgerbus.ErrNotFound), errors.Is(err, tenancybus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("", fmt.Sprintf("There is no such %s, or it is not one you may see.", what)))
	case errors.Is(err, ledgerbus.ErrForbidden):
		web.WriteJSON(w, http.StatusForbidden, web.Problem("", fmt.Sprintf("Your role on this %s does not allow this; it needs a bookkeeper or owner.", what)))
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, "The "+invalid.Field+" will not do: "+invalid.Err.Error()+"."))
	default:
		a.fail(w, r, "reading the books", err)
	}

	return true
}

// accountUnknown is why a check in the person's own checks was not filed
// by the account number read off it. It names the account by its last
// four digits, as the site does, and never says the number back.
func accountUnknown(e receiptbus.AccountUnknown) string {
	switch {
	case e.ReadOnly:
		return fmt.Sprintf("The account ending %s is one the person may only read, so the check was not filed. Nothing was changed. Tell them: somebody who keeps that account's books can give them a role that adds receipts, or file it themselves.", e.Last4)
	case e.Matches > 1:
		return fmt.Sprintf("%d of the person's accounts end %s, so which one the check is drawn on is not certain, and it was not filed. Nothing was changed. Tell the person; they can choose the account for it on their checks' page.", e.Matches, e.Last4)
	}

	return fmt.Sprintf("No account the person may add receipts to ends %s. Nothing was changed. Look at the line of digits at the check's foot again: the account number is the group after the routing number. If it really ends %s, tell the person: the account may not be on the site, or its last four digits not set on its page.", e.Last4, e.Last4)
}

// pathID reads an identifier from the path, answering 404 itself when it
// is not one: nothing has that identifier.
func (a app) pathID(w http.ResponseWriter, r *http.Request, name, what string) (types.ID, bool) {
	id, err := types.ParseID(r.PathValue(name))
	if err != nil {
		web.WriteJSON(w, http.StatusNotFound, web.Problem("", fmt.Sprintf("There is no such %s, or it is not one you may see.", what)))

		return types.ID{}, false
	}

	return id, true
}

// monthParam reads a required YYYY-MM.
func (a app) monthParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	m := r.URL.Query().Get("month")
	if _, _, err := ledgerbus.MonthRange(m); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("month", "Give the month as YYYY-MM, such as 2026-09."))

		return "", false
	}

	return m, true
}

// ids reads identifiers separated by commas.
func (a app) ids(w http.ResponseWriter, s, name string) ([]types.ID, bool) {
	var out []types.ID

	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}

		id, err := types.ParseID(part)
		if err != nil {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, fmt.Sprintf("%q is not an id. Give ids separated by commas, as other answers give them.", part)))

			return nil, false
		}

		out = append(out, id)
	}

	return out, true
}

// --- the handlers -----------------------------------------------------------------

func (a app) whoami(w http.ResponseWriter, r *http.Request) {
	u, _ := mid.UserFrom(r.Context())
	k, _ := mid.KeyFrom(r.Context())

	scopes := make([]string, 0, len(k.Scopes))
	for _, s := range k.Scopes {
		scopes = append(scopes, string(s))
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{
		"name": u.Name, "email": u.Email.String(), "scopes": scopes,
		"key": map[string]string{"name": k.Name, "ends": k.ExpiresAt.UTC().Format("2006-01-02")},
	})
}

func (a app) overview(w http.ResponseWriter, r *http.Request) {
	rd := a.reader(r)

	ov, err := a.books.Tenancy.Overview(r.Context(), rd.me)
	if a.bookRefused(w, r, err, "organization") {
		return
	}

	today := types.DateOf(time.Now())

	accounts := func(list []tenancybus.Account) []accountOut {
		out := make([]accountOut, 0, len(list))

		for _, acct := range list {
			_, access, err := a.books.Tenancy.Account(r.Context(), rd.me, acct.ID)
			if err != nil {
				continue // an account seen through a project only: not readable here
			}

			o := rd.accountOut(acct, access)

			if cover, err := a.books.Ledger.Coverage(r.Context(), rd.me, acct.ID, today); err == nil {
				unsorted, missing := 0, 0

				for _, m := range cover {
					unsorted += m.Unsorted
					if m.State == ledgerbus.Missing {
						missing++
					}

					for _, st := range m.Statements {
						if st.End.String() > o.LastStatementEnds {
							o.LastStatementEnds = st.End.String()
						}
					}
				}

				o.Unsorted, o.MonthsMissing = &unsorted, &missing
			}

			out = append(out, o)
		}

		return out
	}

	projects := func(list []tenancybus.Project) []map[string]string {
		out := make([]map[string]string, 0, len(list))
		for _, p := range list {
			out = append(out, map[string]string{
				"id": p.ID.String(), "name": p.Name, "starts": p.StartsOn.String(), "ends": p.EndsOn.String(),
				"url": rd.url("/projects/" + p.ID.String() + "/book"),
			})
		}

		return out
	}

	orgs := make([]map[string]any, 0, len(ov.Orgs))
	for _, o := range ov.Orgs {
		orgs = append(orgs, map[string]any{
			"id": o.Org.ID.String(), "name": o.Org.Name, "accounts": accounts(o.Accounts), "projects": projects(o.Projects),
			"url": rd.url("/orgs/" + o.Org.ID.String()),
		})
	}

	rd.done(w, r, map[string]any{"organizations": orgs, "accounts": accounts(ov.Accounts), "projects": projects(ov.Projects)})
}

func (a app) listInbox(w http.ResponseWriter, r *http.Request) {
	rd := a.reader(r)

	inbox, err := a.books.Ledger.Inbox(r.Context(), rd.me)
	if a.bookRefused(w, r, err, "inbox") {
		return
	}

	files := make([]types.ID, 0, len(inbox))
	for _, f := range inbox {
		files = append(files, f.FileID)
	}

	var props []ledgerbus.Proposal
	if len(files) > 0 {
		if props, err = a.books.Ledger.Propose(r.Context(), rd.me, files, nil); a.bookRefused(w, r, err, "file") {
			return
		}
	}

	out := make([]map[string]any, 0, len(inbox))

	for _, f := range inbox {
		var parts []map[string]any

		for _, p := range props {
			if p.File.ID == f.FileID {
				parts = append(parts, rd.proposal(p))
			}
		}

		out = append(out, map[string]any{
			"inbox_id": f.ID.String(), "file": f.FileID.String(), "name": f.Name, "source": f.Source,
			"received": f.ReceivedAt.UTC().Format(time.RFC3339), "parts": parts,
		})
	}

	rd.done(w, r, map[string]any{"files": out, "imports_url": rd.url("/imports")})
}

// proposal is one file, or one account's part of it, as the inbox lists it.
func (rd *reader) proposal(p ledgerbus.Proposal) map[string]any {
	d := p.Draft
	out := map[string]any{"part": p.Part, "by": string(p.By), "ready": p.Unattended(), "imported": p.Imported()}

	if !p.Account.Zero() {
		out["account"], out["account_name"] = p.Account.String(), rd.account(p.Account).Name
		out["page"] = rd.url("/accounts/" + p.Account.String() + "/imports/" + p.File.ID.String())
	}

	if p.Problem != nil || p.Account.Zero() {
		out["needs"] = problemOf(p.Problem, p.Account.Zero())

		return out
	}

	if !d.Statement.Start.Zero() {
		out["period"] = spanOut{From: d.Statement.Start.String(), To: d.Statement.End.String()}
	}

	out["transactions"], out["new"] = len(d.Result.Records), d.Statement.Added

	switch {
	case d.Check.OK:
		out["check"] = "balances by " + string(d.Check.Method)
	case d.Check.Failed():
		out["check"] = "does not balance"
	default:
		out["check"] = "not checked"
	}

	if needs := needsOf(p); needs != "" {
		out["needs"] = needs
	}

	return out
}

// needsOf is why a proposal that reads cannot be imported with the others,
// in the words the imports page uses; "" when it can, or when it is in.
func needsOf(p ledgerbus.Proposal) string {
	d := p.Draft

	switch {
	case p.Unattended(), p.Imported():
		return ""
	case d.Choose:
		return "This account's number is not in the file; a person chooses on its own page."
	case d.Unproven():
		return "Its own figures do not show it was read whole, so it cannot be imported."
	case d.Check.Failed():
		return "It does not balance, so it cannot be imported as it is."
	case d.Statement.Locked > 0:
		return "Some of it falls in a reconciled period."
	case !d.Check.OK:
		return "Nothing in it could be checked; its own page asks a person for its balances."
	case d.Statement.SetAside() > 0:
		return "Some rows look like ones already here; its own page says which."
	case d.Unnamed > 0:
		return "Some rows name no holder; its own page asks whose they are."
	case d.Result.Skipped > 0, d.Result.Unplaced:
		return "Something in it could not be read; its own page says what."
	}

	return "It needs a look on its own page."
}

// problemOf is why a file could not be read at all, or for an account.
func problemOf(err error, noAccount bool) string {
	switch {
	case errors.Is(err, ledgerbus.ErrPDFPassword):
		return "The PDF has a password, so it cannot be read."
	case errors.Is(err, ledgerbus.ErrPDFScan):
		return "The PDF is a picture of a statement, with no text in it to read."
	case errors.Is(err, ledgerbus.ErrPDFUnavailable):
		return "PDFs cannot be read on the site just now."
	case errors.Is(err, ledgerbus.ErrPDFNoRows), errors.Is(err, ledgerbus.ErrEmpty):
		return "No transactions could be found in it."
	case errors.Is(err, ledgerbus.ErrUnbalanced):
		return "It does not balance, so it cannot be imported as it is."
	case err != nil:
		return "It could not be read as a statement."
	case noAccount:
		return "No account could be told from the file; a person chooses one on the imports page."
	}

	return ""
}

func (a app) accountMonths(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	rd := a.reader(r)

	cover, err := a.books.Ledger.Coverage(r.Context(), rd.me, id, types.DateOf(time.Now()))
	if a.bookRefused(w, r, err, "account") {
		return
	}

	months := make([]map[string]any, 0, len(cover))

	for _, m := range cover {

		gaps := make([]spanOut, 0, len(m.Gaps))
		for _, g := range m.Gaps {
			gaps = append(gaps, spanOut{From: g.From.String(), To: g.To.String()})
		}

		sts := make([]idName, 0, len(m.Statements))
		for _, st := range m.Statements {
			sts = append(sts, idName{ID: st.ID.String(), Name: st.FileName})
		}

		months = append(months, map[string]any{
			"month": m.Month, "state": string(m.State), "gaps": gaps, "statements": sts,
			"transactions": m.Count, "unsorted": m.Unsorted,
		})
	}

	out, err := a.books.Receipts.OutstandingChecks(r.Context(), rd.me, id, types.Date{})
	if a.bookRefused(w, r, err, "account") {
		return
	}

	rd.done(w, r, map[string]any{
		"account": id.String(), "months": months, "url": rd.url("/accounts/" + id.String() + "/months"),
		"outstanding_checks": map[string]any{"checks": rd.receipts(out.Checks), "total": out.Total, "unpriced": out.Unpriced},
	})
}

func (a app) listStatements(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	rd := a.reader(r)

	sts, err := a.books.Ledger.Statements(r.Context(), rd.me, id)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	out := make([]statementOut, 0, len(sts))
	for _, st := range sts {
		out = append(out, rd.statement(st))
	}

	rd.done(w, r, map[string]any{"account": id.String(), "statements": out})
}

func (a app) getStatement(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "statement", "statement")
	if !ok {
		return
	}

	rd := a.reader(r)

	rv, err := a.books.Ledger.Review(r.Context(), rd.me, id)
	if a.bookRefused(w, r, err, "statement") {
		return
	}

	overlapping := make([]spanOut, 0, len(rv.Overlapping))
	for _, o := range rv.Overlapping {
		overlapping = append(overlapping, spanOut{From: o.Start.String(), To: o.End.String()})
	}

	rd.done(w, r, map[string]any{
		"statement": rd.statement(rv.Statement), "account_name": rv.Account.Name, "currency": rv.Account.Currency,
		"in": rv.In, "out": rv.Out, "net": rv.Net(),
		"transactions": rd.txs(rv.Transactions, rv.Account), "unsorted": rd.txs(rv.Unsorted, rv.Account),
		"overlapping_reconciled": overlapping,
	})
}

func (a app) listTransactions(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	month, ok := a.monthParam(w, r)
	if !ok {
		return
	}

	show := r.URL.Query().Get("show")

	switch show {
	case "", "all", "unsorted", "by_rule", "no_receipt":
	default:
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("show", "Give show as all, unsorted, by_rule or no_receipt."))

		return
	}

	rd := a.reader(r)
	acct := rd.account(id)

	txs, err := a.books.Ledger.Transactions(r.Context(), rd.me, id, month)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	ids := make([]types.ID, 0, len(txs))
	for _, t := range txs {
		ids = append(ids, t.ID)
	}

	receipts, err := a.books.Receipts.OnTransactions(r.Context(), ids)
	if err != nil {
		a.fail(w, r, "reading the receipts", err)

		return
	}

	out := make([]txOut, 0, len(txs))

	for _, t := range txs {
		n := len(receipts[t.ID])

		switch {
		case show == "unsorted" && t.Sorted(), show == "by_rule" && !t.ByRule(), show == "no_receipt" && n > 0:
			continue
		}

		o := rd.tx(t, acct)
		o.Receipts = &n
		out = append(out, o)
	}

	rd.done(w, r, map[string]any{"account": id.String(), "account_name": acct.Name, "month": month, "transactions": out})
}

func (a app) findTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	amount := func(name string) (money.Amount, bool, bool) {
		s := q.Get(name)
		if s == "" {
			return 0, false, true
		}

		v, err := money.Parse(s)
		if err != nil {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, "Give "+name+" as an amount, such as 1240.00."))

			return 0, false, false
		}

		return v.Abs(), true, true
	}

	date := func(name string) (types.Date, bool) {
		s := q.Get(name)
		if s == "" {
			return types.Date{}, true
		}

		d, err := types.ParseDate(s)
		if err != nil {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, "Give "+name+" as a day, YYYY-MM-DD."))

			return types.Date{}, false
		}

		return d, true
	}

	query := ledgerbus.Query{Text: q.Get("text")}

	var ok bool

	exact, hasExact, ok := amount("amount")
	if !ok {
		return
	}

	if query.Min, query.HasMin, ok = amount("min"); !ok {
		return
	}

	if query.Max, query.HasMax, ok = amount("max"); !ok {
		return
	}

	if hasExact {
		query.Min, query.Max, query.HasMin, query.HasMax = exact, exact, true, true
	}

	switch d := q.Get("direction"); d {
	case "":
	case "in", "out":
		query.Direction = rulebus.Direction(d)
	default:
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("direction", "Give direction as in or out, or leave it out."))

		return
	}

	if query.From, ok = date("from"); !ok {
		return
	}

	if query.To, ok = date("to"); !ok {
		return
	}

	if query.Accounts, ok = a.ids(w, q.Get("account"), "account"); !ok {
		return
	}

	if query.Limit, ok = a.number(w, r, "limit", ledgerbus.DefaultFind, 1, ledgerbus.MaxFind); !ok {
		return
	}

	rd := a.reader(r)

	got, err := a.books.Ledger.Find(r.Context(), rd.me, query)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	out := make([]txOut, 0, len(got.Found))
	for _, f := range got.Found {
		rd.accts[f.Account.ID] = f.Account
		out = append(out, rd.named(f.Transaction, f.Account))
	}

	rd.done(w, r, map[string]any{"transactions": out, "more": got.More})
}

func (a app) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "transaction", "transaction")
	if !ok {
		return
	}

	rd := a.reader(r)

	e, err := a.books.Ledger.Transaction(r.Context(), rd.me, id)
	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	rd.accts[e.Account.ID] = e.Account

	t := e.Transaction

	receipts, err := a.books.Receipts.OnTransactions(r.Context(), []types.ID{t.ID})
	if err != nil {
		a.fail(w, r, "reading the receipts", err)

		return
	}

	clearing, err := a.books.Ledger.Clearing(r.Context(), rd.me, []types.ID{t.ID})
	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	out := map[string]any{
		"transaction":  rd.named(t, e.Account),
		"statement":    rd.url("/statements/" + t.StatementID.String()),
		"receipts":     rd.receipts(receipts[t.ID]),
		"explanation":  rd.url("/transactions/" + t.ID.String() + "/explain"),
		"may_sort":     e.CanSort(),
		"is_explained": clearing.Explains[t.ID],
	}

	if e.Lock.Made() {
		out["reconciled"] = reconciledOut{From: e.Lock.Start.String(), To: e.Lock.End.String(), At: e.Lock.At.UTC().Format(time.RFC3339), Note: e.Lock.Note}
	}

	if by, ok := clearing.By[t.ID]; ok {
		if by.Hidden {
			out["cleared_by"] = "a transaction in an account you may not see"
		} else {
			out["cleared_by"] = rd.named(by.Transaction, tenancybus.Account{})
		}
	}

	rd.done(w, r, out)
}

func (rd *reader) receipts(list []receiptbus.Receipt) []map[string]any {
	out := make([]map[string]any, 0, len(list))

	for _, rc := range list {
		m := map[string]any{
			"id": rc.ID.String(), "merchant": rc.Merchant, "note": rc.Note, "pages": len(rc.Files),
			"uploaded": rc.CreatedAt.UTC().Format(time.RFC3339), "url": rd.url("/receipts/" + rc.ID.String()),
		}

		if !rc.SpentOn.Zero() {
			m["spent_on"] = rc.SpentOn.String()
		}

		if rc.HasAmount {
			m["amount"] = rc.Amount
		}

		if rc.CheckImage {
			m["check_image"] = true
			m["check"] = rc.Check

			if rc.Memo != "" {
				m["memo"] = rc.Memo
			}

			if !rc.WrittenOn.Zero() {
				m["written"] = rc.WrittenOn.String()
			}
		}

		out = append(out, m)
	}

	return out
}

func (a app) monthToSort(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	month, ok := a.monthParam(w, r)
	if !ok {
		return
	}

	rd := a.reader(r)

	m, err := a.books.Ledger.ToSort(r.Context(), time.Now(), rd.me, id, month)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	rd.accts[m.Account.ID] = m.Account

	rows := make([]map[string]any, 0, len(m.Rows))

	for _, row := range m.Rows {
		g := row.Guess
		guess := map[string]any{"by": string(g.By)}

		if !g.CategoryID.Zero() {
			guess["category"], guess["category_name"] = g.CategoryID.String(), m.CategoryName(g.CategoryID)
		}

		if !g.ProjectID.Zero() {
			guess["project"], guess["project_name"] = g.ProjectID.String(), rd.project(g.ProjectID)
		}

		if g.By == ledgerbus.ByRule {
			guess["rule"] = g.Rule.Match
		}

		out := map[string]any{"transaction": rd.tx(row.Transaction, m.Account), "guess": guess}
		if !row.Elsewhere.ID.Zero() {
			out["in_project_elsewhere"] = row.Elsewhere.Name
		}

		rows = append(rows, out)
	}

	cats := make([]map[string]string, 0, len(m.Categories))
	for _, c := range m.Categories {
		cats = append(cats, map[string]string{"id": c.ID.String(), "name": c.Name, "kind": string(c.Kind)})
	}

	projects := make([]idName, 0, len(m.Projects))
	for _, p := range m.Projects {
		projects = append(projects, idName{ID: p.ID.String(), Name: p.Name})
	}

	rd.done(w, r, map[string]any{
		"account": id.String(), "account_name": m.Account.Name, "month": m.Month, "rows": rows, "more": m.More,
		"categories": cats, "projects": projects, "url": rd.url("/accounts/" + id.String() + "/sort?month=" + month),
	})
}

func (a app) listRules(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	rd := a.reader(r)

	book, err := a.books.Ledger.Rulebook(r.Context(), time.Now(), rd.me, id)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	rules := make([]ruleOut, 0, len(book.Rules))

	for _, u := range book.Rules {
		o := rd.rule(u.Rule, book.Account)
		parts, waiting := u.Parts, u.Waiting
		o.Parts, o.Waiting, o.Problem = &parts, &waiting, string(u.Problem)
		rules = append(rules, o)
	}

	torn := make([]map[string]any, 0, len(book.Torn))

	for _, t := range book.Torn {
		matches := make([]ruleOut, 0, len(t.Rules))
		for _, rl := range t.Rules {
			matches = append(matches, rd.rule(rl, book.Account))
		}

		torn = append(torn, map[string]any{"transaction": rd.tx(t.Transaction, book.Account), "rules": matches})
	}

	rd.done(w, r, map[string]any{
		"account": id.String(), "account_name": book.Account.Name, "rules": rules, "waiting": book.Waiting, "torn": torn,
		"url": rd.url("/accounts/" + id.String() + "/rules"),
	})
}

func (a app) tryRule(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	q := r.URL.Query()
	f := rulebus.Fields{Match: q.Get("match"), Direction: rulebus.Direction(q.Get("direction"))}

	for name, into := range map[string]*types.ID{"category": &f.CategoryID, "project": &f.ProjectID} {
		if s := q.Get(name); s != "" {
			v, err := types.ParseID(s)
			if err != nil {
				web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, fmt.Sprintf("%q is not an id. Use the id list_rules or get_month_to_sort gives.", s)))

				return
			}

			*into = v
		}
	}

	rd := a.reader(r)

	trial, err := a.books.Ledger.TryRule(r.Context(), time.Now(), rd.me, id, f)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	rd.accts[trial.Account.ID] = trial.Account

	group := func(list []ledgerbus.Transaction, n int) map[string]any {
		return map[string]any{"count": n, "transactions": rd.txs(list, trial.Account)}
	}

	torn := group(trial.Torn, trial.TornCount)

	with := make([]ruleOut, 0, len(trial.With))
	for _, rl := range trial.With {
		with = append(with, rd.rule(rl, trial.Account))
	}

	torn["with"] = with

	rd.done(w, r, map[string]any{
		"rule": rd.rule(trial.Rule, trial.Account), "saved": false,
		"would":     group(trial.Would, trial.WouldCount),
		"elsewhere": group(trial.Elsewhere, trial.ElsewhereCount),
		"torn":      torn,
		"already":   group(trial.Already, trial.AlreadyCount),
	})
}

func (a app) holders(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	month, ok := a.monthParam(w, r)
	if !ok {
		return
	}

	rd := a.reader(r)

	m, err := a.books.Ledger.Holders(r.Context(), rd.me, id, month)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	holders := make([]map[string]any, 0, len(m.Totals))
	for _, t := range m.Totals {
		holders = append(holders, map[string]any{"holder": t.Holder, "transactions": t.Count, "sum": t.Sum})
	}

	rd.done(w, r, map[string]any{
		"account": id.String(), "month": month, "by_holder": len(m.Totals) > 0 || len(m.Missing) > 0,
		"holders": holders, "missing": append([]string{}, m.Missing...), "present": m.Present(), "expected": m.Expected(),
	})
}

func (a app) getExplanation(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "transaction", "transaction")
	if !ok {
		return
	}

	rd := a.reader(r)

	x, err := a.books.Ledger.Explain(r.Context(), rd.me, id)
	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	rd.done(w, r, rd.explanation(x))
}

// explanation is an explanation as get_explanation answers it, and as
// gather_explanation answers after changing it.
func (rd *reader) explanation(x ledgerbus.Explanation) map[string]any {
	rd.accts[x.Account.ID] = x.Account
	for _, c := range x.Choices {
		rd.accts[c.ID] = c
	}

	lines := make([]txOut, 0, len(x.Lines))
	for _, l := range x.Lines {
		lines = append(lines, rd.named(l.Transaction, l.Account))
	}

	state := "open"

	switch {
	case x.Count() == 0:
		state = "not gathered yet"
	case x.Difference() == 0:
		state = "explained"
	case x.Accepted:
		state = "accepted"
	}

	sources := make([]idName, 0, len(x.Sources))
	for _, s := range x.Sources {
		sources = append(sources, idName{ID: s.String(), Name: rd.account(s).Name})
	}

	choices := make([]idName, 0, len(x.Choices))
	for _, c := range x.Choices {
		choices = append(choices, idName{ID: c.ID.String(), Name: c.Name})
	}

	return map[string]any{
		"transaction": rd.named(x.Transaction, x.Account), "lines": lines,
		"hidden": map[string]any{"count": x.Hidden, "sum": x.HiddenSum},
		"sum":    x.Sum, "difference": x.Difference(), "state": state, "note": x.Note,
		"gather":      map[string]any{"accounts": sources, "from": x.From(), "to": x.To()},
		"may_draw_on": choices,
		"url":         rd.url("/transactions/" + x.Transaction.ID.String() + "/explain"),
	}
}

func (a app) explanationCandidates(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "transaction", "transaction")
	if !ok {
		return
	}

	rd := a.reader(r)
	q := r.URL.Query()

	x, err := a.books.Ledger.Explain(r.Context(), rd.me, id)
	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	accounts, ok := a.ids(w, q.Get("account"), "account")
	if !ok {
		return
	}

	if len(accounts) == 0 {
		accounts = x.Sources
	}

	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = x.From()
	}

	if to == "" {
		to = x.To()
	}

	for name, m := range map[string]string{"from": from, "to": to} {
		if _, _, err := ledgerbus.MonthRange(m); err != nil {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, "Give "+name+" as a month, YYYY-MM."))

			return
		}
	}

	lines, err := a.books.Ledger.Candidates(r.Context(), rd.me, id, accounts, from, to)
	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	out := make([]txOut, 0, len(lines))
	for _, l := range lines {
		rd.accts[l.Account.ID] = l.Account
		out = append(out, rd.named(l.Transaction, l.Account))
	}

	rd.done(w, r, map[string]any{"from": from, "to": to, "lines": out})
}

func (a app) waitingReceipts(w http.ResponseWriter, r *http.Request) {
	rd := a.reader(r)

	waiting, err := a.books.Receipts.Waiting(r.Context(), rd.me)
	if a.bookRefused(w, r, err, "receipt") {
		return
	}

	suggestions, err := a.books.Receipts.Suggestions(r.Context(), rd.me, waiting)
	if a.bookRefused(w, r, err, "receipt") {
		return
	}

	out := rd.receipts(waiting)
	for i, rc := range waiting {
		out[i]["suggestions"] = rd.txs(suggestions[rc.ID], tenancybus.Account{})
	}

	mine, err := a.books.Receipts.CheckInbox(r.Context(), rd.me)
	if a.bookRefused(w, r, err, "receipt") {
		return
	}

	for _, c := range mine.Waiting {
		out = append(out, map[string]any{
			"id": c.ID.String(), "merchant": "", "note": "", "pages": 1,
			"uploaded": c.AddedAt.UTC().Format(time.RFC3339), "url": rd.url("/checks"),
			"check_image": true, "check": "", "your_checks": true, "suggestions": []any{},
		})
	}

	rd.done(w, r, map[string]any{"receipts": out})
}

// receiptImage is one page of a receipt as an image: its large picture
// for a photo (filebus.Picture), as it is for a WebP, which Claude reads
// too, and refused for a PDF or a HEIC, which an MCP image cannot be.
func (a app) receiptImage(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "receipt", "receipt")
	if !ok {
		return
	}

	page := 1

	if p := r.URL.Query().Get("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem("page", "Give the page as a number from 1."))

			return
		}

		page = n
	}

	rd := a.reader(r)
	at := rd.url("/receipts/" + id.String())

	// A receipt's page, or else the image of a check in the person's own
	// checks, which is in no account yet.
	f, err := a.books.Receipts.File(r.Context(), rd.me, id, page-1)
	if errors.Is(err, receiptbus.ErrNotFound) {
		f, err = a.books.Receipts.InboxCheckFile(r.Context(), rd.me, id, page-1)
		at = rd.url("/checks")
	}

	if a.bookRefused(w, r, err, "receipt, or no such page of it") {
		return
	}

	var (
		pic  io.ReadSeekCloser
		kind = f.ContentType
	)

	switch {
	case filebus.Sizable(f.ContentType):
		pic, err = a.books.Pictures.Picture(r.Context(), f, filebus.Large)
		kind = filebus.JPEG

		// A photo too small to need a smaller picture is shown as it is.
		if errors.Is(err, filebus.ErrNoPicture) {
			pic, err = a.books.Pictures.Open(f)
			kind = f.ContentType
		}
	case f.ContentType == filebus.WebP:
		pic, err = a.books.Pictures.Open(f)
	default:
		web.WriteJSON(w, http.StatusUnsupportedMediaType, web.Problem("page",
			fmt.Sprintf("That page is a %s, which cannot be shown as an image here. A person can look at it on the receipt's page: %s", kindName(f.ContentType), at)))

		return
	}

	if err != nil {
		a.fail(w, r, "reading a receipt's page", err)

		return
	}
	defer pic.Close()

	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "no-store")

	if _, err := io.Copy(w, pic); err != nil {
		a.log.Info("a receipt's page was cut off while being sent", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	}
}

// kindName is a file's kind in a sentence.
func kindName(contentType string) string {
	switch contentType {
	case filebus.PDF:
		return "PDF"
	case filebus.HEIC:
		return "HEIC photo"
	}

	return contentType
}

// maxBookLines is the most parts a project's book answer lists.
const maxBookLines = 500

func (a app) projectBook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "project", "project")
	if !ok {
		return
	}

	rd := a.reader(r)

	book, err := a.books.Ledger.ProjectBook(r.Context(), rd.me, id)
	if a.bookRefused(w, r, err, "project") {
		return
	}

	totals := func(list []ledgerbus.Total) []map[string]any {
		out := make([]map[string]any, 0, len(list))
		for _, t := range list {
			out = append(out, map[string]any{
				"key": t.Key, "currency": t.Currency, "in": t.In, "out": t.Out, "net": t.In + t.Out,
				"income": t.Income, "expenses": t.Expenses, "transfers": t.Transfers, "pass_through": t.PassThrough, "unsorted": t.Unsorted,
			})
		}

		return out
	}

	lines := make([]map[string]any, 0, min(len(book.Lines), maxBookLines))
	for _, l := range book.Lines[:min(len(book.Lines), maxBookLines)] {
		line := map[string]any{
			"date": l.PostedOn.String(), "description": l.Description, "account": l.AccountName,
			"amount": l.Split.Amount, "currency": l.Currency, "category": l.CategoryName, "kind": string(l.CategoryKind),
			"memo": l.Split.Memo, "transaction": rd.url("/transactions/" + l.Split.TransactionID.String()),
		}

		if l.OwnDescription != "" {
			line["own_description"] = l.OwnDescription
		}

		lines = append(lines, line)
	}

	rd.done(w, r, map[string]any{
		"project": map[string]string{"id": book.Project.ID.String(), "name": book.Project.Name, "starts": book.Project.StartsOn.String(), "ends": book.Project.EndsOn.String()},
		"totals":  totals(book.Totals), "by_category": totals(book.ByCategory), "by_month": totals(book.ByMonth),
		"lines": lines, "more": max(len(book.Lines)-maxBookLines, 0), "url": rd.url("/projects/" + id.String() + "/book"),
	})
}

func (a app) changes(w http.ResponseWriter, r *http.Request) {
	since := time.Now().Add(-24 * time.Hour)

	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t, err = time.Parse("2006-01-02", s)
		}

		if err != nil {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem("since", "Give since as a day, YYYY-MM-DD, or a time such as 2026-10-09T08:00:00Z."))

			return
		}

		since = t
	}

	limit, ok := a.number(w, r, "limit", 100, 1, eventbus.MaxThrough)
	if !ok {
		return
	}

	u, _ := mid.UserFrom(r.Context())

	events, err := a.books.History.Through(r.Context(), u.ID, since, limit)
	if err != nil {
		a.fail(w, r, "reading the history", err)

		return
	}

	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, map[string]any{
			"at": e.At.UTC().Format(time.RFC3339), "via": e.Via, "action": string(e.Action),
			"on": map[string]string{"kind": string(e.Scope.Kind), "id": e.Scope.ID.String()}, "detail": e.Detail,
		})
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"since": since.UTC().Format(time.RFC3339), "changes": out})
}
