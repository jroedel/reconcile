# The shape of a file: recognizing it, declaring it, and what follows

A plan for step 9 (`docs/plan.md`, build order), after pending charges
(`docs/clearing.md`, 4). It comes from the first bank statement the
general PDF reader could not read until it learned four new things at once
(`docs/pdf-statements.md`, "A bank's own statement, laid out by kind"):
sections that sign their amounts, check rows, a table of daily balances,
and several accounts in one document. Each was taught to the reader in Go.
The next bank, or the same bank's next layout, should be taught in a
declaration instead.

## Decisions (from Q&A, 2026-10-09)

| Topic | Decision |
|---|---|
| Where shapes live | **Both**: built-in declarations in the repository, reviewed as pull requests and embedded in the binary; and drafts a site administrator tries on a file before one becomes a pull request |
| A shape never seen | **Read with the general reader, and imported only if it balances** against the document's own figures; otherwise refused, and the shape (never its contents) recorded |
| Check images | **One image per check**, matched to its transaction by the check's number; other sources (a PDF of many checks) left open, so matching is by number whatever the source |

## 1. Recognizing a file

Every file that arrives has a **shape**: what kind of document it is and
how it is laid out, independent of whose money is in it. Recognizing it is
the first step of every import, before a row is read:

- **CSV**: the header row, as today's mapping fingerprint
  (`csvsource.Inspection.Fingerprint`), and the separator.
- **OFX**: the version and the bank's identifiers (`ORG`, `FID`).
- **PDF**: what produced it (pdfinfo's `Producer` and `Creator`), and the
  words the document uses for its structure: its headings, its labels for
  balances and account numbers, its period line, with every digit taken
  out.

A shape is **declared** when a declaration matches it (2), and the file is
then read as the declaration says. Otherwise it is **new**, and read by the
general reader as now; under the decision above, a new PDF shape imports
only when the document's own figures prove it was read whole (running or
daily balances, or opening and closing, or a stated total). A CSV's shape
is new until its columns are mapped, which is already a person's word on
it, so the rule changes nothing for CSV or OFX.

A new shape is recorded as a **sighting**: its signature (a hash of the
structural words above), its format and producer, the structural words
themselves, how often it was seen, and whether the general reader balanced
it. Never a row, an amount, a name or a number: the sighting is what a
developer needs to write a declaration, and nothing an administrator may
not see. The administrator's page lists them, most seen first.

The preview says which: "Read as: consolidated checking statement (2026)"
or "A layout this site has not seen before. It balanced, so it can be
imported." When a declared shape stops matching -- the bank changed its
layout -- its files become new, the sighting appears, and the general
reader carries them meanwhile when it can.

### As built (the first pull request)

