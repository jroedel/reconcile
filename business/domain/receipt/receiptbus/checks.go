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

// Check images (docs/shapes.md, 3). A check's image is a receipt like any
// other, one check to a receipt with its front and back as pages, and
// different in one way: it says which transaction it belongs to. Its number
// is the check's, and a checking account has one transaction with that
// number, so it is attached to that one as it is added -- the one receipt
// that is matched without a person choosing, because nothing is left to
// choose. A number the account has no transaction for is refused rather
// than left waiting: the check has not cleared, or its statement is not
// imported yet, and either way the image is better uploaded after.
//
// What the bank never says about a check is to whom it was written. The
// person with its image in hand may say, as its payee, which is written on
// the transaction too (ledgerbus.SetPayee), where sorting rules and
// suggestions read it.

// NoCheck is a check's image whose number no transaction in the account
// has, or several have.
type NoCheck struct {
	Number  string
	Several bool
}

func (e NoCheck) Error() string {
	if e.Several {
		return fmt.Sprintf("several transactions in the account are check %s", e.Number)
	}

	return fmt.Sprintf("no transaction in the account is check %s", e.Number)
}

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

// AddChecks adds the images of checks to an account, each attached to the
// transaction that paid it. With a number, the files are that one check's
// sides; without, each file's name gives its check's number, and files
// with the same number are one check's. A payee is for one check only. If
// any check cannot be matched, nothing is added.
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
	var (
		order  []string
		checks = map[string][]filebus.File{}
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
		if n == "" {
			return nil, Invalid{Field: "number", Err: fmt.Errorf("the name of %q says no check number", f.Name)}
		}

		if _, ok := checks[n]; !ok {
			order = append(order, n)
		}

		checks[n] = append(checks[n], f)
	}

	if payee != "" && len(order) > 1 {
		return nil, Invalid{Field: "payee", Err: errors.New("say whom one check was paid to at a time")}
	}

	var (
		receipts []Receipt
		paid     []types.ID
	)

	for _, n := range order {
		txs, err := b.ledger.WithCheck(ctx, accountID, n)
		if err != nil {
			return nil, err
		}

		if len(txs) != 1 {
			return nil, NoCheck{Number: n, Several: len(txs) > 1}
		}

		t := txs[0]

		receipts = append(receipts, Receipt{
			ID: types.NewID(), Home: home, UploadedBy: actor, CreatedAt: now, Check: n,
			Details: Details{SpentOn: t.PostedOn, Amount: max(t.Amount, -t.Amount), HasAmount: true, Merchant: payee},
			Files:   checks[n],
			Links:   []Link{{TransactionID: t.ID, LinkedBy: actor, LinkedAt: now}},
		})
		paid = append(paid, t.ID)
	}

	ev := eventbus.New(now, actor, home, Added, map[string]string{"count": strconv.Itoa(len(receipts))})

	if err := b.store.Create(ctx, receipts, ev); err != nil {
		return nil, err
	}

	if payee != "" {
		if _, err := b.ledger.SetPayee(ctx, now, actor, paid[0], payee); err != nil {
			return receipts, err
		}
	}

	return receipts, nil
}

// paidTo writes a check image's shop -- to whom the check was written --
// on the transactions it is attached to, when it has changed.
func (b *Business) paidTo(ctx context.Context, now time.Time, actor types.ID, before, after Receipt) error {
	if after.Check == "" || before.Merchant == after.Merchant {
		return nil
	}

	for _, l := range after.Links {
		if _, err := b.ledger.SetPayee(ctx, now, actor, l.TransactionID, after.Merchant); err != nil {
			return err
		}
	}

	return nil
}
