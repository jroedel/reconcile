package apiapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/web"
)

// Sending a receipt (docs/books-api.md, "Receipts from a person's
// computer"): a program on the person's own computer -- their Claude Code,
// looking through folders for what justifies each charge -- sends the file
// it found, into an inbox or onto the charge.
//
// It is a route and not a tool. A tool's arguments pass through the
// conversation, and a photo's bytes in base64 are too many for it and come
// out mangled, which is why Gmail's statements come through a script
// rather than through Claude (the inbox, inbox.go). A program with a shell
// has no such problem: curl sends the file from the disk as the body, and
// the answer, which is small, is what the conversation sees.

// ReceiptPatterns are the routes whose body is a receipt's file rather
// than JSON: the muxer gives them a branch of their own, as it does the
// statement inbox's.
var ReceiptPatterns = []string{"POST " + Prefix + "/receipts"}

// MaxReceipt is the most a receipt request's body may be: one file, and
// nothing else rides with it.
const MaxReceipt = receiptbus.MaxFile + 64<<10

// Uploads is where a sent file's bytes are kept (filebus), streamed to
// disk and never read whole.
type Uploads interface {
	Save(ctx context.Context, now time.Time, actor types.ID, name string, r io.Reader, limit int64, accept []string) (filebus.File, error)
}

func (a app) receiptEndpoints() []Endpoint {
	if !a.books.complete() {
		return nil
	}

	return []Endpoint{{
		Method: http.MethodPost, Path: Prefix + "/receipts", Scope: userbus.BooksWrite,
		Summary: "Send a receipt's file, from a program that has it on disk, such as curl: into an account's or a project's inbox, where it waits to be matched, or onto a transaction it justifies, attached already. " +
			"Give exactly one of account, project or transaction. The body is the file itself -- a JPEG, PNG, WebP, HEIC or PDF, at most 20 MB -- not JSON. " +
			"Give the date, amount and shop when you can read them: list_waiting_receipts suggests the transaction for a receipt by its amount and date. " +
			"The same file sent again is recognised by its content and adds nothing: the answer is the receipt it made the first time. " +
			"There is no tool for it, since a file cannot pass through a conversation; Claude on claude.ai cannot send one, and the person can share it to the site from their phone instead.",
		Query: []Field{
			{Name: "account", Type: "string", Description: "An account whose inbox the receipt waits in, to be matched later."},
			{Name: "project", Type: "string", Description: "A project whose inbox the receipt waits in, to be matched later."},
			{Name: "transaction", Type: "string", Description: "A transaction the receipt justifies, when the person has agreed which: it is attached at once, and its inbox is the transaction's account's."},
			{Name: "name", Type: "string", Required: true, Description: "The file's name, as it was on the disk."},
			{Name: "spent_on", Type: "string", Description: "The day on the receipt, as YYYY-MM-DD."},
			{Name: "amount", Type: "string", Description: "What the receipt says was paid, without a sign, such as \"45.12\"."},
			{Name: "merchant", Type: "string", Description: fmt.Sprintf("The shop or payee, at most %d characters.", receiptbus.MaxMerchant)},
			{Name: "note", Type: "string", Description: fmt.Sprintf("What it was for, in the person's words, at most %d characters.", receiptbus.MaxNote)},
		},
		Body: &Body{Encoding: "file", Fields: []Field{}},
		Returns: `{"outcome": "received" | "already_there", "receipt"}: received (201) when it is new; already_there when a receipt with the same file is on the transaction or waiting in the inbox, which is the receipt answered. ` +
			"The receipt's attached_to lists the transactions it is on; one already_there and waiting can be attached with attach_receipt.",
		handler: a.sendReceipt,
	}}
}

