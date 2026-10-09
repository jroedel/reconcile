package pdfsource

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/types/money"
)

// figure is an amount at the end of a row, and the column it ends at.
type figure struct {
	amount money.Amount
	col    int
}

// figurePattern is an amount as statements print it: "1,234.56",
// "$1,234.56", "-$0.44", "$-0.44", "(45.00)", "45.00-", "45.00 CR".
const figurePattern = `\(?[-+]?[$€£]?\s?[-+]?(?:\d{1,3}(?:,\d{3})+|\d+)\.\d{2}\)?(?:-|\s?(?:CR|DR|Cr|Dr)\b)?`

// trailing is the right-most amount on a line, with the run of spaces that
// sets it apart from what is before it.
//
// The two spaces are what keep a description whole. A payee carries
// numbers freely -- "TFR FRM CHK 1080", "STORE 0412" -- and a pattern that
// took a number anywhere would cut the description at the first one. A
// laid-out table never separates its columns by one space.
var trailing = regexp.MustCompile(`(?:^|\s{2,})(` + figurePattern + `)\s*$`)

// anyFigure is the first amount in a piece of a line, for a summary's
// label.
var anyFigure = regexp.MustCompile(`(?:^|\s)(` + figurePattern + `)(?:\s|$)`)

// figures takes the amounts off the end of a line, right to left, and
// returns what is left and the amounts in the order printed: a two-figure
// row is (amount, balance), as the headings say.
func figures(s string) (string, []figure) {
	var reversed []figure

	rest := strings.TrimRight(s, " \t")

	for {
		m := trailing.FindStringSubmatchIndex(rest)
		if m == nil {
			break
		}

		a, err := parseFigure(rest[m[2]:m[3]])
		if err != nil {
			break
		}

		reversed = append(reversed, figure{amount: a, col: utf8.RuneCountInString(rest[:m[3]])})
		rest = strings.TrimRight(rest[:m[0]], " \t")
	}

	out := make([]figure, len(reversed))
	for i, f := range reversed {
		out[len(reversed)-1-i] = f
	}

	return rest, out
}

// parseFigure reads one amount, with the markings money.Parse does not
// know: a trailing minus, and CR for a credit. A credit is negative, which
// is the bank's way round on a card: the ledger turns a card's amounts over
// as a whole (ledgerbus.Options.Invert).
func parseFigure(s string) (money.Amount, error) {
	s = strings.TrimSpace(s)
	negative := false

	switch upper := strings.ToUpper(s); {
	case strings.HasSuffix(upper, "CR"):
		negative, s = true, strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(upper, "DR"):
		s = strings.TrimSpace(s[:len(s)-2])
	}

	if strings.HasSuffix(s, "-") {
		negative, s = !negative, strings.TrimSpace(s[:len(s)-1])
	}

	a, err := money.Parse(s)
	if err != nil {
		return 0, err
	}

	if negative {
		a = -a
	}

	return a, nil
}

// fromBalances gives each row's amount its sign from the balances, when
// every row prints a balance and no amount is negative: a statement with a
// debit column and a credit column, each printed without a sign. Read in
// the order of their dates, each balance is the one before it moved by the
// row's amount, one way or the other; the way it moved is the sign.
//
// The first row has no balance before it but the opening balance, if the
// document states one. Without it the first row takes the sign of a row
// whose amount ends at the same column on its page -- the debit column or
// the credit column -- which is the one place where a figure's position is
// all there is to go by. Failing that it is left as printed, and the
// ledger's check, which tries both signs, says whether the rest holds.
//
// When the balances do not follow either way, nothing is changed: the
// ledger's check finds the row where they stop, and the page says which.
func fromBalances(recs []importbus.Record, cols []int, opening importbus.Balance) {
	if len(recs) < 2 {
		return
	}

	for _, r := range recs {
		if !r.HasBalance || r.Amount < 0 {
			return
		}
	}

	forward := make([]int, len(recs))
	for i := range forward {
		forward[i] = i
	}

	backward := make([]int, len(recs))
	for i := range backward {
		backward[i] = len(recs) - 1 - i
	}

	orders := [][]int{forward, backward}
	if recs[len(recs)-1].Date.Before(recs[0].Date) {
		orders = [][]int{backward, forward}
	} else if recs[0].Date.Before(recs[len(recs)-1].Date) {
		orders = orders[:1]
	}

	for _, order := range orders {
		signs, ok := signsOf(recs, order, opening)
		if !ok {
			continue
		}

		first := order[0]
		if signs[first] == 0 {
			signs[first] = byColumn(recs, cols, signs, first)
		}

		for i, s := range signs {
			if s != 0 {
				recs[i].Amount *= s
			}
		}

		return
	}
}

// signsOf walks the rows in one order and says which way each balance
// moved, or that some balance did not move by its row's amount at all.
func signsOf(recs []importbus.Record, order []int, opening importbus.Balance) ([]money.Amount, bool) {
	signs := make([]money.Amount, len(recs))

	sign := func(delta, amount money.Amount) money.Amount {
		switch {
		case delta == amount:
			return 1
		case delta == -amount:
			return -1
		}

		return 0
	}

	for k := 1; k < len(order); k++ {
		i, before := order[k], order[k-1]

		s := sign(recs[i].Balance-recs[before].Balance, recs[i].Amount)
		if s == 0 {
			return nil, false
		}

		signs[i] = s
	}

	if first := order[0]; opening.Known {
		signs[first] = sign(recs[first].Balance-opening.Amount, recs[first].Amount)
	}

	return signs, true
}

// byColumn is the sign of the column the first row's amount is in: the
// sign of the rows on its page whose amounts end nearest it, when rows of
// both signs are there and in columns apart. In one column of amounts for
// both, where it sits says nothing, and the answer is 0.
func byColumn(recs []importbus.Record, cols []int, signs []money.Amount, first int) money.Amount {
	nearest := map[money.Amount]int{}

	for i, s := range signs {
		if s == 0 || recs[i].Page != recs[first].Page {
			continue
		}

		d := abs(cols[i] - cols[first])
		if n, ok := nearest[s]; !ok || d < n {
			nearest[s] = d
		}
	}

	in, inOK := nearest[1]
	out, outOK := nearest[-1]

	switch {
	case !inOK || !outOK:
		return 0
	case in+2 <= out:
		return 1
	case out+2 <= in:
		return -1
	}

	return 0
}
