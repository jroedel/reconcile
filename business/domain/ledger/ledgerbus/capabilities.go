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

// The things a file can have, in order of importance: the first three are
// the ones a page shows at a glance.
const (
	CanBalances    = "balances"
	CanIdentifiers = "identifiers"
	CanWhole       = "whole"
	CanChecks      = "checks"
	CanHolders     = "holders"
	CanNumber      = "number"
)

// Main is how many of a file's capabilities are shown at a glance.
const Main = 3

// Capability is one thing a file has or lacks, and Why, a word the page
// says in a sentence of its own.
type Capability struct {
	Name string
	Can  Can
	Why  string
}

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

	out := []Capability{d.balances(), d.identifiers(), d.whole()}

	switch {
	case slices.ContainsFunc(recs, func(r importbus.Record) bool { return r.CheckNumber != "" }),
		d.Format == CSV && d.Mapping.Check != "":
		out = append(out, Capability{CanChecks, Has, "numbered"})
	default:
		out = append(out, Capability{CanChecks, Unknown, "none-here"})
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
			out = append(out, Capability{CanHolders, Has, "named"})
		case named > 0:
			out = append(out, Capability{CanHolders, Lacks, "some-unnamed"})
		default:
			out = append(out, Capability{CanHolders, Lacks, "unnamed"})
		}
	}

	if d.Result.Last4 != "" || d.Part != "" {
		out = append(out, Capability{CanNumber, Has, "named"})
	} else {
		out = append(out, Capability{CanNumber, Lacks, "unnamed"})
	}

	return out
}

// balances is whether the file states its own balances: the document's,
// not any a person typed, since the question is what the download can do.
// A balance on every row is as good as the two at the ends, and better.
func (d Draft) balances() Capability {
	res := d.Result

	switch {
	case len(res.Records) > 0 && !slices.ContainsFunc(res.Records, func(r importbus.Record) bool { return !r.HasBalance }):
		return Capability{CanBalances, Has, "every-row"}
	case res.Opening.Known && res.Closing.Known:
		return Capability{CanBalances, Has, "stated"}
	case res.LateBalance:
		return Capability{CanBalances, Lacks, "download-day"}
	default:
		return Capability{CanBalances, Lacks, "none"}
	}
}

// identifiers is whether the bank names each row, so that the same row
// from another file is known for itself rather than by its content.
func (d Draft) identifiers() Capability {
	recs := d.Result.Records

	switch {
	case d.Format == PDF:
		return Capability{CanIdentifiers, NotInFormat, "pdf"}
	case d.Format == CSV:
		return Capability{CanIdentifiers, Lacks, "csv"}
	case len(recs) == 0:
		return Capability{CanIdentifiers, Unknown, "no-rows"}
	case slices.ContainsFunc(recs, func(r importbus.Record) bool { return r.ExternalID == "" }):
		return Capability{CanIdentifiers, Lacks, "some-missing"}
	default:
		return Capability{CanIdentifiers, Has, "every-row"}
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
		return Capability{CanWhole, Unknown, "no-rows"}
	case d.Result.CutAt > 0, slices.ContainsFunc(recs, func(r importbus.Record) bool { return CutShort(r.Description) }):
		return Capability{CanWhole, Lacks, "cut"}
	default:
		return Capability{CanWhole, Has, "whole"}
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

// Everything reports whether together they have every main capability.
func (t Together) Everything() bool {
	return !slices.ContainsFunc(t.Capabilities, func(c Capability) bool { return c.Can != Has })
}

// Missing is the main capabilities none of them has.
func (t Together) Missing() []string {
	var out []string

	for _, c := range t.Capabilities {
		if c.Can != Has {
			out = append(out, c.Name)
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

			for c := range Main {
				best := Capability{}

				var has, lacks []string

				for _, r := range g {
					mine := props[r.i].Draft.Capabilities()[c]
					if best.Name == "" || mine.Can.rank() < best.Can.rank() {
						best = mine
					}

					if mine.Can == Has {
						has = appendOnce(has, props[r.i].File.Name)
					} else {
						lacks = append(lacks, props[r.i].File.Name)
					}
				}

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
