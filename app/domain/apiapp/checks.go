package apiapp

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/web"
)

// Printed checks (docs/checks.md): what a program on the treasurer's
// computer that prints checks needs, behind the checks scope, which
// books:write includes. The checking accounts it may print for, the
// numbers each has used and whether one is free, payees' names to
// complete as the person types, and sending and voiding the checks it
// prints.
//
// Routes, not tools. Sending is a file, which cannot pass through a
// conversation (receipts.go), and none of the rest is anything Claude on
// claude.ai has a use for that its own tools do not already give it.
//
// A refusal a program must tell apart carries a code beside the sentence
// every refusal has: number_used, amount_differs, cleared, not_checking,
// no_check.

// CheckPatterns are the routes whose body is a check's image rather than
// JSON: the muxer gives them a branch of their own, as it does a
// receipt's.
var CheckPatterns = []string{"POST " + Prefix + "/accounts/{account}/checks"}

// maxPayeesFound is how many payees' names a search for completion gives.
const maxPayeesFound = 20

func (a app) checkEndpoints() []Endpoint {
	if !a.books.complete() {
		return nil
	}

	checks := a.books.Receipts
	s := userbus.Checks

	return []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix + "/checks/accounts", Scope: s,
			Summary: "The checking accounts the person may print checks for: those they may add receipts to, in use and part of the books. Each with its last four digits, never the whole number, and the check numbers it has used -- the highest, and the five highest -- from the checks sent and the bank's statements. " +
				"Propose the number after the highest, and let the person confirm it against the paper: a counter check or a second checkbook is a stray number among the last ones.",
			Returns: `{accounts: [{id, name, last4, currency, organization, highest, last_numbers: ["1004", "1003", …]}]}; organization is its name, "" for an account on its own; highest is "" when the account has used none.`,
			handler: a.checkAccounts(checks),
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/payees", Scope: s,
			Summary: fmt.Sprintf("Payees' names to complete as the person types whom a check is to: at most %d, those whose name or any way it has been written has the text in it. Nothing else about them.", maxPayeesFound),
			Query:   []Field{{Name: "q", Type: "string", Description: "What has been typed; leave it out for the first few."}},
			Returns: "{payees: [{name}]}; empty when the person may not see the account's payees.",
			handler: a.checkPayees,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/accounts/{account}/checks", Scope: s,
			Summary: "Send a check that was printed: the image of it as printed is the body -- a PNG, JPEG or PDF, at most 20 MB, showing the account as XXXXXX and its last four digits, never the whole number -- and what was printed on it is the query. " +
				"It is attached at once to the transaction that paid the number for its amount, when the account has one; otherwise it waits among the checks outstanding until the statement that pays it is imported. " +
				"Sending again what was sent -- the same number, amount, payee and day written -- is safe: the answer is the check it made the first time. " +
				"Send it after it is printed, and keep it to send again when there is no connection.",
			Query: []Field{
				{Name: "number", Type: "string", Required: true, Description: "The check's number."},
				{Name: "amount", Type: "string", Required: true, Description: "Its amount, without a sign, such as \"12.34\"."},
				{Name: "payee", Type: "string", Required: true, Description: fmt.Sprintf("Whom it is to, as printed, at most %d characters.", ledgerbus.MaxPayee)},
				{Name: "written_on", Type: "string", Required: true, Description: "The date printed on it, as YYYY-MM-DD."},
				{Name: "memo", Type: "string", Description: fmt.Sprintf("Its memo line, at most %d characters.", ledgerbus.MaxCheckMemo)},
				{Name: "name", Type: "string", Required: true, Description: "The image file's name, such as check-1005.pdf."},
			},
			Body: &Body{Encoding: "file", Fields: []Field{}},
			Returns: `{outcome: "outstanding" | "attached" | "already_there", check}: 201 for a new check, 200 for already_there. ` +
				`A check is {id, number, amount, payee, written_on, memo, state: "outstanding" | "attached" | "void", attached_to: [{id, url}], url}. ` +
				`Refused with 409 and code number_used and the check that has the number, when it says something else; with 409 and code amount_differs, bank and sent, when the bank paid the number for another amount; with 422 and code not_checking for an account that is not a checking account in use.`,
			handler: a.sendCheck(checks),
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/checks", Scope: s,
			Summary: "The checks the person sent to the account, the newest written first: those outstanding, or all of them, attached and void too. Only their own checks: not the account's other receipts.",
			Query:   []Field{{Name: "state", Type: "string", Values: []string{"outstanding", "all"}, Description: "outstanding, the default, or all."}},
			Returns: "{checks: [check]}",
			handler: a.listSentChecks(checks),
		},
		{
			Method: http.MethodGet, Path: Prefix + "/accounts/{account}/checks/{number}", Scope: s,
			Summary: "Whether a check number is free in the account, before printing on it. Always 200 with a state: free; outstanding, attached or void, with the check that has it; or cleared, when the bank has paid the number and no check here has it, with what the bank paid. " +
				"404 means only that the account is not one this key reaches: never take it for a free number.",
			Returns: `{number, state: "free" | "outstanding" | "attached" | "void" | "cleared", check, cleared: [{amount, date}]}: check when a check has the number, cleared when the bank paid it, usually one and more when a bank's numbers have come round again.`,
			handler: a.checkNumber(checks),
		},
		{
			Method: http.MethodPost, Path: Prefix + "/accounts/{account}/checks/{number}/void", Scope: s,
			Summary: "Void a check that was spoiled or never sent, by its number: it is kept, so the accountant can see why the number was skipped, but it is not outstanding and nothing is matched to it, and the number can be printed again. Voiding one that is void already answers it as it is.",
			Returns: "{check}; 409 with code cleared when the bank has paid it, and 404 with code no_check when no check in the account has the number.",
			handler: a.voidCheck(checks),
		},
	}
}

