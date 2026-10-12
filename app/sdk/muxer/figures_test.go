package muxer

import (
	"net/url"
	"testing"
)

// The organization's lists say how much each account and project has held,
// and the front page says it of those on their own (issue #106).
func TestTheListsFigures(t *testing.T) {
	t.Parallel()

	e, txs, _, _ := sorted(t)

	choices := options(e.owner.get(txs["ELECTRIC CO"]).Body.String())
	wantRedirect(t, e.owner.post(txs["ELECTRIC CO"], url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {choices["Utilities"]}, "project-0": {choices["World Youth Day"]}, "memo-0": {""},
	}), e.account+"/transactions?month=2026-07&done=sorted")

	page := e.owner.get(e.org)
	wantBody(t, page,
		"From 2026-07-01 to 2026-07-28: 6 transactions, money in $1,250.00, money out -$160.99.",
		"Balance on 2026-07-28: $1,089.01",
		"2026-07-28 · 1 transactions",
		"Short by $120.00")

	imported(t, e.owner, e.ownAccount, "checking-july.csv", july)
	wantBody(t, e.owner.get("/"), "From 2026-07-01 to 2026-07-28: 6 transactions")
}
