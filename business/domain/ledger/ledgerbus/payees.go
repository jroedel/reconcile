package ledgerbus

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// Payees (docs/payees.md): whom each payment went to.
//
// A transaction's payee text (Transaction.Payee) is what was written -- a
// check's "Pay to the order of" line -- and its payee (PayeeID) is who that
// is. The two are kept apart because the same plumber is written three ways
// over a year of checks, and the accountant's question is what the plumber
// was paid, not what each spelling was.
//
// A list of payees is kept as a list of categories is: one per
// organization, shared by all its accounts, or one per account with no
// organization (categorybus.OwnerOf). Every account's books have one; an
// account with no organization is not assumed to be somebody's household,
// since a community may keep its account here without being given the
// organization that owns it in law.
//
// # Spellings
//
// Every way a payee has been written is one of its spellings, and a
// spelling belongs to one payee of a list at most, whatever its case. So a
// transaction's payee follows from its text: the payee whose spelling it
// is, or a new payee named as written. A payee's name is always one of its
// spellings, so that choosing a payee by name finds it.
//
// Joining puts one payee's spellings and transactions under another, and
// splitting a spelling off takes it and its transactions back out. Nothing
// joins on its own: two people with one name are two payees until somebody
// who knows says otherwise. Suggestions only say which look alike.
//
// # Why in the ledger
//
// Payees are kept by the ledger rather than by a domain of their own,
// unlike categories, because joining rewrites which transactions are
// whose: the spellings and the transactions move together, in one SQL
// transaction, and a store of their own could do that only by writing
// another domain's table.
//
// # Through a key
//
// A program -- the person's Claude, on their yes -- may join payees, split
// them and set a payment's payee through a key (docs/payees.md, 5). What
// it changes says so on the site until a person looks: a payee it made,
// joined into or split carries the key (Payee.Via), the spellings it
// brought into a payee carry it too (Payee.BroughtVia), and so does a
// payment's payee (Transaction.PayeeVia). Any change a person makes to a
// payee on the site, or saving its name unchanged, takes its marks off,
// as saving a project or a rule does.

// The history's lines for a list of payees, written into its owner's.
const (
	PayeeAdded    eventbus.Action = "payee.added"
	PayeeRenamed  eventbus.Action = "payee.renamed"
	PayeeArchived eventbus.Action = "payee.archived"
	PayeeRestored eventbus.Action = "payee.restored"
	PayeeJoined   eventbus.Action = "payee.joined"
	PayeeSplit    eventbus.Action = "payee.split"
	PayeeNotSame  eventbus.Action = "payee.not-same"
)

// The reasons a change to payees is refused, each worded by the pages.
var (
	// ErrPayeeName is a name that is empty or longer than MaxPayee.
	ErrPayeeName = errors.New("a payee's name is at most 100 characters, and not empty")

	// ErrPayeeTaken is a name, or a spelling, another payee of the list
	// already has. An archived one is brought back rather than made twice.
	ErrPayeeTaken = errors.New("another payee of the list is already named or written that way")

	// ErrJoinSelf is a payee joined into itself.
	ErrJoinSelf = errors.New("a payee cannot be joined into itself")

	// ErrJoinLists is two payees of different lists: an organization's
	// payees and another's are different people, even with one name.
	ErrJoinLists = errors.New("payees of two lists cannot be joined")

	// ErrJoinArchived is a join into an archived payee, which is off the
	// choices; bring it back first.
	ErrJoinArchived = errors.New("an archived payee cannot be joined into; bring it back first")

	// ErrSplitName is splitting off the spelling that is the payee's own
	// name, which would leave it named by a spelling it no longer has.
	ErrSplitName = errors.New("a payee's own name cannot be split off; rename it first")
)

// Payee is one entry in a list of payees.
type Payee struct {
	ID types.ID

	// Owner is the organization whose list it is on, or the account with
	// no organization.
	Owner types.Scope

	Name string

	// Spellings is every way it has been written, its name among them, in
	// the order of the alphabet.
	Spellings []string

	// Payments is how many transactions it is the payee of.
	Payments int

	// JoinedInto is the payee it was joined into, or zero. A joined payee
	// is off the list; its page sends a reader on to the one it is in now.
	JoinedInto types.ID

	// Reported, DetailsOnFile and Note are the marks a person sets
	// (payees_year.go): whether its payments are to be reported to the
	// accountant, whether its tax details are on file, and why.
	Reported      bool
	DetailsOnFile bool
	Note          string

	// Via is the API key a program last made, joined into or split this
	// payee through, until a person changes or saves it on the site.
	// BroughtVia is the spellings it brought in from other payees, by
	// joining or splitting, until then: what a person is asked to check.
	Via        string
	BroughtVia []string

	CreatedBy  types.ID
	CreatedAt  time.Time
	ArchivedAt time.Time
}

