package receiptbus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Checks printed by a program (issue #166, docs/checks.md). A program on
// the treasurer's computer prints a check on pre-printed stock and sends
// the image of it as printed, with what it printed: the number, amount,
// payee, the day written and the memo. That is everything a check image
// read by Claude says (ReadCheck), known before the check leaves the
// building rather than read back after it clears, so it is kept the same
// way: a check image in its account, attached at once when the bank has
// paid the number for the amount, waiting among the checks outstanding
// otherwise until the import that brings it (MatchChecks).
//
// Two things are new, because a printer is not a camera.
//
// A printed check can be spoiled -- a jam, a typo seen too late -- and the
// paper is torn up. It is voided: kept, with its number, so that the
// accountant can see why check 1003 is missing from the run, but never
// outstanding and never matched. Once a check has cleared, voiding it is
// refused: the bank paid it.
//
// And a program sends again what it is not sure arrived. Two computers
// print from one stack of checks, each queueing what it printed while it
// was offline. A send is recognised as one already made by what it says
// -- the same number, amount, payee and day -- and answered with the check
// it made; not by the file, since the same check drawn again is new bytes.
// The same number saying anything else is a different check, refused
// with the one that has it (NumberUsed).

// CheckState is what an account knows of a check number (NumberState).
type CheckState string

// The states, as the API says them.
const (
	// NumberFree: no image of a check has the number, and the bank has paid
	// none.
	NumberFree CheckState = "free"

	// NumberOutstanding: an image of a check has it, and no transaction yet.
	NumberOutstanding CheckState = "outstanding"

	// NumberAttached: an image of a check has it, on the transaction that paid
	// it.
	NumberAttached CheckState = "attached"

	// NumberVoid: the only image with it was voided.
	NumberVoid CheckState = "void"

	// NumberCleared: the bank has paid it, and no image of the check is there.
	NumberCleared CheckState = "cleared"
)

// StateOf is what one check's image says of its number.
func StateOf(r Receipt) CheckState {
	switch {
	case r.Voided():
		return NumberVoid
	case r.Waiting():
		return NumberOutstanding
	}

	return NumberAttached
}

// NumberUsed is a check sent with a number that another check in the
// account has, not voided, and that says something else: another amount,
// payee or day. Nothing is kept.
type NumberUsed struct {
	Existing Receipt
}

func (e NumberUsed) Error() string {
	return fmt.Sprintf("check %s is in the account already, and says something else", e.Existing.Check)
}

// ErrNotChecking is a check drawn on an account that is not a checking
// account in use: a card, a cash box, an account for reference only or
// one archived.
var ErrNotChecking = errors.New("checks are drawn on a checking account")

// Sent is what sending a check made.
type Sent struct {
	Check Receipt

	// Again is a check sent before with the same number and what it
	// says: nothing was kept this time.
	Again bool
}

// checksAccount is the account, if the actor may add checks to it: read
// it (or ErrNotFound) and attach receipts in it (or ErrForbidden). A
// checking account in use, or ErrNotChecking.
func (b *Business) checksAccount(ctx context.Context, actor, accountID types.ID) (tenancybus.Account, error) {
	home := types.AccountScope(accountID)

	access, err := b.access.AccessTo(ctx, actor, home)

	switch {
	case err != nil:
		return tenancybus.Account{}, err
	case !access.Can(tenancybus.Read):
		return tenancybus.Account{}, ErrNotFound
	case !access.Can(tenancybus.Receipts):
		return tenancybus.Account{}, ErrForbidden
	}

	ov, err := b.access.Overview(ctx, actor)
	if err != nil {
		return tenancybus.Account{}, err
	}

	i := slices.IndexFunc(accountsOf(ov), func(a tenancybus.Account) bool { return a.ID == accountID })
	if i < 0 {
		return tenancybus.Account{}, ErrNotFound
	}

	acct := accountsOf(ov)[i]
	if !Printable(acct) {
		return acct, ErrNotChecking
	}

	return acct, nil
}

// Printable is whether checks are drawn on the account: a checking
// account in use, part of the books.
func Printable(a tenancybus.Account) bool {
	return a.Kind == tenancybus.Checking && !a.Reference && !a.Archived()
}

// SendCheck keeps the image of a check a program printed, with what was
// printed on it: attached at once to the account's transaction that paid
// the number for its amount, or waiting for it, outstanding. Every part of
// the reading is needed but the memo. See the comment at the top of the
// file for a check sent twice; a number the bank paid for another amount
// is AmountDiffers, and nothing is kept.
func (b *Business) SendCheck(ctx context.Context, now time.Time, actor, accountID, fileID types.ID, rd CheckReading) (Sent, error) {
	if _, err := b.checksAccount(ctx, actor, accountID); err != nil {
		return Sent{}, err
	}

	home := types.AccountScope(accountID)

	n := importbus.CheckNumber(rd.Number)
	payee := strings.Join(strings.Fields(rd.Payee), " ")
	memo := strings.Join(strings.Fields(rd.Memo), " ")

	switch {
	case n == "":
		return Sent{}, Invalid{Field: "number", Err: errors.New("a check number is digits")}
	case rd.Amount <= 0:
		return Sent{}, Invalid{Field: "amount", Err: errors.New("a check's amount is more than nothing")}
	case payee == "":
		return Sent{}, Invalid{Field: "payee", Err: errors.New("say whom the check is to")}
	case utf8.RuneCountInString(payee) > ledgerbus.MaxPayee:
		return Sent{}, Invalid{Field: "payee", Err: fmt.Errorf("at most %d characters", ledgerbus.MaxPayee)}
	case utf8.RuneCountInString(memo) > ledgerbus.MaxCheckMemo:
		return Sent{}, Invalid{Field: "memo", Err: fmt.Errorf("at most %d characters", ledgerbus.MaxCheckMemo)}
	case rd.On.Zero():
		return Sent{}, Invalid{Field: "written_on", Err: errors.New("say the day written on the check")}
	}

	f, err := b.files.ByID(ctx, fileID)

	switch {
	case errors.Is(err, filebus.ErrNotFound), err == nil && f.UploadedBy != actor:
		return Sent{}, ErrNotFound
	case err != nil:
		return Sent{}, err
	}

	// A check already there with the number. Sent before, it is the
	// answer; anything else with the number is a different check, unless
	// it is an image nobody has said the amount of yet -- a photo of this
	// very check, numbered -- which this one completes (join, below).
	numbered, err := b.store.Numbered(ctx, accountID, n)
	if err != nil {
		return Sent{}, err
	}

	var twin *Receipt

	for _, r := range numbered {
		switch {
		case r.Voided():
			continue
		case sameCheck(r, rd.Amount, payee, rd.On):
			return Sent{Check: r, Again: true}, nil
		case r.Waiting() && !r.HasAmount:
			twin = &r
		default:
			return Sent{}, NumberUsed{Existing: r}
		}
	}

	txs, err := b.ledger.WithCheck(ctx, accountID, n)
	if err != nil {
		return Sent{}, err
	}

	if len(txs) == 1 && txs[0].Amount.Abs() != rd.Amount {
		return Sent{}, AmountDiffers{Number: n, Bank: txs[0].Amount.Abs(), Read: rd.Amount}
	}

	said := Details{Amount: rd.Amount, HasAmount: true, Merchant: payee, Memo: memo, SpentOn: rd.On, WrittenOn: rd.On}
	ev := eventbus.New(now, actor, home, CheckSent, map[string]string{"number": n, "amount": rd.Amount.String(), "payee": payee})

	if twin != nil {
		r := *twin
		note := r.Note
		r.Details, r.Note = said, note

		if r, err = b.join(ctx, now, actor, r, Receipt{}, []filebus.File{f}, types.ID{}); err != nil {
			return Sent{}, err
		}

		r, err = b.saveCheck(ctx, now, actor, r, paying(txs, r), &ev)

		return Sent{Check: r}, err
	}

	r := Receipt{ID: types.NewID(), Home: home, UploadedBy: actor, CreatedAt: now, CheckImage: true, Check: n, Details: said, Files: []filebus.File{f}}

	t := paying(txs, r)
	if t != nil {
		r = matched(r, *t, actor, now)
	}

	if err := b.store.Create(ctx, []Receipt{r}, ev); err != nil {
		return Sent{}, err
	}

	if t != nil {
		if err := b.writeOnto(ctx, now, actor, t.ID, Receipt{}, r); err != nil {
			return Sent{Check: r}, err
		}
	}

	return Sent{Check: r}, nil
}

// sameCheck is whether a check's image says what a check sent says: the
// same amount, payee (as written, but for case and spaces) and day.
func sameCheck(r Receipt, amount money.Amount, payee string, on types.Date) bool {
	return r.HasAmount && r.Amount == amount && r.WrittenOn == on &&
		strings.EqualFold(strings.Join(strings.Fields(r.Merchant), " "), payee)
}

// VoidCheck marks the account's check of that number void, as a program
// that printed it does when the paper was spoiled. One voided already is
// the answer as it is. A check on its transaction has cleared, and is
// ErrAttached; no check with the number is ErrNotFound.
func (b *Business) VoidCheck(ctx context.Context, now time.Time, actor, accountID types.ID, number string) (Receipt, error) {
	if _, err := b.checksAccount(ctx, actor, accountID); err != nil && !errors.Is(err, ErrNotChecking) {
		return Receipt{}, err
	}

	n := importbus.CheckNumber(number)
	if n == "" {
		return Receipt{}, Invalid{Field: "number", Err: errors.New("a check number is digits")}
	}

	numbered, err := b.store.Numbered(ctx, accountID, n)
	if err != nil {
		return Receipt{}, err
	}

	var void *Receipt

	for _, r := range numbered {
		switch {
		case r.Voided():
			void = &r
		case !r.Waiting():
			return r, ErrAttached
		default:
			return b.setVoided(ctx, now, actor, r, true)
		}
	}

	if void != nil {
		return *void, nil
	}

	return Receipt{}, ErrNotFound
}

// SetCheckVoided marks a check's image void, or takes the mark off, on
// its page: whoever may correct its details may. Only the image of a
// numbered check, not removed, can be void; one attached is ErrAttached.
// Taking the mark off a check whose number another check has since is
// NumberUsed; otherwise it is outstanding again, and attached if its
// transaction has come (MatchChecks).
func (b *Business) SetCheckVoided(ctx context.Context, now time.Time, actor, id types.ID, voided bool) (Receipt, error) {
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
	case !r.CheckImage || r.Check == "" || r.Home.Kind != types.ScopeAccount:
		return r, Invalid{Field: "void", Err: errors.New("only a check with its number can be void")}
	case r.Removed():
		return r, ErrRemoved
	case len(r.Links) > 0:
		return r, ErrAttached
	case r.Voided() == voided:
		return r, nil
	}

	if !voided {
		numbered, err := b.store.Numbered(ctx, r.Home.ID, r.Check)
		if err != nil {
			return r, err
		}

		for _, o := range numbered {
			if o.ID != r.ID && !o.Voided() {
				return r, NumberUsed{Existing: o}
			}
		}
	}

	r, err = b.setVoided(ctx, now, actor, r, voided)
	if err != nil || voided {
		return r, err
	}

	if err := b.MatchChecks(ctx, now, actor, r.Home.ID); err != nil {
		return r, err
	}

	return b.store.ByID(ctx, r.ID)
}

