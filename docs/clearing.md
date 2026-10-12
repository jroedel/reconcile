# Explaining an amount: clearing, holders, and pending charges

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
| Holders | **An option on an account**: its statements arrive one file per holder, a person or card on the account. Without it, nothing changes. (Called "cardholders" when decided; renamed "holder" on 2026-10-09, since nothing in it is about cards, and "segment" or "sub-account" is what an accountant calls part of a chart of accounts) |
| Pending | **Read from the document**, marked, and cleared by the posted charge when it arrives: kept in its statement, counted nowhere (changed from "replaced" on 2026-10-12, issue #104) |

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
any accounts of the same organization (or, for an account on its own,
the other accounts on their own the person may read), that together
make it up.

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
   by account, so a missing holder or month shows as a missing group.
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

## 3. Statements split by holder

A **holder** is a person or card on an account: most often each person on a
shared card, each of whose months arrives as a file of its own.

An account option: **"Statements arrive one file per holder."** Off for
every account that exists, and for any account that does not need it.

- **Who a row belongs to.** A PDF's person column (pdfsource already reads
  it, today into the memo), or a CSV's column mapped as "Holder". A
  file with neither is asked for on the preview: whose is it, from the
  holders seen before, or a new name.
- **Identity.** On such an account the holder is part of each row's
  content key, and the count rule (`docs/duplicates.md`) and the cut-short
  rule compare a holder's rows only with that holder's. Two people
  who park in one garage on one day for one price are two charges. A file
  for the whole account, with no holder, is compared with all of them.
- **Turning it on or off** recomputes the stored rows' hashes in one
  transaction, in Go, because the hash is computed in Go (eumaeus did the
  same when its hash changed).
- **The month list** shows a total per holder, and who is missing: a
  holder with a statement in either of the two months before and none
  in this one. "September: 5 of 6 holders."
- **The accountant's package** gains a "Holder" column.
- **A name cut short** by a narrow printed column ("GONZALO VILLA…") is
  the whole name it begins, when exactly one holder of the account or of
  the file has one (`ledgerbus.WholeName`): on a file's rows as they are
  read, and on rows already stored when an import meets them, outside
  reconciled periods (`Statement.Renamed`).
- **A file that names nobody** -- a bank's download of one card -- is
  taken for the holder whose charges already here it shares, when at
  least three of its rows are that holder's and at most one in twenty of
  those is anybody else's (`inferHolder`). The preview says so, with the
  question still there to answer otherwise; a bulk import carries the
  answer. A file of a new period, or of several holders' charges, is
  asked about as before.
- **Storage.** `transactions.holder`, the holder a file named, kept
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
    (On the printed pages read locally to build this, a stated total of
    one pending charge and of two each matched the rows at the head
    exactly, and the page's total activity counted them.)
  - A CSV's status column, when mapped, with "pending" (or "authorized",
    in English, Spanish or Portuguese) in it. The preview finds the
    column by its heading, as it finds the others.
- **Stored marked pending**, shown as such on the month, the transaction
  and the preview, counted in the document's total check as the document
  counts them, and in the accountant's package in a column of its own.
- **Cleared when posted** (issue #104). A row of the same account (and
  holder), dated on the pending one's day or up to ten days after, not
  pending, that fits it (`ledgerdb.fit`), **clears** it: the pending row
  stays in its statement, marked as having posted as the new row
  (`transactions.posted_as`), and from then on only the posted charge
  counts -- in money in and out, the totals by kind, a project's book, the
  review of a reconciliation and what is to sort. The pending row keeps no
  parts; a person's sorting of it (a category, a project or a memo, not a
  rule's) goes to the posted charge, whose last part takes the tip unless
  that would leave it nothing or turn it round; its place in an
  explanation goes too, and its receipts follow it on their side
  (`receiptbus.AfterImport`). Each page links one to the other. The
  history says "pending at 7.25, posted at 12.00". A posted row worded,
  dated and priced as its pending one was is the same row (the ledger's
  duplicate check finds it) and only stops being pending. A pending
  charge in a reconciled period is never cleared.

  It used to be **replaced**: the posted row took the pending row's
  place, its date, amount and description written over. That kept one
  row, but a statement checked by its sum with the pending amount in it
  stopped adding up, and a printed page's hold could not be told from
  what it became. Rows replaced before the change stay as they are.
