package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Claude makes the project its person said yes to, through a key, and
// changes it; the site marks it as a program's until an owner saves its
// settings (issue #77).
func TestClaudeMakesAProject(t *testing.T) {
	k := newKept(t)
	s := k.s
	org := strings.TrimPrefix(k.e.org, "/orgs/")

	made := s.change(t, http.MethodPost, "/api/v1/projects", k.key,
		map[string]string{"organization": org, "name": "Costs to be repaid", "note": "Paid here, repaid by the national account"}, http.StatusOK)

	id, _ := field(made, "project", "id").(string)
	page := "/projects/" + id

	if field(made, "project", "through") != "Claude" || field(made, "project", "organization") != org ||
		!strings.HasSuffix(field(made, "project", "url").(string), page) || !strings.HasSuffix(field(made, "project", "book_url").(string), page+"/book") {
		t.Errorf("the project made: %v", made)
	}

	// Found again by the next conversation, before it makes another.
	var found map[string]any
	for _, o := range list(t, s.get(t, "/api/v1/overview", k.key), "organizations") {
		for _, p := range field(o, "projects").([]any) {
			if field(p, "id") == id {
				found = p.(map[string]any)
			}
		}
	}

	if found == nil || found["through"] != "Claude" {
		t.Errorf("the project in the overview: %v", found)
	}

	// Marked on the site, in the organization's list and on its page.
	wantBody(t, k.e.owner.get(k.e.org), "Costs to be repaid", "by Claude")
	wantBody(t, k.e.owner.get(page), "A program made or changed this project through the API key")

	// Renamed, with its note kept since none was given.
	changed := s.change(t, http.MethodPut, "/api/v1"+page, k.key, map[string]string{"name": "Costs repaid by the national account"}, http.StatusOK)
	if field(changed, "project", "name") != "Costs repaid by the national account" || field(changed, "project", "note") != "Paid here, repaid by the national account" {
		t.Errorf("the project changed: %v", changed)
	}

	// What the site refuses, the API refuses, with the field to fix.
	refused := s.change(t, http.MethodPut, "/api/v1"+page, k.key, map[string]string{"starts": "next spring"}, http.StatusUnprocessableEntity)
	if field(refused, "error", "field") != "starts" {
		t.Errorf("a date in words: %v", refused)
	}

	s.change(t, http.MethodPost, "/api/v1/projects", k.key, map[string]string{"organization": org}, http.StatusBadRequest)

	// An owner saving its settings has looked at it: the mark goes.
	wantRedirect(t, k.e.owner.post(page+"/edit", url.Values{"name": {"Costs repaid by the national account"}, "note": {"Paid here, repaid by the national account"}}), page+"?done=saved")

	if body := k.e.owner.get(page).Body.String(); strings.Contains(body, "A program made or changed this project") {
		t.Error("the mark is still on the project after an owner saved it")
	}

	if body := k.e.owner.get(k.e.org).Body.String(); strings.Contains(body, "by Claude") {
		t.Error("the mark is still in the organization's list after an owner saved it")
	}

	// With no organization, it is the person's own.
	own := s.change(t, http.MethodPost, "/api/v1/projects", k.key, map[string]string{"name": "Retreat"}, http.StatusOK)
	if field(own, "project", "organization") != nil {
		t.Errorf("a project of the person's own: %v", own)
	}

	wantBody(t, k.e.owner.get("/"), "Retreat", "by Claude")
}
