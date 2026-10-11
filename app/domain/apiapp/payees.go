package apiapp

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/web"
)

// Payees through a key (docs/payees.md, 5): the tidying a long list of
// spellings needs, done by the person's Claude with the person. Claude
// lists the payees and the suggestions of which may be one, says which it
// would join and why, joins on the person's yes, and splits a spelling back
// off when a join was wrong. Each is the method the payee pages call, with
// the same permissions: reading a list takes a role on its owner, joining
// and splitting a bookkeeper's, and saying whom one payment went to whoever
// may attach receipts to it.
//
// What is not here: adding, renaming and archiving a payee, and saying two
// suggested payees are not the same. Those are a person's on the list's
// page, and Claude says them to the person instead. A payee joined into or
// split through a key, and a payment's payee set through one, are marked
// with the key on the site until a person saves the payee or the payment
// there (ledgerbus/payees.go, "Through a key").

// suggestionPayments is how many of each payee's latest payments a
// suggestion shows, for Claude to say why two are or are not one.
const suggestionPayments = 3

// maxJoin is how many payees one join_payees takes, a long list's worth.
const maxJoin = 50

// payeeEndpoints is the payees' endpoints, read and write.
func (a app) payeeEndpoints() []Endpoint {
	if !a.books.complete() {
		return nil
	}

	read, write := userbus.BooksRead, userbus.BooksWrite
	whose := []Field{
		{Name: "organization", Type: "string", Description: "The organization's id, as get_overview gives it: its list, shared by all its accounts."},
		{Name: "account", Type: "string", Description: "Or an account's id: its organization's list, or, for an account with no organization, its own. One of the two."},
	}

	return []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix + "/payees", Scope: read, Tool: "list_payees",
			Summary: "A list of payees -- whom payments went to -- with every way each has been written (its spellings) and how many payments it has. An organization's accounts share one list; an account with no organization has its own. A check's payee comes from reading its image; any other payment's is chosen with set_payee.",
			Query:   whose,
			Returns: "{organization | account, payees: [payee], archived: [payee], url}; a payee is {id, name, spellings, payments, through, spellings_through, url}: through is the key a program last joined or split it through, until a person saves it on the site, and spellings_through those it brought in.",
			handler: a.listPayees,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/payees/suggestions", Scope: read, Tool: "suggest_payee_joins",
			Summary: "Payees of a list that may be one: their names are the same once capitals, accents, punctuation and a firm's ending (LLC, Inc, Ltda, S.A. and the like) are set aside, and nobody has said they are not. Each with both payees' spellings and their latest payments, so that you can say why they are or are not one. Two people can share a name: propose, and join only on the person's yes.",
			Query:   whose,
			Returns: fmt.Sprintf("{suggestions: [{into: payee, from: payee}], url}; each payee with payments: [{transaction, date, account_name, description, written_as, amount, currency, check, url}], its latest %d. from would be joined into into, the one with more payments.", suggestionPayments),
			handler: a.payeeSuggestions,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/payees/{payee}/join", Scope: write, Tool: "join_payees",
			Summary: "Join payees into this one, as Join does on the payee's page: their spellings and payments become its, and they leave the list. Only payees of one list, and only into one that is not archived. Only on the person's yes, after saying why. Marked as joined through this key until a person saves the payee on the site. Undone a spelling at a time by split_payee. Bookkeepers and owners only.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "from", Type: "array of string", Required: true, Description: fmt.Sprintf("The ids of the payees to join into this one, 1 to %d, as list_payees gives them.", maxJoin)},
			}},
			Returns: "{payee}: the payee joined into, with all its spellings now.",
			handler: a.joinPayees,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/payees/{payee}/split", Scope: write, Tool: "split_payee",
			Summary: "Take one of a payee's spellings, and the payments written that way, back out of it: into a payee of its own -- the one it was joined out of, if it was -- or into another payee of the list. It undoes a join, and tells apart two people with one payee's name. The payee's own name cannot be split off. Marked as split through this key until a person saves the payees on the site. Bookkeepers and owners only.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "spelling", Type: "string", Required: true, Description: "The spelling, as list_payees gives it."},
				{Name: "into", Type: "string", Description: "Another payee's id to put it into; left out, it becomes a payee of its own."},
			}},
			Returns: "{payee, from}: payee is the one the spelling went to, from the one it left.",
			handler: a.splitPayee,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/transactions/{transaction}/payee", Scope: write, Tool: "set_payee",
			Summary: "Say whom a payment went to, as \"Paid to\" on its page: a payee of the list, or a new name, which becomes a payee of the list. What is written is the payee's name, and the payment is that payee's. For a check, read its image with read_check instead, which writes what its \"Pay to the order of\" line says. Marked as written through this key until a person saves it on the site. Whoever may attach receipts to the account may; a reconciled period does not stop it, since it is a note and not money.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "payee", Type: "string", Description: "A payee's id, as list_payees gives it."},
				{Name: "name", Type: "string", Description: fmt.Sprintf("Or a name, at most %d characters: the payee of the list written so, or a new one. Empty says nobody.", ledgerbus.MaxPayee)},
			}},
			Returns: "{transaction, payee}: payee is absent when nobody was said.",
			handler: a.setPayee,
		},
	}
}

