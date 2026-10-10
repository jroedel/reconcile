package ledgerbus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/csvsource"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// Many files at once (docs/shapes.md, 4). A person sends a month's
// statements together -- a consolidated statement for three accounts, a
// file from each holder, a CSV from the savings bank -- and each file,
// or each account's part of one, is proposed an account:
//
//   - by its number: the last four digits the file prints for it, matching
//     the one account the person keeps the books of that ends the same;
//   - else remembered: the one such account a file in the same layout was
//     imported into before -- for a CSV, the account whose columns were
//     chosen for its header, which every CSV ever imported has said; for
//     anything else, the account of a statement in its layout
//     (Statement.Shape);
//   - else asked.
//
// Two accounts that both fit is no proposal at all. A wrong guess put in
// front of a person who is busy is worse than a question.
//
// Each proposal is read as its own preview reads it, for its account, and
// is imported with the others only when nothing in it needs a person: it
// balances by its own figures or the file's, the count rule set nothing
// aside, no row was skipped, and no row is waiting to be told whose it is.
// The rest stay on the list, each with its own preview for whatever it
// needs, which is where a person answers it. Nothing a bulk import does
// is anything a single import would not do: it is the same Import, with
// the options a preview starts from.

// MaxBatch is the most files one bulk import takes: a few months of a
// house's statements, and few enough that reading them all fits in the
// time a page may take.
const MaxBatch = 40

// Proposed is how a proposal's account was found.
type Proposed string

// The ways.
const (
	ByNumber Proposed = "number"
	ByMemory Proposed = "remembered"
	ByPerson Proposed = "chosen"
)

// Proposal is one file of a bulk import, or one account's part of a file
// that holds several, and the account proposed for it.
type Proposal struct {
	File filebus.File

	// Part is the last four digits of the part's account, for a file that
	// holds several, and Last4 those the file prints for it, if it prints
	// any.
	Part  string
	Last4 string

	// Account is the account proposed or chosen, and By how; zero when the
	// person is asked.
	Account types.ID
	By      Proposed

	// Draft is the file read for Account, as its preview reads it, when
	// there is an Account and Problem is nil.
	Draft Draft

	// Problem is why the file could not be read at all, or for Account.
	Problem error
}

// Key names a proposal among a bulk import's: its file and its part.
func (p Proposal) Key() string {
	if p.Part == "" {
		return p.File.ID.String()
	}

	return p.File.ID.String() + "-" + p.Part
}

// Unattended reports whether the proposal can be imported with the
// others, nobody having looked at its preview.
func (p Proposal) Unattended() bool {
	d := p.Draft

	return p.Problem == nil && !p.Account.Zero() && !d.Choose && d.Ready() && d.Check.OK &&
		d.Statement.SetAside() == 0 && d.Result.Skipped == 0 && !d.Result.Unplaced && d.Unnamed == 0
}

// Imported reports whether the proposal's file is in its account already.
func (p Proposal) Imported() bool {
	return p.Problem == nil && !p.Account.Zero() && p.Draft.HasEarlier
}

// Bookkept is the accounts the actor keeps the books of, and so may import
// into, by name; archived ones are left out.
func (b *Business) Bookkept(ctx context.Context, actor types.ID) ([]tenancybus.Account, error) {
	ov, err := b.accounts.Overview(ctx, actor)
	if err != nil {
		return nil, err
	}

	all := ov.Accounts
	for _, o := range ov.Orgs {
		all = append(all, o.Accounts...)
	}

	var out []tenancybus.Account

	for _, a := range all {
		_, access, err := b.accounts.Account(ctx, actor, a.ID)

		switch {
		case errors.Is(err, tenancybus.ErrNotFound):
			continue
		case err != nil:
			return nil, err
		}

		if access.Can(tenancybus.Bookkeep) {
			out = append(out, a)
		}
	}

	slices.SortFunc(out, func(a, b tenancybus.Account) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	return out, nil
}

// Propose reads the files the actor uploaded for a bulk import and
// proposes each an account, or each part of one that holds several.
// chosen is the accounts a person chose, by Key, which stand over any
// proposal; one the person does not keep the books of is not chosen. A
// file that is not the actor's is left out, as if it were not there.
func (b *Business) Propose(ctx context.Context, actor types.ID, fileIDs []types.ID, chosen map[string]types.ID) ([]Proposal, error) {
	accounts, err := b.Bookkept(ctx, actor)
	if err != nil {
		return nil, err
	}

	var out []Proposal

	for _, id := range fileIDs[:min(len(fileIDs), MaxBatch)] {
		f, err := b.files.ByID(ctx, id)

		switch {
		case errors.Is(err, filebus.ErrNotFound), err == nil && f.UploadedBy != actor:
			continue
		case err != nil:
			return nil, err
		}

		props, err := b.propose(ctx, actor, f, accounts, chosen)
		if err != nil {
			return nil, err
		}

		out = append(out, props...)
	}

	return out, nil
}

// propose reads one file without an account, to learn its parts and the
// numbers it prints, and proposes each part its account.
func (b *Business) propose(ctx context.Context, actor types.ID, f filebus.File, accounts []tenancybus.Account, chosen map[string]types.ID) ([]Proposal, error) {
	data, err := b.files.ReadAll(f)
	if err != nil {
		return nil, err
	}

	d := Draft{File: f}

	if err := b.read(ctx, &d, data, nil); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return []Proposal{{File: f, Problem: err}}, nil
	}

	var props []Proposal

	if d.Choose {
		for _, p := range d.Parts {
			// A part with no number cannot be told apart from the others,
			// nor chosen on a preview; it is not one.
			if p.Last4 != "" {
				props = append(props, Proposal{File: f, Part: p.Last4, Last4: p.Last4})
			}
		}
	} else {
		props = []Proposal{{File: f, Last4: d.Result.Last4}}
	}

	remembered, err := b.rememberedFor(ctx, d, data, accounts)
	if err != nil {
		return nil, err
	}

	for i := range props {
		p := &props[i]

		byNumber, numbered := only(accounts, func(a tenancybus.Account) bool { return p.Last4 != "" && a.Last4 == p.Last4 })

		switch {
		case slices.ContainsFunc(accounts, func(a tenancybus.Account) bool { return a.ID == chosen[p.Key()] }):
			p.Account, p.By = chosen[p.Key()], ByPerson
		case numbered:
			p.Account, p.By = byNumber.ID, ByNumber
		default:
			// Not an account whose own number says it is not this one.
			fits := slices.DeleteFunc(slices.Clone(remembered), func(id types.ID) bool {
				i := slices.IndexFunc(accounts, func(a tenancybus.Account) bool { return a.ID == id })

				return i < 0 || p.Last4 != "" && accounts[i].Last4 != "" && accounts[i].Last4 != p.Last4
			})

			if len(fits) == 1 {
				p.Account, p.By = fits[0], ByMemory
			}
		}

		if p.Account.Zero() {
			continue
		}

		if p.Draft, p.Problem = b.prepareFor(ctx, actor, p.Account, f.ID, p.Part); p.Problem != nil && !Expected(p.Problem) {
			return nil, p.Problem
		}
	}

	return props, nil
}

