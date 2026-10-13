# Looking through transactions: an account's, an organization's, a project's

The plan for finding transactions on the web. Until now an account's
transactions could be read only a month at a time, oldest first (the month
list, `docs/plan.md` step 5). Finding every charge at one shop this year, or
the 1,240.00 somebody remembers from the spring, meant opening month after
month. The organization's page showed no transactions at all, and a
project's book listed every part it holds at once.

This adds one form with the transactions it finds beneath it. The same form
appears in three places, and each offers only the choices that make sense
there. Programs already have a way to search: `ledgerbus.Find` behind
`find_transactions` (`docs/books-api.md`, "Reading"). It reads each
account's dates and keeps what fits, and the web's search grows out of it.

## Decisions (from Q&A, 2026-10-13)

| Topic | Decision |
|---|---|
| On an account | **The month list gets the form; it is not a second page.** With nothing chosen, or only a month, it is the month list exactly as before |
| On an organization | **A "Latest transactions" card on its page**: a search box, the ten newest charges across its accounts, and a way to the whole list |
| On a project | **The project's own list**, linked from its book: its parts only, from whichever accounts they are in |
| A row | **A transaction**, as the month list shows them. When a category, a kind or a project is chosen, its amount is the part that matches |
| Kind of money | **The top choice of each group of the category list** ("All pass-through"), not a field of its own |
| Money in or out | **Beside the amount**, as part of how much and which way |

## The choices

| Choice | An account | An organization | A project |
|---|---|---|---|
| Words | yes | yes | yes |
| From and to (days), or a month | yes | yes | yes |
| Amount, exact or a range, and money in, out or either | yes | yes | yes |
| Account | -- | yes | yes, those holding its parts |
| Category, kind, not sorted yet | yes | yes | yes |
| Project, or none | yes | yes | -- |
| To check: sorted by rule, changed by a program | yes | yes | -- |
| Receipts: with or without | yes | yes | yes |

- **Words** are looked for as a sorting rule looks for them, without case
  or runs of spaces (`rulebus.Key`). They are matched against the
  description, the payee a person named, the description of the
  transaction's own, a check's memo and each part's memo. `Find` reads only
  the first three today and gains the other two, so that both kinds of
  search find the same things.
- **A month** (`?month=2026-07`) is a short way of saying its first and
  last day, kept because every month link already in the app says it
  that way. From and to are days, both included, chosen with
  `type="date"`.
- **The amount** is compared without its sign, and its direction is a
  choice of its own beside it, as in `Find`. Both are compared with the
  amount the row shows: the matching part's when a category, a kind or a
  project is chosen, the transaction's otherwise. What is filtered is what
  is seen.
- **The category list** is grouped by kind, as on a transaction's page. Each
  group opens with the whole kind: "All income", "All expenses", "All
  transfers", "All pass-through". After the groups come "Not sorted yet",
  meaning a part with no category, and "Kind not said", meaning a part in a
  category whose owner has not said what kind of money it is. These two are
  the two halves of the book's notice that an amount "is not sorted yet,
  or is in a category whose kind is not said". The month list's
  `?unsorted=1` becomes the first of them.
- **Kind and direction are not the same question**, which is why one sits
  in the category list and the other beside the amount. The direction is
  what the bank saw: which way the money moved. The kind is what the money
  was, and it is a category's. The two cross:
  - a refund is money in that is still an expense;
  - pass-through money comes in and goes out. "Pass-through, money in" is
    what was collected to pass on, and "pass-through, money out" is what
    passed it on.
- **Project** is any project the actor may read in the scope's
  organization, or "No project". It is not offered on a project's own
  list, which is that project.
- **To check** is the month list's two marks to look over: sorted by a
  rule (`?byrule=1`), and changed through a person's program
  (`?through=1`). Both addresses keep working.
- **Receipts** with or without any, counted as the rows already count them.
  Receipts are made from the ledger, so the ledger cannot ask them. main
  hands it a function that counts them, as it hands it what to do after an
  import (`ledgerbus.OnImport`).

**Every choice is one value.** There are no lists of checkboxes and no "any
of these". Each choice is a field of one list of transactions. Apache
refuses an address over 8 KB before the app sees it (`ledgerapp/bulk.go`),
and a single choice per field never comes near that.

**This is not the report builder by another door.** `docs/reports.md` warns
against "group by any of these, filter by any of those". These choices narrow
one list, and nothing here groups. The totals are only those of the rows
listed. The list answers *which* charges; how much *by what* is still a
report's question, and a report's figure still links to its own parts.

## What is listed

- **One month** shows the month list as it is today, oldest first as the
  statement reads, because that is how a month is checked against the
  paper. It keeps the month's money in and out, its holders, what is not
  sorted yet and the way to sort it on one page. One month with other
  choices besides keeps that order.
