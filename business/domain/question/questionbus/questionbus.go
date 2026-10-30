// Package questionbus is the questions people ask about the books, and the
// answers (docs/accountant.md, 4).
//
// The accountant needs somewhere to ask "what was check 1322 for?" that is
// not another email, and the bookkeeper somewhere to answer it that is not
// another email either. A question is asked about one thing -- a
// transaction, a statement, a project or a payee -- and is open until
// somebody other than the asker replies, or the asker says it is answered.
// It is never reopened: a new question is a new thread.
//
// # Who
//
// Asking, replying and marking answered take Note on the thing; reading
// takes reading it. A transaction is reached as receipts reach it: through
// its account, or through a project one of its parts is in, so that
// somebody given a project alone asks about the project's charges without
// being given the accounts they are in. Everybody else is told a question
// is not there, the same answer as for one that does not exist.
//
// # Where its history goes
//
// A question's line of history is written where the thing's own history
// is: a transaction's and a statement's on the account, a project's on the
// project, a payee's on the list it is on. That scope is kept with the
// question as its Home, which is also where a list of an organization's
// questions starts from.
package questionbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// The answers every domain gives for a thing that is not there, or not
// theirs to change.
var (
	ErrNotFound  = tenancybus.ErrNotFound
	ErrForbidden = tenancybus.ErrForbidden
)

// ErrBody is a question or a reply with nothing in it, or too long.
var ErrBody = errors.New("write the question or the reply, in at most 2,000 characters")

// MaxBody is the longest a question or a reply may be.
const MaxBody = 2000

// Kind is what a question is about.
type Kind string

// The things a question may be about. Not an account nor an organization:
// a question about an account is asked about its most relevant transaction
// or statement, where the answer is wanted.
const (
	OnTransaction Kind = "transaction"
	OnStatement   Kind = "statement"
	OnProject     Kind = "project"
	OnPayee       Kind = "payee"
)

// Valid reports whether k is one of the four.
func (k Kind) Valid() bool {
	switch k {
	case OnTransaction, OnStatement, OnProject, OnPayee:
		return true
	}

	return false
}

// About is the thing a question is about.
type About struct {
	Kind Kind
	ID   types.ID
}

// State is whether a question is waiting for its answer.
type State string

// The two states. Answered is for good: a reply after it is a reply.
const (
	Open     State = "open"
	Answered State = "answered"
)

// Question is a question with its replies, oldest first.
type Question struct {
	ID    types.ID
	About About

	// Home is the scope the thing's history is written on (the package
	// comment): an account, a project, or a payee list's owner.
	Home types.Scope

	AskedBy types.ID
	Body    string
	AskedAt time.Time

	State      State
	AnsweredBy types.ID
	AnsweredAt time.Time

	// Via is the key it was asked through, '' on the site.
	Via string

	Replies []Reply
}

// openingLen is how much of a question its one line in a list shows.
const openingLen = 120

// Opening is the question's first line, shortened for a list: what a
// thing's page and an organization's list show of it, the whole being on
// its own page.
func (q Question) Opening() string {
	line, _, _ := strings.Cut(q.Body, "\n")
	line = strings.TrimSpace(line)

	if utf8.RuneCountInString(line) > openingLen {
		line = strings.TrimSpace(string([]rune(line)[:openingLen-1])) + "…"
	}

	return line
}

// Reply is a message in a question's thread after the question.
type Reply struct {
	ID         types.ID
	QuestionID types.ID
	AuthorID   types.ID
	Body       string
	At         time.Time
	Via        string
}

// The lines this domain writes into the history.
const (
	Asked          eventbus.Action = "question.asked"
	Replied        eventbus.Action = "question.replied"
	MarkedAnswered eventbus.Action = "question.answered"
)

// Access is how this domain asks who may do what (tenancybus).
type Access interface {
	AccessTo(ctx context.Context, actor types.ID, scope types.Scope) (tenancybus.Access, error)
	Project(ctx context.Context, actor, id types.ID) (tenancybus.Project, tenancybus.Access, error)
	Contents(ctx context.Context, actor, orgID types.ID) ([]tenancybus.Account, []tenancybus.Project, error)
	AccountNamed(ctx context.Context, id types.ID) (tenancybus.Account, error)
}

