package apiapp

import (
	"errors"
	"net/http"
	"time"

	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/foundation/web"
)

// Projects through a key (issue #77): Claude makes the project a person
// asked for, so that it can sort into it, rather than waiting for them to
// make it on the site first. Making one is tenancybus.CreateProject and
// changing one is EditProject, the methods the project pages call, with
// the same permissions: making one in an organization takes a bookkeeper's
// role there, changing one an owner's.
//
// Archiving stays the person's, on the project's page, as removing a
// statement or a receipt does: it is the one thing about a project that
// hides what was put into it.
//
// A project made or changed here is marked with the key on the site until
// a person saves it on its page (Project.Via), as a sorting rule is.

// projectOut is a project as the books' answers give it.
type projectOut struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Organization string `json:"organization,omitempty"`
	Starts       string `json:"starts,omitempty"`
	Ends         string `json:"ends,omitempty"`
	Note         string `json:"note,omitempty"`
	Through      string `json:"through,omitempty"`
	URL          string `json:"url"`
	BookURL      string `json:"book_url"`
}

func (rd *reader) projectOut(p tenancybus.Project) projectOut {
	out := projectOut{
		ID: p.ID.String(), Name: p.Name, Starts: p.StartsOn.String(), Ends: p.EndsOn.String(), Note: p.Note, Through: p.Via,
		URL: rd.url("/projects/" + p.ID.String()), BookURL: rd.url("/projects/" + p.ID.String() + "/book"),
	}

	if !p.OrgID.Zero() {
		out.Organization = p.OrgID.String()
	}

	return out
}

// projectsOf is a list of projects as get_overview gives them.
func (rd *reader) projectsOf(list []tenancybus.Project) []projectOut {
	out := make([]projectOut, 0, len(list))
	for _, p := range list {
		out = append(out, rd.projectOut(p))
	}

	return out
}

// projectIn is what create_project and change_project take. Each field is
// a pointer so that change_project can tell a field left out, which keeps
// what the project has, from one given empty, which clears it.
type projectIn struct {
	Organization string  `json:"organization"`
	Name         *string `json:"name"`
	Starts       *string `json:"starts"`
	Ends         *string `json:"ends"`
	Note         *string `json:"note"`
}

// over is f with what in gives written over it.
func (in projectIn) over(f tenancybus.ProjectFields) tenancybus.ProjectFields {
	if in.Name != nil {
		f.Name = *in.Name
	}

	if in.Starts != nil {
		f.StartsOn = *in.Starts
	}

	if in.Ends != nil {
		f.EndsOn = *in.Ends
	}

	if in.Note != nil {
		f.Note = *in.Note
	}

	return f
}

func (a app) createProject(w http.ResponseWriter, r *http.Request) {
	var in projectIn
	if !a.body(w, r, &in) {
		return
	}

	if in.Name == nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("name", "Give the project a name."))

		return
	}

	org, ok := a.optionalID(w, in.Organization, "organization")
	if !ok {
		return
	}

	rd := a.reader(r)

	p, err := a.books.Tenancy.CreateProject(r.Context(), time.Now(), rd.me, org, in.over(tenancybus.ProjectFields{}))
	if errors.Is(err, tenancybus.ErrForbidden) {
		web.WriteJSON(w, http.StatusForbidden, web.Problem("organization", "Your role in that organization does not allow making a project; it needs a bookkeeper or owner. Nothing was made; tell the person."))

		return
	}

	if a.bookRefused(w, r, err, "organization") {
		return
	}

	rd.done(w, r, map[string]any{"project": rd.projectOut(p)})
}

func (a app) changeProject(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "project", "project")
	if !ok {
		return
	}

	var in projectIn
	if !a.body(w, r, &in) {
		return
	}

	rd := a.reader(r)
	ctx := r.Context()

	p, _, err := a.books.Tenancy.Project(ctx, rd.me, id)
	if a.bookRefused(w, r, err, "project") {
		return
	}

	f := tenancybus.ProjectFields{Name: p.Name, StartsOn: p.StartsOn.String(), EndsOn: p.EndsOn.String(), Note: p.Note}

	p, err = a.books.Tenancy.EditProject(ctx, time.Now(), rd.me, id, in.over(f))
	if errors.Is(err, tenancybus.ErrForbidden) {
		web.WriteJSON(w, http.StatusForbidden, web.Problem("", "Only an owner of the project, or of its organization, may change it. Nothing was changed; tell the person."))

		return
	}

	if a.bookRefused(w, r, err, "project") {
		return
	}

	rd.done(w, r, map[string]any{"project": rd.projectOut(p)})
}
