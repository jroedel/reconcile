package apiapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/web"
)

// Keeping the books (docs/books-api.md, "Keeping the books"): the writes a
// person's Claude may make as them, behind books:write. Each is a method
// the web already calls -- the bulk import, "Sort this month", the split
// editor, the rules page, a receipt's match, an explanation's lines -- so
// nothing here decides anything a page does not; the business layer asks
// tenancybus about the person every time, as for a page.
//
// What is not here is as deliberate as what is: reconciling or reopening
// a period, removing a statement or a receipt, accepting an explanation's
// difference, and entries by hand are the person's, on the web. A test
// lists every write the API has (method and path), so a new one arrives on
// purpose, in a pull request that says so.
//
// Everything written through a key says so (eventbus.ViaFrom, which
// mid.APIKey puts in the request's context): the history names the key,
// a part sorted here says "by" it on the month list until a person saves
// the transaction on the web, and so does a rule on the rules page.

// Rules is the slice of rulebus the books:write endpoints use.
type Rules interface {
	Save(ctx context.Context, now time.Time, actor, accountID types.ID, f rulebus.Fields) (rulebus.Rule, error)
	Change(ctx context.Context, now time.Time, actor, id types.ID, f rulebus.Fields) (rulebus.Rule, error)
	Remove(ctx context.Context, now time.Time, actor, id types.ID) (rulebus.Rule, error)
}

