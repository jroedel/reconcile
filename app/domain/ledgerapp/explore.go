package ledgerapp

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// A search's choices, read from the address (docs/explorer.md). The form is
// a GET, so that a search is a link: bookmarked, sent to the accountant, or
// the way back from a charge opened from it (?back=). Each choice is one
// value, never a list, so the address stays far under the 8 KB the proxy
// refuses (bulk.go).

// search is the choices as they were typed, for the form to give back, and
// what they ask of the ledger.
type search struct {
	Words, From, To, Min, Max string
	Way, Category, Project    string
	Check, Receipts, Account  string

	// Month is ?month=: a month's first day to its last, kept because every
	// month link in the app says it so. Days typed in the form win.
	Month string

	Criteria ledgerbus.Criteria

	// Bad is a day or an amount that could not be read, and was left out
	// of the search rather than refusing all of it.
	Bad bool
}

// maxWords is the longest text searched for, as long as a rule's.
const maxWords = rulebus.MaxMatch

// searchOf reads a search from an address. The month list's older
// addresses -- ?unsorted=1, ?byrule=1, ?through=1, linked from pages and
// bookmarks before the form -- mean what they always did.
func searchOf(q url.Values) search {
	get := func(key string) string { return strings.TrimSpace(q.Get(key)) }

	s := search{
		Words: get("q"), From: get("from"), To: get("to"), Min: get("min"), Max: get("max"),
		Way: get("way"), Category: get("category"), Project: get("project"),
		Check: get("check"), Receipts: get("receipts"), Account: get("account"), Month: get("month"),
	}

	switch {
	case q.Get("unsorted") == "1" && s.Category == "":
		s.Category = "unsorted"
	case q.Get("byrule") == "1" && s.Check == "":
		s.Check = string(ledgerbus.MarkRule)
	case q.Get("through") == "1" && s.Check == "":
		s.Check = string(ledgerbus.MarkProgram)
	}

	if utf8.RuneCountInString(s.Words) > maxWords {
		s.Words = string([]rune(s.Words)[:maxWords])
	}

	c := &s.Criteria
	c.Text = s.Words

	if s.From != "" || s.To != "" {
		s.Month = ""
		c.From = s.day(s.From)
		c.To = s.day(s.To)
	} else if s.Month != "" {
		s.month(s.Month)
	}

	if s.Min != "" {
		c.Min, c.HasMin = s.amount(s.Min)
		c.Max, c.HasMax = c.Min, c.HasMin
	}

	if s.Max != "" {
		c.Max, c.HasMax = s.amount(s.Max)
	}

	switch s.Way {
	case "in":
		c.Direction = rulebus.In
	case "out":
		c.Direction = rulebus.Out
	default:
		s.Way = ""
	}

	switch cat, kind := s.Category, strings.TrimPrefix(s.Category, "kind:"); {
	case cat == "unsorted":
		c.Unsorted = true
	case cat == "unsaid":
		c.KindUnsaid = true
	case kind != cat && categorybus.Kind(kind).Valid():
		c.Kind = categorybus.Kind(kind)
	default:
		c.Category, s.Category = s.id(cat)
	}

	if s.Project == "none" {
		c.NoProject = true
	} else {
		c.Project, s.Project = s.id(s.Project)
	}

	c.Account, s.Account = s.id(s.Account)

	switch m := ledgerbus.Mark(s.Check); m {
	case ledgerbus.MarkRule, ledgerbus.MarkProgram:
		c.Mark = m
	default:
		s.Check = ""
	}

	switch h := ledgerbus.HasReceipts(s.Receipts); h {
	case ledgerbus.WithReceipts, ledgerbus.WithoutReceipts:
		c.Receipts = h
	default:
		s.Receipts = ""
	}

	if n, err := strconv.Atoi(q.Get("skip")); err == nil && n > 0 {
		c.Skip = n
	}

	return s
}

// month makes the search one month, first day to last.
func (s *search) month(m string) {
	from, to, err := ledgerbus.MonthRange(m)
	if err != nil {
		s.Month = ""

		return
	}

	s.Month, s.Criteria.From, s.Criteria.To = m, from, to.AddDays(-1)
}

func (s *search) day(v string) types.Date {
	if v == "" {
		return types.Date{}
	}

	d, err := types.ParseDate(v)
	if err != nil {
		s.Bad = true
	}

	return d
}

func (s *search) amount(v string) (money.Amount, bool) {
	a, err := typedAmount(v)
	if err != nil {
		s.Bad = true

		return 0, false
	}

	return a.Abs(), true
}

// id is a choice that names something by its ID, and the choice as it is
// kept: nothing, when it is not one.
func (s *search) id(v string) (types.ID, string) {
	if v == "" {
		return types.ID{}, ""
	}

	id, err := types.ParseID(v)
	if err != nil {
		return types.ID{}, ""
	}

	return id, v
}