func (b *Business) setVoided(ctx context.Context, now time.Time, actor types.ID, r Receipt, voided bool) (Receipt, error) {
	action, at := CheckVoided, now
	if !voided {
		action, at = CheckUnvoided, time.Time{}
	}

	ev := eventbus.New(now, actor, r.Home, action, map[string]string{"number": r.Check})

	if err := b.store.SetVoided(ctx, r.ID, at, ev); err != nil {
		return r, err
	}

	r.VoidedAt = at

	return r, nil
}

// NumberState is what an account knows of one check number.
type NumberState struct {
	Number string
	State  CheckState

	// Check is the image with the number, when one has it: the one not
	// voided, or a voided one when that is all there is.
	Check Receipt

	// Cleared is the account's transactions with the number, when the
	// bank has paid it and no image of the check is there: usually one,
	// and more when a bank's numbers have come round again.
	Cleared []ledgerbus.Transaction
}

// CheckNumberState says whether a check number is free in the account
// before a program prints on it, and what has it if not. It is for
// whoever may add checks to the account. It reads no more than sending
// the check would refuse with (AmountDiffers), for one number at a time.
func (b *Business) CheckNumberState(ctx context.Context, actor, accountID types.ID, number string) (NumberState, error) {
	if _, err := b.checksAccount(ctx, actor, accountID); err != nil && !errors.Is(err, ErrNotChecking) {
		return NumberState{}, err
	}

	n := importbus.CheckNumber(number)
	if n == "" {
		return NumberState{}, Invalid{Field: "number", Err: errors.New("a check number is digits")}
	}

	out := NumberState{Number: n, State: NumberFree}

	numbered, err := b.store.Numbered(ctx, accountID, n)
	if err != nil {
		return out, err
	}

	for _, r := range numbered {
		if out.State == NumberFree || out.State == NumberVoid && !r.Voided() {
			out.State, out.Check = StateOf(r), r
		}
	}

	if out.State != NumberFree {
		return out, nil
	}

	if out.Cleared, err = b.ledger.WithCheck(ctx, accountID, n); err != nil {
		return out, err
	}

	if len(out.Cleared) > 0 {
		out.State = NumberCleared
	}

	return out, nil
}

