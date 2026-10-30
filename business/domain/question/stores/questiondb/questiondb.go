// Package questiondb stores questions and their replies in SQLite.
package questiondb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/question/questionbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of questionbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ questionbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"questions": {
		"id", "about_kind", "about_id", "transaction_id", "statement_id", "home_kind", "home_id",
		"asked_by", "body", "asked_at", "state", "answered_by", "answered_at", "via",
	},
	"question_replies": {"id", "question_id", "author_id", "body", "at", "via"},
}

// Init creates the tables. After ledgerdb, whose transactions and
// statements they reference.
//
// A question and its replies are two tables rather than one of messages,
// because only the question has what a thread has: what it is about, where
// its history goes, and whether it is answered. A reply is text and who
// wrote it.
//
// about_kind and about_id say what a question is about, whatever it is.
// transaction_id and statement_id repeat about_id for those two kinds, as
// references that go with their rows: removing a statement removes its
// transactions (ledgerdb), and the questions about them and about it go too,
// as receipt links do. A project and a payee are archived, never removed,
// so their questions need no reference to go with them. The CHECK on
// about_kind is safe to have: it is the four things a question may be
// about, which grow only with a decision written in docs/accountant.md.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS questions (
    id             TEXT    PRIMARY KEY,
    about_kind     TEXT    NOT NULL CHECK (about_kind IN ('transaction', 'statement', 'project', 'payee')),
    about_id       TEXT    NOT NULL,
    transaction_id TEXT    REFERENCES transactions (id) ON DELETE CASCADE,
    statement_id   TEXT    REFERENCES statements (id) ON DELETE CASCADE,
    home_kind      TEXT    NOT NULL,
    home_id        TEXT    NOT NULL,
    asked_by       TEXT    NOT NULL,
    body           TEXT    NOT NULL,
    asked_at       INTEGER NOT NULL,
    state          TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'answered')),
    answered_by    TEXT,
    answered_at    INTEGER,
    via            TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS questions_about ON questions (about_kind, about_id, asked_at DESC);
CREATE INDEX IF NOT EXISTS questions_home ON questions (home_kind, home_id, state);
CREATE INDEX IF NOT EXISTS questions_transaction ON questions (transaction_id) WHERE transaction_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS questions_statement ON questions (statement_id) WHERE statement_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS question_replies (
    id          TEXT    PRIMARY KEY,
    question_id TEXT    NOT NULL REFERENCES questions (id) ON DELETE CASCADE,
    author_id   TEXT    NOT NULL,
    body        TEXT    NOT NULL,
    at          INTEGER NOT NULL,
    via         TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS question_replies_question ON question_replies (question_id, at);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the questions tables: %w", err)
	}

	return nil
}

// reference is about_id again, for the kind whose reference column it is.
func reference(about questionbus.About, kind questionbus.Kind) any {
	if about.Kind != kind {
		return nil
	}

	return about.ID.String()
}

// Ask inserts a question and its line of history.
func (s *Store) Ask(ctx context.Context, q questionbus.Question, ev eventbus.Event) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO questions (id, about_kind, about_id, transaction_id, statement_id, home_kind, home_id, asked_by, body, asked_at, state, via)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			q.ID.String(), string(q.About.Kind), q.About.ID.String(),
			reference(q.About, questionbus.OnTransaction), reference(q.About, questionbus.OnStatement),
			string(q.Home.Kind), q.Home.ID.String(), q.AskedBy.String(), q.Body, q.AskedAt.UnixMilli(), string(q.State), q.Via)
		if err != nil {
			return fmt.Errorf("asking a question: %w", err)
		}

		return eventdb.Insert(ctx, tx, ev)
	})
}

// Reply inserts a reply and its line of history, and answers the question
// with it when asked to and it is still open: one UPDATE that finds it
// open or not, never a read and then a write, so that two replies at once
// answer it once.
func (s *Store) Reply(ctx context.Context, r questionbus.Reply, answers bool, ev eventbus.Event) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO question_replies (id, question_id, author_id, body, at, via) VALUES (?, ?, ?, ?, ?, ?)`,
			r.ID.String(), r.QuestionID.String(), r.AuthorID.String(), r.Body, r.At.UnixMilli(), r.Via); err != nil {
			return fmt.Errorf("replying to a question: %w", err)
		}

		if answers {
			if _, err := tx.ExecContext(ctx, `UPDATE questions SET state = 'answered', answered_by = ?, answered_at = ? WHERE id = ? AND state = 'open'`,
				r.AuthorID.String(), r.At.UnixMilli(), r.QuestionID.String()); err != nil {
				return fmt.Errorf("answering a question: %w", err)
			}
		}

		return eventdb.Insert(ctx, tx, ev)
	})
}

// MarkAnswered answers an open question, and writes its line of history
// only when it was open.
func (s *Store) MarkAnswered(ctx context.Context, id, by types.ID, at time.Time, ev eventbus.Event) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE questions SET state = 'answered', answered_by = ?, answered_at = ? WHERE id = ? AND state = 'open'`,
			by.String(), at.UnixMilli(), id.String())
		if err != nil {
			return fmt.Errorf("answering a question: %w", err)
		}

		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}

		return eventdb.Insert(ctx, tx, ev)
	})
}

const questionColumns = `id, about_kind, about_id, home_kind, home_id, asked_by, body, asked_at, state, coalesce(answered_by, ''), coalesce(answered_at, 0), via`

