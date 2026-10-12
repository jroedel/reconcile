package ofxsource

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// OFX comes in two dialects and this parser reads both.
//
// OFX 1.x (which is what nearly every .qfx and most .ofx downloads are) is
// SGML: closing tags are optional on leaf elements, so a transaction looks like
//
//	<STMTTRN>
//	<TRNTYPE>DEBIT
//	<DTPOSTED>20260714120000[-5:EST]
//	<TRNAMT>-33.99
//	<FITID>202607140001
//	<NAME>CORNER GROCERY
//	</STMTTRN>
//
// OFX 2.x is well-formed XML with every tag closed. An off-the-shelf XML parser
// handles the second and chokes on the first, so this is a small tokenizer that
// treats a closing tag as optional — which makes both dialects fall out of one
// code path.
//
// It reads only the subset needed to build ledger records. A full OFX
// implementation is a large document; this is deliberately not one.

// ErrNotOFX reports a file that is not OFX at all.
var ErrNotOFX = errors.New("not an OFX document")

// statement is one account's worth of parsed OFX.
type statement struct {
	AccountID     string // the institution's account number, from ACCTID
	BankID        string // routing number, from BANKID
	AccountType   string // CHECKING, SAVINGS, CREDITLINE, ...
	Currency      string
	Transactions  []ofxTransaction
	LedgerBalance string
	BalanceAsOf   string

	// Start and End are the period the statement covers, from
	// BANKTRANLIST's DTSTART and DTEND.
	Start, End string
}

// ofxTransaction is one STMTTRN, still in native OFX strings.
type ofxTransaction struct {
	Type     string // TRNTYPE: DEBIT, CREDIT, CHECK, DEP, XFER, POS, ATM, FEE, INT, DIV
	Posted   string // DTPOSTED
	Amount   string // TRNAMT, a signed decimal
	FITID    string // the institution's transaction id — the whole point
	Name     string // NAME, historically capped at 32 characters
	Memo     string // MEMO, often carries the rest of a truncated NAME
	CheckNum string

	// NameWidth is how many characters NAME was printed in, trailing
	// spaces and all: a bank that cuts every name at one width cuts some
	// just after a space, and those, trimmed, would look shorter than the
	// width they were cut at (cutWidth).
	NameWidth int
}

// token is one tag or run of text from the document.
type token struct {
	tag     string // upper-cased tag name, "" for text
	closing bool
	text    string
	width   int // the text's characters to the end of its line, trailing spaces kept
}

// lex splits an OFX body into tags and the text between them.
//
// Everything before the first <OFX> is the header block — either
// "OFXHEADER:100" style key/value lines (1.x) or an <?OFX?> processing
// instruction (2.x). Neither carries anything this parser needs, so it is
// skipped rather than parsed.
func lex(body string) ([]token, error) {
	start := strings.Index(strings.ToUpper(body), "<OFX>")
	if start < 0 {
		return nil, ErrNotOFX
	}

	body = body[start:]

	var tokens []token

	for i := 0; i < len(body); {
		switch body[i] {
		case '<':
			end := strings.IndexByte(body[i:], '>')
			if end < 0 {
				return nil, fmt.Errorf("%w: a tag is not closed at byte %d", ErrNotOFX, i)
			}

			raw := strings.TrimSpace(body[i+1 : i+end])
			i += end + 1

			// Skip processing instructions and comments outright.
			if raw == "" || strings.HasPrefix(raw, "?") || strings.HasPrefix(raw, "!") {
				continue
			}

			closing := strings.HasPrefix(raw, "/")
			raw = strings.TrimPrefix(raw, "/")

			// Drop attributes; OFX leaf elements do not use them meaningfully.
			if space := strings.IndexAny(raw, " \t"); space >= 0 {
				raw = raw[:space]
			}

			tokens = append(tokens, token{tag: strings.ToUpper(raw), closing: closing})

		default:
			next := strings.IndexByte(body[i:], '<')
			if next < 0 {
				next = len(body) - i
			}

			raw := body[i : i+next]

			if text := strings.TrimSpace(raw); text != "" {
				line := strings.TrimLeft(raw, " \t\r\n")
				if end := strings.IndexAny(line, "\r\n"); end >= 0 {
					line = line[:end]
				}

				tokens = append(tokens, token{text: unescape(text), width: utf8.RuneCountInString(line)})
			}

			i += next
		}
	}

	return tokens, nil
}

