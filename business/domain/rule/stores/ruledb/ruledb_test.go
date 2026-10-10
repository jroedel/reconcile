package ruledb_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/rule/stores/ruledb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Two rules with the same text in one account, made at once, are one rule
// and a refusal: the second of two people deciding gets told, rather than
// the first decision silently winning forever. The same text in another
// account is that account's own rule.
func TestTheSameTextIsOneRule(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, categorydb.Init, ruledb.Init} {
		if err := init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	// The accounts and categories the rules point at are not there; the
	// references are not what this is about. One connection, so the
	// pragma holds.
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}

	store := ruledb.NewStore(db)
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	account, other := types.NewID(), types.NewID()

	rule := func(account types.ID, match string) rulebus.Rule {
		return rulebus.Rule{
			ID: types.NewID(), AccountID: account, Match: match, Direction: rulebus.Out,
			CategoryID: types.NewID(), CreatedBy: types.NewID(), CreatedAt: now, UpdatedAt: now,
		}
	}

	ev := func(account types.ID) eventbus.Event {
		return eventbus.New(now, types.NewID(), types.AccountScope(account), rulebus.Made, nil)
	}

	first := rule(account, "Shell Oil")
	if err := store.Create(ctx, first, ev(account)); err != nil {
		t.Fatal(err)
	}

	if err := store.Create(ctx, rule(account, "SHELL  OIL"), ev(account)); !errors.Is(err, rulebus.ErrDuplicate) {
		t.Errorf("the same text again: %v", err)
	}

	if err := store.Create(ctx, rule(other, "SHELL OIL"), ev(other)); err != nil {
		t.Errorf("another account's: %v", err)
	}

	rules, err := store.Of(ctx, account)
	if err != nil || len(rules) != 1 || rules[0] != first {
		t.Errorf("the account's rules: %+v %v", rules, err)
	}

	if _, err := store.ByID(ctx, types.NewID()); !errors.Is(err, rulebus.ErrNotFound) {
		t.Errorf("a rule that is not there: %v", err)
	}
}

// rulesBeforeVia is the sort_rules table as it stood before a rule could
// be written through an API key, written out rather than derived from Init,
// so that a change to Init cannot change what "before" was.
const rulesBeforeVia = `
CREATE TABLE IF NOT EXISTS sort_rules (
    id          TEXT    PRIMARY KEY,
    account_id  TEXT    NOT NULL REFERENCES accounts (id),
    match       TEXT    NOT NULL,
    match_key   TEXT    NOT NULL,
    direction   TEXT    NOT NULL CHECK (direction IN ('out', 'in', 'any')),
    category_id TEXT    REFERENCES categories (id),
    project_id  TEXT    REFERENCES projects (id),
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    CHECK (category_id IS NOT NULL OR project_id IS NOT NULL)
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS sort_rules_match ON sort_rules (account_id, match_key);
`

// A database from before gains sort_rules.via, a rule already there reads
// as one written on the web, and a rule written through a key says which
// until it is written again without one.
func TestInitGivesOldRulesNoKey(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, categorydb.Init} {
		if err := init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, rulesBeforeVia); err != nil {
		t.Fatal(err)
	}

	old := types.NewID()
	if _, err := db.ExecContext(ctx, `
INSERT INTO sort_rules (id, account_id, match, match_key, direction, category_id, created_by, created_at, updated_at)
VALUES (?, ?, 'Shell Oil', 'shell oil', 'out', ?, ?, 0, 0)`,
		old.String(), types.NewID().String(), types.NewID().String(), types.NewID().String()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := ruledb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(ctx, db, ruledb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}

	store := ruledb.NewStore(db)

	r, err := store.ByID(ctx, old)
	if err != nil || r.Via != "" || r.Match != "Shell Oil" {
		t.Fatalf("the old rule: %+v %v", r, err)
	}

	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	ev := func() eventbus.Event {
		return eventbus.New(now, types.NewID(), types.AccountScope(r.AccountID), rulebus.Changed, nil)
	}

	r.Via = "Claude"
	if err := store.Update(ctx, r, ev()); err != nil {
		t.Fatal(err)
	}

	if got, err := store.ByID(ctx, old); err != nil || got.Via != "Claude" {
		t.Errorf("written through a key: %+v %v", got, err)
	}

	r.Via = ""
	if err := store.Update(ctx, r, ev()); err != nil {
		t.Fatal(err)
	}

	if got, err := store.ByID(ctx, old); err != nil || got.Via != "" {
		t.Errorf("written again on the web: %+v %v", got, err)
	}
}