- **What fits** (`ledgerdb.fit`), best first: the same wording, any
  amount the same way round (a fuel hold of 1.00 posts at 48.20); one
  wording inside the other, or cut short, within a third or 5.00 (5,
  below); only the first two words alike after a payment processor's
  prefix (`ledgerbus.SameMerchant`: a printed page's "CAFE EXAMPLE PAL
  A…" and the download's "CAFE EXAMPLE CITY ST"), the same amount or more
  by a tip of up to a third (`ledgerbus.Tipped`).
- **Only when it is plain** (`ledgerdb.pair`). A pending row and a posted
  row are paired at import when each fits the other better than anything
  else fits either. Two holds at one shop and one charge, or one hold and
  two charges, fit equally, and nothing is cleared: the account's page
  lists them under "Pending charges that may have posted", and so does the
  pending charge's page, for a bookkeeper to say "the same charge"
  (`ledgerbus.ClearPending`), and Claude through `list_pending` and
  `pair_pending`, with the person's yes. A bookkeeper may pair any posted
  charge the same way round with a pending one, and undo a pairing that
  was wrong ("Not the same charge"), which gives the pending charge one
  part again. Removing the posted charge's statement does that too.
- **Posted in the same file.** A bank's page goes on listing an
  authorization for days after its charge has posted, so one printed
  month can hold both: a coffee pending on the 28th and posted on the
  29th, a car hire's hold beside its charge. Both are imported, so that
  the statement is whole, and the pending row is cleared by the posted
  one by the same rule, which pairs the file's pending rows with its
  posted rows and the account's pending charges together. The preview
  counts them (`Statement.Superseded`). For a few hours before this, such
  a pending row was left out instead.
- **Never posted.** A pending row older than ten days past the latest
  statement of its account is listed on the account's page, "still
  pending", with a way for a bookkeeper to remove it (a hold that was
  released), outside a reconciled period; the history keeps what it
  was.

## 5. Holders never taken for one another (issue #81)

Found on nine months of a shared card's per-holder files, imported into an
account whose option was off: every file imported cleanly and every month
showed as imported, and six charges were gone. Three were two holders'
charges of one amount on one day, the second taken for the first; three
were one holder's pending charge taken by another holder's posted charge
at the same shop days later, at another amount. The option, had it been
on, would have kept them apart (3); nothing said it should be.

### As built

- **Never one by ignoring whose it is.** Whatever the option, a row that
  names a holder is matched by its content hash or by the cut-short rule
  only with a row of the same holder or one that names nobody
  (`ledgerdb.sameHash`, `cutShort`); the posted form of a pending charge
  likewise (`post`, `settle`). Without the option the hash leaves the
  holder out, so two people's identical charges share it: that was the
  silent "already here". Such a row goes on to the count rule, which on
  an account without the option still counts every holder together, and
  it becomes a doubt that names both holders (`Doubt.OtherHolder`).
- **Who decides.** A doubt between two holders, in a file whose rows all
  name one holder, is imported unless a person leaves it out: that file
  is one person's month and cannot list another's charge. In any other
  file it is set aside, as every doubt is (`docs/duplicates.md`), since a
  file of the whole card may spell a holder otherwise. Either way the
  preview names both holders, and a bulk import, and so Claude's import
  from the inbox, waits for a person on any doubt at all, not only one set
  aside (`Proposal.Unattended`).
- **Noticed and asked.** A file whose rows all name one holder, for an
  account without the option that already has other holders' charges on
  the file's days, looks like one holder's month (`Draft.LooksByHolder`).
  The preview says so and, to an owner, offers to turn the option on and
  read the file again; anybody else is told to ask an owner. The imports
  page and the API's `needs` say it too, and the file is not imported
  unattended. A file of the whole card names several holders or none, and
  the first holder's file has nobody to be confused with, so neither is
  asked about.
- **The amount a pending charge may become** (`ledgerbus.PostsAs`). The
  holds this section began with -- a restaurant before the tip, a parking
  or fuel hold, a hotel's deposit -- are why the amounts differ. The tip
  is the one whose wording also changes, in the files read so far, and a
  tip is up to a third. So: the same wording takes any amount the same way
  round, as before (a fuel hold of 1.00 posts at 48.20); wording only
  alike must be within a third of the pending amount, or 5.00
  (`PostSlack`) for a charge so small that a third is less than a tip;
  and a refund is never the posted form of a charge. A posted row refused
  is imported as new, and the pending one waits until "still pending"
  lists it: both visible, where a wrong match was not.
- **Settings.** The option is on the account's transactions page, in
  "Holders", where its holders are; the account's settings say whether it
  is on, and link there.
- **What already happened.** The account's transactions page lists, under
  "Whose charge?", charges named for one holder whose part's memo names
  another holder of the account (`ledgerbus.Disagreeing`): the mark a
  pending charge taken by another holder's posted one left, since a card's
  printout names the holder in the memo too. A charge lost the first way
  left no mark. Both come back the same way: turn the option on, remove
  the statement of the holder's file it was lost from, and import that
  file again; with holders kept apart nothing of another's is taken for
  it. Removing a statement takes how its rows were sorted with it, so this
  is left to a person to choose, file by file.

## Build order

Four pull requests:

1. **Entries by hand**, with their document, in `ledgerbus`/`ledgerapp`.
2. **Explain this amount**: the tables, the page, "cleared by", the
   back-to-zero rule, the package's column and file, the stranger walk.
3. **Split by holder**: the account option, the content key, the
   count and cut-short rules, the preview's question, the month list's
   totals and who is missing, the package's column.
4. **Pending charges**: the readers' marks, the replacement at import, the
   "still pending" list.

## Verification

Each with invented accounts, holders and amounts:

- Six holders' files for two months imported into one split account;
  two holders with the same charge on the same day are two charges.
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
