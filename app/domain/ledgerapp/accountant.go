package ledgerapp

import (
	"net/http"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/question/questionbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// "For the accountant" (docs/accountant.md, 3): an organization's two lists
// of the transactions a bank statement cannot explain on its own, a tab
// each. It is the search's choice of a list, on a page of its own, so that
// what it lists is exactly what the organization's search finds with the
// same choices, and shows nothing a reader of those transactions could not
// already find there.

// accountantView is the page: the organization's search with one list
// chosen, how many are in each list with the same other choices, and the
// months to choose from.
type accountantView struct {
	foundView

	// List is the tab shown, and Needed and Added how many each tab holds.
	List          ledgerbus.ForAccountant
	Needed, Added int

	// Tabs is what a tab's link keeps: every choice but the list and the
	// page.
	Tabs []param

	// Months is the six latest, newest first, as a quick way to choose
	// one; any other is a day or two in the form.
	Months []string

	// Asking is whether the page offers the organization's questions, a
	// third tab; OpenQuestions is how many are open. On that tab, Asked is
	// the questions in State, "open" or "answered", only the reader's own
	// when Mine.
	Asking        bool
	OpenQuestions int
	Asked         []asked
	State         questionbus.State
	Mine          bool
}

// questionsTab is the third tab's List: not one of the ledger's lists,
// but the organization's questions (docs/accountant.md, 4).
const questionsTab ledgerbus.ForAccountant = "questions"

// asked is a question in the tab, with who asked it.
type asked struct {
	questionbus.Listed
	Asker string
}

// shownMonths is how many months the page offers as links.
const shownMonths = 6

func (a app) accountant(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	ctx := r.Context()

	o, access, err := a.cfg.Tenancy.Org(ctx, me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := accountantView{foundView: foundView{Name: o.Name, Path: "/orgs/" + id.String(), Search: searchOf(r.URL.Query())}}

	// The tab that opens first is the reader's own: what is to do, for
	// whoever may keep the books, and what is ready, for everybody else.
	// The address says which once a tab is chosen, so a link one person
	// sends another opens the same tab.
	s := &view.Search
	s.fixed = true

	view.Asking = a.cfg.Questions != nil
	if view.Asking && r.URL.Query().Get("context") == string(questionsTab) {
		a.accountantQuestions(w, r, me.ID, id, view)

		return
	}

	if s.Context == "" {
		s.Context = string(ledgerbus.ContextAdded)
		if access.Can(tenancybus.Bookkeep) {
			s.Context = string(ledgerbus.MoreContextNeeded)
		}

		s.Criteria.Accountant = ledgerbus.ForAccountant(s.Context)
	}

	view.List = s.Criteria.Accountant

	scope := types.OrgScope(id)

	ex, err := a.cfg.Ledger.Explore(ctx, me.ID, scope, s.Criteria)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	// The other tab's count is the same search with the other list. A
	// whole organization's search takes about a second at fifty thousand
	// transactions (docs/explorer.md), so this page reads twice where the
	// organization's own page, opened far more often, reads no count at all.
	other := s.Criteria
	other.Skip, other.Accountant = 0, ledgerbus.ContextAdded

	if view.List == ledgerbus.ContextAdded {
		other.Accountant = ledgerbus.MoreContextNeeded
	}

	counted, err := a.cfg.Ledger.Explore(ctx, me.ID, scope, other)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.Needed, view.Added = ex.Total, counted.Total
	if view.List == ledgerbus.ContextAdded {
		view.Needed, view.Added = counted.Total, ex.Total
	}

	if err := a.fill(ctx, me.ID, &view.foundView, ex); err != nil {
		a.failed(w, r, err)

		return
	}

	if view.Asking {
		open, err := a.cfg.Questions.InOrg(ctx, me.ID, id, questionbus.Open, types.ID{})
		if err != nil {
			a.failed(w, r, err)

			return
		}

		view.OpenQuestions = len(open)
	}

	for _, p := range s.Kept() {
		if p.Key != "context" {
			view.Tabs = append(view.Tabs, p)
		}
	}

	month := a.cfg.Now()
	for range shownMonths {
		view.Months = append(view.Months, month.Format("2006-01"))
		month = time.Date(month.Year(), month.Month()-1, 1, 0, 0, 0, 0, time.UTC)
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "accountant", view)
}

// accountantQuestions is the third tab: the organization's questions,
// open or answered, everybody's or the reader's own, and the two lists'
// counts for their tabs.
func (a app) accountantQuestions(w http.ResponseWriter, r *http.Request, me, id types.ID, view accountantView) {
	ctx := r.Context()
	q := r.URL.Query()

	view.List, view.State, view.Mine = questionsTab, questionbus.Open, q.Get("mine") == "1"
	if q.Get("state") == string(questionbus.Answered) {
		view.State = questionbus.Answered
	}

	for _, c := range []struct {
		list ledgerbus.ForAccountant
		into *int
	}{{ledgerbus.MoreContextNeeded, &view.Needed}, {ledgerbus.ContextAdded, &view.Added}} {
		ex, err := a.cfg.Ledger.Explore(ctx, me, types.OrgScope(id), ledgerbus.Criteria{Accountant: c.list, Limit: 1})
		if err != nil {
			a.failed(w, r, err)

			return
		}

		*c.into = ex.Total
	}

	open, err := a.cfg.Questions.InOrg(ctx, me, id, questionbus.Open, types.ID{})
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.OpenQuestions = len(open)

	var by types.ID
	if view.Mine {
		by = me
	}

	listed := open
	if view.State != questionbus.Open || view.Mine {
		if listed, err = a.cfg.Questions.InOrg(ctx, me, id, view.State, by); err != nil {
			a.failed(w, r, err)

			return
		}
	}

	names := map[types.ID]string{}

	for _, l := range listed {
		name, ok := names[l.AskedBy]
		if !ok {
			u, err := a.cfg.Users.ByID(ctx, l.AskedBy)
			if err != nil {
				a.failed(w, r, err)

				return
			}

			name = u.Named()
			names[l.AskedBy] = name
		}

		view.Asked = append(view.Asked, asked{Listed: l, Asker: name})
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "accountant", view)
}
