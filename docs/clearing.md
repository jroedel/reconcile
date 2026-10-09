# Explaining an amount: clearing, cardholders, and pending charges

A plan for step 9 (`docs/plan.md`, build order). It comes from one
treasurer's month, said in general terms, because the app is for anyone's:

> Somebody else holds the card. Each month they send one file per
> cardholder -- the household has several cards on it -- and then
> take one amount out of the household's account for all of it, with an
> email saying what it covers: "this month's cards, plus a plane ticket I
> paid on my own card for one of yours". The treasurer imports the files,
> adds them up, and wants to know whether the amount is right, and if not,
> by how much and why.

The amount that comes out of the account is one transaction. What it pays
for is many. Today the app can say what each of those is; it cannot say
that they belong together, nor check that they add up.

## The accounting idea

This is **clearing**. Items collect in an account that should come back to
zero -- here, the household's debt for its cards -- each one **open**
until a payment **clears** it. Saying which items a payment pays for is
**applying** it, and a payment's list of what it covers is its
**remittance advice**: the email above is one. Bookkeeping programs offer
the same thing as matching one bank line to several entries.

It is not about cards. The same question -- one amount, what makes it
up? -- is asked of a Sunday collection deposited as one sum, a PayPal or
Square payout of many sales less their fees, a reimbursement cheque for
somebody's receipts, a payroll withdrawal, a diocese's transfer for
several programmes. So it is built once, for any transaction.

## Decisions (from Q&A, 2026-10-09)

| Topic | Decision |
|---|---|
| The need | **General.** Nothing here names a bank, a card or a household; another treasurer, in another country, uses the same pages |
| Explaining | **One tool for any transaction**: "Explain this amount" gathers the transactions and lines that make it up, and says the difference |
| Cardholders | **An option on an account**: its statements arrive one file per cardholder. Without it, nothing changes |
| Pending | **Read from the document**, marked, and replaced by the posted charge when it arrives |

## 1. Entries by hand

The explanation above needs a line no file has: the plane ticket, paid on
somebody else's card and added to this household's month. The app has no
transaction that did not come from a statement.

- **A transaction entered by hand**, in an account, by a bookkeeper: date,
  description, amount, and the document that supports it -- a photo or a
  PDF, as a receipt is. The document is required: an entry nobody can show
  the paper for is the one an accountant asks about.
- It is stored as a statement of its own, format `hand`, whose file is the
  document, with one transaction. So it has the identity, the parts, the
  sorting, the receipts and the history every transaction has, needs no
  change to the statements table, and is removed the way a statement is.
- Marked "entered by hand" wherever it is shown, and in the accountant's
  package. A reconciled period takes none.

## 2. Explain this amount

Any transaction can be **explained**: a list of other transactions, from
any accounts of the same organization (or the same person's personal
ones), that together make it up.

**The page**, `/transactions/{id}/explain`, opened from a question mark on
the transaction:

1. **Gather.** Choose accounts and a span of months; the app lists their
   transactions not already in an explanation, all ticked. Untick what
   does not belong. Add an entry by hand (1) for anything no file has.
   The choice of accounts and months is remembered for the next
   transaction of the same account with a like description, so next
   month's withdrawal is gathered in one press.
2. **Compare.** The lines add up, beside the amount, in the account
   holder's signs: a withdrawal of 4,210.55 is explained by charges that
   come to 4,210.55. The difference is shown, and the lines are grouped
   by account, so a missing cardholder or month shows as a missing group.
3. **Settle.** With no difference the transaction is **explained**. With
   one, it stays **open**, or the person accepts the difference with a
   note ("two parking holds settled lower than pending"), which the
   history keeps.

**Rules.**

- A transaction explains at most one amount, so two months' payments
  cannot both count the same charge. The store keeps that with a unique
  key, not a check before a write.
- Explaining changes nothing about a line: not its amount, its sorting nor
  its reconciliation. So lines may come from reconciled periods.
- Who: a bookkeeper or owner of the explained transaction's account, who
  may read every account a line comes from. Somebody who may see the
  explained transaction but not every line's account sees those lines as
  one sum, "3 lines in accounts you cannot see, 512.40", never their
  descriptions: nobody sees an account they were not given.
- Each line shows "cleared by" the transaction it explains, on its own
  page and in the month list.

**What it does for the rest.**

- **Back to zero** (`docs/plan.md`, "Kinds of money"): a transfer that is
  explained by transactions in the organization's own accounts counts as
  met, so a card paid for by one withdrawal no longer leaves the
  organization's transfers off by the withdrawal every month.