// ByID is a question with its replies.
func (s *Store) ByID(ctx context.Context, id types.ID) (questionbus.Question, error) {
	qs, err := s.questions(ctx, `SELECT `+questionColumns+` FROM questions WHERE id = ?`, id.String())
	if err != nil {
		return questionbus.Question{}, err
	}

	if len(qs) == 0 {
		return questionbus.Question{}, questionbus.ErrNotFound
	}

	return qs[0], nil
}

// On is the questions about a thing, newest first, with their replies.
func (s *Store) On(ctx context.Context, about questionbus.About) ([]questionbus.Question, error) {
	return s.questions(ctx, `SELECT `+questionColumns+` FROM questions WHERE about_kind = ? AND about_id = ? ORDER BY asked_at DESC, rowid DESC`,
		string(about.Kind), about.ID.String())
}

// InHomes is the questions whose home is any of the scopes, newest first.
func (s *Store) InHomes(ctx context.Context, homes []types.Scope, state questionbus.State, askedBy types.ID) ([]questionbus.Question, error) {
	if len(homes) == 0 {
		return nil, nil
	}

	pairs := make([]string, len(homes))
	args := make([]any, 0, 2*len(homes)+2)

	for i, h := range homes {
		pairs[i] = "(home_kind = ? AND home_id = ?)"
		args = append(args, string(h.Kind), h.ID.String())
	}

	where := "(" + strings.Join(pairs, " OR ") + ")"

	if state != "" {
		where += " AND state = ?"
		args = append(args, string(state))
	}

	if !askedBy.Zero() {
		where += " AND asked_by = ?"
		args = append(args, askedBy.String())
	}

	return s.questions(ctx, `SELECT `+questionColumns+` FROM questions WHERE `+where+` ORDER BY asked_at DESC, rowid DESC`, args...)
}

// Answered is which of the transactions have an answered question.
func (s *Store) Answered(ctx context.Context, ids []types.ID) (map[types.ID]bool, error) {
	marks := make([]string, len(ids))
	args := make([]any, len(ids))

	for i, id := range ids {
		marks[i], args[i] = "?", id.String()
	}

	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT transaction_id FROM questions WHERE state = 'answered' AND transaction_id IN (`+strings.Join(marks, ", ")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading which transactions have an answered question: %w", err)
	}
	defer rows.Close()

	out := map[types.ID]bool{}

	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("reading which transactions have an answered question: %w", err)
		}

		id, err := types.ParseID(raw)
		if err != nil {
			return nil, fmt.Errorf("a transaction is unreadable: %w", err)
		}

		out[id] = true
	}

	return out, rows.Err()
}

// questions reads the questions a query selects, in its order, and their
// replies, oldest first.
func (s *Store) questions(ctx context.Context, q string, args ...any) ([]questionbus.Question, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("reading questions: %w", err)
	}
	defer rows.Close()

	var (
		out []questionbus.Question
		at  = map[string]int{}
	)

	for rows.Next() {
		var (
			qn                                  questionbus.Question
			id, kind, about, homeKind, home, by string
			answeredBy, state                   string
			askedAt, answeredAt                 int64
		)

		if err := rows.Scan(&id, &kind, &about, &homeKind, &home, &by, &qn.Body, &askedAt, &state, &answeredBy, &answeredAt, &qn.Via); err != nil {
			return nil, fmt.Errorf("reading questions: %w", err)
		}

		var e [5]error
		qn.ID, e[0] = types.ParseID(id)
		qn.About.ID, e[1] = types.ParseID(about)
		qn.Home.ID, e[2] = types.ParseID(home)
		qn.AskedBy, e[3] = types.ParseID(by)

		if answeredBy != "" {
			qn.AnsweredBy, e[4] = types.ParseID(answeredBy)
			qn.AnsweredAt = time.UnixMilli(answeredAt).UTC()
		}

		if err := errors.Join(e[:]...); err != nil {
			return nil, fmt.Errorf("a question is unreadable: %w", err)
		}

		qn.About.Kind, qn.Home.Kind, qn.State = questionbus.Kind(kind), types.ScopeKind(homeKind), questionbus.State(state)
		qn.AskedAt = time.UnixMilli(askedAt).UTC()

		at[id] = len(out)
		out = append(out, qn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading questions: %w", err)
	}

	if len(out) == 0 {
		return out, nil
	}

	marks := make([]string, 0, len(at))
	ids := make([]any, 0, len(at))

	for id := range at {
		marks, ids = append(marks, "?"), append(ids, id)
	}

	replies, err := s.db.QueryContext(ctx, `SELECT id, question_id, author_id, body, at, via FROM question_replies WHERE question_id IN (`+strings.Join(marks, ", ")+`) ORDER BY at, rowid`, ids...)
	if err != nil {
		return nil, fmt.Errorf("reading the replies to questions: %w", err)
	}
	defer replies.Close()

	for replies.Next() {
		var (
			r                    questionbus.Reply
			id, question, author string
			when                 int64
		)

		if err := replies.Scan(&id, &question, &author, &r.Body, &when, &r.Via); err != nil {
			return nil, fmt.Errorf("reading the replies to questions: %w", err)
		}

		var e [3]error
		r.ID, e[0] = types.ParseID(id)
		r.QuestionID, e[1] = types.ParseID(question)
		r.AuthorID, e[2] = types.ParseID(author)

		if err := errors.Join(e[:]...); err != nil {
			return nil, fmt.Errorf("a reply is unreadable: %w", err)
		}

		r.At = time.UnixMilli(when).UTC()
		out[at[question]].Replies = append(out[at[question]].Replies, r)
	}

	return out, replies.Err()
}

func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback()

		return err
	}

	return tx.Commit()
}
