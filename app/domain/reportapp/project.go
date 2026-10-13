package reportapp

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"

	"github.com/jroedel/reconcile/app/sdk/mid"
)

// projectWords is how many lines the project-words text has.
const projectWords = 11

// projectSheet is a project's report as a CSV: a row per part, with the
// run it is in and the running balance after it, so that a spreadsheet
// can be filtered to one retreat and still show where the project stood.
// A currency's block opens with what was brought forward, when the
// period starts after the project did, and closes with its balance.
func (a app) projectSheet(w http.ResponseWriter, r *http.Request, v reportView) {
	said, err := a.lines(mid.LangFrom(r.Context()), "project-words", projectWords)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	header, currency, unsorted, forward, end := said[:7], said[7], said[8], said[9], said[10]

	var buf bytes.Buffer

	buf.WriteString("\ufeff")

	cw := csv.NewWriter(&buf)
	cw.UseCRLF = true

	rep := v.Project

	for i, s := range rep.Sections {
		if i > 0 {
			_ = cw.Write(nil)
		}

		_ = cw.Write([]string{currency + ": " + s.Currency})
		_ = cw.Write(header)

		if s.Before > 0 {
			_ = cw.Write([]string{forward, "", "", "", "", "", s.BroughtForward.String()})
		}

		for _, e := range s.Episodes {
			run := e.From.String() + " – " + e.To.String()

			for _, p := range e.Parts {
				description, category := p.Description, p.CategoryName
				if p.OwnDescription != "" {
					description = p.OwnDescription
				}

				if p.Split.CategoryID.Zero() {
					category = unsorted
				}

				_ = cw.Write([]string{run, p.PostedOn.String(), cell(p.AccountName), cell(description), cell(category), p.Split.Amount.String(), p.Balance.String()})
			}
		}

		_ = cw.Write([]string{end, "", "", "", "", "", s.Balance.String()})
	}

	name := "project.csv"
	if rep.First != "" {
		name = fmt.Sprintf("project_%s_%s.csv", rep.First, rep.Last)
	}

	a.sheet(w, r, cw, &buf, name)
}