// keepEndpoints is the books:write endpoints.
func (a app) keepEndpoints() []Endpoint {
	if !a.books.complete() {
		return nil
	}

	write := userbus.BooksWrite
	idField := func(name, what string) Field {
		return Field{Name: name, Type: "string", Description: "The " + what + "'s id, as other answers give it."}
	}

	ruleFields := []Field{
		{Name: "match", Type: "string", Required: true, Description: fmt.Sprintf("The text to look for in the description, %d to %d characters, without case.", rulebus.MinMatch, rulebus.MaxMatch)},
		{Name: "direction", Type: "string", Values: []string{"out", "in", "any"}, Description: "Money out, money in, or either (any, the default)."},
		idField("category", "category"),
		idField("project", "project"),
	}

	return []Endpoint{
		{
			Method: http.MethodPost, Path: Prefix + "/inbox/import", Scope: write, Tool: "import_from_inbox",
			Summary: "Import statements waiting in your inbox, as the bulk import on " + a.base + "/imports does. Only what needs no person is imported: a part that checks against its own figures, with nothing set aside, nothing skipped and no row waiting to be told whose it is. The rest stays waiting, and the answer says what each needs. A file leaves the inbox once every part of it is imported.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "files", Type: "array of string", Description: "The files to import, by the file id list_inbox gives; every file waiting when left out."},
				{Name: "accounts", Type: "array of object {file, part, account}", Description: "An account for a file, or for one part of a file, that list_inbox proposed none for, or proposed the wrong one: the file's id, the part as list_inbox gives it (empty for a file of one), and the account. Only an account you keep the books of."},
			}},
			Returns: "{imported, files: [{file, name, parts: [{part, account, account_name, outcome: imported|already_imported|waiting, needs, page}]}], imports_url}",
			handler: a.importInbox,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/accounts/{account}/sort", Scope: write, Tool: "sort_transactions",
			Summary: "Sort transactions of an account, each whole into a category, a project, or both, as \"Sort this month\" does. Only a transaction not sorted yet, in one part, outside a reconciled period, is changed; one somebody sorted or split is left as they did it. Each is marked as sorted through this key until a person saves it on the web. Bookkeepers and owners only.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "choices", Type: "array of object {transaction, category, project}", Required: true, Description: fmt.Sprintf("At most %d. The ids get_month_to_sort gives: its rows' transactions, and its categories and projects.", ledgerbus.MaxSortRows)},
			}},
			Returns: "{sorted, left: [{transaction, why}], refused: [{transaction, problem}], url}: left is those not changed because somebody sorted or split them already; a choice that is what is stored already is in neither.",
			handler: a.sortTransactions,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/transactions/{transaction}/splits", Scope: write, Tool: "set_splits",
			Summary: "Replace a transaction's parts: one charge across categories and projects, as its page does. The parts add up to the whole, each the same way round as it (money out split into money out), none zero. Marked as sorted through this key until a person saves it on the web. Not in a reconciled period.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "parts", Type: "array of object {amount, category, project, memo}", Required: true, Description: fmt.Sprintf("1 to %d. amount with its sign, as the transaction's; category and project ids, either or both; memo optional, at most %d characters.", ledgerbus.MaxParts, ledgerbus.MaxMemo)},
			}},
			Returns: "{transaction}", handler: a.setSplits,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/transactions/{transaction}/description", Scope: write, Tool: "set_description",
			Summary: "Say what a transaction was, in words a person reads in place of the bank's -- \"Summer work\" for \"Check 1322\", from the check's memo line -- or take it away with an empty description. The bank's description is kept, and is still what rules look for. Marked as written through this key until a person saves the transaction on the web. Bookkeepers and owners only; not in a reconciled period.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "description", Type: "string", Required: true, Description: fmt.Sprintf("At most %d characters; empty takes it away.", ledgerbus.MaxDescription)},
			}},
			Returns: "{transaction}", handler: a.setDescription,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/accounts/{account}/rules", Scope: write, Tool: "save_rule",
			Summary: "Save a sorting rule on an account: what a charge with this text is sorted into from now on. The account's rule with the same text is corrected rather than doubled. It changes no transaction until apply_rules or the next import. try_rule first, always. Marked as written through this key until a person saves it on the rules page. Bookkeepers and owners only.",
			Body:    &Body{Encoding: "json", Fields: ruleFields},
			Returns: "{rule}", handler: a.saveRule,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/rules/{rule}", Scope: write, Tool: "change_rule",
			Summary: "Rewrite a sorting rule. What it sorted already stays as it is.",
			Body:    &Body{Encoding: "json", Fields: ruleFields},
			Returns: "{rule}", handler: a.changeRule,
		},
		{
			Method: http.MethodDelete, Path: Prefix + "/rules/{rule}", Scope: write, Tool: "remove_rule",
			Summary: "Remove a sorting rule, such as one written wrongly. What it sorted stays sorted, still marked as sorted by a rule, until a person saves it.",
			Returns: "{removed: rule}", handler: a.removeRule,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/accounts/{account}/rules/apply", Scope: write, Tool: "apply_rules",
			Summary: "Apply an account's sorting rules to every transaction of it not sorted yet, outside a reconciled period, as the rules page's button does. Each it sorts is marked as sorted by its rule. Bookkeepers and owners only.",
			Body:    &Body{Encoding: "json", Fields: []Field{}},
			Returns: "{sorted, url}: url lists what rules sorted, for the person to check.",
			handler: a.applyRules,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/receipts/{receipt}/attach", Scope: write, Tool: "attach_receipt",
			Summary: "Attach a waiting receipt to a transaction it belongs to. list_waiting_receipts suggests the transactions; a receipt may be on more than one.",
			Body:    &Body{Encoding: "json", Fields: []Field{{Name: "transaction", Type: "string", Required: true, Description: "The transaction's id."}}},
			Returns: "{receipt, transaction}", handler: a.attachReceipt,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/receipts/{receipt}/detach", Scope: write, Tool: "detach_receipt",
			Summary: "Take a receipt off a transaction, such as one attached wrongly. Nothing is removed: on no other transaction, it waits in its inbox again.",
			Body:    &Body{Encoding: "json", Fields: []Field{{Name: "transaction", Type: "string", Required: true, Description: "The transaction's id."}}},
			Returns: "{receipt, transaction}", handler: a.detachReceipt,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/receipts/{receipt}/check", Scope: write, Tool: "read_check",
			Summary: "Say what the image of a check says, after looking at it with get_receipt_image: its number, whom it was paid to, its amount, its date and its memo. It is attached to the account's transaction with that number when the bank's amount for it is the amount you read; when the amounts differ nothing changes, and the answer says both, so look again. A check that has not cleared waits with what you read, and is attached when the statement that lists it is imported, if the amounts agree. Only for a waiting check image (list_waiting_receipts, check_image).",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "number", Type: "string", Required: true, Description: "The check's number, as printed at its top right and again in the line of digits at its foot."},
				{Name: "amount", Type: "string", Required: true, Description: "The amount in figures, such as \"120.00\": what it was written for, never negative. Where the figures and the words disagree, the words are what the bank pays."},
				{Name: "payee", Type: "string", Description: fmt.Sprintf("Whom it is paid to, as written on the \"Pay to the order of\" line; at most %d characters. Written on the transaction too.", ledgerbus.MaxPayee)},
				{Name: "date", Type: "string", Description: "The date written on it, YYYY-MM-DD, if it can be read: kept as the day it was written, which may be days before it cleared."},
				{Name: "memo", Type: "string", Description: fmt.Sprintf("What its memo line says it was for (\"Cleaning\", \"Summer work\"), if it has one; at most %d characters. Written on the transaction too.", ledgerbus.MaxCheckMemo)},
			}},
			Returns: "{receipt, attached, transaction}; transaction is the one it was attached to, when it was", handler: a.readCheck,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/transactions/{transaction}/explanation/lines", Scope: write, Tool: "gather_explanation",
			Summary: "Add lines to what explains a transaction's amount, and take others out, then read back the app's sum and difference: the count is the app's, not yours. Only lines list_explanation_candidates offers for the same accounts and months may be added. Explaining changes nothing about a line. With no difference left the transaction is explained; accepting a difference is the person's, on its page, with their note.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "add", Type: "array of string", Description: "Transaction ids to add, from list_explanation_candidates."},
				{Name: "remove", Type: "array of string", Description: "Transaction ids to take out."},
				{Name: "account", Type: "array of string", Description: "The accounts the added lines were offered from; as last time when left out."},
				{Name: "from", Type: "string", Description: "The first month they were offered from, YYYY-MM; with account."},
				{Name: "to", Type: "string", Description: "The last month, YYYY-MM; with account."},
			}},
			Returns: "The explanation, as get_explanation gives it.", handler: a.gatherExplanation,
		},
	}
}