// Ledger is the transactions, statements and payees questions are asked
// about (ledgerbus).
type Ledger interface {
	Lookup(ctx context.Context, id types.ID) (ledgerbus.Transaction, error)
	Statement(ctx context.Context, actor, id types.ID) (ledgerbus.Statement, tenancybus.Access, error)
	Payee(ctx context.Context, actor, id types.ID) (ledgerbus.Payee, tenancybus.Access, error)
}

// Storer keeps questions.
type Storer interface {
	// Ask stores a question and its line of history in one transaction.
	Ask(ctx context.Context, q Question, ev eventbus.Event) error

	// Reply stores a reply and its line of history, and, when answers is
	// set, marks the question answered by the reply's author if it is
	// still open, in one transaction.
	Reply(ctx context.Context, r Reply, answers bool, ev eventbus.Event) error

	// MarkAnswered marks an open question answered, with its line of
	// history; one already answered is left as it is, and writes none.
	MarkAnswered(ctx context.Context, id, by types.ID, at time.Time, ev eventbus.Event) error

	// ByID is a question with its replies.
	ByID(ctx context.Context, id types.ID) (Question, error)

	// On is the questions about a thing, newest first, with their replies.
	On(ctx context.Context, about About) ([]Question, error)

	// InHomes is the questions whose home is any of the scopes, newest
	// first, with their replies: those in a state, or in either when it is
	// "", and those one person asked when askedBy is not zero.
	InHomes(ctx context.Context, homes []types.Scope, state State, askedBy types.ID) ([]Question, error)

	// Answered is which of some transactions have a question about them
	// that has been answered.
	Answered(ctx context.Context, ids []types.ID) (map[types.ID]bool, error)
}

// Business is the set of operations on questions.
type Business struct {
	log    *slog.Logger
	store  Storer
	access Access
	ledger Ledger
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer, access Access, ledger Ledger) *Business {
	return &Business{log: log, store: store, access: access, ledger: ledger}
}

// thing is what a question is about, as this domain reaches it: where its
// history goes, what the history calls it, and whether the actor may read
// it and write on it. A transaction's is t, and whole is whether the actor
// may read its account, rather than only a project one of its parts is in.
type thing struct {
	home       types.Scope
	what       string
	read, note bool
	t          ledgerbus.Transaction
	whole      bool
}

// reach is the thing a question is about, for the actor, or ErrNotFound
// when there is none or they may not read it.
func (b *Business) reach(ctx context.Context, actor types.ID, about About) (thing, error) {
	var (
		th     thing
		access tenancybus.Access
		err    error
	)

	switch about.Kind {
	case OnTransaction:
		t, err := b.ledger.Lookup(ctx, about.ID)
		if err != nil {
			return thing{}, err
		}

		th.home, th.t = types.AccountScope(t.AccountID), t
		th.what = fmt.Sprintf("%s, %s", t.Shown(), t.PostedOn)

		account, err := b.access.AccessTo(ctx, actor, th.home)
		if err != nil {
			return thing{}, err
		}

		th.whole = account.Can(tenancybus.Read)

		if th.read, err = b.onTransaction(ctx, actor, t, tenancybus.Read); err != nil {
			return thing{}, err
		}

		if th.note, err = b.onTransaction(ctx, actor, t, tenancybus.Note); err != nil {
			return thing{}, err
		}

		if !th.read {
			return thing{}, ErrNotFound
		}

		return th, nil

	case OnStatement:
		var st ledgerbus.Statement
		if st, access, err = b.ledger.Statement(ctx, actor, about.ID); err != nil {
			return thing{}, err
		}

		th.home, th.what = types.AccountScope(st.AccountID), st.FileName

	case OnProject:
		var p tenancybus.Project
		if p, access, err = b.access.Project(ctx, actor, about.ID); err != nil {
			return thing{}, err
		}

		th.home, th.what = types.ProjectScope(p.ID), p.Name

	case OnPayee:
		var p ledgerbus.Payee
		if p, access, err = b.ledger.Payee(ctx, actor, about.ID); err != nil {
			return thing{}, err
		}

		th.home, th.what = p.Owner, p.Name

	default:
		return thing{}, ErrNotFound
	}

	th.read, th.note = access.Can(tenancybus.Read), access.Can(tenancybus.Note)
	if !th.read {
		return thing{}, ErrNotFound
	}

	return th, nil
}

