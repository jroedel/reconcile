package ledgerapp

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/types"
)

// --- "Sort this month" --------------------------------------------------------

// sortRow is one row of "Sort this month" as the form shows it: the
// transaction, its guess, and the choices -- the guess's, or what the
// person typed when the page comes back with a problem.
type sortRow struct {
	ledgerbus.SortRow

	CategoryID string
	ProjectID  string
	Always     bool
	Match      string

	// Problem is why this row was not saved, as a code.
	Problem string
}

type monthView struct {
	Sort ledgerbus.MonthToSort
	Path string // the account's page
	Rows []sortRow

	// Saved is how many the last save sorted; NoRule is the descriptions
	// whose "always" box made no rule, though the row was saved.
	Saved  int
	NoRule []string
	Done   string
}

// typedRow is what was posted for one row.
type typedRow struct {
	CategoryID, ProjectID string
	Always                bool
	Match                 string
}

func (a app) sortMonth(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))

	a.monthPage(w, r, http.StatusOK, monthView{Saved: n, Done: r.URL.Query().Get("done")}, nil, nil)
}

// monthPage shows the month's rows. typed is what was posted for rows
// that come back, and problems why, both by transaction.
func (a app) monthPage(w http.ResponseWriter, r *http.Request, status int, view monthView, typed map[types.ID]typedRow, problems map[types.ID]string) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	m, err := a.cfg.Ledger.ToSort(r.Context(), a.cfg.Now(), me.ID, id, r.URL.Query().Get("month"))
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.Sort = m
	view.Path = "/accounts/" + id.String()

	for _, row := range m.Rows {
		t := row.Transaction

		sr := sortRow{
			SortRow:    row,
			CategoryID: idOrEmpty(row.Guess.CategoryID),
			ProjectID:  idOrEmpty(t.Splits[0].ProjectID),
			Match:      rulebus.Payee(t.Description),
			Problem:    problems[t.ID],
		}

		if row.Guess.Made() {
			sr.ProjectID = idOrEmpty(row.Guess.ProjectID)
		}

		if typed, ok := typed[t.ID]; ok {
			sr.CategoryID, sr.ProjectID, sr.Always, sr.Match = typed.CategoryID, typed.ProjectID, typed.Always, typed.Match
		}

		view.Rows = append(view.Rows, sr)
	}

	a.cfg.Render.Render(w, r, status, "sort-month", view)
}

// saveMonth is "Sort this month"'s one Save: every row with a choice,
// then the rules the "always" boxes asked for.
func (a app) saveMonth(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	ctx := r.Context()
	typed := map[types.ID]typedRow{}

	var (
		choices []ledgerbus.Choice
		order   []types.ID
	)

	for i := range ledgerbus.MaxSortRows {
		n := strconv.Itoa(i)

		tx, err := types.ParseID(r.PostForm.Get("tx-" + n))
		if err != nil {
			break
		}

		row := typedRow{
			CategoryID: r.PostForm.Get("category-" + n),
			ProjectID:  r.PostForm.Get("project-" + n),
			Always:     r.PostForm.Get("always-"+n) == "1",
			Match:      strings.TrimSpace(r.PostForm.Get("match-" + n)),
		}

		// An ID that does not parse is no choice at all, as on the
		// transaction's page.
		category, _ := types.ParseID(row.CategoryID)
		project, _ := types.ParseID(row.ProjectID)

		typed[tx] = row
		order = append(order, tx)
		choices = append(choices, ledgerbus.Choice{TransactionID: tx, CategoryID: category, ProjectID: project})
	}

	saved, refused, err := a.cfg.Ledger.SortMany(ctx, a.cfg.Now(), me.ID, id, choices)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	problems := map[types.ID]string{}

	for tx, err := range refused {
		problems[tx] = "locked"

		if invalid, ok := errors.AsType[ledgerbus.Invalid](err); ok {
			problems[tx] = invalid.Field
		}
	}

	// The rules after the rows, so that a rule that will not do costs
	// nobody the sorting: the row is saved, and the page names it.
	var noRule []string

	for i, tx := range order {
		row, c := typed[tx], choices[i]
		if !row.Always || refused[tx] != nil || (c.CategoryID.Zero() && c.ProjectID.Zero()) {
			continue
		}

		t, err := a.cfg.Ledger.Lookup(ctx, tx)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		direction := rulebus.In
		if t.Amount < 0 {
			direction = rulebus.Out
		}

		_, err = a.cfg.Rules.Save(ctx, a.cfg.Now(), me.ID, id, rulebus.Fields{
			Match: row.Match, Direction: direction, CategoryID: c.CategoryID, ProjectID: c.ProjectID,
		})
		if _, invalid := errors.AsType[rulebus.Invalid](err); invalid {
			noRule = append(noRule, t.Description)

			continue
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}
	}

	if len(problems) > 0 || len(noRule) > 0 {
		a.monthPage(w, r, http.StatusUnprocessableEntity, monthView{Saved: saved, NoRule: noRule}, typed, problems)

		return
	}

	back(w, r, "/accounts/"+id.String()+"/sort?month="+url.QueryEscape(r.URL.Query().Get("month"))+"&n="+strconv.Itoa(saved), "sorted")
}