// --- the answers ------------------------------------------------------------------

func (rd *reader) check(rc receiptbus.Receipt) map[string]any {
	attached := make([]map[string]string, 0, len(rc.Links))
	for _, l := range rc.Links {
		attached = append(attached, map[string]string{"id": l.TransactionID.String(), "url": rd.url("/transactions/" + l.TransactionID.String())})
	}

	m := map[string]any{
		"id": rc.ID.String(), "number": rc.Check, "payee": rc.Merchant, "memo": rc.Memo,
		"state": string(receiptbus.StateOf(rc)), "attached_to": attached, "url": rd.url("/receipts/" + rc.ID.String()),
	}

	if rc.HasAmount {
		m["amount"] = rc.Amount
	}

	if !rc.WrittenOn.Zero() {
		m["written_on"] = rc.WrittenOn.String()
	}

	return m
}

func (rd *reader) checks(list []receiptbus.Receipt) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, rc := range list {
		out = append(out, rd.check(rc))
	}

	return out
}

// coded is a refusal a program tells apart by its code, with more beside
// it.
func coded(w http.ResponseWriter, status int, code, field, problem string, more map[string]any) {
	body := map[string]any{"error": web.ProblemDetail{Field: field, Problem: problem}, "code": code}
	for k, v := range more {
		body[k] = v
	}

	web.WriteJSON(w, status, body)
}

// checkRefused answers a refusal of the check routes, and reports whether
// there was one.
func (a app) checkRefused(w http.ResponseWriter, r *http.Request, rd *reader, err error) bool {
	used, isUsed := errors.AsType[receiptbus.NumberUsed](err)
	differs, isDiffers := errors.AsType[receiptbus.AmountDiffers](err)
	invalid, isInvalid := errors.AsType[receiptbus.Invalid](err)

	switch {
	case err == nil:
		return false
	case isUsed:
		coded(w, http.StatusConflict, "number_used", "number", fmt.Sprintf("Check %s in this account says something else: another amount, payee or day. Nothing was kept. If it is the same check, send what it says; if the paper is numbered wrong, void it.", used.Existing.Check),
			map[string]any{"check": rd.check(used.Existing)})
	case isDiffers:
		coded(w, http.StatusConflict, "amount_differs", "amount", fmt.Sprintf("The bank paid check %s for %s, not %s. Nothing was kept. Check which stock is in the printer, or the amount.", differs.Number, differs.Bank, differs.Read),
			map[string]any{"bank": differs.Bank, "sent": differs.Read})
	case errors.Is(err, receiptbus.ErrNotChecking):
		coded(w, http.StatusUnprocessableEntity, "not_checking", "account", "Checks are drawn on a checking account that is in use: this one is a card, cash, kept for reference only, or archived.", nil)
	case errors.Is(err, receiptbus.ErrNoCheck):
		coded(w, http.StatusNotFound, "no_check", "number", "No check in this account has that number. Nothing was voided.", nil)
	case errors.Is(err, receiptbus.ErrAttached):
		coded(w, http.StatusConflict, "cleared", "number", "That check has cleared: the bank paid it, and it is on its transaction. It cannot be voided.", nil)
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, "The "+invalid.Field+" will not do: "+invalid.Err.Error()+"."))
	case errors.Is(err, receiptbus.ErrForbidden):
		web.WriteJSON(w, http.StatusForbidden, web.Problem("", "The person's role on this account does not let them add checks. Somebody who keeps its books can give them one that does."))
	case errors.Is(err, receiptbus.ErrNotFound), errors.Is(err, tenancybus.ErrNotFound), errors.Is(err, filebus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("account", "There is no such account, or it is not one this key reaches. GET /api/v1/checks/accounts lists those it does."))
	default:
		a.fail(w, r, "answering about checks", err)
	}

	return true
}

