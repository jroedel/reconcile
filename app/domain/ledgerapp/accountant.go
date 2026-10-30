package ledgerapp

import (
	"net/http"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
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
