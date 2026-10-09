package pdftext

import "testing"

// SetCandidates replaces where pdftotext is looked for, for one test.
func SetCandidates(t *testing.T, paths []string) {
	old := candidates
	candidates = paths

	t.Cleanup(func() { candidates = old })
}
