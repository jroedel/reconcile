# PDF statements

The plan for step 9's "PDF statements" (`docs/plan.md`, build order). Some
banks and card issuers offer no CSV or OFX for some accounts, or not to
every person on them. What everybody can get is a PDF: the statement the
bank composed, or the account's transaction page printed to PDF from a
browser. This step reads both, by the same rules as every other import:
deterministic, checked against the document's own figures, previewed
before anything is stored, and refused when it does not add up.

## Decisions (from Q&A, 2026-10-09)

| Topic | Decision |
|---|---|
| How | **One generic reader**, no bank-specific code to begin with. Adapters for one bank's format are added only when the generic reader cannot do it and somebody needs it |
| Text | **poppler's `pdftotext -layout`** on the server (present: 22.12). The PDF is never parsed in Go |
| A PDF it cannot read | **Says why and suggests CSV or OFX.** Nothing is imported; there is no column-mapping screen for PDFs |
| First format | A card issuer's transaction page **printed to PDF from a browser**, which a treasurer has in hand now |

## What a PDF is, to this reader

`pdftotext -layout` turns a PDF written by software -- a statement, or a
web page printed to PDF -- into lines of text with the columns kept apart
by runs of spaces, and pages separated by form feeds. A scanned statement
has no text and is refused as one ("this PDF is a picture of the
statement"). Nothing is guessed from images.

The reader then finds the **transaction rows**: a line that starts with a
date and ends with one or two amounts, separated from the rest by at least
two spaces (a payee's own numbers -- "TFR FRM CHK 1080" -- are separated
by one). Dates are read in the forms banks print: `09/30/2026`,
`09/30/26`, `Sep 30, 2026`, `30 Sep 2026`, `2026-09-30`, and `09/30` with
the year from the statement's period.

Around the rows:

- **Page furniture is dropped.** A line that repeats at the top or foot of
  several pages (the site's name, the date it was printed, the address of
  the page, "Page 2 of 5") is not part of any row. A browser prints the
  page's address in the footer; it is dropped like the rest and never
  stored, since it can carry an account's identifier.
- **A line that is not a row, not furniture and has no amount continues
  the row above it.** That is how a long description wraps -- and how a
  printout that broke a row across a page puts the description on the next
  page, under a row that has none.
- **A header line** ("Date  Description  Name  Amount") says what the
  columns between the date and the amounts are. A column whose heading is
  a person (Name, Card member, Cardholder) is kept as the part's memo, not
  in the description, so that "Corner Hardware" is a payee rules and suggestions can
  learn from (`docs/sorting.md`) and the cardholder is still on record.
- **A description cut short by the page** ends in "…". It is kept as it
  is: what the document says is what is stored.

### A bank's own statement, laid out by kind

A bank's own statement -- as against a page printed from its website --
lists its rows by kind, not by date, and a checking account's commonly
looks like this (it is how one large US bank's consolidated statements are
laid out, 26 months of which were read locally to build this, and every
one of the 78 account-months balanced to the cent):

- **Sections with a direction.** "Deposits and additions", "Checks paid",
  "Electronic withdrawals", "ATM & debit card withdrawals", "Fees", each
  under its heading, every amount printed without a sign. A heading is a
  few words alone on a line, outside the description's column, and its
  words say which way the money went (English, Spanish and Portuguese:
  `pdfsource/sections.go`). The direction is used only when no row of the
  document carries a sign or a balance of its own: a card statement that
  prints "-100.00" under "Payments and credits" is read as printed.
- **Checks paid** start with the check's number, not a date: the date
  paid is the last date on the line, and the row is called "Check 1176",
  as the bank's CSV and OFX call it, so the same check from either is the
  same transaction. A number alone on its line is a row the page broke;
  the line after it is the rest.
- **A row broken at a page's foot** -- its date alone on one line, its
  description and amount on the next -- is joined, when the second line
  sits to the right of where the date was. A line with an amount is never
  page furniture, however often its shape repeats at the pages' edges.
- **A date at the start of a description** that is earlier than the row's
  date ("05/16  05/12/2025 Debit for an item processed twice") is the
  description's. A second date is taken as the posting date only when it
  is not before the first.
- **The daily ending balance** is a table of its own: dates, each followed
  by the balance that day ended with, several pairs to a line. Its rows
  are not transactions. Each balance is put on the last row on or before
  its day (the rows sorted by date first, keeping their order within a
  day) and checked as a balance printed on a row is.
- **Several accounts in one document.** Each "Account number" starts an
  account's part, with its own beginning and ending balance, sections and
  daily balances. The import takes the part whose number ends as the
  account's last four digits do, or asks which on the preview; only those
  four digits are kept. The same file can be imported into each account.
- **Marks a bank hides in the text** ("\*start\*deposits and additions")
  are not on the page, and are dropped.

## How it is checked

A PDF is accepted only when its own figures prove it was read whole, or
the person types the balances, exactly as a CSV is (`ledgerbus.verify`).

1. **Running balance.** When rows carry a balance -- every row, or the
   last of each day, from a daily balance table -- each balance must
   follow from the one before, and the rows must also take the opening
   balance the document states to its closing one, since balances printed
   from the first day on say nothing of a row missing before it. The sign of the amount is
   then taken from the balances, never from where the figure sat: a debit
   column and a credit column are only horizontal whitespace after
   extraction, the least durable thing on a page (eumaeus'
   `importing/sources/statement`, whose reasoning this follows).
2. **Stated totals.** When rows carry only an amount -- every card
   statement, and every printed transaction page -- the document's own
   totals are found outside the rows ("Total activity", "Purchases",
   "Credits", "Payments", "New balance" less "Previous balance") and the
   rows must add up to them. A dropped or misread row breaks the sum.
3. **Typed balances**, as for a CSV, when the document states neither.
4. **None of these** is "not checked", shown as such in the preview, as a
   CSV without balances is today.

Since the shapes work (`docs/shapes.md`, 1), 3 and 4 hold only for a
layout a declaration describes. A PDF in a layout none does is read by
this general reader and imported only when 1 or 2 holds: its own figures
are the only check on a reading of a layout nobody has looked at, and
balances a person types are typed from the same page the reader may have
misread. Typed balances are still checked on top, when given.

A check that fails imports nothing, and the preview names the row where it
stopped adding up, as a CSV's does.

**Signs.** A card issuer prints from its own side: a purchase is a
positive amount it is owed, a refund negative. On an account whose kind is
a card, amounts are turned around (as the CSV mapping's "money out is
positive" does today), and the preview has the same switch, set by the
account's kind, for a document printed the other way. Where a running
balance gives the signs, it gives them as a bank account's (the balance
going up is money in), and the switch turns them round for a card the
same way: a card's balance going up is a purchase.

The first row of a running-balance table has no balance before it but the
opening balance. Without one, it takes the sign of the column its amount
is in, when the other rows show two columns of amounts apart; otherwise it
is left as printed.

## Not counting a charge twice

A printed page and the statement that follows it list the same charges,
and the printed page's descriptions may be cut short. The ledger's
duplicate check (`ledgerdb.already`: the bank's ID, or the date,
description and amount) would count those twice. So, for any import, a
row is **already here** when the account has a transaction on the same
day for the same amount whose description starts with the row's (or the
row's with its), "…" taken off. The preview lists them as already here,
as it lists any other; nothing is merged or changed.

## Running pdftotext safely

The file is somebody's upload, and poppler is a large parser.

- Run with no shell, reading the stored file, with a **timeout** (20
  seconds), the **first 50 pages** only (`-l 50`), on files under the
  statement size limit (`ledgerbus.MaxFile`, 10 MB).
- In its own process: if the host stops it for its memory, the app is
  unharmed and the person is told the file could not be read.
- One at a time across the app, as photos are (`imaging.Turn`).
- Its output is capped at a few megabytes before it is read.
- **Without pdftotext** (a developer's machine without poppler), a PDF is
  refused as today, and the tests that need it say they were skipped.
  CI installs `poppler-utils` so that they run there.

## Where the code goes

- `foundation/pdftext`: running pdftotext, lifted from eumaeus, with the
  limits above. It knows nothing of statements.
- `business/domain/importing/sources/pdfsource`: the reader -- rows,
  furniture, headers, continuations, dates, the totals it finds -- giving
  `importbus.Record`s and the stated totals. It knows nothing of accounts.
- `ledgerbus`: a PDF goes through `Prepare` and `Import` like a CSV; the
  stated-totals check joins `verify`; the "already here" rule above joins
  the store's duplicate check; `ErrPDF` goes.
- `ledgerapp`: the preview, with the sign switch, and the reasons a PDF
  could not be read.

## Fixtures

No real statement or printout enters the repository (CLAUDE.md). The
tests draw their PDFs in Go at run time -- a few pages of positioned text,
laid out as the formats above are -- with invented payees, people and
amounts. The reader's rules are tested on the text such PDFs produce,
which is what `pdftotext -layout` returns.

## Build order

One pull request: the reader, the checks, the duplicate rule, the
preview. Adapters for one bank come later, each when a real document the
generic reader refuses is in hand.

## Verification

- `pdfsource`: dates in each form; furniture across pages; a description
  continued past a page break; a person's column kept apart; a row whose
  description is a number; running balances in both orders; stated totals
  found and not found; a scan refused.
- `ledgerbus`: a printed card page imports with its signs turned and its
  totals checked; a statement with balances imports by them; a total that
  does not match imports nothing and names the row; a cut-short
  description matches the full one already imported.
- Through the site: upload a drawn PDF, see the preview, import it; a PDF
  with no text is refused with the reason.
