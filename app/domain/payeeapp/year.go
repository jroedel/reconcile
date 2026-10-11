package payeeapp

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
)

// Reported payees and the year (docs/payees.md, 3 and 4): the marks a
// bookkeeper or an owner sets on a payee's page, and a list's payments by
// payee for a calendar year, on a page and as a spreadsheet. The counting
// is ledgerbus's (payees_year.go); this is how it reads.

// Receipts is what is attached to the payments, for the images of checks.
type Receipts interface {
	OnTransactions(ctx context.Context, ids []types.ID) (map[types.ID][]receiptbus.Receipt, error)
}

// yearColumns is how many columns the year's spreadsheet has; its header,
// text/payee-year-columns.txt, says them in this order.
const yearColumns = 13

// yearView is a list's year.
type yearView struct {
	ledgerbus.PaymentsByPayee

	OwnerName, OwnerPath, ListPath string

	// Path is this page, Prev and Next the years beside it.
	Path       string
	Prev, Next int

	// Checks is the images of checks on each payment, by transaction.
	Checks map[types.ID][]types.ID
}

// year reads the year in a path, answering 404 itself for one that is not.
func (a app) year(w http.ResponseWriter, r *http.Request) (int, bool) {
	y, err := strconv.Atoi(r.PathValue("year"))
	if _, _, spanErr := ledgerbus.YearSpan(y); err != nil || spanErr != nil {
		a.cfg.Render.Render(w, r, http.StatusNotFound, "payees-missing", nil)

		return 0, false
	}

	return y, true
}

// paymentsByPayee is the year of the list a path names, or false with the
// answer written.
func (a app) paymentsByPayee(w http.ResponseWriter, r *http.Request, kind types.ScopeKind) (yearView, bool) {
	me, ok := actor(w, r)
	if !ok {
		return yearView{}, false
	}

	owner, name, ok := a.ownerOf(w, r, me, kind)
	if !ok {
		return yearView{}, false
	}

	y, ok := a.year(w, r)
	if !ok {
		return yearView{}, false
	}

	got, err := a.cfg.Ledger.PaymentsByPayee(r.Context(), me.ID, owner, y)
	if err != nil {
		a.failed(w, r, err)

		return yearView{}, false
	}

	view := yearView{
		PaymentsByPayee: got, OwnerName: name, OwnerPath: ownerPath(owner), ListPath: pathOf(owner),
		Path: pathOf(owner) + "/" + strconv.Itoa(y), Prev: y - 1, Next: y + 1, Checks: map[types.ID][]types.ID{},
	}

	var ids []types.ID

	for _, ys := range [][]ledgerbus.PayeeYear{got.Reported, got.Others} {
		for _, py := range ys {
			for _, p := range py.Payments {
				ids = append(ids, p.ID)
			}
		}
	}

	on, err := a.cfg.Receipts.OnTransactions(r.Context(), ids)
	if err != nil {
		a.failed(w, r, err)

		return yearView{}, false
	}

	for id, list := range on {
		for _, rc := range list {
			if rc.CheckImage {
				view.Checks[id] = append(view.Checks[id], rc.ID)
			}
		}
	}

	return view, true
}

func (a app) yearPage(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view, ok := a.paymentsByPayee(w, r, kind)
		if !ok {
			return
		}

		a.cfg.Render.Render(w, r, http.StatusOK, "payees-year", view)
	}
}

// yearDownload is the same year as a spreadsheet: a row per payment, the
// reported payees first, with a byte-order mark so that a spreadsheet
// reads its accents, as the accountant's package does.
func (a app) yearDownload(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view, ok := a.paymentsByPayee(w, r, kind)
		if !ok {
			return
		}

		lang := mid.LangFrom(r.Context())

		header, err := a.lines(lang, "payee-year-columns", yearColumns)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		words, err := a.lines(lang, "payee-year-words", 3)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		yes, written, posted := words[0], words[1], words[2]

		var buf bytes.Buffer

		buf.WriteString("\ufeff")

		cw := csv.NewWriter(&buf)
		cw.UseCRLF = true

		_ = cw.Write(header)

		said := func(b bool) string {
			if b {
				return yes
			}

			return ""
		}

		for _, ys := range [][]ledgerbus.PayeeYear{view.Reported, view.Others} {
			for _, py := range ys {
				for _, p := range py.Payments {
					day := posted
					if p.Written {
						day = written
					}

					_ = cw.Write([]string{
						cell(py.Payee.Name), said(py.Payee.Reported), said(py.Payee.DetailsOnFile),
						p.On.String(), day, cell(p.AccountName), cell(p.Shown()), p.CheckNumber, cell(p.CheckMemo),
						p.Paid.String(), p.Unsorted.String(), p.Currency, cell(p.Payee),
					})
				}
			}
		}

		cw.Flush()

		if err := cw.Error(); err != nil {
			a.failed(w, r, err)

			return
		}

		name := fmt.Sprintf("payees_%d.csv", view.Year)

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Cache-Control", "private, no-store")
		_, _ = w.Write(buf.Bytes())
	}
}

// lines is a text's lines, which must be n.
func (a app) lines(lang types.Lang, name string, n int) ([]string, error) {
	text, err := a.cfg.Render.Text(lang, name, nil)
	if err != nil {
		return nil, err
	}

	var out []string

	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}

	if len(out) != n {
		return nil, fmt.Errorf("%s.txt has %d lines, not %d", name, len(out), n)
	}

	return out, nil
}

// cell keeps a spreadsheet from taking a name for a formula: one that
// starts with =, +, - or @ is read as text, behind a quote, as the
// accountant's package does.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}

	return s
}

// ownerPath is the page of a list's owner.
func ownerPath(owner types.Scope) string {
	if owner.Kind == types.ScopeOrg {
		return "/orgs/" + owner.ID.String()
	}

	return "/accounts/" + owner.ID.String()
}

// marks saves what a person says about a payee for the accountant.
func (a app) marks(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (ledgerbus.Payee, string, error) {
		p, err := a.cfg.Ledger.SetPayeeMarks(r.Context(), a.cfg.Now(), me.ID, id, ledgerbus.PayeeMarks{
			Reported:      r.PostForm.Get("reported") == "1",
			DetailsOnFile: r.PostForm.Get("details") == "1",
			Note:          r.PostForm.Get("note"),
		})

		return p, "marked", err
	})
}
