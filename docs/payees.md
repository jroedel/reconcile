# Payees: whom the money went to, and whose payments are reported

A plan for step 9 (`docs/plan.md`, build order), the part of issue #71
left for a plan of its own. It comes from an accountant's request: for
each payment to a person or a firm that is not a utility or a shop, who
was paid, how much, and what for, so that what the law says must be
declared can be declared at the year's end.

In the United States that is the 1099. It means nothing in Brazil, where
the same accountant declares what was paid to an *autônomo* or a service
provider, nor anywhere else, and the app is for anyone's books. So nothing
here is named after a form or a law. The app keeps a fact -- **whom the
money went to** -- and one mark a person sets -- **report this payee's
payments** -- and adds up the year. What the law asks for, and of whom, is
the accountant's to know.

## Decisions (proposed, 2026-10-10)

Proposed in conversation and written down for the person to mark up before
anything is built.

| Topic | Decision |
|---|---|
| The name | **Payees.** Not "contractors": that is a legal classification, drawn differently in each country (employee or independent contractor; CLT, *autônomo* or PJ), and calling somebody one would be the app giving an opinion. A payee is only whom a payment went to |
| The tax part | **One mark on a payee**: "Report this payee's payments to the accountant". The app does not know which payments a law reports; the person who does marks the payee |
| Tax papers | **"Their tax details are on file", yes or no.** Never the tax number itself -- SSN, EIN, CPF, CNPJ: those stay with the accountant, and a site anybody may sign up to has no business holding them |
| The year | **The calendar year**, which is the tax year in the United States and in Brazil alike |
| Spellings | **Joined by a person or their Claude**, with suggestions. "Hilltop Plumbing" and "HILLTOP PLUMBING LLC" are one payee only when somebody says so. Claude joins through the API as anything it changes: proposed first, done on the person's yes, marked until a person looks, and undone by splitting off (decided 2026-10-10: a long list of spellings is exactly the work Claude saves) |
| Which payments | **Any money out**, not only checks: a transfer or a card payment to the same person counts. A check's payee comes from its image, as now; any other transaction's is chosen on its page |
| Accounts with no organization | **Have payees too** (decided 2026-10-10). An account with no organization is not necessarily somebody's household: a community or an affiliate may keep its own account here without being given the organization that owns it in law, and the app cannot tell the two apart. So it assumes nothing, and every account's books have payees |

## 1. A payee

Today a payee is text on a transaction (`transactions.payee`), written
when a check's image is read or numbered (`ledgerbus.SetPayee`), shown as
"Paid to", and in the accountant's package. Nothing says two checks with
the same words were paid to the same person.

- **A payee is a record**, kept in the same list as categories are: one
  per organization, or one per account with no organization
  (`payees(id, org_id NULL, account_id NULL, name, reported, details_on_file,
  note, created_by, created_at, archived_at)`). Names are unique in a list,
  whatever their case. Archived, never deleted.
- **Its spellings** (`payee_names(payee_id, name)`): every way it has been
  written, so that the next check written the same way finds it. A
  spelling belongs to one payee of a list at most.
- **A transaction has one payee or none** (`transactions.payee_id`, a later
  column). The text stays as what was written -- the check's "Pay to the
  order of" line -- and the payee is who that is. A spelling known to one
  payee sets it; an unknown one makes the payee new, named as written.
- **On any transaction** of money out, its page has "Paid to": a payee of
  the list, or a new name. Whoever may attach receipts may set it, as now
  for a check's payee, in a reconciled period too: it is a note, not money.
- **Sorting rules** can later learn from it, as `docs/shapes.md` (3) meant
  the payee note to be learnt from. Not in this plan's first step.

## 2. Joining spellings

The list of payees has, for each, the spellings it is known by and the
number of payments to it. **Join** puts one payee's spellings and payments
under another, with a line of history on each, and is undone by **Split
off**, which takes a spelling and its payments back out.

**Suggestions** sit at the top of the list: payees whose names are the same
once case, spaces and punctuation are set aside, or once a firm's ending is
-- LLC, Inc., Ltd., Ltda., ME, S.A. and the like, a short list kept with
the code. A person joins each, or dismisses it. Nothing is joined on its
own: two people with one name are two payees until somebody who knows says
otherwise.