- **The signature** is a hash of the format, the producer and the
  layout's **frame**: the words every month of it prints -- its column
  headings, its labels for the opening and closing balance, the total and
  the account number, and the form of its period line ("M #, # through
  M #, #"). Section headings are kept beside it but are not part of it,
  since a month without checks has no "Checks paid" heading and is still
  the same layout; the pending total's label likewise. Checked locally on
  the treasurer's real files, without them entering the repository: 26
  months of a consolidated checking statement are one signature, with 9
  to 11 section headings among them, and 12 printed card activity pages
  are another.
- **Only words the reader's own patterns matched** are kept, never a
  whole line: a heading's matched words, a label's, a column heading's
  (an unrecognized column is "…"), each lower-cased with every digit a
  "#". A cardholder's name beside a heading, a payee, an amount or an
  account's digits cannot come with them. A CSV's column headings are kept
  only when its date and amount columns were found by name and no heading
  has three digits in a row, since a file without a header row has a row
  taken for one; OFX gives its dialect and its `ORG`.
- **The producer** is the only thing taken from a PDF's metadata
  (`pdftext.Producer`, poppler's `pdfinfo`), digits taken out, so that an
  engine's next version is the same layout. The author and title can name
  a person and are never read.
- **Proven** is a check of the rows against the document's own figures
  alone, apart from the check the preview shows, which includes balances a
  person typed. A new PDF layout needs it; the preview then offers no
  balances to type, and says why it cannot be imported and that the
  administrator can see the layout arrived.
- **Sightings** (`business/domain/shape`) count files by their content
  hash, so a file previewed many times, or sent by two people, is one;
  a file is "balanced" once any preview of it was proven. They belong to
  no account or organization. The administrator's page lists them, most
  files first.
- **The general reader's words** are `pdfsource.General`, a `Layout`;
  `pdfsource.ReadAs` reads by another. Its JSON form and the matcher that
  chooses a declaration are the next pull request.

### As built (the second pull request)

- **The form**: one JSON file per layout in
  `business/domain/importing/shapes/declarations/`, named by its id and
  embedded (`shapes.Parse` says what is wrong with one, and refuses a
  field it does not know, so that a misspelt one is not quietly the
  general reader's). Its `layout` names only the patterns it changes;
  the rest are `pdfsource.General`'s.
- **Matching**: the format, a producer pattern if given, and phrases the
  first three pages must contain (`contains`) and must not (`lacks`),
  ignoring case and spacing. A document that matches one declaration is
  read by it; none, or more than one, is read by the general reader as a
  new layout, and an overlap is logged.
- **Checked by**: each declaration says how its documents prove they were
  read whole (`balances`, `totals`, `sum`). A document recognized as the
  layout and not proven one of those ways is refused -- it was misread, or
  the bank changed the layout -- and the preview says so.
- **The first two**: `us-consolidated-checking-a` (checked by balances,
  or by totals in a month with activity on one day only) and
  `us-cardholder-pdf-a` (checked by its sum). Neither changes a
  pattern: what they add is recognition and the check they are held to.
  Checked locally on the treasurer's real files: all 78 account-months of
  the 26 consolidated statements and all 12 cardholder PDFs are recognized,
  proven their declared way, and ready to import, and none is a sighting.
  That check changed both: the cardholder PDFs' total is past the third page on
  a busy month, so they are recognized by the box at the top instead; and
  two quiet months balance by totals, not daily balances.
- **Fixtures**: `pdfsourcetest.Fixtures` names each drawn document. A
  test reads every built-in declaration's fixture, which it alone must
  recognize, into the rows the general reader reads; another imports it
  through the ledger and finds it proven its declared way.

## 2. Declaring a shape

A declaration is data, not code: what the reader already decides in Go,
said for one layout. One JSON file per shape (`encoding/json`, nothing to
add; TOML and YAML would each be a dependency for a nicer syntax), with a
`notes` field for the prose a reviewer needs. The general reader's
behavior becomes the default declaration, which every other one adjusts.

What a declaration says, from what the readers already distinguish:

- **identity**: an id, a human name, the date it was first seen, a version.
- **match**: the format; a producer pattern; phrases the first pages must
  contain, and phrases they must not; for CSV, header fingerprints.
- **period**: the pattern of the period line.
- **accounts**: the label that starts an account's part ("Account
  number"), how its number is cut to the last four digits, and its
  opening and closing labels.
- **sections**: each heading's pattern, and what the section is: rows of
  money in, rows of money out, **checks**, **daily balances**, or nothing
  to read.
- **rows**: date forms; which of two dates is the posting date; the
  columns between the date and the amount (description, cardholder,
  reference); continuation rules.
- **checks**: where the number is, which date is the date paid, and how
  the row is named ("Check {number}") -- and that the number is kept as
  the transaction's check number (3).
- **signs**: as printed, from the section, from the balances, or turned
  round for a card.
- **checks of the whole**: which totals and balances the document states,
  and so how it is verified.
- **noise**: lines to drop beyond the page furniture, such as the marks
  some banks hide in the text.
- **fixture**: every built-in declaration names a test that draws an
  invented document of its shape (`pdfsourcetest` already draws one) and
  reads it with the declaration. A declaration without one is not merged.

**Built-in declarations** live in `business/domain/importing/shapes/`,
embedded, and change by pull request with their fixture. **Drafts** are
the administrator's: pasted or edited on the administrator's page, stored
on the site, and tried on a file of the administrator's own, side by side
with what the built-in declaration or the general reader makes of it. A
draft that reads its files correctly is copied out as the text of a pull
request. A draft never imports anything, the administrator's included.

## 3. Checks and their images

A check is a transaction with a **number**. Today the number survives only
inside a description ("Check 1176"). It becomes a field of its own:

- **Read** from a declaration's checks section, an OFX `CHECKNUM`, or a
  CSV column mapped as the check number; kept on the transaction
  (`transactions.check_number`, a later column) and shown beside it.
- **The image** of the check is a receipt of a new kind, "check image",
  uploaded one check at a time, front and back as pages of one receipt.
  Typed or read from its file name, its number matches it to the account's
  transaction with that number; it then fills in what the bank never says
  -- to whom the check was written -- as the transaction's payee note,
  which sorting rules and suggestions can then learn from.
- **The accountant's package** gains a "Check" column, and the images in
  a `checks/` folder named by number, date and amount, since the
  accountant has no access to the bank's site.

A PDF holding many checks, cut into one image per check, is a later
source; matching by number is the same.

### As built (the third pull request)

- **The number** is kept as its digits without the leading zeros some
  banks pad it with (`importbus.CheckNumber`), so that "0001176" in one
  file and "1176" in another are one check; anything else in the column
  -- a deposit slip's reference -- is no check number. It is no part of a
  transaction's identity.
- **Read** from a statement's checks paid (the general reader's, and so
  every declaration's), OFX's `CHECKNUM`, and a CSV column found by its
  heading ("Check number", "Check or Slip #", "Cheque", "Número do
  cheque" and the like, never "Number" alone, which is an account's as
  often) or chosen on the preview. A pending charge that posts keeps its
  number if the posted row has none.
- **Shown** on the transaction's page, and beside it in the month and the
  preview when its description does not already say it: a statement
  calls the row "Check 1176", and a CSV may call it "CHECK".
- **The package** has it as its last column, "Check number", so that a
  sheet an accountant set up for the earlier columns still finds them.
- Checked locally on the treasurer's statements: all 133 checks paid in
  26 months carry their number, and no other row has one.
- The rename asked for along with it: the second declaration is
  `us-cardholder-pdf-a`, "cardholder PDF" being what these are called.

### As built (the fourth pull request)

- **Uploaded** on a checking account's receipts page, under "Check
  images": one check at a time, its sides as pages with its number typed,
  or several at once, each file named after its check ("1176-front.jpg",
  "cheque 1176 verso.png"). A name with any word but the check's and its
  side's -- a phone's "IMG_4521" -- gives no number, since its number is
  the photo's (`receiptbus.NumberFromName`).
- **Matched as it is added**: the account's one transaction with the
  number, whose date and amount the receipt takes. It is the one receipt
  attached without a person choosing, because nothing is left to choose.
  A number with no transaction, or with two, is refused and nothing of the
  upload is added: the check has not cleared or its statement is not
  imported, and the image is better added after.
- **Paid to**: typed with one check, or later on its page as the receipt's
  shop, it is written on the transaction (`transactions.payee`, a later
  column, with a line of history), shown beside it in the month, the
  sorting page and its own page, and read by sorting rules and
  suggestions before the description (`Transaction.Words`). Whoever may
  attach receipts to the account may write it; it is a note, not money,
  so a reconciled period does not stop it.
- **The package**: each side in `checks/` as
  `number_date_amount[_payee]_n.ext`, and a "Paid to" column after "Check
  number".

### As built (the fifth pull request)

- **Where**: "Layout drafts" on `/admin`, and `/admin/drafts/…`; like the
  rest of that page, a 404 for anybody but the site administrator.
- **Started** blank, from a layout seen ("Start a draft description of
  this layout", with that layout's plain words as the phrases to match and
  its signature in the notes), or from a built-in declaration with its
  version counted on, to revise it (`shapebus.Skeleton`, `Format`).
- **Kept as typed**, whatever is wrong with it (`shape_drafts`): it is
  being written. What `shapes.Parse` says of it is shown as technical
  detail, in the parser's English, since the reader is writing JSON for
  the code; a draft is tried only once nothing is wrong.
- **Tried, not used.** A PDF of the administrator's is read in memory and
  forgotten -- never stored, never imported (`shapebus.Try`). It is read
  by the draft, whether or not the draft's phrases recognize it, so that
  its patterns can be tried while the phrases are put right; and as it is
  read now, by the built-in declaration that recognizes it or by the
  general reader. Each account's part says how it balanced
  (`ledgerbus.Verify`, the import's own check) and whether it would be
  imported -- by the draft, only if it balanced a way its `checked_by`
  allows -- with its first rows, and the page says whether the two
  readings found the same rows.
- **Copied out** as the file a pull request adds, under its id, in the
  built-in files' order and with only the patterns it changes. The pull
  request still needs its drawn fixture.

## 4. Files to accounts, in bulk

With the shape known and the account's number read, a file can say which
account it belongs to:

- **Read from the file**: a declaration's account label (last four digits),
  an OFX `ACCTID` (last four), a CSV's account column when it has one. A
  consolidated statement belongs to several accounts at once, one part each.
- **Remembered**: when a shape carries no number -- most CSV exports --
  the account a person chose for that shape and header fingerprint last
  time.
- **Asked**: otherwise.

A bulk import takes many files at once, proposes each one's account (or
each part's), and shows them as a list: file, shape, account, period, and
whether it balances. Each still goes through its own preview's checks and
count rules; those that balance are imported together, and the rest are
left on the list for a person, one by one. Only accounts the person keeps
the books of are proposed.

### As built (the sixth pull request)

- **Where**: `/imports`, linked from the front page and from each
  account's "Add a statement". Up to 40 files at once, each as large as a
  statement may be.
- **Proposed**, for each file or each account's part of one, among the
  accounts the person keeps the books of (`ledgerbus.Propose`): by the
  last four digits it prints (a PDF's account number, now kept for a file
  of one account too, or an OFX `ACCTID`); else the account a file in its
  layout went to before -- for a CSV, the account whose columns were
  chosen for its header, which every CSV ever imported has recorded; for
  anything else, a statement in its layout (`statements.shape`, a later
  column). Two accounts that fit is no proposal: the person is asked. A
  remembered account whose own number differs from the file's is not
  proposed.
- **Read** for its account as its own preview reads it, and shown with its
  period, its count of transactions and new ones, and how it checked.
- **Imported together** only when nothing in it needs a person: it checks
  by the file's own figures, and the count rule set nothing aside, no row
  was skipped or left unplaced, and no row waits to be told its
  cardholder (`Proposal.Unattended`). Each goes through the same `Import`
  as a single file. Everything else stays on the list with a link to its
  own preview, which asks what it needs.
- **The list keeps no state**: its files and the accounts chosen are in
  its address, and anybody else's files are left off it.

## Naming (decided 2026-10-09)

Built-in declarations are **named generically** ("us-consolidated-checking-a"),
not after their bank, as the rest of this repository keeps every bank and
card issuer out of its files. A declaration recognizes its documents by
the words of their layout, never by the bank's name.

## Build order

After pending charges, six pull requests:

1. **Shapes, recognized**: the general reader's choices gathered into one
   declaration type with its defaults (no change in behavior), each import
   recognized, sightings stored and listed, the preview saying which, and
   the new-shape rule for PDFs.
2. **Declarations**: the JSON form, the matcher, and the first built-in
   declarations -- the consolidated checking statement and the card's
   printed activity -- each with its drawn fixture.
3. **Check numbers**: read from declarations, OFX and CSV; stored, shown,
   and in the package.
4. **Check images**: the receipt kind, matching by number, the payee note,
   and the package's `checks/` folder.
5. **Drafts**: the administrator's page for trying a declaration on their
   own file beside the built-in reading, and copying it out.
6. **Bulk import**: many files, each to its account or accounts, with the
   list and its per-file previews.

## Verification

- Every built-in declaration's fixture, read with it, balances; read with
  the general reader, the same rows.
- A fixture changed to a new layout (a heading renamed) is no longer
  recognized: it is new, it is still imported when it balances, refused
  when it does not, and its sighting records no number or amount.
- Check images match by number, refuse a number with no transaction, and
  appear in the package.
- A bulk import of a consolidated statement and two CSVs proposes the
  right accounts, imports what balances, and leaves the rest.
- A stranger, a viewer and a contributor get nothing from any new route;
  only the site administrator sees sightings and drafts.