// lastNumbers is how many of the highest numbers CheckNumbers gives.
const lastNumbers = 5

// Numbers is the check numbers an account has used, so that a program
// can propose the next: its images of checks', voided or not, and the
// bank's.
type Numbers struct {
	// Highest is the highest, "" for none.
	Highest string

	// Last is the highest few, highest first, so that one stray number
	// -- a counter check, a second checkbook -- is seen for what it is
	// beside the run the others make.
	Last []string
}

// CheckNumbers is the numbers the account has used, for whoever may add
// checks to it.
func (b *Business) CheckNumbers(ctx context.Context, actor, accountID types.ID) (Numbers, error) {
	if _, err := b.checksAccount(ctx, actor, accountID); err != nil && !errors.Is(err, ErrNotChecking) {
		return Numbers{}, err
	}

	images, err := b.store.CheckNumbers(ctx, accountID)
	if err != nil {
		return Numbers{}, err
	}

	bank, err := b.ledger.CheckNumbers(ctx, accountID)
	if err != nil {
		return Numbers{}, err
	}

	return numbersFrom(append(images, bank...)), nil
}

// numbersFrom is Numbers of these, compared as numbers: 998 before 1001.
func numbersFrom(all []string) Numbers {
	var ns []string

	for _, s := range all {
		if n := importbus.CheckNumber(s); n != "" && !slices.Contains(ns, n) {
			ns = append(ns, n)
		}
	}

	slices.SortFunc(ns, func(x, y string) int {
		return cmp.Or(cmp.Compare(len(y), len(x)), strings.Compare(y, x))
	})

	out := Numbers{Last: ns[:min(len(ns), lastNumbers)]}
	if len(ns) > 0 {
		out.Highest = ns[0]
	}

	return out
}