- **The accountant's package** gains a "Cleared by" column, and
  `explanations.csv`: each explained transaction, its lines, the
  difference and the note.

**Storage.** `explanations(transaction_id PRIMARY KEY, note, accepted,
sources, back_from, back_to, created_by, created_at, updated_by,
updated_at)` -- `sources` and the two `back_` columns are the accounts and
months the lines were last gathered from, for the next one like it -- and
`explanation_lines(transaction_id PRIMARY KEY, explains, added_by,
added_at)`, both `STRICT`, both going with their transactions (`ON DELETE
CASCADE`), and history lines for made, changed and settled.

## 3. Statements split by cardholder

An account option: **"Statements arrive one file per cardholder."** Off for
every account that exists, and for any account that does not need it.

- **Who a row belongs to.** A PDF's person column (pdfsource already reads
  it, today into the memo), or a CSV's column mapped as "Cardholder". A
  file with neither is asked for on the preview: whose is it, from the
  cardholders seen before, or a new name.
- **Identity.** On such an account the cardholder is part of each row's
  content key, and the count rule (`docs/duplicates.md`) and the cut-short
  rule compare a cardholder's rows only with that cardholder's. Two people
  who park in one garage on one day for one price are two charges. A file
  for the whole card, with no cardholder, is compared with all of them.
- **Turning it on or off** recomputes the stored rows' hashes in one
  transaction, in Go, because the hash is computed in Go (eumaeus did the
  same when its hash changed).
- **The month list** shows a total per cardholder, and who is missing: a
  cardholder with a statement in either of the two months before and none
  in this one. "September: 5 of 6 cardholders."
- **The accountant's package** gains a "Cardholder" column.
- **Storage.** `transactions.holder`, the cardholder a file named, kept
  for every account; it joins the identity only where the option is on.
  The option itself is a row of the ledger's `holder_accounts`, not a
  column of `accounts`, so that turning it on or off and giving every row
  its identity again are one transaction of one store.

## 4. Pending charges

A bank's page lists charges that have not posted yet, and a pending amount
often changes when it posts: a parking hold, a restaurant before the tip,
a hotel's deposit. Imported as if posted, the pending charge and the posted
one are two -- with different amounts, so not even the count rule sees
them.

- **Which rows are pending**, from the document, never guessed:
  - a section headed "Pending" (until the next heading); or
  - a stated pending total ("Pending purchases $14.50") and the rows at
    the head of the list that add up to it, which is how a printed page
    lists them. Rows that do not add up to it are not marked, and the
    preview says the document states a pending total it could not place.
  - A CSV's status column, when mapped, with "pending" in it.
- **Stored marked pending**, shown as such, and counted in the document's
  total check as the document counts them.
- **Replaced when posted.** A later row of the same account (and
  cardholder) within ten days after it, not pending, whose description is
  alike (`ledgerdb.alike`, eumaeus' containment first), takes the pending
  row's place: its date, amount and description change to the posted
  ones, and it keeps its sorting, receipts and explanation. A one-part
  transaction's part follows the amount; with several parts the
  difference goes to the last part and the transaction is marked for
  checking. The history says "pending at 12.00, posted at 9.40".
- **Never posted.** A pending row older than ten days past the latest
  statement of its account is listed on the account's page, "still
  pending", with a way to remove it (a hold that was released).

## Build order

Four pull requests:

1. **Entries by hand**, with their document, in `ledgerbus`/`ledgerapp`.
2. **Explain this amount**: the tables, the page, "cleared by", the
   back-to-zero rule, the package's column and file, the stranger walk.
3. **Split by cardholder**: the account option, the content key, the
   count and cut-short rules, the preview's question, the month list's
   totals and who is missing, the package's column.
4. **Pending charges**: the readers' marks, the replacement at import, the
   "still pending" list.

## Verification

Each with invented accounts, cardholders and amounts:

- Six cardholders' files for two months imported into one split account;
  two cardholders with the same charge on the same day are two charges.
- A withdrawal explained by the card's two months and a hand entry for a
  ticket, with a difference left by two pending holds; the holds post
  lower the next month, and the difference becomes zero.
- An explanation's lines cannot explain a second amount; a viewer of the
  checking account who cannot read the card sees the card's lines as one
  sum.
- A collection deposit explained by hand entries from its count sheets.
- The organization's transfers come back to zero once the withdrawal is
  explained.
- A stranger, a viewer and a contributor get nothing from any new route.
