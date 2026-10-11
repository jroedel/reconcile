# The same money twice, and two coffees on one day

A plan for step 9 (`docs/plan.md`, build order). The import's worst failure
is not a file it cannot read, which it says, but a charge it stores twice
or a charge it drops, which it does not say. Both come from deciding
whether a row of a new file is one the account already has. This plan
makes that decision checkable, tests it as a property of every reader, and
adds the check that the exact rules cannot make.

## The two things that must both hold

1. **Two identical charges in one file are two charges.** A coffee in the
   morning and a coffee in the afternoon, at one shop, for one amount, with
   no bank identifier, are indistinguishable rows. Dropping one is the
   worse error: a doubled row shows in a total, a vanished one shows
   nowhere.
2. **One charge in two files is one charge.** Two downloads of one account
   whose date ranges overlap, or one of which contains the other, a CSV
   after an OFX, a statement after a printed page: every charge on the
   days they share must be stored once.

## Where things stand

Lifted from eumaeus (`/opt/projects/eumaeus`), where each rule was paid for
on a real ledger:

- **A bank's identifier** (OFX's FITID) is the identity when there is one.
  An OFX row whose content matches a row imported from a CSV adopts it: the
  stored row gets the identifier rather than a twin.
- **Otherwise, the content and its occurrence in the file.** Rows alike in
  day, description and amount are numbered within one file, and the
  number is part of the identity (`ledgerbus.hash`). Two coffees in one
  download are coffee 1 and coffee 2, and the same two in a wider download
  are coffee 1 and coffee 2 again, so neither is lost and neither is
  doubled. eumaeus measured it on five real overlapping exports of one
  card, where 378 transactions were right: content alone kept 377 (a
  coffee lost), the line number in the file gave 392 (every overlap
  doubled), and counting occurrences gave 378. A later download that has a
  third coffee adds the third, and only the third.
- **The same file twice** is refused by its SHA-256 before any row is
  looked at (`ErrSameFile`).
- **A description cut short** with an ellipsis matches the whole one on
  the same day for the same amount (`ledgerbus.Truncated`, from the PDF
  work).
- **The preview is the import, rolled back** (`Storer.Import` with commit
  false), so the counts it shows are the import's by construction. eumaeus
  learned that a preview which disagrees with the import is worse than
  none.

## The hole that is left

Every rule above is exact, and the description is part of the key. A bank
that words one transaction two ways makes two rows nothing joins. eumaeus
found this on its live ledger: an interest payment worded one way in the
bank's OFX and with the period appended in its PDF statement, so every
month imported from both carried the interest twice. The same happens
between a pending description and a posted one, and between a CSV and a
PDF of the same month.

It cannot be closed by dropping the description from the key: that would
merge the two coffees, which breaks the first rule to keep the second.
eumaeus' answer was a report after the fact (`ledgerbus/duplicate.go`):
rows of one account, day and amount where one description appears whole
inside the other, at least six characters of it. It measured two things
first that this plan keeps:

- Of the clusters in its ledger that shared an account, a day and an
  amount, most were unrelated charges that happened to collide, and the
  clusters of identical rows from **one** import were all real (several
  returned-deposit fees on one day). What marked a doubled row was two
  wordings from **two** imports.
- Widening the day to a week either side found nothing new. A shared
  prefix was rejected: two parcels from one online shop share sixteen
  leading characters.

## What this adds

### 1. The count rule, at import

Before any row is stored, for each day and amount the new file has rows
on, compare what the file lists with what the account already holds from
**other** statements:

> On a day both cover, the account should end with as many charges of an
> amount as the file lists or as it had, whichever is more -- not the sum.

So with F rows of a day and amount in the file, S already stored from
other statements, and N of the file's rows that the exact rules would add,
importing all N would leave S + N; when that is more than max(F, S), the
difference is rows that are probably already here under another wording.

- **Two coffees in one file, nothing stored:** F 2, S 0, N 2. Both added.
- **The same two in a wider download:** the exact rules find both. N 0.
- **The same two, worded differently** (a CSV, then the PDF): F 2, S 2,
  N 2, so 4 would be more than 2. Both are set aside as probably already
  here.
- **A later download with a third coffee, worded differently:** F 3, S 2,
  N 3. One of the three is new; the other two are set aside.
- **Several returned-deposit fees on one day, in one file:** they are
  counted together in F and against nothing of their own, so all are
  kept.

It needs no description at all, which is what lets it catch wordings
nobody has met yet, and it never merges two rows of one file, which is
what keeps the coffees.

