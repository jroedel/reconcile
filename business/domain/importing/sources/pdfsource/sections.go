package pdfsource

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/types/money"
)

// A bank's own statement, as against a page of transactions printed from
// its website, is laid out by kind rather than by date: the deposits, then
// the checks paid, then the withdrawals, each under its heading, each
// amount printed without a sign because the heading says which way it
// went. The balance is not on the rows at all but in a table of its own,
// the balance at the end of each day there was activity. And one document
// may hold several accounts, one after the other, each with its own
// beginning and ending balance -- a "consolidated" statement for everything
// a customer has at the bank.
//
// None of this is one bank's. The words below are what such headings say
// in English, Spanish and Portuguese; a heading is recognized by its words
// and by standing alone, outside the description's column, so that a
// description that mentions a deposit is never taken for one.

// direction is which way a section's money went: into the account (1), out
// of it (-1), or not said (0).
type direction int

var (
	dailyHeading = regexp.MustCompile(`(?i)\b(daily (ending )?balances?|saldos? di[áa]rios?)\b`)
	stopHeading  = regexp.MustCompile(`(?i)\b(summary|detail|information|messages?|disclosures?|interest|resumo|resumen|detalle|detalhe)\b`)
	inHeading    = regexp.MustCompile(`(?i)\b(deposits?|additions|credits|money in|dep[óo]sitos|cr[ée]ditos|entradas|abonos)\b`)
	outHeading   = regexp.MustCompile(`(?i)\b(withdrawals?|checks? paid|debits|fees|service charges|money out|purchases|retiros|saques|d[ée]bitos|tarifas|cargos|cheques? (pagos|pagados|compensados))\b`)
	checkHeading = regexp.MustCompile(`(?i)\b(checks? paid|cheques? (pagos|pagados|compensados))\b`)

	// pendingHeading is a section of charges not yet posted, which says
	// nothing of their direction.
	pendingHeading = regexp.MustCompile(`(?i)^(pending|pendentes?|pendientes?)\b|\bpending (transactions|charges|purchases|activity|authorizations)\b|\b(transa[çc][õo]es|lan[çc]amentos) pendentes\b|\b(transacciones|movimientos) pendientes\b`)

	// accountNumber is a line that names the account the lines after it
	// are about; "Primary account" on a consolidated statement's every
	// page is not one.
	accountNumber = regexp.MustCompile(`(?i)\b(account (number|no\.?|#)|n[úu]mero de (la )?(cuenta|conta)|conta n[º°o.]?)\s*:?\s*([0-9][0-9 \-]{3,}[0-9])`)
)

// account starts the part of the document a line names, when it names an
// account other than the one being read. The first number named is the
// first part's; a number repeated on every page, as a single account's
// statement does, starts nothing.
func (r *reader) account(s string) bool {
	m := accountNumber.FindStringSubmatch(s)
	if m == nil {
		return false
	}

	digits := strings.NewReplacer(" ", "", "-", "").Replace(m[len(m)-1])
	last4 := digits[len(digits)-4:]

	switch {
	case r.last4 == "":
		r.last4 = last4
	case r.last4 != last4:
		r.nextAccount(last4)
	}

	return true
}

// nextAccount files what was read as one account's, and starts the next.
func (r *reader) nextAccount(last4 string) {
	r.parts = append(r.parts, part{last4: r.last4, res: r.res, cols: r.cols, dirs: r.dirs})
	r.res, r.cols, r.dirs = importbus.Result{}, nil, nil
	r.last4 = last4
	r.dir, r.checks, r.daily, r.check, r.held = 0, false, false, "", false
	r.open = -1
}

// part is one account's rows, and how to finish them.
type part struct {
	last4 string
	res   importbus.Result
	cols  []int
	dirs  []direction
}

// section reads a line as a section's heading, if it is one, and takes up
// what it says. A heading is a few words alone on a line, with no date or
// amount, and not where descriptions are.
func (r *reader) section(s string) bool {
	t := strings.TrimSpace(s)

	switch {
	case utf8.RuneCountInString(t) > 60 || len(strings.Fields(t)) > 7 || len(cells(s, 0)) != 1:
		return false
	case strings.HasPrefix(strings.ToLower(t), "total"):
		return false
	case r.descCol > 0 && abs(indent(s)-r.descCol) <= 3:
		return false
	}

	if _, figs := figures(t); len(figs) > 0 {
		return false
	}

	if _, _, ok := leadingDate(t, r.doc.dayFirst); ok {
		return false
	}

	in, out := inHeading.MatchString(t), outHeading.MatchString(t)
	r.held = false

	switch {
	case pendingHeading.MatchString(t):
		r.dir, r.checks, r.daily, r.held = 0, false, false, true
	case dailyHeading.MatchString(t):
		r.dir, r.checks, r.daily = 0, false, true
	case stopHeading.MatchString(t), in && out:
		r.dir, r.checks, r.daily = 0, false, false
	case in:
		r.dir, r.checks, r.daily = 1, false, false
	case out:
		r.dir, r.checks, r.daily = -1, checkHeading.MatchString(t), false
	default:
		return false
	}

	r.open, r.check = -1, ""

	return true
}