// --- reading what was sent --------------------------------------------------------

// body reads a request's JSON, answering 400 itself when it is not that.
func (a app) body(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := web.ReadJSON(r, v); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", "The body is not the JSON this takes: "+err.Error()+"."))

		return false
	}

	return true
}

// optionalID reads an id that may be empty, answering 400 itself when it
// is something else.
func (a app) optionalID(w http.ResponseWriter, s, name string) (types.ID, bool) {
	if s == "" {
		return types.ID{}, true
	}

	id, err := types.ParseID(s)
	if err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, fmt.Sprintf("%q is not an id. Use the id another answer gave.", s)))

		return types.ID{}, false
	}

	return id, true
}

// idList reads a list of ids.
func (a app) idList(w http.ResponseWriter, list []string, name string) ([]types.ID, bool) {
	out := make([]types.ID, 0, len(list))

	for _, s := range list {
		id, err := types.ParseID(s)
		if err != nil {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, fmt.Sprintf("%q is not an id. Use the ids other answers give.", s)))

			return nil, false
		}

		out = append(out, id)
	}

	return out, true
}

// --- the inbox --------------------------------------------------------------------

func (a app) importInbox(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Files    []string `json:"files"`
		Accounts []struct {
			File    string `json:"file"`
			Part    string `json:"part"`
			Account string `json:"account"`
		} `json:"accounts"`
	}

	if !a.body(w, r, &in) {
		return
	}

	asked, ok := a.idList(w, in.Files, "files")
	if !ok {
		return
	}

	chosen := map[string]types.ID{}

	for _, c := range in.Accounts {
		file, ok := a.optionalID(w, c.File, "accounts")
		if !ok {
			return
		}

		account, ok := a.optionalID(w, c.Account, "accounts")
		if !ok {
			return
		}

		if file.Zero() || account.Zero() {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem("accounts", "Each account choice needs a file and an account, and a part for a file of several."))

			return
		}

		chosen[ledgerbus.Proposal{File: filebus.File{ID: file}, Part: c.Part}.Key()] = account
	}

	rd := a.reader(r)

	inbox, err := a.books.Ledger.Inbox(r.Context(), rd.me)
	if a.bookRefused(w, r, err, "inbox") {
		return
	}

	// Only files in the inbox: an id that is not one there is nothing to
	// import here, the same as one that does not exist.
	var files []types.ID

	names := map[types.ID]string{}

	for _, f := range inbox {
		if len(asked) == 0 || slices.Contains(asked, f.FileID) {
			files = append(files, f.FileID)
			names[f.FileID] = f.Name
		}
	}

	for _, id := range asked {
		if _, ok := names[id]; !ok {
			web.WriteJSON(w, http.StatusNotFound, web.Problem("files", fmt.Sprintf("%s is not a file waiting in your inbox. list_inbox lists them.", id)))

			return
		}
	}

	if len(files) > ledgerbus.MaxBatch {
		files = files[:ledgerbus.MaxBatch]
	}

	var props []ledgerbus.Proposal
	if len(files) > 0 {
		if props, err = a.books.Ledger.Propose(r.Context(), rd.me, files, chosen); a.bookRefused(w, r, err, "file") {
			return
		}
	}

	n, err := a.books.Ledger.ImportProposals(r.Context(), time.Now(), rd.me, props)
	if a.bookRefused(w, r, err, "file") {
		return
	}

	ready := 0

	out := make([]map[string]any, 0, len(files))

	for _, id := range files {
		var parts []map[string]any

		for _, p := range props {
			if p.File.ID != id {
				continue
			}

			part := rd.proposal(p)

			switch {
			case p.Imported():
				part["outcome"] = "already_imported"
			case p.Unattended():
				part["outcome"] = "imported"
				ready++
			default:
				part["outcome"] = "waiting"
			}

			delete(part, "ready")
			delete(part, "imported")

			parts = append(parts, part)
		}

		out = append(out, map[string]any{"file": id.String(), "name": names[id], "parts": parts})
	}

	answer := map[string]any{"imported": n, "files": out, "imports_url": rd.url("/imports")}

	// Imported meanwhile in another tab, say, or refused at the last
	// moment: the import's own page says why, and list_inbox says what
	// is still waiting.
	if n < ready {
		answer["note"] = fmt.Sprintf("%d that were ready could not be imported after all. list_inbox says what is still waiting.", ready-n)
	}

	rd.done(w, r, answer)
}

