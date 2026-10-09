package rulebus

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/jroedel/reconcile/business/types"
)

// Suggesting a category for a transaction nobody has sorted, from the ones
// a person has (docs/sorting.md). Lifted from eumaeus' categorizebus
// suggest.go, whose reasoning holds here: the treasurer's own sorting is
// better evidence than any rule an outsider could guess, so this extends it
// and, where it says nothing, says nothing too.
//
// Two ways, in the order they can be trusted:
//
//  1. The same payee (Payee) has been sorted before, mostly one way.
//  2. No transaction from that payee was, but one from a payee whose name
//     is nearly the same string was: "JOES PIZZA" and "JOE'S PIZZA".
//     Compared by character trigrams, because whole words do not see that
//     those are the same word.
//
// eumaeus' third way, a distinctive word that has meant one thing across
// several payees, is left out: it needs more history than one parish
// account has, and it is the way most able to be confidently wrong.
//
// A suggestion is only ever a preselected choice that a person saves. It
// is never stored, and a rule's sorting is never evidence: a rule agreeing
// with itself would look like a person agreeing with it.

// Example is one transaction a person sorted: its description as the bank
// wrote it, and the category they chose.
type Example struct {
	Description string
	CategoryID  types.ID
}

// Method is how a suggestion was reached.
type Method string

const (
	SamePayee   Method = "payee"
	SimilarName Method = "similar"
)

// The bars a suggestion clears. Two examples agreeing four times in five:
// low in absolute terms, but the alternative to a suggestion is a choice
// left empty, which is a perfectly good outcome, and a wrong suggestion
// accepted costs more than none.
const (
	MinExamples   = 2
	MinAgreement  = 0.8
	MinSimilarity = 0.6
)

// Suggestion is a category, and the evidence for it as a page says it:
// Agree of Of earlier transactions from Payee were that category.
type Suggestion struct {
	CategoryID types.ID
	Method     Method
	Payee      string // the payee the evidence is about, as the bank wrote it
	Agree, Of  int
}

type bucket struct {
	key      string
	label    string
	counts   map[types.ID]int
	total    int
	trigrams map[string]bool
}

// Suggester is what was learned from an account's examples. It holds
// nothing but the evidence.
type Suggester struct {
	buckets map[string]*bucket
	ordered []*bucket
}

// Learn learns from examples. One without a category teaches nothing:
// "not sorted yet" is the absence of a decision, not one.
func Learn(examples []Example) Suggester {
	s := Suggester{buckets: map[string]*bucket{}}

	for _, e := range examples {
		if e.CategoryID.Zero() {
			continue
		}

		label := Payee(e.Description)
		key := Key(label)

		if key == "" {
			continue
		}

		b, ok := s.buckets[key]
		if !ok {
			b = &bucket{key: key, label: label, counts: map[types.ID]int{}, trigrams: trigrams(label)}
			s.buckets[key] = b
			s.ordered = append(s.ordered, b)
		}

		b.counts[e.CategoryID]++
		b.total++
	}

	// The same input gives the same answer: map order would otherwise
	// decide which of two equally similar payees was asked.
	slices.SortFunc(s.ordered, func(a, b *bucket) int { return strings.Compare(a.key, b.key) })

	return s
}

// Suggest is a category for a description, if the evidence clears the bars.
func (s Suggester) Suggest(description string) (Suggestion, bool) {
	label := Payee(description)
	key := Key(label)

	if key == "" {
		return Suggestion{}, false
	}

	if b, ok := s.buckets[key]; ok {
		return b.suggest(SamePayee, 1)
	}

	want := trigrams(label)

	var (
		best  *bucket
		score float64
	)

	for _, b := range s.ordered {
		if d := dice(want, b.trigrams); d > score {
			best, score = b, d
		}
	}

	if best == nil || score < MinSimilarity {
		return Suggestion{}, false
	}

	// A resemblance is weaker evidence than a name, so the agreement it
	// needs is scaled by how close the names are.
	return best.suggest(SimilarName, score)
}

// suggest is the bucket's most common category, if enough examples agree
// on it once their agreement is weighed by how far the names are alike.
func (b *bucket) suggest(m Method, likeness float64) (Suggestion, bool) {
	if b.total < MinExamples {
		return Suggestion{}, false
	}

	best := slices.SortedFunc(maps.Keys(b.counts), func(x, y types.ID) int {
		return cmp.Or(b.counts[y]-b.counts[x], strings.Compare(x.String(), y.String()))
	})[0]

	if float64(b.counts[best])/float64(b.total)*likeness < MinAgreement {
		return Suggestion{}, false
	}

	return Suggestion{CategoryID: best, Method: m, Payee: b.label, Agree: b.counts[best], Of: b.total}, true
}

// trigrams is a name's three-character windows, letters and digits only.
func trigrams(name string) map[string]bool {
	var b strings.Builder

	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}

	flat := b.String()
	if len(flat) < 3 {
		return nil
	}

	out := make(map[string]bool, len(flat))
	for i := 0; i+3 <= len(flat); i++ {
		out[flat[i:i+3]] = true
	}

	return out
}

// dice is twice the shared members over the total: more forgiving than
// Jaccard for short strings, which is what payees are.
func dice(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	shared := 0

	for t := range a {
		if b[t] {
			shared++
		}
	}

	return 2 * float64(shared) / float64(len(a)+len(b))
}
