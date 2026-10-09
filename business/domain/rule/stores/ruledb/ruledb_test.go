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