// --- what answers are made of -----------------------------------------------

// payeeOut is a payee as the answers give it.
type payeeOut struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Spellings []string `json:"spellings"`
	Payments  int      `json:"payments"`
	Archived  bool     `json:"archived,omitempty"`

	// Through is the key a program last made, joined into or split the
	// payee through, and BroughtThrough the spellings it brought in, until
	// a person saves the payee on the site.
	Through        string   `json:"through,omitempty"`
	BroughtThrough []string `json:"spellings_through,omitempty"`

	// Latest is its latest payments, in a suggestion.
	Latest []paymentOut `json:"payments_latest,omitempty"`

	URL string `json:"url"`
}

// paymentOut is a payment to a payee.
type paymentOut struct {
	Transaction string       `json:"transaction"`
	Date        string       `json:"date"`
	AccountName string       `json:"account_name"`
	Description string       `json:"description"`
	WrittenAs   string       `json:"written_as"`
	Amount      money.Amount `json:"amount"`
	Currency    string       `json:"currency"`
	Check       string       `json:"check,omitempty"`
	URL         string       `json:"url"`
}

func (rd *reader) payeeOut(p ledgerbus.Payee) payeeOut {
	return payeeOut{
		ID: p.ID.String(), Name: p.Name, Spellings: append([]string{}, p.Spellings...), Payments: p.Payments, Archived: p.Archived(),
		Through: p.Via, BroughtThrough: p.BroughtVia, URL: rd.url("/payees/" + p.ID.String()),
	}
}

func (rd *reader) paymentOut(p ledgerbus.PayeePayment) paymentOut {
	return paymentOut{
		Transaction: p.ID.String(), Date: p.PostedOn.String(), AccountName: p.AccountName, Description: p.Shown(),
		WrittenAs: p.Payee, Amount: p.Amount, Currency: p.Currency, Check: p.CheckNumber, URL: rd.url("/transactions/" + p.ID.String()),
	}
}

// listURL is the page of an owner's list.
func (rd *reader) listURL(owner types.Scope) string {
	if owner.Kind == types.ScopeOrg {
		return rd.url("/orgs/" + owner.ID.String() + "/payees")
	}

	return rd.url("/accounts/" + owner.ID.String() + "/payees")
}

// ownerOut names an owner as list_payees does.
func ownerOut(owner types.Scope, answer map[string]any) map[string]any {
	key := "account"
	if owner.Kind == types.ScopeOrg {
		key = "organization"
	}

	answer[key] = owner.ID.String()

	return answer
}

// --- reading ----------------------------------------------------------------------

// payeeOwner reads whose list is asked for: an organization, or an
// account, which is its organization's list or its own. It answers 400 or
// 404 itself.
func (a app) payeeOwner(w http.ResponseWriter, r *http.Request) (types.Scope, bool) {
	q := r.URL.Query()
	org, account := q.Get("organization"), q.Get("account")

	if (org == "") == (account == "") {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("organization", "Give organization or account, one of the two, as get_overview gives them."))

		return types.Scope{}, false
	}

	if org != "" {
		id, ok := a.optionalID(w, org, "organization")

		return types.Scope{Kind: types.ScopeOrg, ID: id}, ok
	}

	id, ok := a.optionalID(w, account, "account")
	if !ok {
		return types.Scope{}, false
	}

	acct, _, err := a.books.Tenancy.Account(r.Context(), a.reader(r).me, id)
	if a.bookRefused(w, r, err, "account") {
		return types.Scope{}, false
	}

	return ledgerbus.PayeeOwner(acct), true
}