// Archived reports whether it is off the choices.
func (p Payee) Archived() bool { return !p.ArchivedAt.IsZero() }

// Marked reports whether a program changed it and no person has looked.
func (p Payee) Marked() bool { return p.Via != "" || len(p.BroughtVia) > 0 }

// Joined reports whether it was joined into another.
func (p Payee) Joined() bool { return !p.JoinedInto.Zero() }

// PayeePayment is a payment to a payee, with the name and currency of the
// account it was made from.
type PayeePayment struct {
	Transaction
	AccountName string
	Currency    string
}

// JoinSuggestion is two payees of a list whose names say they may be one
// (PayeeKey): From would be joined into Into, the one with more payments.
type JoinSuggestion struct {
	Into, From Payee
}

// PayeeOwner is whose list an account's payees are on: its
// organization's, or its own when it has none.
func PayeeOwner(a tenancybus.Account) types.Scope { return categorybus.OwnerOf(a) }

// requirePayees answers ErrNotFound for a list the actor cannot read and
// ErrForbidden for one they cannot keep. Only an organization or an account
// with no organization has a list, as with categories.
func (b *Business) requirePayees(ctx context.Context, actor types.ID, owner types.Scope, p tenancybus.Permission) (tenancybus.Access, error) {
	var (
		access tenancybus.Access
		err    error
	)

	switch owner.Kind {
	case types.ScopeOrg:
		access, err = b.accounts.AccessTo(ctx, actor, owner)
	case types.ScopeAccount:
		var a tenancybus.Account

		a, access, err = b.accounts.Account(ctx, actor, owner.ID)
		if err == nil && !a.OrgID.Zero() {
			return tenancybus.Access{}, ErrNotFound
		}
	default:
		return tenancybus.Access{}, ErrNotFound
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return tenancybus.Access{}, ErrNotFound
	case err != nil:
		return tenancybus.Access{}, err
	case !access.Can(tenancybus.Read):
		return tenancybus.Access{}, ErrNotFound
	case !access.Can(p):
		return access, ErrForbidden
	}

	return access, nil
}

// Payees is a list, archived payees last, and what the actor holds on its
// owner.
func (b *Business) Payees(ctx context.Context, actor types.ID, owner types.Scope) ([]Payee, tenancybus.Access, error) {
	access, err := b.requirePayees(ctx, actor, owner, tenancybus.Read)
	if err != nil {
		return nil, tenancybus.Access{}, err
	}

	list, err := b.store.Payees(ctx, owner)

	return list, access, err
}

// Payee is one payee and what the actor holds on its list. A joined payee
// is found, so that its page can send the reader on.
func (b *Business) Payee(ctx context.Context, actor, id types.ID) (Payee, tenancybus.Access, error) {
	p, err := b.store.PayeeByID(ctx, id)
	if err != nil {
		return Payee{}, tenancybus.Access{}, err
	}

	access, err := b.requirePayees(ctx, actor, p.Owner, tenancybus.Read)
	if err != nil {
		return Payee{}, tenancybus.Access{}, err
	}

	return p, access, nil
}

// PayeePayments is a payee's most recent transactions, newest first, at
// most limit, for its page. Who may read the list may read them: a role
// on an organization is one on all its accounts.
func (b *Business) PayeePayments(ctx context.Context, actor, id types.ID, limit int) ([]PayeePayment, error) {
	if _, _, err := b.Payee(ctx, actor, id); err != nil {
		return nil, err
	}

	return b.store.PayeeTransactions(ctx, id, limit)
}

// CreatePayee adds a payee to a list, named and written as given.
func (b *Business) CreatePayee(ctx context.Context, now time.Time, actor types.ID, owner types.Scope, name string) (Payee, error) {
	if _, err := b.requirePayees(ctx, actor, owner, tenancybus.Bookkeep); err != nil {
		return Payee{}, err
	}

	name, err := cleanPayee(name)
	if err != nil {
		return Payee{}, err
	}

	p := Payee{ID: types.NewID(), Owner: owner, Name: name, Spellings: []string{name}, Via: eventbus.ViaFrom(ctx), CreatedBy: actor, CreatedAt: now}

	if err := b.store.CreatePayee(ctx, p, eventbus.New(now, actor, owner, PayeeAdded, map[string]string{"name": name})); err != nil {
		return Payee{}, err
	}

	return p, nil
}