// onTransaction reports whether the actor holds p on a transaction: on its
// account, or on a project one of its parts is in, as receipts are.
func (b *Business) onTransaction(ctx context.Context, actor types.ID, t ledgerbus.Transaction, p tenancybus.Permission) (bool, error) {
	scopes := []types.Scope{types.AccountScope(t.AccountID)}

	for _, s := range t.Splits {
		if !s.ProjectID.Zero() {
			scopes = append(scopes, types.ProjectScope(s.ProjectID))
		}
	}

	for _, scope := range scopes {
		access, err := b.access.AccessTo(ctx, actor, scope)
		if err != nil {
			return false, err
		}

		if access.Can(p) {
			return true, nil
		}
	}

	return false, nil
}

// body is a question's or a reply's text as kept: trimmed, and refused
// when empty or too long.
func body(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > MaxBody {
		return "", ErrBody
	}

	return s, nil
}

// Ask asks a question about a thing.
func (b *Business) Ask(ctx context.Context, now time.Time, actor types.ID, about About, text string) (Question, error) {
	th, err := b.reach(ctx, actor, about)
	if err != nil {
		return Question{}, err
	}

	if !th.note {
		return Question{}, ErrForbidden
	}

	text, err = body(text)
	if err != nil {
		return Question{}, err
	}

	q := Question{
		ID: types.NewID(), About: about, Home: th.home,
		AskedBy: actor, Body: text, AskedAt: now, State: Open,
		Via: eventbus.ViaFrom(ctx),
	}

	ev := eventbus.New(now, actor, th.home, Asked, map[string]string{"question": q.ID.String(), "on": string(about.Kind), "what": th.what})

	if err := b.store.Ask(ctx, q, ev); err != nil {
		return Question{}, err
	}

	return q, nil
}

// Thread is a question as its own page shows it, to one reader.
type Thread struct {
	Question

	// What is what it is about, as the history names it.
	What string

	// CanReply is whether the reader may reply, and CanMark whether they
	// may mark it answered: the asker, while it is open.
	CanReply, CanMark bool

	// Transaction is, for a question about one, the transaction as the
	// reader may see it: all of it when Whole, which is when they may
	// read its account; otherwise only its parts in the projects they
	// were given, never the rest of the charge (docs/plan.md, "Domain
	// model": roles). Account names it and its currency, nothing more.
	Transaction ledgerbus.Transaction
	Whole       bool
	Account     tenancybus.Account
}

// Question is a question's thread, for anyone who may read what it is
// about.
func (b *Business) Question(ctx context.Context, actor, id types.ID) (Thread, error) {
	q, err := b.store.ByID(ctx, id)
	if err != nil {
		return Thread{}, err
	}

	th, err := b.reach(ctx, actor, q.About)
	if err != nil {
		return Thread{}, err
	}

	t := Thread{
		Question: q, What: th.what,
		CanReply: th.note, CanMark: th.note && actor == q.AskedBy && q.State == Open,
	}

	if q.About.Kind != OnTransaction {
		return t, nil
	}

	t.Transaction, t.Whole = th.t, th.whole

	if !t.Whole {
		var parts []ledgerbus.Split

		for _, s := range th.t.Splits {
			if s.ProjectID.Zero() {
				continue
			}

			access, err := b.access.AccessTo(ctx, actor, types.ProjectScope(s.ProjectID))
			if err != nil {
				return Thread{}, err
			}

			if access.Can(tenancybus.Read) {
				parts = append(parts, s)
			}
		}

		t.Transaction.Splits = parts
	}

	if t.Account, err = b.access.AccountNamed(ctx, th.t.AccountID); err != nil {
		return Thread{}, err
	}

	return t, nil
}

// Summary is the questions about a thing, for the thing's own page.
type Summary struct {
	About About

	// Open is those still waiting, and Answered the others, each newest
	// first.
	Open, Answered []Question

	// CanAsk is whether the reader may ask one.
	CanAsk bool
}