// balances reads a line of the daily balance table: dates, each followed
// by the balance that day ended with, in as many columns as the page has.
// A pair may break across lines -- a date at the end of one and its
// balance at the start of the next -- so the date waits for its amount.
func (r *reader) balances(s string) {
	for _, c := range cells(s, 0) {
		if d, rest, ok := leadingDate(c.s, r.doc.dayFirst); ok && strings.TrimSpace(rest) == "" {
			r.waiting, r.hasWaiting = d, true

			continue
		}

		a, err := parseFigure(c.s)
		if err != nil || !r.hasWaiting {
			continue
		}

		if date, ok := r.doc.resolve(r.waiting); ok {
			r.res.Daily = append(r.res.Daily, importbus.Balance{Amount: a, AsOf: date, Known: true})
		}

		r.hasWaiting = false
	}
}

// checkNumber is the start of a row of the checks paid: the check's number,
// and the marks a bank puts beside it ("*" for a gap in the numbers, "^"
// for an image online).
var checkNumber = regexp.MustCompile(`^\s*(\d{3,7})((?:\s*[*^])*)\s*`)

// checkRow reads a line of the checks paid, which starts with the check's
// number rather than a date: the date it was paid is the last date on the
// line, and a date in the description column is the date written on the
// check. A number alone on its line is a row the page broke, and the line
// after it is the rest of the row.
func (r *reader) checkRow(l line) bool {
	m := checkNumber.FindStringSubmatchIndex(l.text)
	if m == nil {
		return false
	}

	number, rest := l.text[m[2]:m[3]], l.text[m[1]:]

	body, figs := figures(rest)
	if len(figs) == 0 {
		if strings.TrimSpace(rest) == "" {
			r.check = number

			return true
		}

		return false
	}

	var (
		paid  day
		found bool
		words []string
	)

	for _, c := range cells(body, 0) {
		if d, after, ok := leadingDate(c.s, r.doc.dayFirst); ok && strings.TrimSpace(after) == "" {
			paid, found = d, true

			continue
		}

		words = append(words, c.s)
	}

	if !found {
		return false
	}

	r.check = number
	r.row(l, row{date: paid, figures: figs, desc: strings.Join(words, " "), given: true})

	return true
}

// checkDescription is what a check's row is called: "Check 1176", the way
// a bank's CSV and OFX name it, so that the same check found there is the
// same transaction, and any description printed beside it after.
func checkDescription(number, desc string) string {
	if desc == "" {
		return "Check " + number
	}

	return "Check " + number + " " + desc
}

// finish makes one account's rows a Result: their signs from their
// sections, if nothing else gave them signs, and the daily balances
// attached to the rows they follow.
func (p part) finish() importbus.Result {
	res := p.res

	signed := slices.ContainsFunc(res.Records, func(r importbus.Record) bool { return r.Amount < 0 || r.HasBalance })
	if !signed {
		for i, d := range p.dirs {
			if d < 0 {
				res.Records[i].Amount = -res.Records[i].Amount
			}
		}
	}

	placePending(&res)
	fromBalances(res.Records, p.cols, res.Opening)
	daily(res.Records, res.Daily)

	return res
}

// placePending marks the rows a stated pending total accounts for, when no
// section said which they are: a printed page lists what has not posted at
// the head of its list, out of date order, and the rows from the head that
// add up to the total -- in either sign, since the total is printed the
// bank's way round -- are those. A total no such rows add up to marks
// nothing, and says so (Unplaced): a guess presented as the document's
// word is what this reader never does.
func placePending(res *importbus.Result) {
	if slices.ContainsFunc(res.Records, func(r importbus.Record) bool { return r.Pending }) {
		return
	}

	total := res.Pending.Amount
	if !res.Pending.Known || total == 0 {
		return
	}

	var sum money.Amount

	for i, r := range res.Records {
		sum += r.Amount

		if sum == total || sum == -total {
			for j := range i + 1 {
				res.Records[j].Pending = true
			}

			return
		}
	}

	res.Unplaced = true
}

// daily puts each day's ending balance on the last row on or before that
// day, so that the ledger checks it as it checks a balance printed on a
// row (ledgerbus.verify). The rows are put in date order first, keeping
// the order within a day: a statement by kind lists the deposits of the
// whole month before the withdrawals, and an end-of-day balance holds
// whatever order the day's rows are in.
func daily(recs []importbus.Record, balances []importbus.Balance) {
	if len(balances) == 0 || len(recs) == 0 {
		return
	}

	slices.SortStableFunc(recs, func(a, b importbus.Record) int { return a.Date.Compare(b.Date) })

	for _, b := range balances {
		last := -1

		for i, r := range recs {
			if r.Date.After(b.AsOf) {
				break
			}

			last = i
		}

		if last >= 0 {
			recs[last].Balance, recs[last].HasBalance = b.Amount, true
		}
	}
}