// keptPayee is a payee, not joined into another, and the check that the
// actor may keep its list.
func (b *Business) keptPayee(ctx context.Context, actor, id types.ID) (Payee, error) {
	p, err := b.store.PayeeByID(ctx, id)
	if err != nil {
		return Payee{}, err
	}

	if _, err := b.requirePayees(ctx, actor, p.Owner, tenancybus.Bookkeep); err != nil {
		return Payee{}, err
	}

	if p.Joined() {
		return Payee{}, ErrNotFound
	}

	return p, nil
}

// RenamePayee changes a payee's name. The new name becomes one of its
// spellings too, so that a check written that way next month finds it; the
// old name stays one, since a check was written that way. A name that will
// not do comes back with the payee as it was.
//
// Saved on the web, it takes a program's marks off the payee, even
// unchanged: saving says a person has looked, and the history has nothing
// new to hear.
func (b *Business) RenamePayee(ctx context.Context, now time.Time, actor, id types.ID, name string) (Payee, error) {
	p, err := b.keptPayee(ctx, actor, id)
	if err != nil {
		return Payee{}, err
	}

	name, err = cleanPayee(name)
	if err != nil {
		return p, err
	}

	via := eventbus.ViaFrom(ctx)

	if name == p.Name {
		if via != "" || !p.Marked() {
			return p, nil
		}

		p.Via, p.BroughtVia = "", nil

		return p, b.store.UpdatePayee(ctx, p, "", nil)
	}

	from := p.Name
	p.Name, p.Via = name, via

	if via == "" {
		p.BroughtVia = nil
	}

	if !slices.ContainsFunc(p.Spellings, func(s string) bool { return strings.EqualFold(s, name) }) {
		p.Spellings = append(p.Spellings, name)
		slices.SortFunc(p.Spellings, compareFold)
	}

	ev := eventbus.New(now, actor, p.Owner, PayeeRenamed, map[string]string{"from": from, "name": name})

	return p, b.store.UpdatePayee(ctx, p, name, &ev)
}

// SetPayeeArchived takes a payee off the choices, or puts it back. Its
// transactions keep it, and a check written one of its ways still finds
// it.
func (b *Business) SetPayeeArchived(ctx context.Context, now time.Time, actor, id types.ID, archived bool) (Payee, error) {
	p, err := b.keptPayee(ctx, actor, id)
	if err != nil {
		return Payee{}, err
	}

	if p.Archived() == archived {
		return p, nil
	}

	action := PayeeRestored

	p.ArchivedAt = time.Time{}
	if archived {
		p.ArchivedAt, action = now, PayeeArchived
	}

	if p.Via = eventbus.ViaFrom(ctx); p.Via == "" {
		p.BroughtVia = nil
	}

	ev := eventbus.New(now, actor, p.Owner, action, map[string]string{"name": p.Name})

	return p, b.store.UpdatePayee(ctx, p, "", &ev)
}

// JoinPayees puts each of from, with its spellings and transactions, into
// into: one SQL transaction, a line of history for each payee joined. The
// joined payees leave the list, and SplitPayee takes a spelling back out.
//
// All must be on one list the actor keeps the books of; into must not be
// archived, and none may be joined already.
func (b *Business) JoinPayees(ctx context.Context, now time.Time, actor, into types.ID, from []types.ID) (Payee, error) {
	target, err := b.keptPayee(ctx, actor, into)
	if err != nil {
		return Payee{}, err
	}

	if target.Archived() {
		return target, ErrJoinArchived
	}

	via := eventbus.ViaFrom(ctx)

	var (
		ids     []types.ID
		events  []eventbus.Event
		brought []string
	)

	for _, id := range from {
		if id == into {
			return target, ErrJoinSelf
		}

		if slices.Contains(ids, id) {
			continue
		}

		// Asked of its own list first, so that a payee of a list the
		// actor cannot read is not found, rather than "of another list".
		p, err := b.keptPayee(ctx, actor, id)
		if err != nil {
			return target, err
		}

		if p.Owner != target.Owner {
			return target, ErrJoinLists
		}

		ids = append(ids, id)
		events = append(events, eventbus.New(now, actor, target.Owner, PayeeJoined, map[string]string{
			"from": p.Name, "name": target.Name, "spellings": strings.Join(p.Spellings, "; "), "payments": strconv.Itoa(p.Payments),
		}))

		target.Spellings = append(target.Spellings, p.Spellings...)
		target.Payments += p.Payments
		brought = append(brought, p.Spellings...)
	}

	if len(ids) == 0 {
		return target, nil
	}

	slices.SortFunc(target.Spellings, compareFold)

	if err := b.store.JoinPayees(ctx, now, target.ID, ids, via, events); err != nil {
		return Payee{}, err
	}

	target.Via = via

	switch via {
	case "":
		target.BroughtVia = nil
	default:
		target.BroughtVia = append(target.BroughtVia, brought...)
		slices.SortFunc(target.BroughtVia, compareFold)
	}

	return target, nil
}

