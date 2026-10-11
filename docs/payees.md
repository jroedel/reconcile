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
| Spellings | **Joined by a person**, with suggestions. "Hilltop Plumbing" and "HILLTOP PLUMBING LLC" are one payee only when somebody says so |
| Which payments | **Any money out**, not only checks: a transfer or a card payment to the same person counts. A check's payee comes from its image, as now; any other transaction's is chosen on its page |

## 1. A payee

Today a payee is text on a transaction (`transactions.payee`), written
when a check's image is read or numbered (`ledgerbus.SetPayee`), shown as
"Paid to", and in the accountant's package. Nothing says two checks with
the same words were paid to the same person.

- **A payee is a record**, kept in the same list as categories are: one
  per organization, or one per personal account
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

A page for an organization's (or a personal account's) payees, by year:

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

`list_payees` and `get_payee_payments` (`books:read`), and a `payee` on the
tool that sets a transaction's description, or a tool of its own, for
`books:write`, marked by Claude until a person saves the transaction, as
everything a key changes is. Claude may suggest joining two payees and
reporting one; joining and the two marks stay a person's, on the site,
because they are statements about real people that the accountant will
rely on.

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
2. **Reported payees and the year** (3, 4): the marks, the page by year,
   the download, the package's column.
3. **Through the API** (5).

## Open questions

- **Personal accounts**: a payee list of their own, as categories have, or
  none, since nobody reports a household's payments?
- **Money in**: a payer is the same idea the other way round (who gave),
  and a donor list is something a parish asks for. Left out here, and the
  record is shaped so it could be added.
