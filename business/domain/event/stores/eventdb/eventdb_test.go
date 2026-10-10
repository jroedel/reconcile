package eventdb_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// before is the history as it stood before via, written out rather than
// derived (CLAUDE.md).
const before = `
CREATE TABLE IF NOT EXISTS events (
    id          TEXT    PRIMARY KEY,
    actor_id    TEXT,
    scope_kind  TEXT    NOT NULL,
    scope_id    TEXT    NOT NULL,
    action      TEXT    NOT NULL,
    detail      TEXT    NOT NULL DEFAULT '{}',
    at          INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS events_scope ON events (scope_kind, scope_id, at DESC);
`

// A history from before via keeps its lines, each made on a page; a line
// written while serving a request that came with a key names the key.
func TestTheHistorySaysWhichKey(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.ExecContext(t.Context(), before); err != nil {
		t.Fatal(err)
	}

	scope := types.AccountScope(types.NewID())
	at := time.UnixMilli(1_700_000_000_000).UTC()

	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO events (id, actor_id, scope_kind, scope_id, action, at) VALUES (?, NULL, ?, ?, 'created', ?)`,
		types.NewID().String(), string(scope.Kind), scope.ID.String(), at.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := eventdb.Init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, eventdb.Expected); err != nil {
		t.Fatal(err)
	}

	actor := types.NewID()

	write := func(ctx context.Context, action eventbus.Action, n int) {
		t.Helper()

		if err := eventdb.Insert(ctx, db, eventbus.New(at.Add(time.Duration(n)*time.Minute), actor, scope, action, nil)); err != nil {
			t.Fatal(err)
		}
	}

	write(t.Context(), eventbus.Renamed, 1)
	write(eventbus.WithVia(t.Context(), "claude.ai"), eventbus.Edited, 2)

	events, err := eventdb.NewStore(db).ForScope(t.Context(), scope, 10)
	if err != nil || len(events) != 3 {
		t.Fatalf("the history: %+v, %v", events, err)
	}

	for _, e := range events {
		want := ""
		if e.Action == eventbus.Edited {
			want = "claude.ai"
		}

		if e.Via != want {
			t.Errorf("%s: via %q, want %q", e.Action, e.Via, want)
		}
	}
}

// Through is one person's changes through a key, newest first, since a
// moment: not their changes on a page, and not anybody else's.
func TestThroughIsOnePersonsChangesThroughAKey(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := eventdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	me, other := types.NewID(), types.NewID()
	scope := types.AccountScope(types.NewID())
	at := time.UnixMilli(1_700_000_000_000).UTC()
	keyed := eventbus.WithVia(t.Context(), "claude.ai")

	for i, w := range []struct {
		ctx   context.Context
		actor types.ID
	}{{t.Context(), me}, {keyed, me}, {keyed, other}, {keyed, me}} {
		if err := eventdb.Insert(w.ctx, db, eventbus.New(at.Add(time.Duration(i)*time.Minute), w.actor, scope, eventbus.Edited, nil)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := eventdb.NewStore(db).Through(t.Context(), me, at.Add(time.Minute), 10)
	if err != nil || len(got) != 2 || !got[0].At.After(got[1].At) || got[0].Scope != scope {
		t.Fatalf("through: %+v, %v", got, err)
	}

	if got, _ := eventdb.NewStore(db).Through(t.Context(), me, at.Add(2*time.Minute), 10); len(got) != 1 {
		t.Errorf("since a later moment: %d", len(got))
	}
}