// --- sorting ----------------------------------------------------------------------

func (a app) sortTransactions(w http.ResponseWriter, r *http.Request) {
	account, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	var in struct {
		Choices []struct {
			Transaction string `json:"transaction"`
			Category    string `json:"category"`
			Project     string `json:"project"`
		} `json:"choices"`
	}

	if !a.body(w, r, &in) {
		return
	}

	if len(in.Choices) == 0 || len(in.Choices) > ledgerbus.MaxSortRows {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("choices", fmt.Sprintf("Send 1 to %d choices, each {transaction, category, project}.", ledgerbus.MaxSortRows)))

		return
	}

	choices := make([]ledgerbus.Choice, 0, len(in.Choices))

	for _, c := range in.Choices {
		var ch ledgerbus.Choice

		if ch.TransactionID, ok = a.optionalID(w, c.Transaction, "choices"); !ok {
			return
		}

		if ch.CategoryID, ok = a.optionalID(w, c.Category, "choices"); !ok {
			return
		}

		if ch.ProjectID, ok = a.optionalID(w, c.Project, "choices"); !ok {
			return
		}

		if ch.TransactionID.Zero() || ch.CategoryID.Zero() && ch.ProjectID.Zero() {
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("choices", "Each choice needs a transaction, and a category, a project or both."))

			return
		}

		choices = append(choices, ch)
	}

	rd := a.reader(r)
	ctx := r.Context()

	saved, refused, err := a.books.Ledger.SortMany(ctx, time.Now(), rd.me, account, choices)
	if a.bookRefused(w, r, err, "account or transaction") {
		return
	}

	// What SortMany left alone without refusing it is what somebody had
	// sorted or split already; said, so that Claude does not report it
	// sorted.
	left := []map[string]any{}
	problems := []map[string]any{}

	for _, c := range choices {
		if err, no := refused[c.TransactionID]; no {
			problems = append(problems, map[string]any{"transaction": c.TransactionID.String(), "problem": refusalOf(err)})

			continue
		}

		e, err := a.books.Ledger.Transaction(ctx, rd.me, c.TransactionID)
		if a.bookRefused(w, r, err, "transaction") {
			return
		}

		t := e.Transaction
		if len(t.Splits) == 1 && t.Splits[0].CategoryID == c.CategoryID && t.Splits[0].ProjectID == c.ProjectID {
			continue
		}

		why := "sorted already"
		if len(t.Splits) > 1 {
			why = "split into parts already"
		}

		left = append(left, map[string]any{"transaction": c.TransactionID.String(), "why": why})
	}

	rd.done(w, r, map[string]any{
		"sorted": saved, "left": left, "refused": problems,
		"url": rd.url("/accounts/" + account.String() + "/transactions"),
	})
}