func (a app) sendReceipt(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	name := q.Get("name")
	if name == "" {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("name", "Give the file's name as ?name=, such as receipt.jpg."))

		return
	}

	// Where it goes is asked before the body is read, so that a mistake
	// in the address keeps nothing.
	var where [3]types.ID

	for i, field := range []string{"account", "project", "transaction"} {
		id, ok := a.optionalID(w, q.Get(field), field)
		if !ok {
			return
		}

		where[i] = id
	}

	account, project, transaction := where[0], where[1], where[2]

	given := 0
	for _, id := range where {
		if !id.Zero() {
			given++
		}
	}

	if given != 1 {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", "Give exactly one of account, project or transaction: an inbox for the receipt to wait in, or the transaction it justifies."))

		return
	}

	d, ok := a.receiptDetails(w, r)
	if !ok {
		return
	}

	rd := a.reader(r)
	ctx := r.Context()
	now := time.Now()

	f, err := a.books.Uploads.Save(ctx, now, rd.me, name, r.Body, receiptbus.MaxFile, receiptbus.Accept)

	_, overLimit := errors.AsType[*http.MaxBytesError](err)

	switch {
	case overLimit, errors.Is(err, filebus.ErrTooBig):
		web.WriteJSON(w, http.StatusRequestEntityTooLarge, web.Problem("", "That file is larger than a receipt may be, 20 MB. Make the photo smaller, or the PDF's pages fewer, and send it again."))

		return
	case errors.Is(err, filebus.ErrType):
		web.WriteJSON(w, http.StatusUnsupportedMediaType, web.Problem("", "A receipt is a JPEG, PNG, WebP or HEIC photo, or a PDF, and this file is none of them, by its content. Nothing was kept. Send the file itself as the body, not its name or a form."))

		return
	case err != nil:
		a.fail(w, r, "keeping a receipt's file", err)

		return
	}

	home := types.AccountScope(account)
	if !project.Zero() {
		home = types.ProjectScope(project)
	}

	var (
		rc   receiptbus.Receipt
		held bool
	)

	if transaction.Zero() {
		rc, held, err = a.books.Receipts.Holding(ctx, rd.me, home, f.ID)
	} else {
		rc, held, err = a.books.Receipts.HoldingFor(ctx, rd.me, transaction, f.ID)
	}

	if a.receiptRefused(w, r, err) {
		return
	}

	status, outcome := http.StatusOK, "already_there"

	if !held {
		var made []receiptbus.Receipt

		if transaction.Zero() {
			made, err = a.books.Receipts.Add(ctx, now, rd.me, home, []types.ID{f.ID}, true, d)
		} else {
			made, err = a.books.Receipts.AddToTransaction(ctx, now, rd.me, transaction, []types.ID{f.ID}, true, d)
		}

		if a.receiptRefused(w, r, err) {
			return
		}

		rc, status, outcome = made[0], http.StatusCreated, "received"
	}

	out := rd.receipts([]receiptbus.Receipt{rc})[0]

	attached := make([]map[string]string, 0, len(rc.Links))
	for _, l := range rc.Links {
		attached = append(attached, map[string]string{"id": l.TransactionID.String(), "url": rd.url("/transactions/" + l.TransactionID.String())})
	}

	out["attached_to"] = attached

	if rd.failed != nil {
		a.fail(w, r, "answering for a receipt", rd.failed)

		return
	}

	web.WriteJSON(w, status, map[string]any{"outcome": outcome, "receipt": out})
}

// receiptDetails reads what is known about a receipt from the query, and
// answers 400 itself for what will not do: before the file is kept, so
// that a refusal keeps nothing.
func (a app) receiptDetails(w http.ResponseWriter, r *http.Request) (receiptbus.Details, bool) {
	q := r.URL.Query()
	d := receiptbus.Details{Merchant: q.Get("merchant"), Note: q.Get("note")}

	refuse := func(field, problem string) (receiptbus.Details, bool) {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem(field, problem))

		return receiptbus.Details{}, false
	}

	if s := q.Get("spent_on"); s != "" {
		on, err := types.ParseDate(s)
		if err != nil {
			return refuse("spent_on", "Give the day on the receipt as YYYY-MM-DD, or leave it out.")
		}

		d.SpentOn = on
	}

	if s := q.Get("amount"); s != "" {
		amt, err := money.Parse(s)
		if err != nil || amt < 0 {
			return refuse("amount", "Give what was paid as a number without a sign, such as \"45.12\", or leave it out.")
		}

		d.Amount, d.HasAmount = amt, true
	}

	switch {
	case utf8.RuneCountInString(d.Merchant) > receiptbus.MaxMerchant:
		return refuse("merchant", fmt.Sprintf("The merchant may be at most %d characters.", receiptbus.MaxMerchant))
	case utf8.RuneCountInString(d.Note) > receiptbus.MaxNote:
		return refuse("note", fmt.Sprintf("The note may be at most %d characters.", receiptbus.MaxNote))
	}

	return d, true
}

// receiptRefused is bookRefused for sending a receipt, whose refusal for a
// role is a different one: adding receipts is a contributor's, not only a
// bookkeeper's.
func (a app) receiptRefused(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, receiptbus.ErrForbidden) {
		web.WriteJSON(w, http.StatusForbidden, web.Problem("", "The person's role there does not allow adding receipts. Nothing was added; tell them, and somebody who keeps those books can give them a role that does."))

		return true
	}

	return a.bookRefused(w, r, err, "account, project or transaction")
}
