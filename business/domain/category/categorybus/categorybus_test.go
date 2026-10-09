package categorybus_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

var now = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

type world struct {
	t       *testing.T
	cats    *categorybus.Business
	ten     *tenancybus.Business
	users   *userdb.Store
	history *eventbus.Business
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, categorydb.Init} {
		if err := init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := userdb.NewStore(db)
	ten := tenancybus.NewBusiness(log, tenancydb.NewStore(db), userbus.NewBusiness(log, users))

	return &world{
		t:       t,
		cats:    categorybus.NewBusiness(log, categorydb.NewStore(db), ten),
		ten:     ten,
		users:   users,
		history: eventbus.NewBusiness(eventdb.NewStore(db)),
	}
}

func (w *world) user(addr string) types.ID {
	w.t.Helper()

	e, _ := types.ParseEmail(addr)
	u := userbus.User{ID: types.NewID(), Email: e, Enabled: true, CreatedAt: now, UpdatedAt: now}

	if err := w.users.CreateUser(w.t.Context(), u); err != nil {
		w.t.Fatal(err)
	}

	return u.ID
}

func (w *world) grant(by types.ID, scope types.Scope, addr string, role tenancybus.Role) {
	w.t.Helper()

	e, _ := types.ParseEmail(addr)
	if _, _, err := w.ten.Grant(w.t.Context(), now, by, scope, e, role); err != nil {
		w.t.Fatal(err)
	}
}

func names(list []categorybus.Category) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.Name
	}

	return out
}

func TestAnOrganizationsList(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")

	org, err := w.ten.CreateOrg(t.Context(), now, me, "St. Joseph Parish")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"Utilities", "  Offerings  ", "Travel"} {
		if _, err := w.cats.Create(t.Context(), now, me, org.Scope(), name, categorybus.Expense); err != nil {
			t.Fatal(err)
		}
	}

	// The same name in another case is the same category.
	if _, err := w.cats.Create(t.Context(), now, me, org.Scope(), "utilities", categorybus.Expense); !errors.Is(err, categorybus.ErrDuplicate) {
		t.Errorf("a second Utilities: %v", err)
	}

	if _, err := w.cats.Create(t.Context(), now, me, org.Scope(), " ", categorybus.Expense); err == nil {
		t.Error("an empty name was accepted")
	}

	list, _, err := w.cats.List(t.Context(), me, org.Scope())
	if err != nil {
		t.Fatal(err)
	}

	travel := list[1]

	if _, err := w.cats.SetArchived(t.Context(), now, me, travel.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := w.cats.Rename(t.Context(), now.Add(time.Minute), me, list[0].ID, "Offerings and collections"); err != nil {
		t.Fatal(err)
	}

	list, _, _ = w.cats.List(t.Context(), me, org.Scope())
	if got := names(list); len(got) != 3 || got[0] != "Offerings and collections" || got[1] != "Utilities" || got[2] != "Travel" || !list[2].Archived() {
		t.Errorf("the list, archived last: %v", got)
	}

	events, _ := w.history.Recent(t.Context(), org.Scope(), 1)
	if events[0].Action != categorybus.Renamed || events[0].Detail["from"] != "Offerings" {
		t.Errorf("history: %+v", events[0])
	}

	// An account in the organization sorts with the organization's list.
	acct, err := w.ten.CreateAccount(t.Context(), now, me, org.ID, tenancybus.AccountFields{Name: "Checking", Kind: "checking"})
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := w.cats.ForAccount(t.Context(), acct); len(got) != 3 {
		t.Errorf("the account's choices: %v", names(got))
	}

	// And has no list of its own.
	if _, err := w.cats.Create(t.Context(), now, me, acct.Scope(), "Mine", categorybus.Expense); !errors.Is(err, categorybus.ErrNotFound) {
		t.Errorf("a list for an account in an organization: %v", err)
	}
}

func TestAPersonalAccountKeepsItsOwnList(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")

	acct, err := w.ten.CreateAccount(t.Context(), now, me, types.ID{}, tenancybus.AccountFields{Name: "My card", Kind: "card"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.cats.Create(t.Context(), now, me, acct.Scope(), "Groceries", categorybus.Expense); err != nil {
		t.Fatal(err)
	}

	if got, _ := w.cats.ForAccount(t.Context(), acct); len(got) != 1 || got[0].Name != "Groceries" {
		t.Errorf("choices: %v", names(got))
	}

	// A project has no list.
	p, _ := w.ten.CreateProject(t.Context(), now, me, types.ID{}, tenancybus.ProjectFields{Name: "Camino"})
	if _, err := w.cats.Create(t.Context(), now, me, p.Scope(), "Boots", categorybus.Expense); !errors.Is(err, categorybus.ErrNotFound) {
		t.Errorf("a project's list: %v", err)
	}
}

func TestWhoMayKeepAList(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	viewer := w.user("viewer@example.org")
	bookkeeper := w.user("bookkeeper@example.org")
	stranger := w.user("stranger@example.org")

	org, _ := w.ten.CreateOrg(t.Context(), now, me, "St. Joseph Parish")
	w.grant(me, org.Scope(), "viewer@example.org", tenancybus.Viewer)
	w.grant(me, org.Scope(), "bookkeeper@example.org", tenancybus.Bookkeeper)

	c, err := w.cats.Create(t.Context(), now, bookkeeper, org.Scope(), "Utilities", categorybus.Expense)
	if err != nil {
		t.Fatalf("a bookkeeper: %v", err)
	}

	if list, _, err := w.cats.List(t.Context(), viewer, org.Scope()); err != nil || len(list) != 1 {
		t.Errorf("a viewer reads: %v, %v", list, err)
	}

	if _, err := w.cats.Rename(t.Context(), now, viewer, c.ID, "Mine"); !errors.Is(err, categorybus.ErrForbidden) {
		t.Errorf("a viewer renamed: %v", err)
	}

	for what, err := range map[string]error{
		"list": func() error { _, _, err := w.cats.List(t.Context(), stranger, org.Scope()); return err }(),
		"create": func() error {
			_, err := w.cats.Create(t.Context(), now, stranger, org.Scope(), "X", categorybus.Expense)
			return err
		}(),
		"rename":  func() error { _, err := w.cats.Rename(t.Context(), now, stranger, c.ID, "X"); return err }(),
		"archive": func() error { _, err := w.cats.SetArchived(t.Context(), now, stranger, c.ID, true); return err }(),
	} {
		if !errors.Is(err, categorybus.ErrNotFound) {
			t.Errorf("a stranger's %s: %v", what, err)
		}
	}
}