// refusalOf is why one transaction of several could not be sorted.
func refusalOf(err error) string {
	if part, ok := errors.AsType[ledgerbus.Invalid](err); ok {
		return "That will not do: " + part.Err.Error() + "."
	}

	if errors.Is(err, ledgerbus.ErrLocked) {
		return "It is in a reconciled period, so it cannot change."
	}

	return "It could not be sorted."
}

func (a app) setSplits(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "transaction", "transaction")
	if !ok {
		return
	}

	var in struct {
		Parts []struct {
			Amount   money.Amount `json:"amount"`
			Category string       `json:"category"`
			Project  string       `json:"project"`
			Memo     string       `json:"memo"`
		} `json:"parts"`
	}

	if !a.body(w, r, &in) {
		return
	}

	parts := make([]ledgerbus.Part, 0, len(in.Parts))

	for _, p := range in.Parts {
		part := ledgerbus.Part{Amount: p.Amount, Memo: p.Memo}

		if part.CategoryID, ok = a.optionalID(w, p.Category, "parts"); !ok {
			return
		}

		if part.ProjectID, ok = a.optionalID(w, p.Project, "parts"); !ok {
			return
		}

		parts = append(parts, part)
	}

	rd := a.reader(r)

	t, err := a.books.Ledger.SetSplits(r.Context(), time.Now(), rd.me, id, parts)
	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	rd.done(w, r, map[string]any{"transaction": rd.named(t, rd.account(t.AccountID))})
}

// setDescription is a transaction's own description (ledgerbus.Describe).
// A tool of its own rather than a field of set_splits or
// sort_transactions: those change only a transaction's parts, and
// sort_transactions only one nobody has sorted, while a check's memo
// belongs on a transaction already sorted as much as on one that is not.
func (a app) setDescription(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "transaction", "transaction")
	if !ok {
		return
	}

	var in struct {
		Description *string `json:"description"`
	}

	if !a.body(w, r, &in) {
		return
	}

	if in.Description == nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("description", "Give the description, or an empty one to take it away."))

		return
	}

	rd := a.reader(r)

	t, err := a.books.Ledger.Describe(r.Context(), time.Now(), rd.me, id, *in.Description)
	if errors.Is(err, ledgerbus.ErrDescription) {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("description", fmt.Sprintf("A description is at most %d characters.", ledgerbus.MaxDescription)))

		return
	}

	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	rd.done(w, r, map[string]any{"transaction": rd.named(t, rd.account(t.AccountID))})
}

