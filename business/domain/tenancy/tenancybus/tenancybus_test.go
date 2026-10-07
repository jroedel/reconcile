package tenancybus_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

var now = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

type world struct {
	t       *testing.T
	ten     *tenancybus.Business
	users   *userbus.Business
	store   *userdb.Store
	history *eventbus.Business
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init} {
		if err := init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := userdb.NewStore(db)
	users := userbus.NewBusiness(log, store)

	return &world{
		t:       t,
		ten:     tenancybus.NewBusiness(log, tenancydb.NewStore(db), users),
		users:   users,
		store:   store,
		history: eventbus.NewBusiness(eventdb.NewStore(db)),
	}
}

func email(t *testing.T, s string) types.Email {
	t.Helper()

	e, err := types.ParseEmail(s)
	if err != nil {
		t.Fatal(err)
	}

	return e
}

// user makes somebody who signs in with the address.
func (w *world) user(addr string) types.ID {
	w.t.Helper()

	u := userbus.User{ID: types.NewID(), Email: email(w.t, addr), Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := w.store.CreateUser(w.t.Context(), u); err != nil {
		w.t.Fatal(err)
	}

	return u.ID
}

func (w *world) org(owner types.ID, name string) tenancybus.Org {
	w.t.Helper()

	o, err := w.ten.CreateOrg(w.t.Context(), now, owner, name)
	if err != nil {
		w.t.Fatal(err)
	}

	return o
}

func (w *world) grant(actor types.ID, scope types.Scope, addr string, role tenancybus.Role) tenancybus.Grant {
	w.t.Helper()

	g, _, err := w.ten.Grant(w.t.Context(), now, actor, scope, email(w.t, addr), role)
	if err != nil {
		w.t.Fatal(err)
	}

	return g
}

func (w *world) can(actor types.ID, scope types.Scope, p tenancybus.Permission) bool {
	w.t.Helper()

	a, err := w.ten.AccessTo(w.t.Context(), actor, scope)
	if err != nil {
		w.t.Fatal(err)
	}

	return a.Can(p)
}

var checking = tenancybus.AccountFields{Name: "Parish checking", Kind: "checking", Last4: "1234"}

func TestTheCreatorOwnsAndNobodyElseSeesIt(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.user("ana@example.org"), w.user("bo@example.org")

	o := w.org(ana, "  St. Joseph   Parish ")
	if o.Name != "St. Joseph Parish" {
		t.Errorf("name = %q; spaces were not tidied", o.Name)
	}

	if !w.can(ana, o.Scope(), tenancybus.Manage) {
		t.Error("the creator cannot manage what they made")
	}

	if _, _, err := w.ten.Org(t.Context(), bo, o.ID); !errors.Is(err, tenancybus.ErrNotFound) {
		t.Errorf("a stranger read the organization: %v", err)
	}

	if _, err := w.ten.RenameOrg(t.Context(), now, bo, o.ID, "Mine now"); !errors.Is(err, tenancybus.ErrNotFound) {
		t.Errorf("a stranger renamed it: %v", err)
	}

	ov, _ := w.ten.Overview(t.Context(), bo)
	if !ov.Empty() {
		t.Errorf("a stranger's overview shows %+v", ov)
	}
}

// A role on the organization holds on everything in it; a role on one thing
// holds on that thing and nowhere else.
func TestInheritance(t *testing.T) {
	w := newWorld(t)
	owner, viewer, treasurer, pilgrim := w.user("owner@example.org"), w.user("viewer@example.org"),
		w.user("treasurer@example.org"), w.user("pilgrim@example.org")

	o := w.org(owner, "Movement")

	acct, err := w.ten.CreateAccount(t.Context(), now, owner, o.ID, checking)
	if err != nil {
		t.Fatal(err)
	}

	proj, err := w.ten.CreateProject(t.Context(), now, owner, o.ID, tenancybus.ProjectFields{Name: "World Youth Day"})
	if err != nil {
		t.Fatal(err)
	}

	w.grant(owner, o.Scope(), "viewer@example.org", tenancybus.Viewer)
	w.grant(owner, acct.Scope(), "treasurer@example.org", tenancybus.Bookkeeper)
	w.grant(owner, proj.Scope(), "pilgrim@example.org", tenancybus.Contributor)

	for _, tc := range []struct {
		who   types.ID
		scope types.Scope
		p     tenancybus.Permission
		want  bool
	}{
		{viewer, acct.Scope(), tenancybus.Read, true},
		{viewer, proj.Scope(), tenancybus.Read, true},
		{viewer, acct.Scope(), tenancybus.Bookkeep, false},
		{treasurer, acct.Scope(), tenancybus.Bookkeep, true},
		{treasurer, o.Scope(), tenancybus.Read, false},
		{treasurer, proj.Scope(), tenancybus.Read, false},
		{pilgrim, proj.Scope(), tenancybus.Receipts, true},
		{pilgrim, acct.Scope(), tenancybus.Read, false},
		{pilgrim, o.Scope(), tenancybus.Read, false},
		{owner, acct.Scope(), tenancybus.Manage, true},
		{owner, proj.Scope(), tenancybus.Manage, true},
	} {
		if got := w.can(tc.who, tc.scope, tc.p); got != tc.want {
			t.Errorf("%s may %s on %s: %v, want %v", tc.who.String()[:6], tc.p, tc.scope.Kind, got, tc.want)
		}
	}

	// The overview: the viewer sees the organization with both inside it;
	// the treasurer sees the one account on its own.
	ov, _ := w.ten.Overview(t.Context(), viewer)
	if len(ov.Orgs) != 1 || len(ov.Orgs[0].Accounts) != 1 || len(ov.Orgs[0].Projects) != 1 || len(ov.Accounts) != 0 {
		t.Errorf("viewer's overview = %+v", ov)
	}

	ov, _ = w.ten.Overview(t.Context(), treasurer)
	if len(ov.Orgs) != 0 || len(ov.Accounts) != 1 || ov.Accounts[0].ID != acct.ID {
		t.Errorf("treasurer's overview = %+v", ov)
	}
}

func TestWhoMayMakeWhatInAnOrganization(t *testing.T) {
	w := newWorld(t)
	owner, keeper := w.user("owner@example.org"), w.user("keeper@example.org")
	o := w.org(owner, "Parish")
	w.grant(owner, o.Scope(), "keeper@example.org", tenancybus.Bookkeeper)

	if _, err := w.ten.CreateAccount(t.Context(), now, keeper, o.ID, checking); !errors.Is(err, tenancybus.ErrForbidden) {
		t.Errorf("a bookkeeper made an account: %v", err)
	}

	if _, err := w.ten.CreateProject(t.Context(), now, keeper, o.ID, tenancybus.ProjectFields{Name: "Retreat"}); err != nil {
		t.Errorf("a bookkeeper could not make a project: %v", err)
	}

	stranger := w.user("stranger@example.org")
	if _, err := w.ten.CreateAccount(t.Context(), now, stranger, o.ID, checking); !errors.Is(err, tenancybus.ErrNotFound) {
		t.Errorf("a stranger made an account in it: %v", err)
	}
}

func TestPersonalAccountsAndProjects(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.user("ana@example.org"), w.user("bo@example.org")

	a, err := w.ten.CreateAccount(t.Context(), now, ana, types.ID{}, tenancybus.AccountFields{Name: "Petty cash", Kind: "cash"})
	if err != nil {
		t.Fatal(err)
	}

	if a.Currency != "USD" || !a.OrgID.Zero() {
		t.Errorf("account = %+v", a)
	}

	if !w.can(ana, a.Scope(), tenancybus.Manage) || w.can(bo, a.Scope(), tenancybus.Read) {
		t.Error("a personal account is not its creator's alone")
	}

	p, err := w.ten.CreateProject(t.Context(), now, ana, types.ID{}, tenancybus.ProjectFields{Name: "Camino", StartsOn: "2026-07-01", EndsOn: "2026-07-20"})
	if err != nil {
		t.Fatal(err)
	}

	if !w.can(ana, p.Scope(), tenancybus.Manage) || w.can(bo, p.Scope(), tenancybus.Read) {
		t.Error("a personal project is not its creator's alone")
	}

	ov, _ := w.ten.Overview(t.Context(), ana)
	if len(ov.Accounts) != 1 || len(ov.Projects) != 1 {
		t.Errorf("overview = %+v", ov)
	}
}

func TestWhatPeopleType(t *testing.T) {
	w := newWorld(t)
	ana := w.user("ana@example.org")

	for name, tc := range map[string]struct {
		f     tenancybus.AccountFields
		field string
	}{
		"no name":         {tenancybus.AccountFields{Kind: "checking"}, "name"},
		"unknown kind":    {tenancybus.AccountFields{Name: "x", Kind: "crypto"}, "kind"},
		"a whole number":  {tenancybus.AccountFields{Name: "x", Kind: "card", Last4: "4111111111111111"}, "last4"},
		"a bad currency":  {tenancybus.AccountFields{Name: "x", Kind: "card", Currency: "dollars"}, "currency"},
		"a date of words": {tenancybus.AccountFields{Name: "x", Kind: "card", OpenedOn: "last May"}, "opened"},
	} {
		_, err := w.ten.CreateAccount(t.Context(), now, ana, types.ID{}, tc.f)

		invalid, ok := errors.AsType[tenancybus.Invalid](err)
		if !ok || invalid.Field != tc.field {
			t.Errorf("%s: %v, want a problem with %s", name, err, tc.field)
		}
	}

	a, err := w.ten.CreateAccount(t.Context(), now, ana, types.ID{}, tenancybus.AccountFields{Name: "Euro", Kind: "savings", Currency: " eur "})
	if err != nil || a.Currency != "EUR" {
		t.Errorf("a lower-case currency: %+v, %v", a, err)
	}

	_, err = w.ten.CreateProject(t.Context(), now, ana, types.ID{}, tenancybus.ProjectFields{Name: "Backwards", StartsOn: "2026-05-02", EndsOn: "2026-05-01"})
	if invalid, ok := errors.AsType[tenancybus.Invalid](err); !ok || invalid.Field != "ends" {
		t.Errorf("an end before the start: %v", err)
	}
}

// A grant to an address nobody uses waits, and is claimed when they sign up.
func TestAnInvitationWaitsAndIsClaimed(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@example.org")
	o := w.org(owner, "Parish")

	g, known, err := w.ten.Grant(t.Context(), now, owner, o.Scope(), email(t, "new@example.org"), tenancybus.Accountant)
	if err != nil || known || !g.Pending() {
		t.Fatalf("Grant = %+v, %v, %v", g, known, err)
	}

	if _, _, err := w.ten.Grant(t.Context(), now, owner, o.Scope(), email(t, "new@example.org"), tenancybus.Viewer); !errors.Is(err, tenancybus.ErrAlreadyGranted) {
		t.Errorf("a second invitation to one address: %v", err)
	}

	newcomer := w.user("new@example.org")
	if w.can(newcomer, o.Scope(), tenancybus.Read) {
		t.Fatal("the invitation held before it was claimed")
	}

	if n, err := w.ten.Claim(t.Context(), now, newcomer, email(t, "new@example.org")); err != nil || n != 1 {
		t.Fatalf("Claim = %d, %v", n, err)
	}

	if !w.can(newcomer, o.Scope(), tenancybus.Export) {
		t.Error("the claimed role does not hold")
	}

	if n, _ := w.ten.Claim(t.Context(), now, newcomer, email(t, "new@example.org")); n != 0 {
		t.Errorf("claimed again: %d", n)
	}

	// Somebody who already signs in gets it at once.
	known2 := w.user("known@example.org")
	if _, known, _ := w.ten.Grant(t.Context(), now, owner, o.Scope(), email(t, "known@example.org"), tenancybus.Viewer); !known || !w.can(known2, o.Scope(), tenancybus.Read) {
		t.Error("a grant to a known address waited")
	}
}

// An invitation to an address somebody moves to, on a scope where they
// already have a role, is dropped and their role kept.
func TestAClaimNeverDoublesARole(t *testing.T) {
	w := newWorld(t)
	owner, ana := w.user("owner@example.org"), w.user("ana@example.org")
	o := w.org(owner, "Parish")

	w.grant(owner, o.Scope(), "ana@example.org", tenancybus.Viewer)
	w.grant(owner, o.Scope(), "ana.new@example.org", tenancybus.Owner)

	if n, err := w.ten.Claim(t.Context(), now, ana, email(t, "ana.new@example.org")); err != nil || n != 0 {
		t.Fatalf("Claim = %d, %v", n, err)
	}

	direct, _, _ := w.ten.People(t.Context(), owner, o.Scope())
	if len(direct) != 2 {
		t.Fatalf("%d grants left, want the owner's and ana's", len(direct))
	}

	if w.can(ana, o.Scope(), tenancybus.Manage) {
		t.Error("the dropped invitation's role was taken")
	}
}

func TestTheLastOwnerStays(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@example.org")
	w.user("second@example.org")
	o := w.org(owner, "Parish")

	direct, _, _ := w.ten.People(t.Context(), owner, o.Scope())
	mine := direct[0]

	if err := w.ten.RemoveGrant(t.Context(), now, owner, o.Scope(), mine.ID); !errors.Is(err, tenancybus.ErrLastOwner) {
		t.Errorf("the last owner removed themselves: %v", err)
	}

	if _, err := w.ten.ChangeRole(t.Context(), now, owner, o.Scope(), mine.ID, tenancybus.Viewer); !errors.Is(err, tenancybus.ErrLastOwner) {
		t.Errorf("the last owner stepped down: %v", err)
	}

	// An invitation to be owner is not an owner.
	w.grant(owner, o.Scope(), "invited@example.org", tenancybus.Owner)
	if err := w.ten.RemoveGrant(t.Context(), now, owner, o.Scope(), mine.ID); !errors.Is(err, tenancybus.ErrLastOwner) {
		t.Errorf("a waiting invitation counted as an owner: %v", err)
	}

	// With a second owner, the first may go.
	w.grant(owner, o.Scope(), "second@example.org", tenancybus.Owner)
	if _, err := w.ten.ChangeRole(t.Context(), now, owner, o.Scope(), mine.ID, tenancybus.Viewer); err != nil {
		t.Errorf("handing over: %v", err)
	}

	// An account in the organization has the organization's owners, so its
	// own owner may go.
	w.user("acct@example.org")

	second := w.ownerOf(o.Scope(), "second@example.org")
	acct, _ := w.ten.CreateAccount(t.Context(), now, second, o.ID, checking)
	g := w.grant(second, acct.Scope(), "acct@example.org", tenancybus.Owner)

	if err := w.ten.RemoveGrant(t.Context(), now, second, acct.Scope(), g.ID); err != nil {
		t.Errorf("an account's own owner could not be removed: %v", err)
	}
}

// ownerOf is the user behind a claimed grant to addr on scope.
func (w *world) ownerOf(scope types.Scope, addr string) types.ID {
	w.t.Helper()

	id, ok, err := w.users.UserIDByEmail(w.t.Context(), email(w.t, addr))
	if err != nil || !ok {
		w.t.Fatalf("%s: %v", addr, err)
	}

	return id
}

// Two owners removing each other at the same moment: one of them stays.
func TestTwoOwnersCannotRemoveEachOtherAtOnce(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.user("ana@example.org"), w.user("bo@example.org")
	o := w.org(ana, "Parish")
	boGrant := w.grant(ana, o.Scope(), "bo@example.org", tenancybus.Owner)

	direct, _, _ := w.ten.People(t.Context(), ana, o.Scope())
	anaGrant := direct[0]

	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Go(func() { errs[0] = w.ten.RemoveGrant(t.Context(), now, ana, o.Scope(), boGrant.ID) })
	wg.Go(func() { errs[1] = w.ten.RemoveGrant(t.Context(), now, bo, o.Scope(), anaGrant.ID) })
	wg.Wait()

	direct, _, _ = w.ten.People(t.Context(), ana, o.Scope())
	people, _, _ := w.ten.People(t.Context(), bo, o.Scope())

	if len(direct)+len(people) != 1 {
		t.Fatalf("errors %v; %d and %d grants left, want exactly one owner", errs, len(direct), len(people))
	}
}

// A grant's ID from one scope cannot be used through another's page.
func TestAGrantIsChangedOnlyThroughItsOwnScope(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.user("ana@example.org"), w.user("bo@example.org")
	mine := w.org(ana, "Mine")
	theirs := w.org(bo, "Theirs")

	direct, _, _ := w.ten.People(t.Context(), bo, theirs.Scope())

	if err := w.ten.RemoveGrant(t.Context(), now, ana, mine.Scope(), direct[0].ID); !errors.Is(err, tenancybus.ErrNotFound) {
		t.Errorf("a grant on another organization was removed through this one: %v", err)
	}

	if _, err := w.ten.ChangeRole(t.Context(), now, ana, mine.Scope(), direct[0].ID, tenancybus.Viewer); !errors.Is(err, tenancybus.ErrNotFound) {
		t.Errorf("a grant on another organization was changed through this one: %v", err)
	}
}

func TestOnlyAnOwnerManages(t *testing.T) {
	w := newWorld(t)
	owner, keeper := w.user("owner@example.org"), w.user("keeper@example.org")
	o := w.org(owner, "Parish")
	w.grant(owner, o.Scope(), "keeper@example.org", tenancybus.Bookkeeper)

	if _, _, err := w.ten.Grant(t.Context(), now, keeper, o.Scope(), email(t, "friend@example.org"), tenancybus.Owner); !errors.Is(err, tenancybus.ErrForbidden) {
		t.Errorf("a bookkeeper granted a role: %v", err)
	}

	if _, err := w.ten.SetOrgArchived(t.Context(), now, keeper, o.ID, true); !errors.Is(err, tenancybus.ErrForbidden) {
		t.Errorf("a bookkeeper archived it: %v", err)
	}
}

func TestArchivingHidesButKeeps(t *testing.T) {
	w := newWorld(t)
	ana := w.user("ana@example.org")
	o := w.org(ana, "Parish")

	if _, err := w.ten.SetOrgArchived(t.Context(), now, ana, o.ID, true); err != nil {
		t.Fatal(err)
	}

	if ov, _ := w.ten.Overview(t.Context(), ana); !ov.Empty() {
		t.Error("an archived organization is still on the overview")
	}

	got, _, err := w.ten.Org(t.Context(), ana, o.ID)
	if err != nil || !got.Archived() {
		t.Errorf("the archived organization: %+v, %v", got, err)
	}

	if got, _ := w.ten.SetOrgArchived(t.Context(), now, ana, o.ID, false); got.Archived() {
		t.Error("it could not be brought back")
	}
}

// Every change leaves a line, written with it.
func TestHistory(t *testing.T) {
	w := newWorld(t)
	ana := w.user("ana@example.org")
	o := w.org(ana, "Parish")

	w.ten.RenameOrg(t.Context(), now.Add(time.Minute), ana, o.ID, "St. Joseph")
	g := w.grant(ana, o.Scope(), "bo@example.org", tenancybus.Viewer)
	w.ten.RemoveGrant(t.Context(), now.Add(2*time.Minute), ana, o.Scope(), g.ID)

	events, err := w.history.Recent(t.Context(), o.Scope(), 10)
	if err != nil {
		t.Fatal(err)
	}

	var actions []eventbus.Action
	for _, e := range events {
		actions = append(actions, e.Action)
	}

	want := []eventbus.Action{eventbus.GrantRemoved, eventbus.Renamed, eventbus.GrantAdded, eventbus.Created}
	if len(actions) != len(want) {
		t.Fatalf("history = %v, want %v", actions, want)
	}

	for _, a := range want {
		found := false
		for _, got := range actions {
			found = found || got == a
		}

		if !found {
			t.Errorf("no %s in %v", a, actions)
		}
	}

	if events[0].Action != eventbus.GrantRemoved || events[0].Detail["who"] != "bo@example.org" || events[0].ActorID != ana {
		t.Errorf("newest = %+v", events[0])
	}

	// A refused change leaves no line.
	if _, err := w.ten.RenameOrg(t.Context(), now, ana, o.ID, ""); err == nil {
		t.Fatal("an empty name was accepted")
	}

	if again, _ := w.history.Recent(t.Context(), o.Scope(), 10); len(again) != len(events) {
		t.Error("a refused change was recorded")
	}
}

func TestRolesAllow(t *testing.T) {
	for role, want := range map[tenancybus.Role][]tenancybus.Permission{
		tenancybus.Viewer:      {tenancybus.Read},
		tenancybus.Accountant:  {tenancybus.Read, tenancybus.Note, tenancybus.Export},
		tenancybus.Contributor: {tenancybus.Read, tenancybus.Note, tenancybus.Receipts},
	} {
		for _, p := range []tenancybus.Permission{tenancybus.Read, tenancybus.Note, tenancybus.Receipts, tenancybus.Export, tenancybus.Bookkeep, tenancybus.Manage} {
			wanted := false
			for _, w := range want {
				wanted = wanted || w == p
			}

			if role.Allows(p) != wanted {
				t.Errorf("%s allows %s: %v", role, p, !wanted)
			}
		}
	}

	if tenancybus.Role("superuser").Allows(tenancybus.Read) {
		t.Error("an unknown role allowed something")
	}
}