// Summary is the questions about a thing, for anyone who may read it.
func (b *Business) Summary(ctx context.Context, actor types.ID, about About) (Summary, error) {
	th, err := b.reach(ctx, actor, about)
	if err != nil {
		return Summary{}, err
	}

	qs, err := b.store.On(ctx, about)
	if err != nil {
		return Summary{}, err
	}

	s := Summary{About: about, CanAsk: th.note}

	for _, q := range qs {
		if q.State == Open {
			s.Open = append(s.Open, q)
		} else {
			s.Answered = append(s.Answered, q)
		}
	}

	return s, nil
}

// Listed is a question in an organization's list, with what it is about.
type Listed struct {
	Question
	What string
}

// InOrg is the questions about anything in an organization -- its
// accounts' transactions and statements, its projects, its payees --
// newest first, for anyone who may read the organization, and so all of
// it: those in a state, or either when it is "", and those one person
// asked when askedBy is not zero.
func (b *Business) InOrg(ctx context.Context, actor, orgID types.ID, state State, askedBy types.ID) ([]Listed, error) {
	org := types.OrgScope(orgID)

	access, err := b.access.AccessTo(ctx, actor, org)
	if err != nil {
		return nil, err
	}

	if !access.Can(tenancybus.Read) {
		return nil, ErrNotFound
	}

	accounts, projects, err := b.access.Contents(ctx, actor, orgID)
	if err != nil {
		return nil, err
	}

	homes := []types.Scope{org}
	for _, a := range accounts {
		homes = append(homes, types.AccountScope(a.ID))
	}

	for _, p := range projects {
		homes = append(homes, types.ProjectScope(p.ID))
	}

	qs, err := b.store.InHomes(ctx, homes, state, askedBy)
	if err != nil {
		return nil, err
	}

	out := make([]Listed, 0, len(qs))

	for _, q := range qs {
		th, err := b.reach(ctx, actor, q.About)

		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			return nil, err
		}

		out = append(out, Listed{Question: q, What: th.what})
	}

	return out, nil
}

// Reply replies to a question. A reply by anybody but the asker answers it
// if it was open.
func (b *Business) Reply(ctx context.Context, now time.Time, actor, id types.ID, text string) (Reply, error) {
	q, err := b.store.ByID(ctx, id)
	if err != nil {
		return Reply{}, err
	}

	th, err := b.reach(ctx, actor, q.About)
	if err != nil {
		return Reply{}, err
	}

	if !th.note {
		return Reply{}, ErrForbidden
	}

	text, err = body(text)
	if err != nil {
		return Reply{}, err
	}

	r := Reply{ID: types.NewID(), QuestionID: q.ID, AuthorID: actor, Body: text, At: now, Via: eventbus.ViaFrom(ctx)}
	answers := q.State == Open && actor != q.AskedBy

	action := Replied
	if answers {
		action = MarkedAnswered
	}

	ev := eventbus.New(now, actor, q.Home, action, map[string]string{"question": q.ID.String(), "on": string(q.About.Kind), "what": th.what})

	if err := b.store.Reply(ctx, r, answers, ev); err != nil {
		return Reply{}, err
	}

	return r, nil
}

// MarkAnswered is the asker saying their question is answered, which is
// theirs alone to say: anybody else answers it by replying.
func (b *Business) MarkAnswered(ctx context.Context, now time.Time, actor, id types.ID) error {
	q, err := b.store.ByID(ctx, id)
	if err != nil {
		return err
	}

	th, err := b.reach(ctx, actor, q.About)
	if err != nil {
		return err
	}

	if !th.note || actor != q.AskedBy {
		return ErrForbidden
	}

	ev := eventbus.New(now, actor, q.Home, MarkedAnswered, map[string]string{"question": q.ID.String(), "on": string(q.About.Kind), "what": th.what})

	return b.store.MarkAnswered(ctx, q.ID, actor, now, ev)
}

// Answered is which of some transactions have a question about them that
// has been answered, for the accountant's lists (ledgerbus.OnAnswered),
// which have already decided who may see the transactions.
func (b *Business) Answered(ctx context.Context, ids []types.ID) (map[types.ID]bool, error) {
	if len(ids) == 0 {
		return map[types.ID]bool{}, nil
	}

	return b.store.Answered(ctx, ids)
}
