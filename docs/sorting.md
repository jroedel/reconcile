# Sorting: rules, suggestions, and a month at a time

The plan for step 9's "categorization rules and suggestions" (`docs/plan.md`,
build order). Sorting is the work that comes back every month: each charge
on a statement is given a category and, where it belongs to one, a project.
Most charges are the same payees as last month. This step makes the app
remember what a person decided, apply it when the next statement arrives,
and turn the rest of the month into one screen instead of one page per
charge.

It builds on the kinds of money (`docs/plan.md`, "Kinds of money"): a
category is income, an expense, a transfer or pass-through, so choosing a
category says what the money was. A rule that sorts "CAPITAL ONE AUTOPAY"
into "Transfers between our accounts" keeps the monthly card payment out of
income and expenses on both accounts, which is the rule most accounts need
first.

## Where things stand

- A new transaction gets one split for its full amount, with no category and
  no project. The month list counts the unsorted and filters to them.
- Sorting is the split editor on the transaction's own page
  (`ledgerbus.SetSplits`), one transaction at a time, by a bookkeeper or
  owner of the account. A project may be chosen if the person may use it
  (`projectChoices`).
- A reconciled period is locked; nothing in it changes.

## Decisions (from Q&A, 2026-10-08)

| Topic | Decision |
|---|---|
| A matching rule at import | **Sorts the charge at once**, marked "sorted by rule" so it can be checked. A charge a person has sorted is never touched by a rule |
| Where a rule lives | **Per account.** Each account has its own rules; who may make them is who may sort there (bookkeeper or owner) |
| What a rule sets | **A category, a project, or both.** The category carries its kind, so a rule can sort a card payment as a transfer. A rule naming a project stops applying when the project ends or is archived |
| Sorting a month at once | **Yes:** one page per account and month with every unsorted charge, the suggestion preselected, saved with one button |
| Guesses from history | **Never applied on their own.** A suggestion only preselects; a person saves it |

## What gets lifted from eumaeus

| Need | From eumaeus | Notes |
|---|---|---|
| A rule as text to look for, matched case-insensitively | `categorize/categorizebus/model.go` `Rule`, `classify` | Against the description as the bank wrote it; reconcile has no canonical merchant |
| Two rules that disagree are reported, never silently resolved | `Conflict`, `Conflicts` | Here: the longer match wins; two of the same length that disagree sort nothing and say so |
| Suggestions from what a person already sorted: the same payee, then a nearly-same name by trigram overlap, each with support and agreement | `categorizebus/suggest.go`: `Learn`, `Suggest`, `payeeKey`, `trigramSet`, `dice` | Category only, per account. The "token" method is left out: it needs more history than one parish account has |

Not lifted: transfer rules, Amazon order attribution, the config file as a
second rule source.

## Storage

A new domain, `rule` (`business/domain/rule/rulebus`, `stores/ruledb`).

- **`sort_rules(id, account_id, match, direction, category_id NULL,
  project_id NULL, created_by, created_at, updated_at)`**, `STRICT`.
  - `match` is the text to look for, kept as typed and compared
    case-insensitively, at least 3 characters. Unique per account, so making
    the same rule again is a correction, not a second rule.
  - `direction` is `out`, `in` or `any`: a refund from the same payee is
    usually not the same thing as the charge.
  - At least one of `category_id` and `project_id` is set (a `CHECK`).
- **`splits` gains `rule_id TEXT NULL`** (`AddColumn`, with the old-schema
  test): the rule that sorted this part, cleared the moment a person saves
  the transaction. That is the "sorted by rule" mark, and what lets a person
  find everything a rule did.
- Rule changes go in the account's history (`events`): made, changed,
  removed. An import's event says how many it sorted by rule.

## The rules (rulebus)

- **What matches:** a rule whose `match` is in the transaction's
  description, ignoring case and runs of spaces, in its direction. Of
  several, the longest `match` wins. Two of the same length that disagree
  sort nothing, and the account's rules page names them.