// --- rules ------------------------------------------------------------------------

// ruleIn reads a rule's fields.
func (a app) ruleIn(w http.ResponseWriter, r *http.Request) (rulebus.Fields, bool) {
	var in struct {
		Match     string `json:"match"`
		Direction string `json:"direction"`
		Category  string `json:"category"`
		Project   string `json:"project"`
	}

	if !a.body(w, r, &in) {
		return rulebus.Fields{}, false
	}

	f := rulebus.Fields{Match: in.Match, Direction: rulebus.Direction(in.Direction)}
	if f.Direction == "" {
		f.Direction = rulebus.Either
	}

	var ok bool

	if f.CategoryID, ok = a.optionalID(w, in.Category, "category"); !ok {
		return f, false
	}

	if f.ProjectID, ok = a.optionalID(w, in.Project, "project"); !ok {
		return f, false
	}

	return f, true
}

func (a app) saveRule(w http.ResponseWriter, r *http.Request) {
	account, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	f, ok := a.ruleIn(w, r)
	if !ok {
		return
	}

	rd := a.reader(r)

	rl, err := a.books.Rules.Save(r.Context(), time.Now(), rd.me, account, f)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	rd.done(w, r, map[string]any{"rule": rd.rule(rl, rd.account(rl.AccountID))})
}

func (a app) changeRule(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "rule", "rule")
	if !ok {
		return
	}

	f, ok := a.ruleIn(w, r)
	if !ok {
		return
	}

	rd := a.reader(r)

	rl, err := a.books.Rules.Change(r.Context(), time.Now(), rd.me, id, f)
	if a.bookRefused(w, r, err, "rule") {
		return
	}

	rd.done(w, r, map[string]any{"rule": rd.rule(rl, rd.account(rl.AccountID))})
}

func (a app) removeRule(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "rule", "rule")
	if !ok {
		return
	}

	rd := a.reader(r)

	rl, err := a.books.Rules.Remove(r.Context(), time.Now(), rd.me, id)
	if a.bookRefused(w, r, err, "rule") {
		return
	}

	out := rd.rule(rl, rd.account(rl.AccountID))
	out.ID, out.URL = rl.ID.String(), ""

	rd.done(w, r, map[string]any{"removed": out})
}

func (a app) applyRules(w http.ResponseWriter, r *http.Request) {
	account, ok := a.pathID(w, r, "account", "account")
	if !ok {
		return
	}

	rd := a.reader(r)

	n, err := a.books.Ledger.SortUnsorted(r.Context(), time.Now(), rd.me, account)
	if a.bookRefused(w, r, err, "account") {
		return
	}

	rd.done(w, r, map[string]any{"sorted": n, "url": rd.url("/accounts/" + account.String() + "/transactions")})
}

// --- receipts ---------------------------------------------------------------------

func (a app) attachReceipt(w http.ResponseWriter, r *http.Request) { a.linkReceipt(w, r, true) }
func (a app) detachReceipt(w http.ResponseWriter, r *http.Request) { a.linkReceipt(w, r, false) }