// --- the handlers ---------------------------------------------------------------

func (a app) checkAccounts(checks Receipts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rd := a.reader(r)

		ov, err := a.books.Tenancy.Overview(r.Context(), rd.me)
		if err != nil {
			a.fail(w, r, "listing the accounts for checks", err)

			return
		}

		type account struct {
			acct tenancybus.Account
			org  string
		}

		var all []account
		for _, acct := range ov.Accounts {
			all = append(all, account{acct: acct})
		}

		for _, o := range ov.Orgs {
			for _, acct := range o.Accounts {
				all = append(all, account{acct: acct, org: o.Org.Name})
			}
		}

		out := []map[string]any{}

		for _, x := range all {
			if !receiptbus.Printable(x.acct) {
				continue
			}

			ns, err := checks.CheckNumbers(r.Context(), rd.me, x.acct.ID)

			switch {
			case errors.Is(err, receiptbus.ErrForbidden), errors.Is(err, receiptbus.ErrNotFound):
				continue
			case err != nil:
				a.fail(w, r, "reading an account's check numbers", err)

				return
			}

			out = append(out, map[string]any{
				"id": x.acct.ID.String(), "name": x.acct.Name, "last4": x.acct.Last4, "currency": x.acct.Currency,
				"organization": x.org, "highest": ns.Highest, "last_numbers": append([]string{}, ns.Last...),
			})
		}

		web.WriteJSON(w, http.StatusOK, map[string]any{"accounts": out})
	}
}

func (a app) checkPayees(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	rd := a.reader(r)

	acct, access, err := a.books.Tenancy.Account(r.Context(), rd.me, id)
	if err == nil && !access.Can(tenancybus.Receipts) {
		err = receiptbus.ErrForbidden
	}

	if a.checkRefused(w, r, rd, err) {
		return
	}

	names := []map[string]string{}

	list, _, err := a.books.Ledger.Payees(r.Context(), rd.me, ledgerbus.PayeeOwner(acct))

	switch {
	case errors.Is(err, ledgerbus.ErrNotFound), errors.Is(err, ledgerbus.ErrForbidden):
		// A role on the account and not on its organization: no list to
		// complete from, which is no refusal.
	case err != nil:
		a.fail(w, r, "reading payees for a check", err)

		return
	}

	q := strings.ToLower(strings.Join(strings.Fields(r.URL.Query().Get("q")), " "))

	for _, p := range list {
		if len(names) == maxPayeesFound {
			break
		}

		if p.Archived() || q != "" && !slices.ContainsFunc(p.Spellings, func(s string) bool { return strings.Contains(strings.ToLower(s), q) }) {
			continue
		}

		names = append(names, map[string]string{"name": p.Name})
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"payees": names})
}

