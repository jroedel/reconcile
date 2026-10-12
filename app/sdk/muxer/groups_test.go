package muxer

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var (
	groupForm    = regexp.MustCompile(`action="(/category-groups/[0-9a-f]+)/rename"`)
	categoryForm = regexp.MustCompile(`action="(/categories/[0-9a-f]+)/rename" class="inline">\s*<label[^>]*>[^<]*</label>\s*<input[^>]*value="([^"]+)"`)
	groupFigure  = regexp.MustCompile(`href="([^"]*/parts\?[^"]*group=[^"]+)"`)
)

// addedGroup adds a group of a kind to a list and returns its path.
func addedGroup(t *testing.T, b *browser, list, name, kind string) string {
	t.Helper()

	wantRedirect(t, b.post(list+"/groups", url.Values{"name": {name}, "kind": {kind}}), list+"?done=group-added")

	m := groupForm.FindStringSubmatch(b.get(list).Body.String())
	if m == nil {
		t.Fatalf("no group on %s", list)
	}

	return m[1]
}

// categoriesOn is a list's categories' paths, by name.
func categoriesOn(b *browser, list string) map[string]string {
	out := map[string]string{}
	for _, m := range categoryForm.FindAllStringSubmatch(b.get(list).Body.String(), -1) {
		out[html.UnescapeString(m[2])] = m[1]
	}

	return out
}

// Groceries and the electric bill under one group of the organization's
// list: the categories page shows them under it, a report by group shows
// one line for both, and its figure opens both; the group refuses a
// category of another kind, an archived one takes no more, and the
// history says each step.
func TestGroupsOfCategories(t *testing.T) {
	t.Parallel()

	e, txs, _, _ := sorted(t)
	list := e.org + "/categories"

	wantRedirect(t, e.owner.post(list, url.Values{"name": {"Offerings"}, "kind": {"income"}}), list+"?done=added")

	choices := options(e.owner.get(txs["CORNER GROCERY"]).Body.String())

	for name, category := range map[string]string{"CORNER GROCERY": "Groceries", "ELECTRIC CO": "Utilities", "PARISH OFFERTORY": "Offerings"} {
		wantRedirect(t, e.owner.post(txs[name], url.Values{
			"action": {"save"}, "amount-0": {""}, "category-0": {choices[category]}, "project-0": {""}, "memo-0": {""},
		}), e.account+"/transactions?month=2026-07&done=sorted")
	}

	house := addedGroup(t, e.owner, list, "House", "expense")
	cats := categoriesOn(e.owner, list)

	for _, name := range []string{"Groceries", "Utilities"} {
		wantRedirect(t, e.owner.post(cats[name]+"/group", url.Values{"group": {strings.TrimPrefix(house, "/category-groups/")}}), list+"?done=grouped")
	}

	wantRedirect(t, e.owner.post(cats["Offerings"]+"/group", url.Values{"group": {strings.TrimPrefix(house, "/category-groups/")}}), list+"?problem=group-kind")
	wantRedirect(t, e.owner.post(list+"/groups", url.Values{"name": {"house"}, "kind": {"income"}}), list+"?problem=group-duplicate")
	wantRedirect(t, e.owner.post(list+"/groups", url.Values{"name": {" "}, "kind": {"income"}}), list+"?problem=group-name")

	wantBody(t, e.owner.get(list), "<h3>House", "Under no group")

	report := e.org + "/reports/income-expenses-by-month?from=2026-07&to=2026-07"
	wantBody(t, e.owner.get(report), "Groceries", "Utilities", `name="detail" value="group"`)

	byGroup := e.owner.get(report + "&detail=group").Body.String()
	if !strings.Contains(byGroup, "House") || strings.Contains(byGroup, "<th scope=\"row\">Groceries") || !strings.Contains(byGroup, "-$153.99") {
		t.Errorf("by group:\n%s", byGroup)
	}

	m := groupFigure.FindStringSubmatch(byGroup)
	if m == nil {
		t.Fatalf("the group's figures open nothing:\n%s", byGroup)
	}

	parts := e.owner.get(html.UnescapeString(m[1])).Body.String()
	if !strings.Contains(parts, "House") || !strings.Contains(parts, "CORNER GROCERY") || !strings.Contains(parts, "ELECTRIC CO") || strings.Contains(parts, "PARISH OFFERTORY") {
		t.Errorf("the group's parts:\n%s", parts)
	}

	saved := saveReport(t, e.owner, e.org, url.Values{"style": {"income-expenses-by-month"}, "title": {"P&L"}, "detail": {"group"}})
	wantBody(t, e.owner.get(saved+"?from=2026-07&to=2026-07"), "House", "-$153.99")
	wantBody(t, e.owner.get(saved+"/edit"), `name="detail" value="group" checked`)

	wantRedirect(t, e.owner.post(house+"/rename", url.Values{"name": {"The house"}}), list+"?done=group-renamed")
	wantRedirect(t, e.owner.post(house+"/archive", url.Values{"archived": {"1"}}), list+"?done=group-archived")
	wantRedirect(t, e.owner.post(cats["Groceries"]+"/group", url.Values{"group": {""}}), list+"?done=ungrouped")
	wantRedirect(t, e.owner.post(cats["Groceries"]+"/group", url.Values{"group": {strings.TrimPrefix(house, "/category-groups/")}}), list+"?problem=group-archived")

	// Still holding the electric bill, so it is shown over it, archived.
	wantBody(t, e.owner.get(list), "<h3>The house <span class=\"badge\">archived</span>")
	wantBody(t, e.owner.get(saved+"?from=2026-07&to=2026-07"), "The house", "-$120.00", "Groceries")

	wantBody(t, e.owner.get(e.org), "added the group of categories House", "put the category Groceries under the group House",
		"renamed the group of categories House to The house", "archived the group of categories The house",
		"took the category Groceries out of the group The house")

	if rec := e.owner.post(cats["Groceries"]+"/group", url.Values{"group": {"not-an-id"}}); rec.Code != http.StatusNotFound {
		t.Errorf("a group that is not one: %d", rec.Code)
	}
}