// SplitPayee takes one of a payee's spellings, with the transactions
// written that way, out of it: into another payee of the list, or, with
// into zero, a payee of its own named as written -- the one it was joined
// out of, brought back, when there is one. It undoes a join a spelling at a
// time, and is how two people with one payee's name are told apart.
//
// The answer is the payee the spelling went to.
func (b *Business) SplitPayee(ctx context.Context, now time.Time, actor, id types.ID, spelling string, into types.ID) (Payee, error) {
	p, err := b.keptPayee(ctx, actor, id)
	if err != nil {
		return Payee{}, err
	}

	i := slices.IndexFunc(p.Spellings, func(s string) bool { return strings.EqualFold(s, strings.Join(strings.Fields(spelling), " ")) })
	if i < 0 {
		return p, ErrNotFound
	}

	spelling = p.Spellings[i]

	if strings.EqualFold(spelling, p.Name) {
		return p, ErrSplitName
	}

	var target Payee

	switch {
	case into == id:
		return p, ErrJoinSelf
	case !into.Zero():
		if target, err = b.keptPayee(ctx, actor, into); err != nil {
			return p, err
		}

		if target.Owner != p.Owner {
			return p, ErrJoinLists
		}

		if target.Archived() {
			return p, ErrJoinArchived
		}
	default:
		// The payee it was joined out of, if one is named so; or a new
		// one. A payee named so and not joined has the spelling already,
		// which i found it on, so it is this one.
		was, err := b.store.PayeeNamed(ctx, p.Owner, spelling)

		switch {
		case err == nil && was.Joined():
			target = was
			target.JoinedInto, target.ArchivedAt = types.ID{}, time.Time{}
		case err == nil || errors.Is(err, ErrNotFound):
			target = Payee{ID: types.NewID(), Owner: p.Owner, Name: spelling, CreatedBy: actor, CreatedAt: now}
		default:
			return p, err
		}
	}

	target.Via = eventbus.ViaFrom(ctx)

	ev := eventbus.New(now, actor, p.Owner, PayeeSplit, map[string]string{"from": p.Name, "spelling": spelling, "name": target.Name})

	moved, err := b.store.SplitPayee(ctx, p.ID, spelling, target, ev)
	if err != nil {
		return p, err
	}

	same := func(s string) bool { return strings.EqualFold(s, spelling) }

	target.Spellings = append(slices.DeleteFunc(target.Spellings, same), spelling)
	slices.SortFunc(target.Spellings, compareFold)
	target.Payments += moved

	switch target.Via {
	case "":
		target.BroughtVia = nil
	default:
		target.BroughtVia = append(slices.DeleteFunc(target.BroughtVia, same), spelling)
		slices.SortFunc(target.BroughtVia, compareFold)
	}

	return target, nil
}

// PayeeSuggestions is the payees of a list that look like one another
// once case, accents, punctuation and a firm's ending are set aside
// (PayeeKey), and nobody has said are not the same: each to be joined into
// the one of its group with the most payments, the oldest when they tie.
// Archived payees are left out; they are off the choices.
func (b *Business) PayeeSuggestions(ctx context.Context, actor types.ID, owner types.Scope) ([]JoinSuggestion, error) {
	list, _, err := b.Payees(ctx, actor, owner)
	if err != nil {
		return nil, err
	}

	notSame, err := b.store.PayeesNotSame(ctx, owner)
	if err != nil {
		return nil, err
	}

	groups := map[string][]Payee{}

	var keys []string

	for _, p := range list {
		if p.Archived() {
			continue
		}

		k := PayeeKey(p.Name)
		if k == "" {
			continue
		}

		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}

		groups[k] = append(groups[k], p)
	}

	var out []JoinSuggestion

	for _, k := range keys {
		g := groups[k]
		if len(g) < 2 {
			continue
		}

		slices.SortStableFunc(g, func(a, b Payee) int {
			if c := cmp.Compare(b.Payments, a.Payments); c != 0 {
				return c
			}

			return a.CreatedAt.Compare(b.CreatedAt)
		})

		for _, p := range g[1:] {
			if !notSame[PairOf(g[0].ID, p.ID)] {
				out = append(out, JoinSuggestion{Into: g[0], From: p})
			}
		}
	}

	return out, nil
}

