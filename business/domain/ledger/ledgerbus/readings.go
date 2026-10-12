package ledgerbus

import (
	"slices"
	"sync"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/shapes"
)

// A PDF's reading is remembered, by the file's SHA-256, for as long as the
// server runs and the file is among the last few read.
//
// A bulk import read each file far more often than it changed, which is
// never: once to list it, once more for each account's part of it to
// propose, and once more for each part to import -- eleven times for a
// consolidated statement of five accounts, each a pdftotext run, which
// runs one at a time, and a parse. Thirty-eight such statements took 26
// seconds on a desk (2026-10-12), past the 15 the proxy in front of the
// server waits before it answers "Bad Gateway", and the import stopped
// wherever it had got to.
//
// Remembering is safe because nothing a reading depends on can change
// under it: the file's bytes are named by their hash and never rewritten
// (filebus), and what reads them -- pdftotext, the reader, the built-in
// declarations -- is the binary's, and is replaced only with the binary.
// A file that could not be read at all is not remembered: whether
// pdftotext was there, or the request still wanted an answer, is not the
// file's to say.
type reading struct {
	text, producer string

	res         importbus.Result
	err         error
	shape       importbus.Shape
	declaration shapes.Declaration
}

// readings is a few readings, the most recently used kept.
type readings struct {
	mu    sync.Mutex
	byKey map[string]reading
	order []string
}

// keptReadings is how many files' readings are kept: more than a bulk
// import takes at once (40), and a few megabytes of text at most.
const keptReadings = 64

func (rs *readings) get(key string) (reading, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	r, ok := rs.byKey[key]
	if ok {
		rs.touch(key)
	}

	return r, ok
}

func (rs *readings) put(key string, r reading) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if rs.byKey == nil {
		rs.byKey = map[string]reading{}
	}

	if _, ok := rs.byKey[key]; !ok && len(rs.order) >= keptReadings {
		delete(rs.byKey, rs.order[0])
		rs.order = rs.order[1:]
	}

	rs.byKey[key] = r
	rs.touch(key)
}

// touch moves key to the end of the order, the most recently used.
func (rs *readings) touch(key string) {
	rs.order = append(slices.DeleteFunc(rs.order, func(k string) bool { return k == key }), key)
}

// own is a reading's result for one draft to change: its rows are turned
// round for a card and given their holder in place, and a remembered
// reading must come back the same to the next draft that asks.
func own(res importbus.Result) importbus.Result {
	res.Records = slices.Clone(res.Records)
	res.Warnings = slices.Clone(res.Warnings)
	res.Daily = slices.Clone(res.Daily)
	res.Accounts = slices.Clone(res.Accounts)

	for i := range res.Accounts {
		res.Accounts[i].Result = own(res.Accounts[i].Result)
	}

	return res
}
