package receiptbus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// Check images (docs/shapes.md, 3, and docs/phone.md, 1). A check's image
// is a receipt like any other, one check to a receipt with its front and
// back as pages, and different in one way: it says which transaction it
// belongs to. Its number is the check's, and a checking account has one
// transaction with that number, so it is attached to that one as soon as
// both are known -- the one receipt that is matched without a person
// choosing, because nothing is left to choose.
//
// Until then it waits in the account's inbox, still a check image. Its
// number may not be known: a screenshot from a bank's app is named for the
// moment it was taken, and somebody -- a person on its page, or Claude --
// has to read the number off it. Or the number is known and the account
// has no transaction with it yet, because the check has not cleared or its
// statement is not imported; an import that brings it attaches the image
// (MatchChecks). Or the account has several, and a person chooses among
// them, which its suggestions offer. None of these is a mistake, so none
// refuses an upload: an earlier version refused the whole of one when a
// single check in it could not be matched, which was right for one check
// typed at a desk and wrong for fifty shared from a phone.
//
// What the bank never says about a check is to whom it was written. The
// person with its image in hand may say, as its payee, which is written on
// the transaction too (ledgerbus.SetPayee), where sorting rules and
// suggestions read it.

// nameWords is what a check image's file name may say beside the number,
// in English, Spanish and Portuguese: that it is a check, and which side.
// A name with any other word in it -- "IMG_4521", a phone's -- says no
// check number, since its number is the photo's.
var nameWords = regexp.MustCompile(`(?i)^(check|cheque|chk|ck|no|nr|n[º°o]|front|back|f|b|frente|verso|anverso|reverso|side|page|p)$`)

