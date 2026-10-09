// Package csvsource reads a bank's CSV export.
//
// Every bank invents its own dialect, so the column mapping is data rather
// than code: Detect guesses one from the header, a person confirms or
// corrects it on the mapping screen, and it is stored against the account
// and the header's Fingerprint so that the next export from the same bank
// imports with no questions.
//
// Adapted from eumaeus. What is new here is what a public, three-language
// site meets that one person's ledger did not:
//
//   - A separator other than a comma. Banks in Brazil and most of Spanish-
//     speaking America export with semicolons, because the comma is their
//     decimal point. Detected from the header line.
//   - A decimal comma: "1.234,56". money.Parse assumes a point, and reading
//     that as 1.23456 would be a wrong number rather than an error. Detected
//     from the amounts themselves.
//   - Latin-1. An export from an older banking system is often not UTF-8, and
//     "Depósito" must not arrive as "Dep\xf3sito".
//   - A running-balance column, which is the strongest check there is on a
//     statement: every row's balance must follow from the one before
//     (ledgerbus).
//   - The date format is chosen once for the whole file, as the first format
//     every date in it parses with, rather than row by row. Row by row, a
//     file of day-first dates would read the 3rd of April as the 4th of March
//     for twelve days of every month and be right for the rest.
package csvsource

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/types/money"
)

// ErrNoHeader reports an export whose header row cannot be read, or that
// names no date or no amount column.
var ErrNoHeader = errors.New("no header row with a date and an amount")

// Mapping names the columns in one bank's export. Names match the header
// case-insensitively.
//
// Either Amount (one signed column) or the Debit/Credit pair (two unsigned
// columns), not both.
type Mapping struct {
	Date        string `json:"date"`
	Description string `json:"description"`
	Amount      string `json:"amount,omitempty"`
	Debit       string `json:"debit,omitempty"`
	Credit      string `json:"credit,omitempty"`

	// Balance names a running-balance column, if the export has one.
	Balance string `json:"balance,omitempty"`

	// Holder names the column that says which cardholder made a charge,
	// in a card's export for several cards.
	Holder string `json:"holder,omitempty"`

	// DateFormat is a Go layout. Empty chooses one from the file (see the
	// package comment).
	DateFormat string `json:"date_format,omitempty"`

	// Separator is the field separator, one character. Empty detects it.
	Separator string `json:"separator,omitempty"`

	// DecimalComma reads "1.234,56" as one thousand two hundred and
	// thirty-four. Detected on the mapping screen, stored with the mapping.
	DecimalComma bool `json:"decimal_comma,omitempty"`

	// SkipLines drops lines before the header row. Several banks preface an
	// export with a title and a blank line.
	SkipLines int `json:"skip_lines,omitempty"`

	// Invert flips the sign of every amount. A card's export commonly shows
	// a purchase as a positive number, which is the opposite of money out.
	Invert bool `json:"invert,omitempty"`
}

// DateFormats are what an empty DateFormat chooses from, in order. The
// day-first forms come after the month-first ones they are ambiguous with,
// so that a file is read day-first only when some date in it cannot be
// month-first -- a 13th or later -- and the mapping screen shows which was
// chosen so that a person can say otherwise.
var DateFormats = []string{
	"2006-01-02",
	"01/02/2006",
	"1/2/2006",
	"01/02/06",
	"1/2/06",
	"02/01/2006",
	"2/1/2006",
	"02/01/06",
	"02.01.2006",
	"02-01-2006",
	"2006/01/02",
	"Jan 2, 2006",
	"2-Jan-2006",
	"02-Jan-2006",
	"20060102",
}

// Candidate header names used by Detect: English, Spanish and Portuguese, as
// banks in all three write them. Exact matches win over substrings (pick).
var (
	dateNames = []string{
		"date", "transaction date", "posted date", "post date", "posting date", "trans date", "effective date",
		"fecha", "fecha operación", "fecha operacion", "fecha valor", "data", "data lançamento", "data lancamento", "data movimento",
	}
	descriptionNames = []string{
		"description", "payee", "name", "memo", "merchant", "details", "transaction", "narrative",
		"descripción", "descripcion", "concepto", "detalle", "descrição", "descricao", "histórico", "historico", "lançamento", "lancamento",
	}
	amountNames  = []string{"amount", "value", "transaction amount", "importe", "monto", "valor", "valor (r$)"}
	debitNames   = []string{"debit", "withdrawal", "withdrawals", "money out", "paid out", "spent", "cargo", "cargos", "débito", "debito", "saída", "saida"}
	creditNames  = []string{"credit", "deposit", "deposits", "money in", "paid in", "received", "abono", "abonos", "crédito", "credito", "entrada"}
	balanceNames = []string{"balance", "running balance", "saldo", "saldo disponible"}

	// Not "name", which is a payee as often as a person.
	holderNames = []string{
		"cardholder", "card holder", "card member", "cardmember", "card member name", "cardholder name",
		"titular", "tarjetahabiente", "titular do cartão", "titular do cartao", "portador",
	}
)

