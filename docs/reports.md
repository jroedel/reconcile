# Reports: an engine, a few styles, and saved reports

The plan for reports (`docs/plan.md`, build order, step 9). A report is
what the person who answers for the money brings to the people it belongs
to: a house's month, read aloud at a house meeting or opened on a phone,
saying what came in, what went out, and how much of it is not sorted yet.
It is where the rest of the app has been heading. The kinds of money take
transfers and pass-through out of income and expenses; the sorting rules,
payees and receipts make the parts right; a report is where that shows.

The first case, written as the general one: a house whose money moves
through one checking account and the card it pays from, and through two
cards closed two years ago that have no new transactions and whose history
still belongs in every year they were used. The house wants its true
income and expenses by month, with the card's charges in the months they
were made, the card's payments counted nowhere, and personal charges that
were repaid counted nowhere either. Until the sorting is done, the money
not sorted yet is a line of its own, and watching it shrink month by month
is how the house sees the books coming right.

## Decisions (from Q&A, 2026-10-12)

| Topic | Decision |
|---|---|
| A builder on the web | **Not now, and not ruled out.** A person does not design a report; they choose one and adjust it. The code is built so that a builder could come later without rewriting what is under it |
| Three levels | **An engine, styles, saved reports.** The engine adds up parts; a style is one layout, written in code and reviewed by pull request; a saved report is a style with its choices made, kept by a person in the database |
| What a style fixes | **Its shape**: what the rows are, what the columns are, what sits below the line. Months against weeks, or categories against payees, are two styles |
| What a report chooses | **Within the shape**: which accounts, how much detail (each category or its group), which comparisons beside it. Never its dates: a report is run for a period chosen each time, starting from a default it keeps |
| Who reads it | **Both on the site and on paper.** Anyone with a role on the scope opens it; the same page prints cleanly, and the browser's "Save as PDF" makes the file |
| Categories into fewer lines | **Category groups**, in this plan: a list's categories may be gathered into groups ("Food", "House", "Vehicles"), and a report shows either |
| Pass-through on a house's report | **One total, by choice.** A report may show pass-through as one line ("Personal charges waiting to be repaid: $85.00") or each category's balance |
| Its title and words (2026-10-12) | **A saved report's title is the person's own** ("P&L"), with a description beneath it that every run shows, and a note for each period that only that period's run shows. A style's own name stays in code, translated, and is the title until a report is saved |

## Three levels

### The engine

`business/domain/report/reportbus`. It answers one question: **the parts
in a scope, in a period, summed by some dimensions, in each currency.**

- **The dimensions** are kind, category, category group, project, payee,
  account and month. A question names which of them it groups by and in
  which order; the answer is a sum per combination that has money, per
  currency, with the count of parts in it.