func (a app) linkReceipt(w http.ResponseWriter, r *http.Request, attach bool) {
	id, ok := a.pathID(w, r, "receipt", "receipt")
	if !ok {
		return
	}

	var in struct {
		Transaction string `json:"transaction"`
	}

	if !a.body(w, r, &in) {
		return
	}

	tx, ok := a.optionalID(w, in.Transaction, "transaction")
	if !ok {
		return
	}

	if tx.Zero() {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("transaction", "Give the transaction's id."))

		return
	}

	rd := a.reader(r)
	ctx := r.Context()

	var (
		rc  receiptbus.Receipt
		err error
	)

	if attach {
		rc, err = a.books.Receipts.Attach(ctx, time.Now(), rd.me, id, tx)
	} else {
		rc, err = a.books.Receipts.Detach(ctx, rd.me, id, tx)
	}

	if a.bookRefused(w, r, err, "receipt or transaction") {
		return
	}

	e, err := a.books.Ledger.Transaction(ctx, rd.me, tx)
	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	on, err := a.books.Receipts.OnTransactions(ctx, []types.ID{tx})
	if err != nil {
		a.fail(w, r, "reading a transaction's receipts", err)

		return
	}

	out := rd.named(e.Transaction, e.Account)
	out.Receipts = new(len(on[tx]))

	rd.done(w, r, map[string]any{"receipt": rd.receipts([]receiptbus.Receipt{rc})[0], "transaction": out})
}

// readCheck is what Claude read off a check's image (receiptbus.ReadCheck).
func (a app) readCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "receipt", "receipt")
	if !ok {
		return
	}

	var in struct {
		Number string        `json:"number"`
		Amount *money.Amount `json:"amount"`
		Payee  string        `json:"payee"`
		Date   string        `json:"date"`
		Memo   string        `json:"memo"`
	}

	if !a.body(w, r, &in) {
		return
	}

	if in.Amount == nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("amount", "Give the amount the check was written for, such as \"120.00\"."))

		return
	}

	rd := receiptbus.CheckReading{Number: in.Number, Payee: in.Payee, Amount: *in.Amount, Memo: in.Memo}

	if in.Date != "" {
		on, err := types.ParseDate(in.Date)
		if err != nil {
			web.WriteJSON(w, http.StatusBadRequest, web.Problem("date", "Give the date as YYYY-MM-DD, or leave it out."))

			return
		}

		rd.On = on
	}

	rdr := a.reader(r)
	ctx := r.Context()

	rc, err := a.books.Receipts.ReadCheck(ctx, time.Now(), rdr.me, id, rd)
	if a.bookRefused(w, r, err, "receipt") {
		return
	}

	out := map[string]any{"receipt": rdr.receipts([]receiptbus.Receipt{rc})[0], "attached": !rc.Waiting()}

	if !rc.Waiting() {
		e, err := a.books.Ledger.Transaction(ctx, rdr.me, rc.Links[0].TransactionID)
		if a.bookRefused(w, r, err, "transaction") {
			return
		}

		out["transaction"] = rdr.named(e.Transaction, e.Account)
	}

	rdr.done(w, r, out)
}

// --- explaining -------------------------------------------------------------------

func (a app) gatherExplanation(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "transaction", "transaction")
	if !ok {
		return
	}

	var in struct {
		Add     []string `json:"add"`
		Remove  []string `json:"remove"`
		Account []string `json:"account"`
		From    string   `json:"from"`
		To      string   `json:"to"`
	}

	if !a.body(w, r, &in) {
		return
	}

	add, ok := a.idList(w, in.Add, "add")
	if !ok {
		return
	}

	remove, ok := a.idList(w, in.Remove, "remove")
	if !ok {
		return
	}

	accounts, ok := a.idList(w, in.Account, "account")
	if !ok {
		return
	}

	if len(accounts) > 0 {
		for name, m := range map[string]string{"from": in.From, "to": in.To} {
			if _, _, err := ledgerbus.MonthRange(m); err != nil {
				web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, "With account, give from and to as YYYY-MM, the months the lines were offered from."))

				return
			}
		}
	}

	rd := a.reader(r)
	ctx := r.Context()

	if err := a.books.Ledger.Gather(ctx, time.Now(), rd.me, id, add, remove, accounts, in.From, in.To); a.bookRefused(w, r, err, "transaction") {
		return
	}

	x, err := a.books.Ledger.Explain(ctx, rd.me, id)
	if a.bookRefused(w, r, err, "transaction") {
		return
	}

	rd.done(w, r, rd.explanation(x))
}