// Searched reports whether anything beyond a month was chosen: then the
// page says how many fit, and a charge opened from it comes back to it.
func (s search) Searched() bool { return s.Words != "" || s.More() }

// More reports whether a choice beyond the words was made, so that the
// form's further choices are open to show it.
func (s search) More() bool {
	return s.Bad || s.From != "" || s.To != "" || s.Min != "" || s.Max != "" || s.Way != "" ||
		s.Category != "" || s.Project != "" || s.Check != "" || s.Receipts != "" || s.Account != ""
}

// param is one value of an address, for a link that keeps the choices: the
// template escapes each.
type param struct{ Key, Value string }

// Carry is the choices but the days and the page: what a month's link
// keeps, so that "not sorted yet" can be read one month at a time.
func (s search) Carry() []param {
	var out []param

	for _, p := range []param{
		{"q", s.Words}, {"min", s.Min}, {"max", s.Max}, {"way", s.Way}, {"category", s.Category},
		{"project", s.Project}, {"check", s.Check}, {"receipts", s.Receipts}, {"account", s.Account},
	} {
		if p.Value != "" {
			out = append(out, p)
		}
	}

	return out
}

// Kept is every choice but the page: what a link to another page of the
// same search keeps.
func (s search) Kept() []param {
	out := s.Carry()

	switch {
	case s.Month != "":
		out = append(out, param{"month", s.Month})
	default:
		if s.From != "" {
			out = append(out, param{"from", s.From})
		}

		if s.To != "" {
			out = append(out, param{"to", s.To})
		}
	}

	return out
}

// found is what fits a search, with where its page starts and ends and
// where the page before it starts, for the words between the pages.
type found struct {
	ledgerbus.Explored

	First, Last, Prev int
}

func foundOf(ex ledgerbus.Explored) found {
	return found{Explored: ex, First: ex.Skip + 1, Last: ex.Skip + len(ex.Rows), Prev: max(ex.Skip-ledgerbus.DefaultExplore, 0)}
}

// --- an organization's and a project's lists ---------------------------------

// foundView is an organization's or a project's transactions: the search's
// form and what fits it, as the account's month list shows them without
// the month's own figures, which belong to one account's statement.
type foundView struct {
	Name string
	Path string

	Search search
	Found  found
	Groups []categorybus.Group

	// Names of the categories and projects the rows' parts point at, the
	// receipts each row has, and what explains what, as on the month list.
	Categories map[types.ID]string
	Projects   map[types.ID]string
	Receipts   map[types.ID]int
	Clearing   ledgerbus.Clearing
	ByHolder   bool

	// Project is a project's list: its rows are its parts, and what is to
	// check is not offered, because a project's role sorts nothing.
	Project bool
}

func (a app) orgTransactions(w http.ResponseWriter, r *http.Request) {
	a.foundPage(w, r, types.ScopeOrg)
}

func (a app) projectTransactions(w http.ResponseWriter, r *http.Request) {
	a.foundPage(w, r, types.ScopeProject)
}

// foundPage is the list of a scope that is not one account: the account's
// is its month list (transactionsPage).
func (a app) foundPage(w http.ResponseWriter, r *http.Request, kind types.ScopeKind) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	ctx := r.Context()
	view := foundView{Search: searchOf(r.URL.Query()), Project: kind == types.ScopeProject}

	switch kind {
	case types.ScopeOrg:
		o, _, err := a.cfg.Tenancy.Org(ctx, me.ID, id)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		view.Name, view.Path = o.Name, "/orgs/"+id.String()
	default:
		p, _, err := a.cfg.Tenancy.Project(ctx, me.ID, id)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		view.Name, view.Path = p.Name, "/projects/"+id.String()
	}

	ex, err := a.cfg.Ledger.Explore(ctx, me.ID, types.Scope{Kind: kind, ID: id}, view.Search.Criteria)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.Found, view.Groups = foundOf(ex), categorybus.Grouped(ex.Categories)

	view.Categories = make(map[types.ID]string, len(ex.Categories))
	for _, c := range ex.Categories {
		view.Categories[c.ID] = c.Name
	}

	var (
		ids      []types.ID
		projects []types.ID
	)

	for _, row := range ex.Rows {
		ids = append(ids, row.Transaction.ID)

		for _, s := range row.Parts {
			if !s.ProjectID.Zero() {
				projects = append(projects, s.ProjectID)
			}
		}
	}

	if view.Projects, err = a.cfg.Tenancy.ProjectNames(ctx, projects); err != nil {
		a.failed(w, r, err)

		return
	}

	on, err := a.cfg.Receipts.OnTransactions(ctx, ids)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.Receipts = make(map[types.ID]int, len(on))
	for id, rs := range on {
		view.Receipts[id] = len(rs)
	}

	if view.Clearing, err = a.cfg.Ledger.Clearing(ctx, me.ID, ids); err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "found", view)
}