- **The parts are the ledger's.** The engine reads them through
  `ledgerbus` (`OrgLines` already reads every part of an organization's
  accounts in a period, archived accounts included) and adds no counting
  rule of its own. An account for reference (`docs/plan.md`, "Whose
  books") is not one of the books', so it is never among a report's
  accounts and is not offered as a choice. A pending charge that has posted, a transfer explained
  by what it paid for, an entry by hand: each counts in a report exactly
  as it counts in the month list, a project's book and a budget, because
  it is the same parts. A report that disagreed with the book would be
  worse than no report.
- **Sums by kind, signed**, as everywhere (`ledgerbus.Operations`): a
  refund lowers its expense category. A part with no category, or in one
  whose kind is not said yet, is **not sorted yet**, counted under that
  and nowhere else.
- **Never converted.** Each currency is its own answer, and a style lays
  them out as sections of their own.
- **Access is asked of tenancybus**, as everywhere: the engine takes the
  actor and a scope, and answers `ErrNotFound` for a scope they hold no
  role on.
- **It knows nothing of pages.** It returns numbers. Turning a style's
  numbers into a page, a printed sheet or a spreadsheet is the app's
  (`app/domain/reportapp`), the same for every style.

The project book (`ledgerbus.ProjectBook`) and the budget comparison
(`budgetbus`) add up the same parts by hand today. Once the engine
exists they may become questions to it, so that every figure in the app
comes from one sum; that is a later change, not part of this plan.

### Styles

A style is **Go in this repository**: a value in `reportbus` that says
which scopes it takes, which choices it offers and their defaults, the
question it asks the engine, and the result it shapes from the answer;
and a template in `reportapp` that lays the result out. Adding one is a
pull request, reviewed like any other, and that is the point: a style
decides what a household or an accountant will believe about the money,
and it should be read by somebody before it does.

- **A style's choices are typed**, a Go struct of its own with a default
  for each field. They are not a general list of options, and nothing a
  person types is ever a query: the moment a choice becomes "group by
  any of these, filter by any of those", the builder has arrived by the
  back door, and it should arrive by the front one instead.
- **A style's words go through `t`**: its name, its headings, the
  sentence that says what it adds up. A saved report's name is the
  person's own and is not translated.
- **A style has a stable key** (`income-expenses-by-month`), never shown,
  that saved reports point to. A style's name on the page may change; its
  key does not.
- **A style can grow.** A new choice gets a default, and every report
  saved before it reads as the default. A choice is removed only with
  the reports that hold it rewritten in the same pull request, by `Init`
  if it must be.
- **A style can go.** A saved report whose style is no longer in the
  code says so on its page ("This report's layout is no longer offered")
  and offers to remove it, rather than failing.

**Fewer styles is better.** Before adding one, ask whether an existing
style's choice would do; before adding a choice, ask whether it changes
the shape (then it is a style) or only the detail (then it is a choice).

### Saved reports

A saved report is **a style's key, its choices, a title, a description,
and the scope it belongs to**, kept by a person so that the same report comes out the same
way every month.

- **One scope**: an organization, an account on its own, or a project.
  It can only include money in that scope, and choosing accounts means
  choosing among that scope's. A report never grants anything: anyone
  who opens it sees it only if they may read its scope, and sees then
  what any page of that scope would show them.
- **Its period is chosen each time it is run.** It keeps a default
  ("last month, with the year to date"; "the budget year"; "the last
  twelve months"; "all of it") and the page offers the others beside it.
  The period is in the address (`?from=2026-07&to=2026-09`), so a link
  to one month's report is a link to that month.
- **A style can be run without saving it.** A scope's page links to each
  style that takes it, with the defaults. A saved report is for the one
  that is run every month, not a step before every report.
- **An account's mark can change under it.** Putting an account for
  reference, or taking it off, changes every period's figures, past ones
  too. A period's note written before the change says, beneath it, that
  the figures have changed since; and an account the report was saved
  with that is for reference now is named as left out.
- **What it names is kept by ID and checked when it is read.** An archived
  account or category is still counted, because history needs it, and
  is shown as archived. One that no longer exists is named on the report
  as missing, never dropped in silence.
- Saving, changing and removing one go in its scope's history
  (`report.saved`, `report.changed`, `report.removed`), with what changed.

### A title, a description, and a note for each period

Three places for a person's own words, and no more, so that a house can
call its report what it calls it and the treasurer can say what the
figures do not.

- **The title.** Unsaved, a style's run is headed by the style's name
  ("Income and expenses by month"), in the reader's language. A saved
  report is headed by the title its keeper gave it: "P&L", "Profit and
  loss", "Informe mensual de la casa". It is the person's words and is
  not translated; the style's name stays beneath it, small, so that a
  reader who does not know what "P&L" means here can find out. The
  title does not change from run to run: a house compares this month's
  sheet with last month's, and the same heading on both is part of that.
- **The description**, beneath the title on every run: what the report
  is and how to read it, written once ("Our true income and expenses.
  Card charges count in the month they were made; personal charges that
  were repaid are left out."). Plain text, a few paragraphs at most.
- **A note for each period**, beneath the description on that period's
  run only: what the treasurer wants the house to know about these
  months ("The roof repair is in August; the insurance claim should
  cover most of it in October."). A note belongs to the report and the
  exact period it was written for, so August's note is on August's run
  and not on the year to date's. Plain text, line breaks kept, no markup
  and no links: it is printed as it was typed.
- **A note does not freeze the figures.** Nothing a report adds up is
  stored, so sorting a charge after the note was written changes the
  figures under it. The note says who wrote it and when, and keeps a
  fingerprint of the figures as they stood then; when a run's figures
  no longer match it -- a part sorted or split, a statement imported or
  removed, a category's kind, an account put for reference -- the run
  says the figures have changed since the note. (Planned as a reading of
  the history, and changed while building it, 2026-10-12: the history
  records a part put in a project but not a part sorted into a
  category, and recording every sort would drown each account's history
  in lines nobody reads. The figures are what the note was written
  beside, and they say whether they moved.) Keeping a printed copy as it
  stood is the paper's job, or a later plan's.
- **Who:** the title, the description and the notes are written by whoever
  may save the report (Bookkeep on its scope); everyone who may run it
  reads them. Writing, changing and removing a note go in the history
  (`report.noted`), with the text before and after.
- An unsaved style has neither a description nor notes. "Save this
  report" is where both begin.

## The first two styles

### Income and expenses by month

For an organization or an account on its own.

```
                         Jul       Aug       Sep      Year
Income
  Donations              ...
  Stipends            1,200.00    900.00  1,150.00  3,250.00
Expenses
  Food                 -640.00   -712.00   -598.00 -1,950.00
  Vehicles               ...
────────────────────────────────────────────────────────────
Net                      ...
Not sorted yet       -3,400.00 -2,900.00 -4,100.00
  sorted                  61%       70%       48%
Should come back to zero
  Transfers between our accounts       0.00
  Personal charges waiting to be repaid   85.00
Reconciled                 yes       yes       not yet
```

- **Rows** are income categories, then expense categories, each under its
  kind's heading with the kind's total; then the net (income and expenses
  together). **Columns** are the months of the period, then their total.
  A month is a calendar month by the date a transaction posted: a card's
  charge counts in the month it was made, and its payment from checking
  is a transfer and counts in neither.
- **Not sorted yet** is always shown, with how much of each month's money
  out and in is sorted, by amount. It is the line that tells a house how
  far the books are from finished, and the one a treasurer wants to see
  move. Each month's figure links to that month's unsorted parts.
- **What should come back to zero**, below the line, as the categories
  page already reckons it (`ledgerbus.Settling`): each pass-through
  category's balance at the end of the period, which is what is still
  owed or not yet forwarded then; and the transfers, with each month of
  the period whose transfers do not net to zero. A transfer that does not
  is a leg not imported, which is worth seeing on a report that reaches
  back to accounts closed years ago.
- **Reconciled**, per month: yes when every account in the report that
  has parts in the month, or a statement covering it, is reconciled for
  every day of it. A month that is not says "not yet", in words, and its
  figures are provisional; nothing about it is hidden.
- **Every figure links to the parts that make it up** ("every number says
  where it came from", `docs/design.md`).

Its choices:

| Choice | Values | Default |
|---|---|---|
| Accounts | every account of the scope, or some of them | every one, archived included |
| Detail | each category, or each group (a category in no group is its own line) | each category |
| Pass-through | one total, or each category | one total |
| Compare with | nothing, the same months a year before, the budget year's lines | nothing |
| Period | last month with the year to date, the budget year, the last twelve months, all of it | last month with the year to date |

"Compare with the budget" is offered only for an organization, whose year
has a budget (`docs/budgets.md`); the budget's lines are for the year, so
it is shown beside the year's total and never spread over the months.

### A project

For a project. The project's book, as a report: income and expenses by
category over the project's life or a period of it, its net, its budget
beside it when it has one, what is not sorted yet, and which accounts the
money came from. No months across the top; a pilgrimage's money arrives
in lumps, and its question is "are we coming out even", not "how was
August".

| Choice | Values | Default |
|---|---|---|
| Detail | each category, or each group | each category |
| Show accounts | yes or no | yes |
| Compare with | nothing, the budget | the budget, when it has one |
| Period | the project's life, a period of it | the project's life |

Building the second style is what proves the engine general: if it needs
a question the engine cannot ask, the engine changes, not the style.

## Category groups

A list's categories may be gathered into groups, so that a house's forty
lines can be read as ten.

- **A group belongs to a list** (an organization's, or an account on its
  own's) **and to one kind**: "Food" is a group of expense categories. A
  group of mixed kinds could not sit under one kind's heading, and the
  report's rows are under the kinds.
- **A category is in one group or none.** Changing a category's kind
  takes it out of its group, and the history says so; it is put in a
  group of its new kind by a person.
- Groups are made, renamed, archived and filled on the categories page,
  by whoever may change the list's categories. The split editor's choice
  is still the categories, grouped by kind as now; groups are for
  reading, not sorting.
- `category_groups(id, owner_kind, owner_id, kind, name, created_by,
  created_at, archived_at)`, `STRICT`, unique on the owner and name; and
  `categories.group_id NULL` by `AddColumn` in `categorydb`, with the
  old-schema test. Making, renaming and archiving a group, and moving a
  category in or out, go in the history.

## Who

- **Running a report** is reading its scope: a viewer of the organization
  runs its reports and any style it takes.
- **Saving, changing and removing a report** is Bookkeep on its scope,
  the people who keep the books for the people who read them. A viewer
  gets `ErrForbidden`, and anyone without a role `ErrNotFound`.
- **Pass-through as one total is a way of showing, not a secret.** A
  viewer who may read the organization may read its transactions too; a
  report that shows one total does not stop them. What it does is keep a
  house meeting's sheet about the house's money, not about one person's
  repaid lunch.
- The new routes go in the stranger walk (`app/sdk/muxer/tenancy_test.go`).

## Storage

`business/domain/report/stores/reportdb`:

- **`reports(id, scope_kind, scope_id, style, title, description, choices,
  period, created_by, created_at, updated_by, updated_at, removed_at)`**,
  `STRICT`. `style`
  is the style's key. `period` is the preset a run shows when its address
  names no months ("last-month", "year-to-date", …), '' for the style's
  own default. A removed report keeps its row, and its notes, for the
  history. `choices` is TEXT holding JSON that the style
  decodes into its own struct: unknown fields are ignored, missing ones
  take their default. It is stored as JSON because each style's choices
  differ and a column per choice would mean a migration per choice; it
  is read only by its style, never queried, and never a language.
- **`report_notes(report_id, period_from, period_to, note, figures,
  updated_by, updated_at)`**, `STRICT`, with the report and the period as
  its key, so that writing a note twice is a correction. The period is
  the run's first and last month, as the address has them, both '' for
  every month there is. `figures` is the fingerprint of the figures the
  note was written beside. A note set to nothing is removed.
- Nothing the engine computes is stored. A report is read from the parts
  every time, so that sorting a charge changes the next report at once.

## The pages

- **`/orgs/{id}/reports`**, and the same for a project and an account on
  its own: the saved reports, and each style that takes the scope, with a
  line saying what it shows. Linked from the scope's page.
- **`/reports/{id}?from=…&to=…`**: a saved report run for a period, its
  title, description and that period's note at the head, with the period
  chooser and links to the period before and after. For those who may, a
  link to change its title, description and choices, and a form beneath
  the note to write or change it for this period.
- **`/orgs/{id}/reports/{style}?from=…&to=…&…`**, and the same for a
  project and an account on its own: a style run without saving it, its choices in the address, with "Save this report" for those who
  may.
- **Printing** is the same page, with a print stylesheet: no navigation,
  no forms, no links underlined, the title, the period, the date it was
  printed, the description and the note at the head, and the reconciled line kept. A month's report for a house fits
  on one sheet in landscape when it has groups as its detail.
- **`/reports/{id}/download?from=…&to=…`**: the same figures as a CSV for
  a spreadsheet, its header row through `page.Renderer.Text`.
- All of it works without JavaScript, and at 360 px the months scroll
  inside the table, never the page.

## Not in this plan

- **A report builder on the web.** The engine is built for it: its
  dimensions are what a builder would offer. Whether a person ever
  should is a later decision.
- **Weeks, quarters, or any other columns.** Each is a style, when
  somebody needs it.
- **A PDF made on the server.** The release is one static binary, and a
  PDF library is a dependency for what every browser's "Save as PDF"
  already does. It comes back if a report is ever sent by mail on its
  own.
- **Reports by mail, or on a schedule.**
- **Reports through the API and MCP.** Claude on claude.ai can already
  read the books (`docs/books-api.md`); a `run_report` tool is a small
  addition once the styles have settled.
- **Converting currencies.**

## Build order

Four pull requests:

1. **The engine and the first style, unsaved.** `reportbus` with its
   dimensions; "Income and expenses by month" run from an organization's
   and an account on its own's page with its choices in the address; not
   sorted yet, what should come back to zero, the reconciled line; the
   period chooser; printing; the CSV; the stranger walk.
2. **Saved reports.** `reportdb`, saving, changing, removing, the list,
   the title and description, notes for a period, history lines, a style
   or a choice no longer known.
3. **Category groups.** `category_groups`, `categories.group_id`, the
   categories page, and the "each group" detail.
4. **The project style and the comparisons.** "A project", and the
   first style's "compare with" for the year before and the budget.

## Verification

- `reportbus`: sums by each dimension and by two together; signed sums
  within a kind (a refund lowers its expense); transfers and pass-through
  outside income and expenses; unsorted parts and parts of a kind not
  said yet under "not sorted yet"; an archived account's parts counted; a
  pending charge that has posted counted once; two currencies never
  added; a month's reconciled mark with an account that has no parts that
  month, one that is half covered, and one archived years before; the
  same totals as the project book and the organization's budget for the
  same parts; a stranger gets `ErrNotFound`.
- Styles: each style's default choices; a saved choice the style no
  longer has, ignored; a missing one, defaulted; a style key no longer
  known.
- `reportdb`: the JSON round trip; the scope's reports; one note per
  report and period, a second a correction, an empty one removed.
- Notes: August's note on August's run and not on the year to date's;
  "changed since the note" when a part changes after it, and not before.
- `categorydb`: the unique group per list; the old-schema test for
  `categories.group_id`; changing a category's kind takes it out of its
  group.
- Through the site: run "Income and expenses by month" for an
  organization with an archived card and see its old months; save it
  as "P&L" with a description, groups and pass-through as one total;
  write September's note; open it as a viewer and print it; a viewer cannot change it; a stranger finds nothing.