// only is the one account that fits, if exactly one does.
func only(accounts []tenancybus.Account, fits func(tenancybus.Account) bool) (tenancybus.Account, bool) {
	var found []tenancybus.Account

	for _, a := range accounts {
		if fits(a) {
			found = append(found, a)
		}
	}

	if len(found) != 1 {
		return tenancybus.Account{}, false
	}

	return found[0], true
}

// rememberedFor is the accounts a file in the same layout went to before. A
// file whose parts each print a number is proposed by those, and a
// remembered account is for a file of one.
func (b *Business) rememberedFor(ctx context.Context, d Draft, data []byte, accounts []tenancybus.Account) ([]types.ID, error) {
	if d.Choose {
		return nil, nil
	}

	if d.Format != CSV {
		ids := make([]types.ID, 0, len(accounts))
		for _, a := range accounts {
			ids = append(ids, a.ID)
		}

		return b.store.Shaped(ctx, d.Shape.Signature, ids)
	}

	// A CSV's header is remembered with the columns a person chose for it,
	// per account, since long before layouts were (mapping).
	var out []types.ID

	for _, a := range accounts {
		saved, err := b.store.Mappings(ctx, a.ID)
		if err != nil {
			return nil, err
		}

		for fingerprint, m := range saved {
			if in, err := csvsource.Inspect(data, m.SkipLines); err == nil && in.Fingerprint == fingerprint {
				out = append(out, a.ID)

				break
			}
		}
	}

	return out, nil
}

// prepareFor reads a file for an account as its preview first does, and a
// part of it other than the one the account's number picks, if one is
// asked for.
func (b *Business) prepareFor(ctx context.Context, actor, accountID, fileID types.ID, part string) (Draft, error) {
	d, err := b.Prepare(ctx, actor, accountID, fileID, nil)
	if err != nil || part == "" || d.Part == part {
		return d, err
	}

	opts := d.defaults()
	opts.Part = part

	return b.Prepare(ctx, actor, accountID, fileID, &opts)
}

// defaults is the options that read a file as Prepare reads it with none:
// the columns it chose, a card's PDF turned round, and the part it chose.
// Opening and closing balances are left to the file, as with none.
func (d Draft) defaults() Options {
	return Options{Mapping: d.Mapping, Invert: d.Format == PDF && d.Account.Kind == tenancybus.Card, Part: d.Part}
}

// Expected reports an error that is a file's or a choice's, said to the
// person on the page, rather than the site's.
func Expected(err error) bool {
	for _, e := range []error{ErrPDFUnavailable, ErrPDFScan, ErrPDFPassword, ErrPDFUnreadable, ErrPDFNoRows,
		ErrUnreadable, ErrEmpty, ErrUnbalanced, ErrSameFile, ErrUnproven, ErrLocked, ErrWhichAccount, ErrNotFound, ErrForbidden} {
		if errors.Is(err, e) {
			return true
		}
	}

	return false
}

// ImportProposals imports every proposal that can be imported unattended,
// each as its own preview would, and says how many were. One that can no
// longer be -- imported in another tab meanwhile, say -- is left for its
// preview to explain.
func (b *Business) ImportProposals(ctx context.Context, now time.Time, actor types.ID, props []Proposal) (int, error) {
	n := 0

	for _, p := range props {
		if !p.Unattended() {
			continue
		}

		_, err := b.Import(ctx, now, actor, p.Account, p.File.ID, p.Draft.defaults())

		switch {
		case err == nil:
			n++
		case !Expected(err):
			return n, err
		}
	}

	return n, nil
}