// entity is a character written as SGML and XML write the ones markup
// uses: by name, or by number.
var entity = regexp.MustCompile(`&(lt|gt|amp|quot|apos|nbsp|#[0-9]{1,7}|#x[0-9a-fA-F]{1,6});`)

// unescape writes the characters a value's entities stand for: a shop's
// "BEACH &amp; BREW" is "BEACH & BREW", as the bank's CSV says it. Only the
// entities OFX's two dialects define, and numbers, and not HTML's whole
// list (html.UnescapeString): that reads some names with no semicolon,
// and a payee "SMITH&REGAN" would come out with a registered-trademark
// sign in it. An ampersand a bank wrote bare is left as it is.
func unescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}

	return entity.ReplaceAllStringFunc(s, func(e string) string {
		name := e[1 : len(e)-1]

		switch name {
		case "lt":
			return "<"
		case "gt":
			return ">"
		case "amp":
			return "&"
		case "quot":
			return `"`
		case "apos":
			return "'"
		case "nbsp":
			return " "
		}

		base, digits := 10, name[1:]
		if strings.HasPrefix(digits, "x") {
			base, digits = 16, digits[1:]
		}

		n, err := strconv.ParseInt(digits, base, 32)
		if err != nil || !utf8.ValidRune(rune(n)) || n == 0 {
			return e
		}

		return string(rune(n))
	})
}

// parse walks the token stream and pulls out every statement it finds.
//
// The walk is a small state machine rather than a tree build, because the
// SGML dialect has no reliable nesting to build a tree from: a leaf's value is
// simply the text that follows it, terminated by whatever tag comes next.
func parse(r io.Reader) ([]statement, error) {
	// OFX files are one statement download — kilobytes to a few megabytes.
	// Reading it whole keeps the tokenizer simple, and the size is bounded by
	// what a bank will export.
	data, err := io.ReadAll(bufio.NewReader(r))
	if err != nil {
		return nil, fmt.Errorf("ofxsource: reading document: %w", err)
	}

	tokens, err := lex(string(data))
	if err != nil {
		return nil, err
	}

	var (
		statements []statement
		current    *statement
		txn        *ofxTransaction
		pending    string // the leaf tag awaiting its text
		inBalance  bool
	)

	// newStatement starts an account block. Both bank and credit-card
	// statements are handled; they differ only in which wrapper announces them.
	newStatement := func() {
		if current != nil {
			statements = append(statements, *current)
		}

		current = &statement{}
	}

	for _, t := range tokens {
		if t.tag == "" {
			// Text: it belongs to the leaf tag most recently opened.
			assign(current, txn, pending, t, inBalance)
			pending = ""

			continue
		}

		if t.closing {
			switch t.tag {
			case "STMTTRN":
				if current != nil && txn != nil {
					current.Transactions = append(current.Transactions, *txn)
				}

				txn = nil
			case "LEDGERBAL":
				inBalance = false
			}

			pending = ""

			continue
		}

		switch t.tag {
		case "STMTRS", "CCSTMTRS":
			newStatement()
		case "STMTTRN":
			if current == nil {
				// A transaction outside any statement block: malformed, but
				// recoverable — treat it as opening one.
				newStatement()
			}

			txn = &ofxTransaction{}
		case "LEDGERBAL":
			inBalance = true
		case "AVAILBAL":
			// Closed here as well as by </LEDGERBAL>, because SGML-style OFX
			// omits closing tags entirely. Without this, a file in that dialect
			// leaves the balance block "open" and the AVAILABLE balance
			// overwrites the ledger one — which on a credit card is not a small
			// error: it would report four thousand dollars of unused credit as
			// four thousand dollars of assets.
			inBalance = false
		default:
			pending = t.tag
		}
	}

	if current != nil {
		statements = append(statements, *current)
	}

	if len(statements) == 0 {
		return nil, fmt.Errorf("%w: no statement found", ErrNotOFX)
	}

	return statements, nil
}