// Inspection is what the mapping screen shows about a file before a mapping
// is chosen.
type Inspection struct {
	Header      []string
	Sample      [][]string // the first rows after the header
	Fingerprint string

	// Detected is the mapping guessed from the header and the sample,
	// including the separator and whether amounts use a decimal comma.
	Detected Mapping
}

// sampleRows is how many rows the mapping screen previews.
const sampleRows = 10

// Inspect reads the header and the first rows of an export.
func Inspect(data []byte, skip int) (Inspection, error) {
	text := decode(data)

	header, rows, err := records(text, skip, "", sampleRows)
	if err != nil {
		return Inspection{}, err
	}

	m := Detect(header)
	m.SkipLines = skip
	m.Separator = string(separator(firstLine(text, skip)))
	m.DecimalComma = decimalComma(header, rows, m)

	return Inspection{Header: header, Sample: rows, Fingerprint: Fingerprint(header), Detected: m}, nil
}

// Read parses an export with a mapping.
func Read(data []byte, m Mapping) (importbus.Result, error) {
	header, rows, err := records(decode(data), m.SkipLines, m.Separator, -1)
	if err != nil {
		return importbus.Result{}, err
	}

	index := indexHeader(header)
	get := func(row []string, column string) string {
		i, ok := index[strings.ToLower(strings.TrimSpace(column))]
		if column == "" || !ok || i >= len(row) {
			return ""
		}

		return strings.TrimSpace(row[i])
	}

	switch {
	case m.Date == "" || !has(index, m.Date):
		return importbus.Result{}, fmt.Errorf("%w: the date column %q is not in the file", ErrNoHeader, m.Date)
	case m.Amount == "" && m.Debit == "" && m.Credit == "":
		return importbus.Result{}, fmt.Errorf("%w: no amount column", ErrNoHeader)
	}

	layout := m.DateFormat
	if layout == "" {
		dates := make([]string, 0, len(rows))
		for _, row := range rows {
			dates = append(dates, get(row, m.Date))
		}

		layout = ChooseDateFormat(dates)
	}

	var res importbus.Result

	for i, row := range rows {
		// Lines as a person counts them in the file: the skipped preamble,
		// the header, then this row.
		line := m.SkipLines + 2 + i

		if row == nil {
			res.Skipped++
			res.Warn(importbus.Warning{Line: line, Problem: importbus.BadRow})

			continue
		}

		if isBlank(row) {
			continue
		}

		rec, warning := toRecord(row, get, m, layout)
		if warning != nil {
			warning.Line = line
			res.Skipped++
			res.Warn(*warning)

			continue
		}

		rec.Line = line
		res.Records = append(res.Records, rec)
	}

	return res, nil
}

// toRecord converts one row, or says what is wrong with it.
func toRecord(row []string, get func([]string, string) string, m Mapping, layout string) (importbus.Record, *importbus.Warning) {
	raw := get(row, m.Date)
	if raw == "" {
		return importbus.Record{}, &importbus.Warning{Problem: importbus.NoDate}
	}

	date, err := parseDate(raw, layout)
	if err != nil {
		return importbus.Record{}, &importbus.Warning{Problem: importbus.BadDate, Value: raw}
	}

	description := strings.Join(strings.Fields(get(row, m.Description)), " ")

	amount, warning := parseAmount(row, get, m)
	if warning != nil {
		return importbus.Record{}, warning
	}

	if m.Invert {
		amount = -amount
	}

	rec := importbus.Record{Date: date, Description: description, Amount: amount}

	if m.Holder != "" {
		rec.Holder = strings.Join(strings.Fields(get(row, m.Holder)), " ")
	}

	if m.Balance != "" {
		raw := get(row, m.Balance)
		if raw != "" {
			b, err := ParseAmount(raw, m.DecimalComma)
			if err != nil {
				return importbus.Record{}, &importbus.Warning{Problem: importbus.BadBalance, Value: raw}
			}

			rec.Balance, rec.HasBalance = b, true
		}
	}

	return rec, nil
}

