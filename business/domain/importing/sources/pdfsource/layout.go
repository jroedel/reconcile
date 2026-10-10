package pdfsource

import (
	"fmt"
	"regexp"
)

// Layout is what the reader knows of how a statement is laid out: the words
// of its headings and its labels, each a pattern. The reader's rules --
// rows start with a date and end with an amount, a line repeated at the
// pages' edges is furniture, a line at the description's column continues
// the row above -- are the same for every document; what a document calls
// its deposits, its balances and its account number is not, and is said
// here (docs/shapes.md, 2).
//
// General is what the reader has always used, and reads every bank it has
// been shown. A declaration for one bank's layout adjusts it.
type Layout struct {
	// Name is what the preview calls a document read with it.
	Name string

	// The headings of sections. A heading is a few words alone on a line,
	// outside the description's column (sections.go); its words say what
	// the rows under it are. In is money into the account, Out money out of
	// it, Checks the checks paid (out of it, starting with the check's
	// number), Daily the table of each day's ending balance, Pending the
	// charges not yet posted, and Stop anything else, whose lines are not
	// rows: a summary, a page of disclosures.
	In, Out, Checks, Daily, Pending, Stop string

	// AccountNumber names the account the lines after it are about. Its
	// last group is the number, of which the last four digits are kept.
	AccountNumber string

	// The labels a balance or total is printed after, outside the rows or
	// as a row of its own: the balance before the period, the balance
	// after it, the total of the rows, and the total still pending.
	Opening, Closing, Total, PendingTotal string

	// Noise is lines that are never part of a row, wherever they are: a
	// page count, an address, marks a bank hides in the text.
	Noise string

	// The column headings: a line whose first heading is a Date, with a
	// Description after it and an Amount after that, is the table's
	// headings, and a column between the date and the amount headed as a
	// Person is the cardholder's, kept apart from the description.
	DateHeading, DescHeading, AmountHeading, PersonHeading string
}

// General is the reader as it reads a layout nobody has declared: the
// words of English, Spanish and Portuguese statements it has been shown.
var General = Layout{
	Name: "general",

	In:      `(?i)\b(deposits?|additions|credits|money in|dep[óo]sitos|cr[ée]ditos|entradas|abonos)\b`,
	Out:     `(?i)\b(withdrawals?|checks? paid|debits|fees|service charges|money out|purchases|retiros|saques|d[ée]bitos|tarifas|cargos|cheques? (pagos|pagados|compensados))\b`,
	Checks:  `(?i)\b(checks? paid|cheques? (pagos|pagados|compensados))\b`,
	Daily:   `(?i)\b(daily (ending )?balances?|saldos? di[áa]rios?)\b`,
	Pending: `(?i)^(pending|pendentes?|pendientes?)\b|\bpending (transactions|charges|purchases|activity|authorizations)\b|\b(transa[çc][õo]es|lan[çc]amentos) pendentes\b|\b(transacciones|movimientos) pendientes\b`,
	Stop:    `(?i)\b(summary|detail|information|messages?|disclosures?|interest|resumo|resumen|detalle|detalhe)\b`,

	// "Primary account" on a consolidated statement's every page is not
	// one: it has no number after it.
	AccountNumber: `(?i)\b(account (number|no\.?|#)|n[úu]mero de (la )?(cuenta|conta)|conta n[º°o.]?)\s*:?\s*([0-9][0-9 \-]{3,}[0-9])`,

	Opening:      `(?i)\b(previous|beginning|opening|starting) (statement )?balance\b|\bbalance (brought )?forward\b`,
	Closing:      `(?i)\b(new|ending|closing) (statement )?balance\b`,
	Total:        `(?i)\b(total|net) activity\b`,
	PendingTotal: `(?i)\bpending (purchases|charges|transactions|activity|total|amount)\b|\b(compras|transa[çc][õo]es|lan[çc]amentos) pendentes\b|\b(compras|transacciones|cargos) pendientes\b`,

	// The marks some banks hide in the text around each part of a
	// statement, "*start*deposits and additions", are not on the page at
	// all.
	Noise: `(?i)(\bpage \d+ of \d+\b|^page \d+$|https?://|\bwww\.|^\(?continued\b|^\*(start|end)\*)`,

	DateHeading:   `(?i)\bdate\b`,
	DescHeading:   `(?i)^(transaction )?(description|details|payee|merchant|narrative|transaction)\b`,
	AmountHeading: `(?i)\b(amount|debits?|credits?|withdrawals?|deposits?|charges?|payments?|balance|money)\b`,
	PersonHeading: `(?i)^(name|card ?member|card ?holder|member|employee|user|spender)\b`,
}

// vocabulary is a Layout compiled.
type vocabulary struct {
	name string

	in, out, checks, daily, pending, stop *regexp.Regexp
	account                               *regexp.Regexp
	opening, closing, total, pendingTotal *regexp.Regexp
	noise                                 *regexp.Regexp

	dateHeading, descHeading, amountHeading, personHeading *regexp.Regexp
}

// compile makes a layout's patterns ready to read with, or says which one
// is not a pattern. A pattern left empty matches nothing.
func (l Layout) compile() (*vocabulary, error) {
	v := vocabulary{name: l.Name}

	for _, p := range []struct {
		what    string
		pattern string
		into    **regexp.Regexp
	}{
		{"in", l.In, &v.in},
		{"out", l.Out, &v.out},
		{"checks", l.Checks, &v.checks},
		{"daily", l.Daily, &v.daily},
		{"pending", l.Pending, &v.pending},
		{"stop", l.Stop, &v.stop},
		{"account number", l.AccountNumber, &v.account},
		{"opening", l.Opening, &v.opening},
		{"closing", l.Closing, &v.closing},
		{"total", l.Total, &v.total},
		{"pending total", l.PendingTotal, &v.pendingTotal},
		{"noise", l.Noise, &v.noise},
		{"date heading", l.DateHeading, &v.dateHeading},
		{"description heading", l.DescHeading, &v.descHeading},
		{"amount heading", l.AmountHeading, &v.amountHeading},
		{"person heading", l.PersonHeading, &v.personHeading},
	} {
		pattern := p.pattern
		if pattern == "" {
			pattern = `[^\s\S]`
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("the layout's %s pattern: %w", p.what, err)
		}

		*p.into = re
	}

	if v.account.NumSubexp() < 1 {
		return nil, fmt.Errorf("the layout's account number pattern has no group for the number")
	}

	return &v, nil
}

// general is General, compiled once.
var general = func() *vocabulary {
	v, err := General.compile()
	if err != nil {
		panic(err)
	}

	return v
}()