// assign stores a leaf value against whatever is currently open.
func assign(st *statement, txn *ofxTransaction, tag string, t token, inBalance bool) {
	text := t.text

	if tag == "" {
		return
	}

	if txn != nil {
		switch tag {
		case "TRNTYPE":
			txn.Type = text
		case "DTPOSTED":
			txn.Posted = text
		case "TRNAMT":
			txn.Amount = text
		case "FITID":
			txn.FITID = text
		case "NAME":
			txn.Name, txn.NameWidth = text, t.width
		case "MEMO":
			txn.Memo = text
		case "CHECKNUM":
			txn.CheckNum = text
		}

		return
	}

	if st == nil {
		return
	}

	switch {
	case inBalance && tag == "BALAMT":
		st.LedgerBalance = text
	case inBalance && tag == "DTASOF":
		st.BalanceAsOf = text
	case tag == "ACCTID":
		st.AccountID = text
	case tag == "BANKID":
		st.BankID = text
	case tag == "ACCTTYPE":
		st.AccountType = text
	case tag == "CURDEF":
		st.Currency = text
	case tag == "DTSTART":
		st.Start = text
	case tag == "DTEND":
		st.End = text
	}
}

// parseDate reads an OFX timestamp.
//
// The format is YYYYMMDD optionally followed by HHMMSS, an optional
// .XXX fraction, and an optional [offset:TZ] suffix — "20260714120000.000[-5:EST]".
//
// Only the date is kept. The clock time is discarded deliberately: a bank can
// report the same charge with two different posting times across two exports,
// and a date that shifted by a timezone would move a transaction between months
// at the boundary. The ledger stores dates, not instants.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)

	// Drop the timezone suffix before anything else.
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[:i]
	}

	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}

	if len(s) < 8 {
		return time.Time{}, fmt.Errorf("ofxsource: %q is too short to be an OFX date", s)
	}

	t, err := time.Parse("20060102", s[:8])
	if err != nil {
		return time.Time{}, fmt.Errorf("ofxsource: date %q: %w", s, err)
	}

	return t, nil
}

// describe builds a readable description from NAME, MEMO and CHECKNUM.
//
// OFX historically caps NAME at 32 characters and banks routinely spill the
// rest into MEMO, so the two are joined — but only when MEMO adds something.
// Plenty of banks repeat NAME in MEMO verbatim, or put a useless constant
// there, and concatenating those would double every payee on the dashboard.
//
// cut is a NAME the bank cut short at its width (cutWidth). When it is the
// whole description it ends in an ellipsis, which is what it is: the
// beginning of a longer one. That is what lets the same charge from the
// bank's CSV, which has the rest, be found as the one already here, and
// lend it the rest (ledgerbus.Truncated).
func describe(t ofxTransaction, cut bool) string {
	name := strings.Join(strings.Fields(t.Name), " ")
	memo := strings.Join(strings.Fields(t.Memo), " ")

	description := name

	switch {
	case memo == "":
	case name == "":
		description = memo
	case strings.EqualFold(memo, name):
		// MEMO repeats NAME; adding it would just double the payee.
	case strings.HasPrefix(strings.ToUpper(memo), strings.ToUpper(name)):
		// MEMO is the untruncated form of NAME. Prefer the longer one.
		description = memo
	default:
		description = name + " " + memo
	}

	if t.CheckNum != "" && !strings.Contains(description, t.CheckNum) {
		description = strings.TrimSpace(description + " check " + t.CheckNum)
	}

	if cut && description == name && name != "" {
		description += "…"
	}

	return description
}
