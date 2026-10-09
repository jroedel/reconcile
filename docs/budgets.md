# Budgets: a project's, and an organization's year

The plan for step 9's "Budgets per project and category" (`docs/plan.md`,
build order). A budget is what the owners expect the money to do, written
down before it happens, so that between the accountant's reports anyone
with a role can see whether the pilgrimage is on its way to breaking even
and whether the parish's utilities are running ahead of the year.

It builds on the kinds of money (`docs/plan.md`, "Kinds of money"): only
income and expenses are budgeted, because only they are operations.
Transfers and pass-through should come back to zero, and already have
their own page for that (the categories page, "What should come back to
zero").

## Decisions (from Q&A, 2026-10-09)

| Topic | Decision |
|---|---|
| What has a budget | **A project, and an organization's year.** A project's is for its whole life; an organization's is per budget year |
| What it covers | **Income and expenses.** A pilgrimage expects pilgrims' fees as well as costs; "will we break even" needs both |
| Who sets it | **Owners** (Manage on the project or the organization), recorded in the history. Anyone who may read the scope sees it |
| An organization's year | **Starts in a month the owner chooses**, January unless they say otherwise (many parishes run July to June) |
| Partway through a year | **The whole year's figure, with a pace mark** showing where an even pace would be by today. Never pro-rated: insurance and a summer camp are not spent evenly, and a pro-rated figure would call them overspent in the month they are paid |

## What a budget is

- **Lines, one per category**, each an amount the owners expect over the
  project's life or the budget year. A category's kind says whether the
  line is income or an expense; only income and expense categories may
  have one. Amounts are typed as positive numbers; an expense line of
  $6,000 is $6,000 to be spent.
- **A total without categories** is allowed for each kind ("expenses:
  $15,000"), for an owner who knows the whole and not the parts. When
  there are category lines of that kind as well, the total is the larger
  of the typed total and their sum, and the page says when the lines do
  not add up to it.
- **One currency per budget**, chosen when its first line is set: the
  currency most of the organization's accounts use (for a personal
  project, the owner's personal accounts'). Money in another currency is
  listed beside the budget, never converted.
- **Nothing is ever budgeted for transfers or pass-through**, and parts
  in a category whose kind is not said yet, or in no category, are shown
  as "not sorted yet" beside the comparison rather than counted.

## What is compared with it

- **A project:** every part in the project, from any account, all time --
  the same parts as its book.
- **An organization's year:** every part of the organization's accounts
  posted in the budget year (from the first day of the starting month,
  twelve months), whether or not it is in a project. A personal account is
  in no organization and in no organization's budget.
- Actuals are signed sums within a kind, as everywhere: a refund lowers
  its expense category. A line shows the budget, the actual, and what is
  left (or how far over), in words first and colour second.
- **The pace mark** (organization's year only): the share of the year
  gone by today, as a mark on each line's bar and a sentence ("58% of the
  year has gone by"). A past year is 100%; a future one shows no actuals.
  A project has no pace mark: its dates are often unknown and its money
  arrives in lumps (deposits up front, the bus at the end).
- **Categories with money and no line** are listed under "Not in the
  budget", so that nothing spent disappears from the page.

## Storage

A new domain, `budget` (`business/domain/budget/budgetbus`,
`stores/budgetdb`).

- **`budget_lines(id, project_id NULL, org_id NULL, year INTEGER NULL,
  category_id NULL, kind, amount, currency, updated_by, updated_at)`**,
  `STRICT`.
  - Exactly one of `project_id` and `org_id` (a `CHECK`); `year` is set
    for an organization's line and only then: the calendar year the
    budget year starts in, so "2026–27" is 2026.
  - `kind` is `income` or `expense` (a `CHECK`: it is the two kinds that
    are operations, not a list that grows). For a category line it is the
    category's kind when the line was set; if a category's kind changes
    later, the page says the line is under the other kind now.
  - `category_id` NULL is the total for that kind.
  - Unique on `(project_id, org_id, year, category_id, kind)`, with the
    NULLs coalesced in the index expression, so setting a line twice is a
    correction.
  - `amount` is cents, positive; a line set to zero is removed.
- **`orgs.fiscal_start INTEGER NOT NULL DEFAULT 1`** (`AddColumn` in
  `tenancydb`, with the old-schema test): the month the budget year
  starts. Set by an owner on the organization's edit page; changing it
  changes which months every budget year covers, and the history says so.
- Setting, changing and removing a line go in the scope's history
  (`budget.set`, `budget.removed`), with the before and after amounts.

## The pages

- **A project's book** (`/projects/{id}/book`) gains a "Budget" section
  above the totals when there is a budget: income lines, expense lines,
  each with budget, actual and left; the expected net (budgeted income
  less budgeted expenses) beside the actual net; "Not in the budget".
  Owners get "Set the budget" (`/projects/{id}/budget`), a form with one
  amount per income and expense category of the list the project's money
  is sorted with, and the two totals.
- **`/orgs/{id}/budget?year=2026`:** the organization's year, the same
  layout with the pace mark; links to the year before and after; "Copy
  last year's budget" for owners when this year has none. Linked from the
  organization's page. The form for owners is the same as a project's.
- **The organization's edit page** gains "Budget year starts in" (a month).
- All of it works without JavaScript, and the bars are decoration: every
  figure is also written out.

## Who

Reading a budget is reading its scope: a project's viewer sees its
budget, an organization's viewer its year. Setting one is Manage on the
scope; a bookkeeper gets `ErrForbidden`, and anyone without a role
`ErrNotFound`, as everywhere. The new routes go in the stranger walk.

## Build order

Two pull requests:

1. **Projects.** `budgetbus` and `budgetdb`, a project's budget page and
   form, the "Budget" section of the book, history lines, the stranger
   walk.
2. **An organization's year.** `orgs.fiscal_start`, the year's page with
   the pace mark, copying last year, the edit page's month.

## Verification

- `budgetbus`: only income and expense categories; a total and lines that
  do not add up; a refund lowering its line; transfers, pass-through and
  unsorted outside the comparison; the fiscal year's months for January
  and July starts, across a year's end; the pace for a past, current and
  future year; owners only.
- `budgetdb`: the unique line; the old-schema test for `orgs.fiscal_start`.
- Through the site: set a project's budget and see it beside its book;
  set an organization's July-to-June year and see July to June; copy last
  year; a viewer reads and cannot set; a stranger finds nothing.
