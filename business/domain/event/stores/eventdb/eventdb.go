// Package eventdb stores the history in SQLite.
package eventdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"events": {"id", "actor_id", "scope_kind", "scope_id", "action", "detail", "at"},
}

// Init creates the table. Idempotent, run at every startup.
//
// actor_id is not a foreign key. History outlives what it mentions, and a
// constraint here is one more thing that could refuse the change it is
// recording. Nor is scope_id, which names one of three tables.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
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

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the history table: %w", err)
	}

	return nil
}

// Execer is a database or a transaction: Insert is called by other domains'
// stores inside their own transactions.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Insert appends one event, through whatever transaction the change it
// records is in.
func Insert(ctx context.Context, db Execer, e eventbus.Event) error {
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return fmt.Errorf("writing the history's detail: %w", err)
	}

	if e.Detail == nil {
		detail = []byte("{}")
	}

	var actor any
	if !e.ActorID.Zero() {
		actor = e.ActorID.String()
	}

	if _, err := db.ExecContext(ctx, `
INSERT INTO events (id, actor_id, scope_kind, scope_id, action, detail, at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID.String(), actor, string(e.Scope.Kind), e.Scope.ID.String(), string(e.Action), string(detail),
		e.At.UnixMilli()); err != nil {
		return fmt.Errorf("writing the history: %w", err)
	}

	return nil
}

// Store is the SQLite implementation of eventbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ eventbus.Storer = (*Store)(nil)

// ForScope is the newest events on one scope, newest first.
func (s *Store) ForScope(ctx context.Context, scope types.Scope, limit int) ([]eventbus.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, actor_id, scope_kind, scope_id, action, detail, at
FROM events WHERE scope_kind = ? AND scope_id = ?
ORDER BY at DESC, id DESC LIMIT ?`, string(scope.Kind), scope.ID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("reading the history: %w", err)
	}
	defer rows.Close()

	var out []eventbus.Event

	for rows.Next() {
		var (
			e                  eventbus.Event
			id, kind, sid, act string
			actor              sql.NullString
			detail             string
			at                 int64
		)

		if err := rows.Scan(&id, &actor, &kind, &sid, &act, &detail, &at); err != nil {
			return nil, fmt.Errorf("reading an event: %w", err)
		}

		if e.ID, err = types.ParseID(id); err != nil {
			return nil, fmt.Errorf("a stored event has a bad identifier: %w", err)
		}

		if actor.Valid {
			if e.ActorID, err = types.ParseID(actor.String); err != nil {
				return nil, fmt.Errorf("a stored event names a bad actor: %w", err)
			}
		}

		e.Scope = scope
		e.Action = eventbus.Action(act)
		e.At = time.UnixMilli(at).UTC()

		if err := json.Unmarshal([]byte(detail), &e.Detail); err != nil {
			return nil, fmt.Errorf("a stored event's detail is unreadable: %w", err)
		}

		out = append(out, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the history: %w", err)
	}

	return out, nil
}
