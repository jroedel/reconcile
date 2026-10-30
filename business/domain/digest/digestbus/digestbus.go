// Package digestbus is what a person's weekly email says (docs/accountant.md,
// 5): for each organization they read, what is new since the last one that
// is theirs to look at.
//
// # What is new
//
// Questions say when they were asked and answered. The accountant's lists
// say nothing of when a transaction entered them: context comes from a
// receipt attached, a description written, a payee named or a question
// answered, and attaching a receipt writes no line of history, on purpose.
// So rather than reading the history, an email remembers what it saw: the
// transactions in each list for that person when it was sent (Seen), and
// the next one's "new" is what is in a list now and was not in it then,
// however it got there. That is not a mark anybody sets, and it makes no
// work for anybody: it is what the email told them, as its cursor is.
//
// # Whose lines
//
//   - Whoever may keep an organization's books hears how many transactions
//     newly need more context, with how many need it in all: theirs to do.
//   - An accountant there who does not keep the books hears how many
//     newly have context added, with how many have it: theirs to read.
//   - Whoever may write on it hears of the questions others asked since,
//     still open, and of the answers others gave to their own.
//
// A viewer hears nothing, and an email with nothing in it is not sent.
// What a person did themselves is left out where a line could name it: their
// own questions are not news to them, nor their own answers.
package digestbus

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/question/questionbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// FirstWeek is how far back the first email looks for questions, for a
// person who has had none before: a week, so that somebody given a role
// today hears of this week's, and not of every question ever asked.
const FirstWeek = 7 * 24 * time.Hour

// Access is how this domain asks what a person reads (tenancybus).
type Access interface {
	Overview(ctx context.Context, actor types.ID) (tenancybus.Overview, error)
	AccessTo(ctx context.Context, actor types.ID, scope types.Scope) (tenancybus.Access, error)
}

// Ledger is the accountant's lists (ledgerbus).
type Ledger interface {
	Explore(ctx context.Context, actor types.ID, scope types.Scope, c ledgerbus.Criteria) (ledgerbus.Explored, error)
}

// Questions is an organization's questions (questionbus).
type Questions interface {
	InOrg(ctx context.Context, actor, orgID types.ID, state questionbus.State, askedBy types.ID) ([]questionbus.Listed, error)
}

// Storer keeps what each person's last email saw.
type Storer interface {
	// Seen is the transactions in each list when the person's last email
	// was sent.
	Seen(ctx context.Context, userID types.ID) (map[types.ID]ledgerbus.ForAccountant, error)

	// SetSeen replaces it, in one transaction.
	SetSeen(ctx context.Context, userID types.ID, seen map[types.ID]ledgerbus.ForAccountant) error
}

// Business builds the weekly email's contents.
type Business struct {
	store     Storer
	access    Access
	ledger    Ledger
	questions Questions
}

// NewBusiness constructs one.
func NewBusiness(store Storer, access Access, ledger Ledger, questions Questions) *Business {
	return &Business{store: store, access: access, ledger: ledger, questions: questions}
}

// Week is what one person's email says, organization by organization, and
// what it saw, to keep once it is sent.
type Week struct {
	Orgs []OrgWeek
	Seen map[types.ID]ledgerbus.ForAccountant
}

// Empty reports whether there is nothing to say, and so no email.
func (w Week) Empty() bool { return len(w.Orgs) == 0 }

// OrgWeek is one organization's part of an email.
type OrgWeek struct {
	Org tenancybus.Org

	// Keeps is whether the person may keep its books: then Needed is how
	// many transactions need more context, and NewlyNeeded how many of
	// them did not when the last email was sent.
	Keeps               bool
	Needed, NewlyNeeded int

	// Reads is whether they are its accountant and do not keep its books:
	// then Ready is how many have context added, and NewlyReady how many
	// of them did not.
	Reads             bool
	Ready, NewlyReady int

	// Asked is questions others asked since, still open, when the person
	// may answer; Answered is the person's own questions others answered
	// since.
	Asked, Answered []questionbus.Listed
}