- **What may be applied:** a category from the account's list (the
  organization's, or the personal account's own) that is not archived; a
  project the account's splits may use (`projectChoices`), not archived, and
  not ended before the transaction's date. A rule whose category or project
  can no longer be used applies nothing, and its page says why.
- **When it applies:**
  - At import, to each new transaction, before it is stored: its one split
    gets the rule's category and project, and `rule_id`.
  - On the rules page, "Sort what is not sorted yet": every transaction of
    the account that is still one part with nothing chosen, outside a
    reconciled period.
  - Never to a transaction a person has saved, never to one in more than
    one part, never in a reconciled period, and never by changing a rule:
    editing or removing a rule leaves what it already sorted as it is.
- **Suggestions:** learned per account from the transactions a person sorted
  (a split with a category and no `rule_id`), the eumaeus way: the same
  payee first, then a nearly-same name. At least 2 examples agreeing at
  least 80% (weighed by how alike the names are, for a nearly-same one).
  Only one-part transactions are evidence: a split one's description says
  nothing about which part is which. Payees are grouped by `rulebus.Payee`,
  which passes over a card network's words in front ("POS", "SQ *").
  Category only, and so its kind: a payee always sorted as
  pass-through is suggested as pass-through. Shown, with the sentence saying why ("3 of the
  last 3 SHELL OIL charges were Fuel"), never stored.
- **Who:** a rule is read and written by a bookkeeper or owner of the
  account; anybody else gets `ErrNotFound` for its account, as everywhere.

## The pages

- **Transaction page:** the split editor preselects a rule's or a
  suggestion's choice when nothing is chosen, and says which ("suggested
  from 3 earlier charges"). Under it: **"Always sort charges like this the
  same way"**, a box with the match text prefilled from the description
  (the payee, without the store number and dates), editable. Saving with
  it ticked makes or corrects the rule.
- **`/accounts/{id}/sort?month=2026-07`, "Sort this month":** every unsorted
  charge of that month that is one part, in date order, each with its
  date, description and amount, a category choice grouped by kind and a
  project choice (preselected by rule or suggestion, marked as such), and
  the "always" box. One **Save** at the foot sorts every row with a category or project
  chosen and leaves the rest. Linked beside "{n} not sorted yet" on the month
  list. Works without JavaScript. The first 100 rows are shown; the rest
  come up as those are saved, so there is no paging to lose a choice in. A
  row whose choice will not do is shown again with why, and the others are
  saved regardless.
- **`/accounts/{id}/rules`:** each rule with what it sets, how many parts
  it sorted, and its problem if it has one (a category archived, a project
  ended, a disagreement); change it or remove it; make one; "Sort what is
  not sorted yet". Linked from the account page.
- **The month list** marks a part sorted by rule ("by rule") and can show
  only those, for checking a statement's worth at a glance.

## Build order

After the kinds of money (`docs/plan.md`, step 9), then two pull requests:

1. **Rules.** `rulebus` and `ruledb`, `splits.rule_id`, applying at import
   and from the rules page, the rules page, the "always" box on the
   transaction page, the "by rule" mark and filter, the history lines, the
   stranger walk.
2. **A month at a time.** Suggestions, preselection on the transaction page,
   and the "Sort this month" screen.

## Verification

- `rulebus`: matching ignores case and spacing; direction; the longest
  match wins; a disagreement sorts nothing and is reported; a rule never
  touches a person's sorting, a split transaction or a reconciled period; a
  rule with an ended or archived project, or an archived category, applies
  nothing.
- `ruledb`: the old-schema test for `splits.rule_id`.
- Suggestions: the same payee, a nearly-same name, too little evidence says
  nothing, disagreement says nothing, a rule's sorting is not evidence.
- Through the site: import a statement with rules and see "by rule";
  correct one and the mark goes; sort a month in one save; the "always" box
  makes a rule that sorts the next import; a viewer, a contributor and a
  stranger get nothing from any new route.