**What the person sees.** The preview lists what was set aside, by day
and amount, beside what the account already has: "This file lists 2
charges of $3.50 on 4 September; the account has 2 already, as
STARBUCKS 1234 and STARBUCKS STORE 1234. They are left out as already
here." Each can be ticked to import anyway. Where only some of a group are
new (the third coffee), the ones whose descriptions are least like any
stored row's are ticked to import, and the person can change which.

Nothing is deleted or changed. What is set aside is counted with Already,
and the history line says how many were set aside this way.

**Remembering the answer.** When rows are set aside, the new wording is
kept as another name for the stored row it matched (`transaction_aliases`:
the account, the wording's content hash, the transaction), and a row with
a bank identifier gives it to the stored row, as an OFX row adopting a CSV
row does now. The next file in the same format then matches exactly,
without asking.

**Two holders** (issue #81, `docs/clearing.md`, 5). On an account
that does not keep its holders apart, the rule counts every holder's rows
together, and a row of one holder's file can be paired with another
holder's stored charge. The rule takes them for one only by ignoring whose
the files say they are, so such a doubt names both holders, and in a file
whose rows all name one holder it is ticked to import rather than to leave
out. The exact rules no longer match across two named holders at all: that
was where a charge was lost without being asked about.

**Why set aside rather than import.** Both mistakes are caught when the
month is reconciled, since the stored rows no longer come to the
statement's closing balance. But a doubled charge also misleads every
total until then, and the count rule is right far more often than it is
wrong, so the default is to leave the row out and show it.

### 2. Idempotence, as a tripwire

The preview imports the file and rolls it back. It will also import the
same rows a second time in that transaction, and expect nothing added and
nothing set aside. Anything else means a rule depends on something it
should not -- a row's position, the time, the order of a map -- and the
file is refused with "This file could not be imported safely" and a log
line for the developer. It costs one more pass over rows already in
memory, and it should never fire.

### 3. Idempotence, as a test of every reader

For every statement fixture, CSV, OFX and PDF:

- importing it twice adds nothing the second time;
- importing it with its rows in another order adds nothing;
- importing a download of a wider period, then a narrower one inside it,
  and the other way round, gives the same rows;
- the two coffees: identical rows in one file are all kept, through each
  of the above;
- a third identical charge in a later, wider download is added, alone;
- the same month worded differently (a fixture pair invented for it) sets
  aside every row, and importing the pair the other way round does the
  same;
- a pending row posted later under a longer description is set aside.

### 4. Possible duplicates already in an account

Statements imported before this, or between two formats the count rule
does not see (below), may hold a charge twice. The account's months page
lists, for each month, eumaeus' clusters that came from two statements:
one account, one day, one amount, one description inside the other by six
characters or more. Each shows the rows and the statements they came
from. A bookkeeper may remove one row of a cluster as a duplicate of
another, with the history recording both; the store refuses the removal
of a row that has no twin left in the cluster, inside the DELETE itself,
so that no caller can erase the only record of money that moved. Outside
a reconciled period only.

## What this does not catch

- **An amount that changes between files:** a restaurant's pending charge
  before the tip and its posted charge after. The count rule groups by
  amount. Reconciling the month catches it.
- **A day that changes between files:** the date of the sale in one and
  of the posting in the other. eumaeus' ledger had no such case within a
  week, and widening the day would group unrelated charges. Reconciling
  catches it; the readers prefer the posting date where a document prints
  both (`pdfsource`).
- **Two files of different accounts** are never compared: the rule is per
  account.

## Build order

Two pull requests:

1. **At import:** the count rule in `ledgerdb.Import` and its preview,
   the set-aside list with its choices on the preview page,
   `transaction_aliases` (with `Init`, `Expected` and an old-schema test),
   the idempotence tripwire, and the property tests over every fixture.
2. **After the fact:** possible duplicates on the months page, and
   removing one row of a cluster, with its history line and the stranger
   walk.

## Verification

- `ledgerdb` and `ledgerbus`: the cases in "What this adds", each as a
  test with invented rows; the tripwire fires on a deliberately unstable
  rule in a test; an alias makes the second file of a format match
  without setting anything aside.
- Through the site: import a CSV, then a PDF of the same month worded
  differently, and see every row set aside with its stored twin; tick one
  to import it; import a wider download with a third coffee and see it
  alone added.
- A stranger, a viewer and a contributor get nothing from the removal
  route.