// parseAmount reads the signed column, or the debit and credit pair.
func parseAmount(row []string, get func([]string, string) string, m Mapping) (money.Amount, *importbus.Warning) {
	if m.Amount != "" {
		raw := get(row, m.Amount)
		if raw == "" {
			return 0, &importbus.Warning{Problem: importbus.NoAmount}
		}

		a, err := ParseAmount(raw, m.DecimalComma)
		if err != nil {
			return 0, &importbus.Warning{Problem: importbus.BadAmount, Value: raw}
		}

		return a, nil
	}

	debit, credit := get(row, m.Debit), get(row, m.Credit)
	if debit == "" && credit == "" {
		return 0, &importbus.Warning{Problem: importbus.NoAmount}
	}

	var out money.Amount

	// A debit is money out however the bank signed it, and a credit money
	// in: taking the magnitude means a column of unsigned numbers and a
	// column of negative ones both come out right.
	if debit != "" {
		a, err := ParseAmount(debit, m.DecimalComma)
		if err != nil {
			return 0, &importbus.Warning{Problem: importbus.BadAmount, Value: debit}
		}

		out -= a.Abs()
	}

	if credit != "" {
		a, err := ParseAmount(credit, m.DecimalComma)
		if err != nil {
			return 0, &importbus.Warning{Problem: importbus.BadAmount, Value: credit}
		}

		out += a.Abs()
	}

	return out, nil
}

// debitCredit is the "D" or "C" some Brazilian banks print after an amount
// instead of a sign.
var debitCredit = regexp.MustCompile(`(?i)\s*([DC])$`)

// ParseAmount reads an amount as banks print it, with a decimal comma when
// asked. Beyond money.Parse it accepts a currency written with letters
// ("R$", "US$"), a trailing minus ("12.50-"), and a trailing D or C.
func ParseAmount(s string, comma bool) (money.Amount, error) {
	s = strings.TrimSpace(s)

	for _, symbol := range []string{"US$", "R$", "S/", "MXN", "BRL", "USD", "EUR"} {
		s = strings.TrimSpace(strings.Replace(s, symbol, "", 1))
	}

	negate := false

	if m := debitCredit.FindStringSubmatch(s); m != nil && len(s) > len(m[0]) {
		negate = strings.EqualFold(m[1], "D")
		s = strings.TrimSpace(s[:len(s)-len(m[0])])
	}

	if rest, ok := strings.CutSuffix(s, "-"); ok && rest != "" {
		negate = !negate
		s = rest
	}

	if comma {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	}

	a, err := money.Parse(s)
	if err != nil {
		return 0, err
	}

	if negate {
		a = -a
	}

	return a, nil
}

// ChooseDateFormat is the first of DateFormats that every non-empty value
// parses with, or failing that, the one that parses the most.
func ChooseDateFormat(values []string) string {
	best, bestCount := DateFormats[0], -1

	for _, layout := range DateFormats {
		count, total := 0, 0

		for _, v := range values {
			if v == "" {
				continue
			}

			total++

			if _, err := parseDate(v, layout); err == nil {
				count++
			}
		}

		if total > 0 && count == total {
			return layout
		}

		if count > bestCount {
			best, bestCount = layout, count
		}
	}

	return best
}

// parseDate reads one date. A Schwab-style "07/12/2026 as of 07/10/2026" is
// the posting date followed by the trade date, and the posting date is the
// one the statement means.
func parseDate(s, layout string) (time.Time, error) {
	if before, _, found := strings.Cut(s, " as of "); found {
		s = strings.TrimSpace(before)
	}

	// Some exports carry a time after the date; the day is what matters.
	if len(s) > len(layout) && (s[len(layout)] == ' ' || s[len(layout)] == 'T') {
		s = s[:len(layout)]
	}

	return time.Parse(layout, s)
}

// records splits the text into a header and rows, after skipping lines.
// limit < 0 reads every row. A row that is not valid CSV is nil, so the
// caller can count it.
func records(text string, skip int, sep string, limit int) ([]string, [][]string, error) {
	buf := bufio.NewReader(strings.NewReader(text))

	for i := range skip {
		if _, err := buf.ReadString('\n'); err != nil {
			return nil, nil, fmt.Errorf("%w: the file ends before line %d", ErrNoHeader, i+1)
		}
	}

	comma := ','
	if sep == "" {
		comma = separator(firstLine(text, skip))
	} else if r, _ := utf8.DecodeRuneInString(sep); r != utf8.RuneError {
		comma = r
	}

	reader := csv.NewReader(buf)
	reader.Comma = comma
	reader.FieldsPerRecord = -1 // ragged rows and trailing junk columns are common
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNoHeader, err)
	}

	var rows [][]string

	for limit < 0 || len(rows) < limit {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}

		if err != nil {
			rows = append(rows, nil)

			continue
		}

		rows = append(rows, row)
	}

	return header, rows, nil
}