**A join made through a key** -- the person's Claude, on their yes -- is
marked "joined by Claude" on the payee, with the spellings it brought in,
until a person saves the payee on the site; and it is a line in the
history naming the key, as every change through a key is. Split off undoes
it there, or through the API.

## 3. Reported payees

On a payee's page, for a bookkeeper or an owner:

- **"Report this payee's payments to the accountant"**, on or off.
- **"Their tax details are on file"**, yes or no, with a hint whose
  example is a translation's to give: "such as a W-9 in the United
  States" in English, its own country's form in another language.
- **A note** ("registered as a sole trader; form received in March").

Each change is a line in the history of the list's owner. An accountant
sees all three and changes none: the marks are the organization's word
about its payees, given to the accountant.

## 4. Payments by payee

A page for an organization's payees, or an account's with no
organization, by year:

- **The reported payees first**, each with the total paid in the year,
  the number of payments, whether their details are on file, and the
  payments themselves on opening it: date, account, amount, memo, and the
  check's image where there is one.
- **Then everybody else**, largest total first, so that someone who should
  have been reported is seen. No threshold is built in: the amounts the
  law cares about differ by country and change with the years, and a
  threshold in the app would be wrong somewhere.
- **The date that decides the year** is the day a check was written,
  where it is known (#71), and otherwise the day the payment posted. A
  check written on 30 December and cleared on 3 January belongs to the
  year it was written. Which date a law uses is the accountant's call;
  the page says which it used for each payment.
- **What counts** is the parts of a payment that are expenses, or not
  sorted yet (shown as such). Transfers and pass-through are not payments
  to a payee: repaying somebody what they paid on the organization's
  behalf is not paying them.
- **A download** of the same, as a spreadsheet whose header row goes
  through `t` (an app's `text/*.txt`), and in the accountant's package, a
  "Reported payee" column beside "Paid to".

## 5. Through the API

Claude's part is the tidying a long list of spellings needs, done with the
person, as the rest of the books are (`docs/claude.md`: it proposes, the
person decides):

| Tool | Scope | What it does |
|---|---|---|
| `list_payees` | `books:read` | A list's payees: name, spellings, number of payments and their total for a year, the two marks, and its `url` |
| `suggest_payee_joins` | `books:read` | The suggestions of 2, each with both payees' spellings and a few of their payments, so Claude can say why |
| `get_payee_payments` | `books:read` | A payee's payments in a year, as 4 counts them, with each check's image a `get_receipt_image` away |
| `join_payees` | `books:write` | One or more payees into another, as Join does on the site. Refused across two lists |
| `split_payee` | `books:write` | A spelling and its payments back out, into a payee of its own or another |
| `set_payee` | `books:write` | A transaction's payee: one of the list, or a new name |

- **The routine** (Claude's guide): list the payees and the suggestions;
  say which it would join and why, from the spellings and the payments,
  and which look alike but are not ("two people named Silva"); join on
  the person's yes; end with what it joined, each with its `url`.
- **Marked** until a person looks, as a sorting rule Claude wrote is: a
  join and a payee set through a key say "by Claude" on the site until a
  person saves the payee or the transaction there.
- **The two marks stay a person's**, on the site: whether a payee's
  payments are reported, and whether their tax details are on file. They
  are the organization's word about real people, which the accountant
  relies on, and Claude can read them and say which payee looks like it
  should be reported, but not set them.

## What this is not

- **Not a tax engine.** No thresholds, no withholding (Brazil's IRRF and
  INSS on an *autônomo*'s payment, a US backup withholding), no forms
  filled in or filed. The accountant does those, from what this page
  shows.
- **Not a place for tax numbers.** Ever.
- **Not accounts payable.** Nothing here is a bill to pay; it is what was
  paid.

## Build order

Three pull requests after this plan, each useful when merged:

1. **Payees** (1, 2): the list, spellings, a transaction's payee on any
   payment and from every check read, joining and its suggestions.
2. **Through the API** (5), early, because joining a long list of
   spellings is the first thing the person wants help with.
3. **Reported payees and the year** (3, 4): the marks, the page by year,
   the download, the package's column, and `get_payee_payments`.

## Open questions

- **Money in**: a payer is the same idea the other way round (who gave),
  and a donor list is something a parish asks for. Left out here, and the
  record is shaped so it could be added.
