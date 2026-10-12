package ledgerbus

import (
	"slices"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// What a file can do (issue #111).
//
// A bank offers the same days in several formats, and each lacks something:
// an OFX has the bank's identifier on every row and cuts names short; a CSV
// has the whole names and no identifier and no balance. Files of the same
// days already fill each other's gaps when both are imported (ledgerdb's
// Import, and ImportProposals' passes), but a person choosing what to
// download could not see any of it: which of the file's rows can be checked,
// whether importing it again could duplicate them, and that the CSV beside
// the OFX on the bank's site would have mended its names.
//
// So each read file says, of each thing, whether it has it, from what the
// reading already found. Nothing here changes what is imported: it explains
// what is decided elsewhere.

// Can is whether a file has a thing.
type Can string

// The answers, best first when files are taken together.
const (
	Has     Can = "yes"
	Unknown Can = "unknown"
	Lacks   Can = "no"

	// NotInFormat is a thing the file's format never has: a PDF's rows carry
	// no identifier of the bank's. Said apart from Lacks because no other
	// download of the same format would have it either.
	NotInFormat Can = "na"
)

// rank orders the answers for taking the best of several files.
func (c Can) rank() int {
	return slices.Index([]Can{Has, Unknown, Lacks, NotInFormat}, c)
}

// The things a file can have, in order of importance. The first four are
// what make an import sound -- its balances to check it by, at its ends and
// on its rows; the bank's own name for each row, so that the same row from
// another file is known; and the whole of each description -- and are the
// ones a page shows at a glance (Main).
const (
	CanEnds        = "ends"
	CanRows        = "rows"
	CanIdentifiers = "identifiers"
	CanWhole       = "whole"
	CanChecks      = "checks"
	CanHolders     = "holders"
	CanNumber      = "number"
)

// mains is the capabilities shown at a glance, in their order.
var mains = []string{CanEnds, CanRows, CanIdentifiers, CanWhole}

// Capability is one thing a file has or lacks, and Why, a word the page
// says in a sentence of its own.
//
// Better is, for a main one the file lacks, the kind of download that
// usually has it: "ofx", "csv" or "statement" (the bank's monthly PDF
// statement), never the file's own kind; empty when there is none to
// suggest. It is advice about formats in general, not about this bank:
// what a person can try the next time they download, said the moment the
// file is read (issue #111, as its reporter meant it).
type Capability struct {
	Name   string
	Can    Can
	Why    string
	Better string
}

// Main reports a capability shown at a glance.
func (c Capability) Main() bool { return slices.Contains(mains, c.Name) }

// Capabilities is what the draft's file can do, in order of importance,
// or nothing for a draft that read nothing: one with no columns chosen, or
// a document whose part nobody has chosen.
//
// The holders are said only for a card with several -- one whose
// statements arrive a file per holder, or whose file names two or more --
// where a charge's holder is who made it. For a card of one holder, a
// cross beside "Holders" on every download would be a worry about nothing.
func (d Draft) Capabilities() []Capability {
	if d.Unmapped || d.Choose {
		return nil
	}

	recs := d.Result.Records

	out := []Capability{d.ends()}

	// A card's downloads seldom carry a balance on each row, and nothing is
	// missing for it: its statement's balances are what it is checked by. So
	// for a card the mark is shown only when a file has one, rather than a
	// cross on every file.
	if rows := d.rows(); rows.Can == Has || d.Account.Kind != tenancybus.Card {
		out = append(out, rows)
	}

	out = append(out, d.identifiers(), d.whole())

	for i := range out {
		out[i].Better = better(out[i], d.Format)
	}

	switch {
	case slices.ContainsFunc(recs, func(r importbus.Record) bool { return r.CheckNumber != "" }),
		d.Format == CSV && d.Mapping.Check != "":
		out = append(out, Capability{Name: CanChecks, Can: Has, Why: "numbered"})
	default:
		out = append(out, Capability{Name: CanChecks, Can: Unknown, Why: "none-here"})
	}

	named, holders := 0, map[string]bool{}
	for _, r := range recs {
		if r.Holder != "" {
			named++
			holders[r.Holder] = true
		}
	}

	if d.Account.Kind == tenancybus.Card && (d.ByHolder || len(holders) > 1) {
		switch {
		case len(recs) > 0 && named == len(recs):
			out = append(out, Capability{Name: CanHolders, Can: Has, Why: "named"})
		case named > 0:
			out = append(out, Capability{Name: CanHolders, Can: Lacks, Why: "some-unnamed"})
		default:
			out = append(out, Capability{Name: CanHolders, Can: Lacks, Why: "unnamed"})
		}
	}

	if d.Result.Last4 != "" || d.Part != "" {
		out = append(out, Capability{Name: CanNumber, Can: Has, Why: "named"})
	} else {
		out = append(out, Capability{Name: CanNumber, Can: Lacks, Why: "unnamed"})
	}

	return out
}

// ends is whether the file states its balances at its start and its end:
// the document's, not any a person typed, since the question is what the
// download can do. A balance on every row gives both, the first row's less
// its amount and the last row's.
func (d Draft) ends() Capability {
	res := d.Result

	switch {
	case res.Opening.Known && res.Closing.Known:
		return Capability{Name: CanEnds, Can: Has, Why: "stated"}
	case everyRowBalanced(res.Records):
		return Capability{Name: CanEnds, Can: Has, Why: "from-rows"}
	case res.LateBalance:
		return Capability{Name: CanEnds, Can: Lacks, Why: "download-day"}
	case res.Closing.Known:
		return Capability{Name: CanEnds, Can: Lacks, Why: "closing-only"}
	default:
		return Capability{Name: CanEnds, Can: Lacks, Why: "none"}
	}
}

// rows is whether the file states a balance on each row, or at the end of
// each day: what finds a row missing from the middle of a file, and
// where (Check's ByBalances).
func (d Draft) rows() Capability {
	res := d.Result

	switch {
	case everyRowBalanced(res.Records):
		return Capability{Name: CanRows, Can: Has, Why: "every-row"}
	case len(res.Daily) > 0:
		return Capability{Name: CanRows, Can: Has, Why: "daily"}
	case slices.ContainsFunc(res.Records, func(r importbus.Record) bool { return r.HasBalance }):
		return Capability{Name: CanRows, Can: Lacks, Why: "some"}
	default:
		return Capability{Name: CanRows, Can: Lacks, Why: "none"}
	}
}

func everyRowBalanced(recs []importbus.Record) bool {
	return len(recs) > 0 && !slices.ContainsFunc(recs, func(r importbus.Record) bool { return !r.HasBalance })
}

// better is the kind of download that usually has what c lacks, other than
// the file's own kind: an OFX names its rows, a CSV keeps whole descriptions, and
// the bank's statement states its balances, and a checking account's its
// balance day by day. A guess about banks in general -- each says only
// "usually" on the page -- and none for what the file has.
func better(c Capability, format Format) string {
	if c.Can == Has || !c.Main() {
		return ""
	}

	var kinds []string

	switch c.Name {
	case CanIdentifiers:
		kinds = []string{"ofx"}
	case CanWhole:
		kinds = []string{"csv"}
	case CanEnds:
		kinds = []string{"statement"}
	case CanRows:
		kinds = []string{"statement", "csv"}
	}

	// A PDF is not taken for the statement: one that lacks balances is a
	// page printed from the bank's site, whose statement has them.
	own := map[Format]string{OFX: "ofx", CSV: "csv"}[format]

	for _, k := range kinds {
		if k != own {
			return k
		}
	}

	return ""
}

// identifiers is whether the bank names each row, so that the same row
// from another file is known for itself rather than by its content.
func (d Draft) identifiers() Capability {
	recs := d.Result.Records

	switch {
	case d.Format == PDF:
		return Capability{Name: CanIdentifiers, Can: NotInFormat, Why: "pdf"}
	case d.Format == CSV:
		return Capability{Name: CanIdentifiers, Can: Lacks, Why: "csv"}
	case len(recs) == 0:
		return Capability{Name: CanIdentifiers, Can: Unknown, Why: "no-rows"}
	case slices.ContainsFunc(recs, func(r importbus.Record) bool { return r.ExternalID == "" }):
		return Capability{Name: CanIdentifiers, Can: Lacks, Why: "some-missing"}
	default:
		return Capability{Name: CanIdentifiers, Can: Has, Why: "every-row"}
	}
}

// whole is whether the descriptions are whole. A bank that cut them says
// so with the width it cut at (an OFX's names), or the reader marked each
// cut one with an ellipsis, as a web page printed does; a file with
// neither is taken for whole, which is what it most likely is.
func (d Draft) whole() Capability {
	recs := d.Result.Records

	switch {
	case len(recs) == 0:
		return Capability{Name: CanWhole, Can: Unknown, Why: "no-rows"}
	case d.Result.CutAt > 0, slices.ContainsFunc(recs, func(r importbus.Record) bool { return CutShort(r.Description) }):
		return Capability{Name: CanWhole, Can: Lacks, Why: "cut"}
	default:
		return Capability{Name: CanWhole, Can: Has, Why: "whole"}
	}
}

// --- together ----------------------------------------------------------------

// Together is files of a batch whose days overlap in one account, and what
// they can do between them: each thing the best any of them has, and which
// of them bring it. Import them all and each transaction gets the best of
// them, because the store matches them row by row.
type Together struct {
	Account    types.ID
	Start, End types.Date

	// Keys are the proposals' (Proposal.Key), in the batch's order.
	Keys []string

	// Capabilities is the main ones, the best of the files'; Bring is, of
	// those some file has and another lacks, the files that have it, by
	// name.
	Capabilities []Capability
	Bring        map[string][]string
}

// Checkable reports whether together they have balances to check the
// days by: at their ends, or on their rows.
func (t Together) Checkable() bool { return t.Can(CanEnds) == Has || t.Can(CanRows) == Has }

// Everything reports whether together they have all an import needs to be
// sound: balances of either kind, the bank's identifiers, and whole
// descriptions.
func (t Together) Everything() bool { return len(t.Missing()) == 0 }

// Missing is what none of them has: "balances" when neither kind, and the
// identifiers and the whole descriptions.
func (t Together) Missing() []string {
	var out []string

	if !t.Checkable() {
		out = append(out, "balances")
	}

	for _, name := range []string{CanIdentifiers, CanWhole} {
		if t.Can(name) != Has {
			out = append(out, name)
		}
	}

	return out
}

// Can is what together they say of one capability.
func (t Together) Can(name string) Can {
	for _, c := range t.Capabilities {
		if c.Name == name {
			return c.Can
		}
	}

	return Unknown
}

// Togethers is the groups of a batch's proposals that cover overlapping
// days of one account, from more than one file: the only ones with
// anything to say about each other. A proposal with no account, or not
// read, or of no days, is in none.
func Togethers(props []Proposal) []Together {
	byAccount := map[types.ID][]dated{}

	var order []types.ID

	for i, p := range props {
		st := p.Draft.Statement
		if p.Problem != nil || p.Account.Zero() || p.Draft.Capabilities() == nil || st.Start.Zero() || st.End.Zero() {
			continue
		}

		if _, ok := byAccount[p.Account]; !ok {
			order = append(order, p.Account)
		}

		byAccount[p.Account] = append(byAccount[p.Account], dated{i, st.Start, st.End})
	}

	var out []Together

	for _, account := range order {
		reads := byAccount[account]

		// Grouped by overlap: sorted by start, a read joins the group
		// whose days reach its start.
		slices.SortStableFunc(reads, func(a, b dated) int { return compareDates(a.start, b.start) })

		var groups [][]dated

		for _, r := range reads {
			if n := len(groups); n > 0 && !groupEnd(groups[n-1]).Before(r.start) {
				groups[n-1] = append(groups[n-1], r)

				continue
			}

			groups = append(groups, []dated{r})
		}

		for _, g := range groups {
			files := map[types.ID]bool{}
			for _, r := range g {
				files[props[r.i].File.ID] = true
			}

			if len(files) < 2 {
				continue
			}

			slices.SortFunc(g, func(a, b dated) int { return a.i - b.i })

			t := Together{Account: account, Start: g[0].start, End: groupEnd(g), Bring: map[string][]string{}}

			for _, r := range g {
				t.Keys = append(t.Keys, props[r.i].Key())

				if r.start.Before(t.Start) {
					t.Start = r.start
				}
			}

			for _, name := range mains {
				best := Capability{Name: name, Can: NotInFormat}

				var has, lacks []string

				asked := false

				for _, r := range g {
					mine, ok := capability(props[r.i].Draft.Capabilities(), name)
					asked = asked || ok

					if mine.Can.rank() < best.Can.rank() {
						best = mine
					}

					if mine.Can == Has {
						has = appendOnce(has, props[r.i].File.Name)
					} else {
						lacks = append(lacks, props[r.i].File.Name)
					}
				}

				// A card's balance on each row, asked of none of them, is
				// nothing to say of them together either.
				if !asked {
					continue
				}

				best.Better = ""
				t.Capabilities = append(t.Capabilities, best)

				if len(has) > 0 && len(lacks) > 0 {
					t.Bring[best.Name] = has
				}
			}

			out = append(out, t)
		}
	}

	return out
}

// capability is the one by name, and whether the file was asked it: not a
// card's balance on each row, when it has none.
func capability(caps []Capability, name string) (Capability, bool) {
	for _, c := range caps {
		if c.Name == name {
			return c, true
		}
	}

	return Capability{Name: name, Can: Unknown}, false
}

// dated is a proposal of a batch, by its index, and its days.
type dated struct {
	i          int
	start, end types.Date
}

// groupEnd is the last day any of a group reaches.
func groupEnd(g []dated) types.Date {
	end := g[0].end
	for _, r := range g[1:] {
		if end.Before(r.end) {
			end = r.end
		}
	}

	return end
}

// compareDates orders two days.
func compareDates(a, b types.Date) int {
	switch {
	case a.Before(b):
		return -1
	case b.Before(a):
		return 1
	}

	return 0
}

// appendOnce adds v to s unless it is there.
func appendOnce(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}

	return append(s, v)
}