// NumberFromName is the check number a file's name gives -- "1176.jpg",
// "check-1176-front.jpg", "cheque 1176 verso.png" -- or "". A side numbered
// as well ("1176-1", "1176 page 2") gives the first, longer number.
func NumberFromName(name string) string {
	base := strings.TrimSuffix(name, path.Ext(name))

	var numbers []string

	for _, w := range strings.FieldsFunc(base, func(r rune) bool {
		return !('0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || r > 127)
	}) {
		if _, err := strconv.Atoi(w); err == nil {
			numbers = append(numbers, w)

			continue
		}

		if !nameWords.MatchString(w) {
			return ""
		}
	}

	// The check's number, and perhaps a side's: the longest is the check's.
	slices.SortStableFunc(numbers, func(a, b string) int { return len(b) - len(a) })

	if len(numbers) == 0 || len(numbers) > 1 && len(numbers[1]) >= 3 || len(numbers[0]) < 3 {
		return ""
	}

	return importbus.CheckNumber(numbers[0])
}

// AddChecks adds the images of checks to an account. With a number, the
// files are that one check's sides; without, each file's name gives its
// check's number, files with the same number are one check's, and a file
// whose name gives none is a check of its own. Each check is attached to
// the account's one transaction with its number, and the others wait. A
// payee is for one check only.
func (b *Business) AddChecks(ctx context.Context, now time.Time, actor, accountID types.ID, fileIDs []types.ID, number, payee string) ([]Receipt, error) {
	home := types.AccountScope(accountID)

	access, err := b.access.AccessTo(ctx, actor, home)

	switch {
	case err != nil:
		return nil, err
	case !access.Can(tenancybus.Read):
		return nil, ErrNotFound
	case !access.Can(tenancybus.Receipts):
		return nil, ErrForbidden
	}

	payee = strings.Join(strings.Fields(payee), " ")
	if utf8.RuneCountInString(payee) > ledgerbus.MaxPayee {
		return nil, Invalid{Field: "payee", Err: fmt.Errorf("at most %d characters", ledgerbus.MaxPayee)}
	}

	if len(fileIDs) == 0 {
		return nil, Invalid{Field: "files", Err: errors.New("there are no files")}
	}

	given := importbus.CheckNumber(number)
	if strings.TrimSpace(number) != "" && given == "" {
		return nil, Invalid{Field: "number", Err: errors.New("a check number is digits")}
	}

	// The files, by check, in the order their first file came.
	type check struct {
		number string
		files  []filebus.File
	}

	var (
		checks   []*check
		byNumber = map[string]*check{}
	)

	for _, id := range fileIDs {
		f, err := b.files.ByID(ctx, id)
		if errors.Is(err, filebus.ErrNotFound) || err == nil && f.UploadedBy != actor {
			return nil, ErrNotFound
		}

		if err != nil {
			return nil, err
		}

		n := cmp.Or(given, NumberFromName(f.Name))
		if c, ok := byNumber[n]; ok {
			c.files = append(c.files, f)

			continue
		}

		c := &check{number: n, files: []filebus.File{f}}
		checks = append(checks, c)

		if n != "" {
			byNumber[n] = c
		}
	}

	if payee != "" && len(checks) > 1 {
		return nil, Invalid{Field: "payee", Err: errors.New("say whom one check was paid to at a time")}
	}

	var (
		receipts []Receipt
		paid     []types.ID
	)

	for _, c := range checks {
		r := Receipt{
			ID: types.NewID(), Home: home, UploadedBy: actor, CreatedAt: now, CheckImage: true, Check: c.number,
			Details: Details{Merchant: payee}, Files: c.files,
		}

		t, ok, err := b.paidBy(ctx, accountID, c.number)
		if err != nil {
			return nil, err
		}

		if ok {
			r = matched(r, t, actor, now)
			paid = append(paid, t.ID)
		}

		receipts = append(receipts, r)
	}

	ev := eventbus.New(now, actor, home, Added, map[string]string{"count": strconv.Itoa(len(receipts))})

	if err := b.store.Create(ctx, receipts, ev); err != nil {
		return nil, err
	}

	if payee != "" && len(paid) > 0 {
		if _, err := b.ledger.SetPayee(ctx, now, actor, paid[0], payee); err != nil {
			return receipts, err
		}
	}

	return receipts, nil
}

// paidBy is the account's one transaction with the check's number, if it
// has exactly one.
func (b *Business) paidBy(ctx context.Context, accountID types.ID, number string) (ledgerbus.Transaction, bool, error) {
	if number == "" {
		return ledgerbus.Transaction{}, false, nil
	}

	txs, err := b.ledger.WithCheck(ctx, accountID, number)
	if err != nil || len(txs) != 1 {
		return ledgerbus.Transaction{}, false, err
	}

	return txs[0], true, nil
}

// matched is a check image attached to the transaction that paid it, whose
// date and amount it takes: the bank's word on them is better than any
// typed before the check was matched.
func matched(r Receipt, t ledgerbus.Transaction, actor types.ID, now time.Time) Receipt {
	r.SpentOn, r.Amount, r.HasAmount = t.PostedOn, t.Amount.Abs(), true
	r.Links = []Link{{TransactionID: t.ID, LinkedBy: actor, LinkedAt: now}}

	return r
}

// NumberCheck says which check a waiting check image is. It is attached
// to the account's one transaction with the number, and its payee written
// on that transaction, as an upload with the number would have done; with
// no such transaction, or several, it waits with its number. Whoever may
// correct the receipt's details may number it. One already attached is
// ErrAttached: taking it off its transaction first says what is being
// undone.
func (b *Business) NumberCheck(ctx context.Context, now time.Time, actor, id types.ID, number string) (Receipt, error) {
	r, err := b.readable(ctx, actor, id)
	if err != nil {
		return Receipt{}, err
	}

	if ok, err := b.mayEdit(ctx, actor, r); err != nil {
		return Receipt{}, err
	} else if !ok {
		return Receipt{}, ErrForbidden
	}

	switch {
	case !r.CheckImage || r.Home.Kind != types.ScopeAccount:
		return r, Invalid{Field: "number", Err: errors.New("only the image of a check has a check number")}
	case r.Removed():
		return r, ErrRemoved
	case !r.Waiting():
		return r, ErrAttached
	}

	n := importbus.CheckNumber(number)
	if n == "" {
		return r, Invalid{Field: "number", Err: errors.New("a check number is digits")}
	}

	r.Check = n

	return b.saveCheck(ctx, now, actor, r)
}

// MatchChecks attaches the account's waiting check images whose number
// now has exactly one transaction. It is what follows an import
// (ledgerbus.AfterImport), so that a check photographed before it cleared
// finds its transaction when the statement that lists it arrives, as
// though it had been numbered then. Somebody who may not attach receipts
// to the account matches nothing.
func (b *Business) MatchChecks(ctx context.Context, now time.Time, actor, accountID types.ID) error {
	home := types.AccountScope(accountID)

	if ok, err := b.can(ctx, actor, home, tenancybus.Receipts); err != nil || !ok {
		return err
	}

	waiting, err := b.store.InHomes(ctx, []types.Scope{home}, true)
	if err != nil {
		return err
	}

	n := 0

	for _, r := range waiting {
		if !r.CheckImage || r.Check == "" {
			continue
		}

		if _, ok, err := b.paidBy(ctx, accountID, r.Check); err != nil {
			return err
		} else if !ok {
			continue
		}

		if _, err := b.saveCheck(ctx, now, actor, r); err != nil {
			return err
		}

		n++
	}

	if n > 0 {
		b.log.Info("waiting check images were matched", "account_id", accountID.String(), "matched", n)
	}

	return nil
}

// saveCheck writes a waiting check image's number, attaching it to the
// transaction that paid it when the account has exactly one.
func (b *Business) saveCheck(ctx context.Context, now time.Time, actor types.ID, r Receipt) (Receipt, error) {
	t, ok, err := b.paidBy(ctx, r.Home.ID, r.Check)
	if err != nil {
		return r, err
	}

	if ok {
		r = matched(r, t, actor, now)
	}

	if err := b.store.SaveCheck(ctx, r); err != nil {
		return r, err
	}

	if ok && r.Merchant != "" {
		if _, err := b.ledger.SetPayee(ctx, now, actor, t.ID, r.Merchant); err != nil {
			return r, err
		}
	}

	return r, nil
}

// paidTo writes a check image's shop -- to whom the check was written --
// on the transactions it is attached to, when it has changed.
func (b *Business) paidTo(ctx context.Context, now time.Time, actor types.ID, before, after Receipt) error {
	if !after.CheckImage || before.Merchant == after.Merchant {
		return nil
	}

	for _, l := range after.Links {
		if _, err := b.ledger.SetPayee(ctx, now, actor, l.TransactionID, after.Merchant); err != nil {
			return err
		}
	}

	return nil
}