- **Anything wider**, or no month on an organization or a project, is
  newest first, a hundred to a page, with a link to the next hundred
  (`skip=`). Above it is one line per currency: how many fit, money in, and
  money out. Those totals are over every row that fits, not only the page.
- **Months stay on an account's page** under the form, each with its count,
  as a quick way to choose one. They carry the other choices, so "not sorted
  yet" can be read across every month or within one.
- **A pending charge that has since posted** is listed with its mark, as
  on the month list, and left out of the totals. It is the posted charge
  that counts.
- **An organization's accounts for reference** are left out unless the
  account choice names one. Their parts count nowhere (`docs/plan.md`,
  "Whose books"), and totals that counted them would be wrong. A charge in
  one is still a single choice away, and the page says so when the
  organization has any.
- **A row** shows what the month list's row shows, plus its account on an
  organization's or a project's list. When the shown amount is one part of
  a split charge, the whole is beneath it ("of $412.00").
- **A row's link** to the transaction carries `?back=` with the whole
  address. A person who opens a charge from the results and saves it comes
  back to the same results (`app/sdk/page/back.go`). On a project's list, a
  charge in an account the reader may not open is shown without a link,
  as the book does.

## Who

Reading is reading the scope, as everywhere. A role on an account reads its
list. A role on an organization reads its list and each of its accounts'
lists. A role on a project reads the project's list: its parts, never the
rest of a charge they are part of (`docs/plan.md`, "Domain model": roles). Anyone without a
role gets `ErrNotFound`, and the new routes go in the stranger walk
(`app/sdk/muxer/tenancy_test.go`).

## The pages

- **`/accounts/{id}/transactions`:** the month list, with the form above
  its months.
- **`/orgs/{id}/transactions`:** an organization's list. Linked from the
  "Latest transactions" card on `/orgs/{id}`. The card shows a search box
  that sends its words here, the ten newest charges across the
  organization's accounts (not those for reference), and "See more".
- **`/projects/{id}/transactions`:** a project's list. Linked from its book
  and its page. The book's "Every part" stays as it is.

All three are a GET form and a page that answers it, with no script
(`docs/design.md`). At 360 px the fields stack and the table scrolls inside
itself, never the page. The choices are in the address, so a search can be
bookmarked, sent to the accountant, or come back to.

## Storage

No table and no column. A new `ledgerdb` read takes the accounts and the
days, as `Transactions` does, and adds `EXISTS (SELECT 1 FROM splits …)`
for a chosen category, kind (joined to `categories`) or project, which the
indexes on `splits` answer. It never uses the
`IN (SELECT … FROM transactions)` that took 37 seconds over 2,000 rows
(`ledgerdb/rules.go`). The words, the amount, what is to check and the
receipts are kept in Go, as in `Find` today, so that words are folded
exactly as the sorting rules fold them.

The organization's card reads with `ORDER BY posted_on DESC LIMIT 10` on the
`(account_id, posted_on)` index, not a whole read, because it runs every
time the organization's page is opened.

## The API

`find_transactions` keeps its contract and becomes a call to the same read,
with every account the key may read. Its own tests pin it. Whether it
gains the category, kind and project choices is a separate change to
`docs/books-api.md`, made when a person's Claude needs them.

## Build order

Three pull requests after this one:

1. **The read.** `ledgerbus.Explore` over a scope (account, organization,
   project) and the choices above, its totals and paging, the store's read,
   the receipts function from main, and `Find` on top of it. Nothing on a
   page changes yet.
2. **An account.** The form on the month list, the months carrying the
   other choices, `?back=` on its rows.
3. **An organization and a project.** Their lists, the organization page's
   card, the book's link, the ways back, and the stranger walk.

## Verification

- `ledgerbus`, for each choice alone and together:
  - a charge split between an expense and a pass-through part is found
    under either kind, showing that part;
  - a project's list never shows the rest of a charge;
  - accounts for reference are left out until named;
  - totals cover every row and not only the page, and leave out a cleared
    pending charge;
  - one month keeps the statement's order, and anything wider is newest
    first;
  - a stranger gets `ErrNotFound` for every scope;
  - `Find`'s tests pass unchanged.
- Timing: a test over 50,000 invented transactions in ten accounts answers
  an organization's widest search in well under the proxy's fifteen
  seconds.
- `ledgerapp`:
  - `?month=` alone renders the month list as before, and `?unsorted=1`
    and `?byrule=1` still work;
  - the form gives back what was chosen;
  - rows carry `?back=`.
- Through the site:
  - search an organization for a word that appears in two accounts;
  - choose "All pass-through" with money in;
  - open a result, save it, and land back on the same results;
  - check it at 360 px.