func (a app) sendCheck(checks Receipts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.pathID(w, r, "account", "account")
		if !ok {
			return
		}

		q := r.URL.Query()

		refuse := func(field, problem string) {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem(field, problem))
		}

		// What was printed is read before the body, so that a mistake in
		// it keeps nothing.
		name := q.Get("name")
		if name == "" {
			refuse("name", "Give the image file's name as ?name=, such as check-1005.pdf.")

			return
		}

		amount, err := money.Parse(q.Get("amount"))
		if err != nil || amount <= 0 {
			refuse("amount", "Give the check's amount as a number without a sign, such as \"12.34\".")

			return
		}

		on, err := types.ParseDate(q.Get("written_on"))
		if err != nil || on.Zero() {
			refuse("written_on", "Give the date printed on the check as YYYY-MM-DD.")

			return
		}

		if strings.TrimSpace(q.Get("payee")) == "" {
			refuse("payee", "Give whom the check is to, as printed on it.")

			return
		}

		rd := a.reader(r)
		reading := receiptbus.CheckReading{Number: q.Get("number"), Amount: amount, Payee: q.Get("payee"), On: on, Memo: q.Get("memo")}

		// The account and the number are asked about before the bytes
		// are kept too: an account the key does not reach, or a number
		// that is no number, keeps nothing.
		if _, err := checks.CheckNumberState(r.Context(), rd.me, id, reading.Number); a.checkRefused(w, r, rd, err) {
			return
		}

		now := time.Now()

		f, err := a.books.Uploads.Save(r.Context(), now, rd.me, name, r.Body, receiptbus.MaxFile, receiptbus.Accept)

		_, overLimit := errors.AsType[*http.MaxBytesError](err)

		switch {
		case overLimit, errors.Is(err, filebus.ErrTooBig):
			web.WriteJSON(w, http.StatusRequestEntityTooLarge, web.Problem("", "That image is larger than a check's may be, 20 MB. Nothing was kept."))

			return
		case errors.Is(err, filebus.ErrType):
			web.WriteJSON(w, http.StatusUnsupportedMediaType, web.Problem("", "A check's image is a PNG, JPEG, WebP or HEIC picture, or a PDF, and this file is none of them, by its content. Nothing was kept. Send the file itself as the body."))

			return
		case err != nil:
			a.fail(w, r, "keeping a check's image", err)

			return
		}

		sent, err := checks.SendCheck(r.Context(), now, rd.me, id, f.ID, reading)
		if a.checkRefused(w, r, rd, err) {
			return
		}

		status, outcome := http.StatusCreated, "outstanding"

		switch {
		case sent.Again:
			status, outcome = http.StatusOK, "already_there"
		case !sent.Check.Waiting():
			outcome = "attached"
		}

		web.WriteJSON(w, status, map[string]any{"outcome": outcome, "check": rd.check(sent.Check)})
	}
}

func (a app) listSentChecks(checks Receipts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.pathID(w, r, "account", "account")
		if !ok {
			return
		}

		state := r.URL.Query().Get("state")
		if state != "" && state != "outstanding" && state != "all" {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem("state", "Give state as outstanding or all, or leave it out for outstanding."))

			return
		}

		rd := a.reader(r)

		list, err := checks.SentChecks(r.Context(), rd.me, id, state == "all")
		if a.checkRefused(w, r, rd, err) {
			return
		}

		web.WriteJSON(w, http.StatusOK, map[string]any{"checks": rd.checks(list)})
	}
}

func (a app) checkNumber(checks Receipts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.pathID(w, r, "account", "account")
		if !ok {
			return
		}

		rd := a.reader(r)

		st, err := checks.CheckNumberState(r.Context(), rd.me, id, r.PathValue("number"))
		if a.checkRefused(w, r, rd, err) {
			return
		}

		out := map[string]any{"number": st.Number, "state": string(st.State)}

		if st.State != receiptbus.NumberFree && st.State != receiptbus.NumberCleared {
			out["check"] = rd.check(st.Check)
		}

		if st.State == receiptbus.NumberCleared {
			cleared := make([]map[string]any, 0, len(st.Cleared))
			for _, t := range st.Cleared {
				cleared = append(cleared, map[string]any{"amount": t.Amount.Abs(), "date": t.PostedOn.String()})
			}

			out["cleared"] = cleared
		}

		web.WriteJSON(w, http.StatusOK, out)
	}
}

func (a app) voidCheck(checks Receipts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.pathID(w, r, "account", "account")
		if !ok {
			return
		}

		rd := a.reader(r)

		rc, err := checks.VoidCheck(r.Context(), time.Now(), rd.me, id, r.PathValue("number"))
		if a.checkRefused(w, r, rd, err) {
			return
		}

		web.WriteJSON(w, http.StatusOK, map[string]any{"check": rd.check(rc)})
	}
}
