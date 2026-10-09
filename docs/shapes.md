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
on the site, and used only when the administrator imports a file of their
own with that draft chosen, side by side with what the built-in
declaration or the general reader makes of it. A draft that reads its
files correctly is copied out as the text of a pull request. A draft is
never used for anybody else's import.

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

## Open question

Built-in declarations describe real banks' layouts, which are public, in a
public repository. Naming a declaration after its bank says nothing about
who uses it, but this repository has so far kept every issuer out of its
fixtures and messages. **Should built-in declarations be named for their
bank ("chase-consolidated-checking-2026"), or generically
("us-consolidated-checking-a")?**

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