func (a app) listPayees(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.payeeOwner(w, r)
	if !ok {
		return
	}

	rd := a.reader(r)

	list, _, err := a.books.Ledger.Payees(r.Context(), rd.me, owner)
	if a.bookRefused(w, r, err, "list of payees") {
		return
	}

	payees, archived := []payeeOut{}, []payeeOut{}

	for _, p := range list {
		if p.Archived() {
			archived = append(archived, rd.payeeOut(p))
		} else {
			payees = append(payees, rd.payeeOut(p))
		}
	}

	rd.done(w, r, ownerOut(owner, map[string]any{"payees": payees, "archived": archived, "url": rd.listURL(owner)}))
}

func (a app) payeeSuggestions(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.payeeOwner(w, r)
	if !ok {
		return
	}

	rd := a.reader(r)
	ctx := r.Context()

	list, err := a.books.Ledger.PayeeSuggestions(ctx, rd.me, owner)
	if a.bookRefused(w, r, err, "list of payees") {
		return
	}

	// A payee is in as many suggestions as its group has others; its
	// payments are read once.
	latest := map[types.ID][]paymentOut{}

	with := func(p ledgerbus.Payee) (payeeOut, error) {
		out := rd.payeeOut(p)

		if got, ok := latest[p.ID]; ok {
			out.Latest = got

			return out, nil
		}

		payments, err := a.books.Ledger.PayeePayments(ctx, rd.me, p.ID, suggestionPayments)
		if err != nil {
			return out, err
		}

		out.Latest = []paymentOut{}
		for _, pp := range payments {
			out.Latest = append(out.Latest, rd.paymentOut(pp))
		}

		latest[p.ID] = out.Latest

		return out, nil
	}

	suggestions := make([]map[string]payeeOut, 0, len(list))

	for _, s := range list {
		into, err := with(s.Into)
		if a.bookRefused(w, r, err, "payee") {
			return
		}

		from, err := with(s.From)
		if a.bookRefused(w, r, err, "payee") {
			return
		}

		suggestions = append(suggestions, map[string]payeeOut{"into": into, "from": from})
	}

	rd.done(w, r, map[string]any{"suggestions": suggestions, "url": rd.listURL(owner)})
}

// --- changing ---------------------------------------------------------------------

// payeeRefused answers a refusal of a change to payees in the API's words,
// and the rest as bookRefused does.
func (a app) payeeRefused(w http.ResponseWriter, r *http.Request, err error, what string) bool {
	switch {
	case errors.Is(err, ledgerbus.ErrJoinSelf):
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("", "A payee cannot be joined into itself. Nothing was changed."))
	case errors.Is(err, ledgerbus.ErrJoinLists):
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("", "Those payees are on two lists: an organization's payees, and an account's with no organization, are its own, even with one name. Nothing was changed."))
	case errors.Is(err, ledgerbus.ErrJoinArchived):
		web.WriteJSON(w, http.StatusConflict, web.Problem("", "That payee is archived. Bringing it back is the person's, on its page; tell them, or join into another. Nothing was changed."))
	case errors.Is(err, ledgerbus.ErrSplitName):
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("spelling", "That spelling is the payee's own name, which cannot be split off. Split off its other spellings instead, or ask the person to rename it on its page first."))
	case errors.Is(err, ledgerbus.ErrPayeeTaken):
		web.WriteJSON(w, http.StatusConflict, web.Problem("", "Another payee of the list is already named or written that way. list_payees shows which; join the two if they are one."))
	case errors.Is(err, ledgerbus.ErrPayee), errors.Is(err, ledgerbus.ErrPayeeName):
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("name", fmt.Sprintf("A payee's name is at most %d characters.", ledgerbus.MaxPayee)))
	default:
		return a.bookRefused(w, r, err, what)
	}

	return true
}