// firstLine is the header line, after skipping.
func firstLine(text string, skip int) string {
	lines := strings.SplitN(text, "\n", skip+2)
	if len(lines) <= skip {
		return ""
	}

	return lines[skip]
}

// separator picks whichever of comma, semicolon and tab the header line has
// most of. A header that has none is one column, and a comma is as good as
// anything.
func separator(header string) rune {
	best, count := ',', 0

	for _, r := range []rune{',', ';', '\t'} {
		if n := strings.Count(header, string(r)); n > count {
			best, count = r, n
		}
	}

	return best
}

// commaAmount is a number written with a decimal comma: "1.234,56", "-12,5".
var commaAmount = regexp.MustCompile(`^[^0-9]*-?[0-9.\s]*[0-9],[0-9]{1,2}\s*[^0-9]*$`)

// pointAmount is one written with a decimal point.
var pointAmount = regexp.MustCompile(`^[^0-9]*-?[0-9,\s]*[0-9]\.[0-9]{1,2}\s*[^0-9]*$`)

// decimalComma reports whether the sample's amounts are written with a
// decimal comma: more of them look that way than the other.
func decimalComma(header []string, rows [][]string, m Mapping) bool {
	index := indexHeader(header)
	commas, points := 0, 0

	for _, row := range rows {
		for _, column := range []string{m.Amount, m.Debit, m.Credit, m.Balance} {
			i, ok := index[strings.ToLower(column)]
			if column == "" || !ok || i >= len(row) {
				continue
			}

			switch v := strings.TrimSpace(row[i]); {
			case commaAmount.MatchString(v):
				commas++
			case pointAmount.MatchString(v):
				points++
			}
		}
	}

	return commas > points
}

// decode is the file as text: UTF-8 without its byte-order mark, or Latin-1
// read as what it is.
func decode(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))

	if utf8.Valid(data) {
		return string(data)
	}

	// Latin-1 maps every byte to the code point of the same number, so the
	// conversion is a cast per byte. Windows-1252 differs only in 0x80-0x9F,
	// which hold curly quotes and the euro sign -- never the accented
	// letters a bank's payee names are made of.
	runes := make([]rune, len(data))
	for i, b := range data {
		runes[i] = rune(b)
	}

	return string(runes)
}

// Fingerprint normalizes a header row into a comparable signature, so that
// the next export from the same bank finds its mapping. Case and surrounding
// whitespace vary between a bank's own exports and mean nothing.
func Fingerprint(header []string) string {
	parts := make([]string, 0, len(header))

	for _, h := range header {
		h = strings.ToLower(strings.Join(strings.Fields(h), " "))
		if h == "" {
			continue
		}

		parts = append(parts, h)
	}

	return strings.Join(parts, ",")
}

// Detect infers a Mapping from a header row.
func Detect(header []string) Mapping {
	m := Mapping{
		Date:        pick(header, dateNames),
		Description: pick(header, descriptionNames),
		Amount:      pick(header, amountNames),
		Balance:     pick(header, balanceNames),
		Holder:      pick(header, holderNames),
	}

	if m.Holder == m.Description {
		m.Holder = ""
	}

	if m.Amount == "" {
		m.Debit = pick(header, debitNames)
		m.Credit = pick(header, creditNames)
	}

	return m
}

// pick returns the first header cell matching a candidate name. Exact
// matches are tried across all candidates before any substring match, so a
// "Transaction Date" column is not claimed by the "transaction" description
// candidate just because that candidate is checked first.
func pick(header, candidates []string) string {
	for _, c := range candidates {
		for _, h := range header {
			if strings.EqualFold(strings.TrimSpace(h), c) {
				return strings.TrimSpace(h)
			}
		}
	}

	for _, c := range candidates {
		for _, h := range header {
			if strings.Contains(strings.ToLower(strings.TrimSpace(h)), c) {
				return strings.TrimSpace(h)
			}
		}
	}

	return ""
}

func indexHeader(header []string) map[string]int {
	index := make(map[string]int, len(header))

	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(h))
		if _, exists := index[key]; !exists {
			index[key] = i
		}
	}

	return index
}

func has(index map[string]int, column string) bool {
	_, ok := index[strings.ToLower(strings.TrimSpace(column))]

	return ok
}

func isBlank(row []string) bool {
	for _, f := range row {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}

	return true
}