func (o OrgWeek) something() bool {
	return o.NewlyNeeded > 0 || o.NewlyReady > 0 || len(o.Asked) > 0 || len(o.Answered) > 0
}

// Build is what a person's email says, sent at now, about what is new since
// their last one, sent at since: zero for none yet, when the lists are only
// seen, with nothing new in them, and questions are read from FirstWeek
// back.
func (b *Business) Build(ctx context.Context, now time.Time, userID types.ID, since time.Time) (Week, error) {
	first := since.IsZero()
	if first {
		since = now.Add(-FirstWeek)
	}

	before, err := b.store.Seen(ctx, userID)
	if err != nil {
		return Week{}, err
	}

	ov, err := b.access.Overview(ctx, userID)
	if err != nil {
		return Week{}, err
	}

	week := Week{Seen: map[types.ID]ledgerbus.ForAccountant{}}

	for _, o := range ov.Orgs {
		org := o.Org

		access, err := b.access.AccessTo(ctx, userID, org.Scope())
		if err != nil {
			return Week{}, err
		}

		if !access.Can(tenancybus.Read) {
			continue
		}

		ow := OrgWeek{Org: org, Keeps: access.Can(tenancybus.Bookkeep)}
		ow.Reads = !ow.Keeps && slices.Contains(access.Roles, tenancybus.Accountant)

		// Both lists are seen for everybody who reads, whichever they are
		// told of, so that a role that changes between two emails does not
		// make a year's transactions new.
		for _, list := range []ledgerbus.ForAccountant{ledgerbus.MoreContextNeeded, ledgerbus.ContextAdded} {
			all, err := b.ledger.Explore(ctx, userID, org.Scope(), ledgerbus.Criteria{Accountant: list, Limit: math.MaxInt32})
			if err != nil {
				return Week{}, err
			}

			fresh := 0

			for _, r := range all.Rows {
				id := r.Transaction.ID
				week.Seen[id] = list

				if !first && before[id] != list {
					fresh++
				}
			}

			if list == ledgerbus.MoreContextNeeded {
				ow.Needed, ow.NewlyNeeded = all.Total, fresh
			} else {
				ow.Ready, ow.NewlyReady = all.Total, fresh
			}
		}

		if !ow.Keeps {
			ow.NewlyNeeded = 0
		}

		if !ow.Reads {
			ow.NewlyReady = 0
		}

		if ow.Asked, ow.Answered, err = b.news(ctx, userID, org.ID, since, access.Can(tenancybus.Note)); err != nil {
			return Week{}, err
		}

		if ow.something() {
			week.Orgs = append(week.Orgs, ow)
		}
	}

	return week, nil
}

// news is an organization's questions others asked since, still open, when
// the person may answer them, and their own that others answered since.
func (b *Business) news(ctx context.Context, userID, orgID types.ID, since time.Time, answers bool) ([]questionbus.Listed, []questionbus.Listed, error) {
	var asked, answered []questionbus.Listed

	if answers {
		open, err := b.questions.InOrg(ctx, userID, orgID, questionbus.Open, types.ID{})
		if err != nil {
			return nil, nil, err
		}

		for _, q := range open {
			if q.AskedBy != userID && q.AskedAt.After(since) {
				asked = append(asked, q)
			}
		}
	}

	mine, err := b.questions.InOrg(ctx, userID, orgID, questionbus.Answered, userID)
	if err != nil {
		return nil, nil, err
	}

	for _, q := range mine {
		if q.AnsweredBy != userID && q.AnsweredAt.After(since) {
			answered = append(answered, q)
		}
	}

	return asked, answered, nil
}

// Sent keeps what an email saw, once it has gone.
func (b *Business) Sent(ctx context.Context, userID types.ID, w Week) error {
	return b.store.SetSeen(ctx, userID, w.Seen)
}