func (a app) joinPayees(w http.ResponseWriter, r *http.Request) {
	into, ok := a.pathID(w, r, "payee", "payee")
	if !ok {
		return
	}

	var in struct {
		From []string `json:"from"`
	}

	if !a.body(w, r, &in) {
		return
	}

	if len(in.From) == 0 || len(in.From) > maxJoin {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("from", fmt.Sprintf("Send 1 to %d payees' ids to join into this one.", maxJoin)))

		return
	}

	from, ok := a.idList(w, in.From, "from")
	if !ok {
		return
	}

	rd := a.reader(r)

	p, err := a.books.Ledger.JoinPayees(r.Context(), time.Now(), rd.me, into, from)
	if a.payeeRefused(w, r, err, "payee") {
		return
	}

	rd.done(w, r, map[string]any{"payee": rd.payeeOut(p)})
}

func (a app) splitPayee(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "payee", "payee")
	if !ok {
		return
	}

	var in struct {
		Spelling string `json:"spelling"`
		Into     string `json:"into"`
	}

	if !a.body(w, r, &in) {
		return
	}

	into, ok := a.optionalID(w, in.Into, "into")
	if !ok {
		return
	}

	rd := a.reader(r)
	ctx := r.Context()

	// Asked first, so that a spelling the payee does not have is said to
	// be that, rather than the payee not being there.
	was, _, err := a.books.Ledger.Payee(ctx, rd.me, id)
	if a.bookRefused(w, r, err, "payee") {
		return
	}

	spelling := strings.Join(strings.Fields(in.Spelling), " ")
	if !slices.ContainsFunc(was.Spellings, func(s string) bool { return strings.EqualFold(s, spelling) }) {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("spelling", fmt.Sprintf("%q is not one of this payee's spellings. list_payees gives them.", in.Spelling)))

		return
	}

	p, err := a.books.Ledger.SplitPayee(ctx, time.Now(), rd.me, id, spelling, into)
	if a.payeeRefused(w, r, err, "payee") {
		return
	}

	from, _, err := a.books.Ledger.Payee(ctx, rd.me, id)
	if a.bookRefused(w, r, err, "payee") {
		return
	}

	rd.done(w, r, map[string]any{"payee": rd.payeeOut(p), "from": rd.payeeOut(from)})
}

func (a app) setPayee(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "transaction", "transaction")
	if !ok {
		return
	}

	var in struct {
		Payee string  `json:"payee"`
		Name  *string `json:"name"`
	}

	if !a.body(w, r, &in) {
		return
	}

	if (in.Payee == "") == (in.Name == nil) {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("payee", "Give payee, a payee's id, or name, one of the two; an empty name says nobody."))

		return
	}

	rd := a.reader(r)
	ctx := r.Context()

	var name string

	if in.Name != nil {
		name = *in.Name
	} else {
		pid, ok := a.optionalID(w, in.Payee, "payee")
		if !ok {
			return
		}

		e, err := a.books.Ledger.Transaction(ctx, rd.me, id)
		if a.bookRefused(w, r, err, "transaction") {
			return
		}

		p, _, err := a.books.Ledger.Payee(ctx, rd.me, pid)
		if a.bookRefused(w, r, err, "payee") {
			return
		}

		switch {
		case p.Joined():
			web.WriteJSON(w, http.StatusConflict, web.Problem("payee", fmt.Sprintf("That payee was joined into another, %s; use that one.", p.JoinedInto)))

			return
		case p.Owner != ledgerbus.PayeeOwner(e.Account):
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("payee", "That payee is on another list than the transaction's account. list_payees with the account gives its list."))

			return
		}

		name = p.Name
	}

	t, err := a.books.Ledger.SetPayee(ctx, time.Now(), rd.me, id, name)
	if errors.Is(err, ledgerbus.ErrForbidden) {
		web.WriteJSON(w, http.StatusForbidden, web.Problem("", "Your role on this account does not allow saying whom its payments went to; it needs one that may attach receipts. Nothing was changed; tell the person."))

		return
	}

	if a.payeeRefused(w, r, err, "transaction") {
		return
	}

	answer := map[string]any{"transaction": rd.named(t, rd.account(t.AccountID))}

	if !t.PayeeID.Zero() {
		p, _, err := a.books.Ledger.Payee(ctx, rd.me, t.PayeeID)
		if errors.Is(err, ledgerbus.ErrNotFound) {
			// A role that may attach receipts to one account need not be
			// one on the organization's list: the payment says whom it went
			// to, and the payee's page is not theirs to open.
			rd.done(w, r, answer)

			return
		}

		if a.bookRefused(w, r, err, "payee") {
			return
		}

		answer["payee"] = rd.payeeOut(p)
	}

	rd.done(w, r, answer)
}