// PairOf is two payees in the order a "not the same" is kept in, whichever
// way round they were said.
func PairOf(a, b types.ID) [2]types.ID {
	if b.String() < a.String() {
		a, b = b, a
	}

	return [2]types.ID{a, b}
}

// NotTheSame says two payees a suggestion paired are different people, so
// that it is not suggested again.
func (b *Business) NotTheSame(ctx context.Context, now time.Time, actor, one, other types.ID) error {
	p, err := b.keptPayee(ctx, actor, one)
	if err != nil {
		return err
	}

	q, err := b.keptPayee(ctx, actor, other)
	if err != nil {
		return err
	}

	switch {
	case p.ID == q.ID:
		return ErrJoinSelf
	case p.Owner != q.Owner:
		return ErrJoinLists
	}

	ev := eventbus.New(now, actor, p.Owner, PayeeNotSame, map[string]string{"name": p.Name, "other": q.Name})

	return b.store.NotSame(ctx, PairOf(p.ID, q.ID), actor, now, ev)
}

// PayeesFor is the payees an account's transactions may be paid to, asking
// nobody's permission: for a page that has asked whether the reader may
// see the account, as a category list is (categorybus.ForAccount).
// Archived ones are left out.
func (b *Business) PayeesFor(ctx context.Context, a tenancybus.Account) ([]Payee, error) {
	list, err := b.store.Payees(ctx, PayeeOwner(a))

	return slices.DeleteFunc(list, Payee.Archived), err
}

// payeeFor is the payee a transaction of an account written as name is
// paid to: the one with that spelling, or one to be made, named so, with
// the history's line for making it. "" is no payee.
//
// A payee made so carries no key's mark, even through one: the payment it
// was made for carries it (Transaction.PayeeVia), and that is what a
// person checks. Marking it too would mark every payee of every check
// Claude reads, and a mark on everything is a mark on nothing.
func (b *Business) payeeFor(ctx context.Context, now time.Time, actor types.ID, account tenancybus.Account, name string) (Payee, *eventbus.Event, error) {
	if name == "" {
		return Payee{}, nil, nil
	}

	owner := PayeeOwner(account)

	p, err := b.store.PayeeSpelled(ctx, owner, name)
	if err == nil {
		return p, nil, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return Payee{}, nil, err
	}

	p = Payee{ID: types.NewID(), Owner: owner, Name: name, Spellings: []string{name}, CreatedBy: actor, CreatedAt: now}
	ev := eventbus.New(now, actor, owner, PayeeAdded, map[string]string{"name": name, "from": account.Name})

	return p, &ev, nil
}

// cleanPayee is a name with its spaces tidied, or ErrPayeeName.
func cleanPayee(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")

	if name == "" || utf8.RuneCountInString(name) > MaxPayee {
		return "", ErrPayeeName
	}

	return name, nil
}

func compareFold(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }

// firmEndings is the words a firm's name ends in that say what kind of firm
// it is and not which: "Hilltop Plumbing LLC" is Hilltop Plumbing. Kept
// short, since a word wrongly here is two firms suggested as one. As
// PayeeKey leaves them: lower case, without their dots.
var firmEndings = map[string]bool{
	"llc": true, "inc": true, "ltd": true, "ltda": true, "me": true,
	"sa": true, "corp": true, "co": true,
}

// accents is the letters with marks that Spanish, Portuguese and the other
// languages a list may be written in use, and the letter each is without:
// "José Peña" and "JOSE PENA" are the same name typed on two keyboards.
var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n",
)

// PayeeKey is what a payee's name says once case, accents, punctuation and
// a firm's ending are set aside, by which suggestions find two payees that
// may be one: "Hilltop Plumbing, LLC" and "HILLTOP PLUMBING" have one key.
//
// Dots are taken out where they stand, so that "S.A." is "sa" and "Inc."
// is "inc"; any other mark is a space between words. A name that is
// nothing but a firm's ending keeps it.
func PayeeKey(name string) string {
	name = accents.Replace(strings.ToLower(name))

	var b strings.Builder

	for _, r := range name {
		switch {
		case r == '.':
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}

	words := strings.Fields(b.String())

	for len(words) > 1 {
		last := words[len(words)-1]

		switch {
		case firmEndings[last]:
			words = words[:len(words)-1]
		case len(words) > 2 && last == "a" && words[len(words)-2] == "s":
			words = words[:len(words)-2]
		default:
			return strings.Join(words, " ")
		}
	}

	return strings.Join(words, " ")
}
