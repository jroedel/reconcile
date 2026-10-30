# Telling the accountant what needs explaining

A plan for step 9 (`docs/plan.md`, build order), issue #145. It comes from
one treasurer's month, said in general terms, because the app is for
anyone's:

> Three or four emails a day go to the accountants, each on a different
> subject: what a check was for, why money went to another account, whose
> payment a deposit passed on. If every justification were sent, it would
> be ten to thirty more a month. Most of the answers are already somewhere
> in the books -- a receipt, a description somebody wrote, a payee -- and
> the accountant cannot see which.

The app exists to save those emails. A bank statement says when, how much
and a few words of the bank's. For most charges that is enough: the
grocery store is the grocery store. For some it never is. A check says
nothing of what it paid for, and a transfer says nothing of why. This plan
lets everyone see which charges need explaining and whether somebody has
already explained them. It gives the accountant a place to ask about the
rest, and lets people hear about new activity without signing in to look.

## Principles

- **General.** Nothing names a house, an accountant or an account. Another
  treasurer, in another country, uses the same pages.
- **Every list comes from what is stored.** A person or their Claude writes
  a description, attaches a receipt, names a payee. The app reads those
  facts and the statement's. It never guesses a quality nobody stored.
- **No score and no heuristics.** No "this payee looks opaque", no "this
  amount looks large". A rule that is right for one organization is
  wrong for the next, and a score nobody can explain is one nobody trusts.
- **As few new fields as possible**, and **nothing that makes work for the
  accountant**. No "seen" mark and no "reviewed" button: a list the
  accountant must tick through is a second job, not a saved email.

## Decisions (from Q&A, 2026-10-29 and 2026-10-30)

| Topic | Decision |
|---|---|
| The need | **General**, as above |
| Important | **Four stored facts**, any one of which is enough (1). Nothing else |
| Account for reference | **A link by an explanation** (decided 2026-10-30). A transaction in an ordinary account is important when an explanation joins it to a transaction in an account for reference, in either direction: the money is explained from outside these books. A transaction *in* an account for reference is never to do (2), as nothing there is ever to sort (`docs/plan.md`, "Whose books") |
| Context | **Anything we gave the accountant that the bank did not** (2). The bank's description is not context, nor a category a rule chose |
| The two lists | **"More context needed"** (yellow) and **"Context added"** (green). The second was "Context augmented" in the issues; renamed on 2026-10-30 because "augmented" is not a plain word and translates poorly, and "context added" is the definition's own name |
| Colour | **Never alone.** Each list says its words wherever its colour is shown (`docs/design.md`) |
| Questions | **On a transaction, a statement, a project or a payee**, never on an account or an organization (4). A question about an account is asked on its most relevant transaction or statement |
| Who asks | **Whoever holds Note** on the thing: accountants, contributors, bookkeepers and owners. Reading follows the thing's reading |
| Mail | **Weekly, and only when there is something.** No email the moment a question is asked: an urgent one goes by the person's own email, with the question's link pasted in |
| Stopping the email | **One tap, confirmed by a button.** A link that stopped the email on opening would be opened by the scanners some mail systems run over every link in a message, and would stop it for people who never asked |
| Time zone | **The person's own**, from the browser at the first sign-in, editable on their profile |
| Scheduling | **A loop in the server**, every ten minutes, as old sign-in codes are pruned. `foundation/jobs` takes slow work off a request and keeps nothing between restarts; it is not a scheduler |

## 1. Important to the accountant

A transaction is **important** when the statement alone cannot say what
it was for. That is when any of these is true:

- **It has a check number.** A check says whom at most, never what for.
- **A part of it is in a category whose kind is transfer or pass-through**
  (`docs/plan.md`, "Kinds of money"). Money between our own accounts, and
  money held for somebody else, are exactly what an accountant asks about.
- **An explanation joins it to a transaction in an account for reference**
  (`docs/clearing.md`, "Explain this amount"). The money's other side is
  kept somewhere these books only mirror.
- **Its payee is marked "report this payee's payments"** (`docs/payees.md`,
  3). Somebody already said the accountant must see these.

Each is a column the ledger already has (`transactions.check_number`,
`categories.kind`, `explanation_lines` with `accounts.reference`,
`payees.reported`). No new column is needed for this list.

## 2. Context added

A transaction has **context added** when we have given the accountant
something the bank did not. Any one of these:

- a **description of its own** (`own_description`; an import never writes
  one);
- a **receipt** attached, which includes the image of a check;
- a check's **memo** (`check_memo`);
- an **explanation** that is settled: its lines add up, or it was accepted
  with a note;
- a **project** on any part;
- a **memo** on any part;
- a **payee**;
- a **question about it that has been answered** (4).

Not counted: the bank's own description, however complete, and a
category a sorting rule chose. A rule's category is the app's guess, made
from the same few words the accountant already reads on the statement. A
category a person chose is not counted either: it says what kind of
expense the money was, not what it was for.

Like the first list, this is read from what is stored, when a list is
asked for. Nothing is written when a receipt is attached or a payee named,
so nothing can fall out of step.

## 3. The two lists

| | Important, no context added | Important, context added |
|---|---|---|
| Name | **More context needed** | **Context added** |
| Colour | Yellow, as the app's other notices of something to do | Green, as its other notices that something is done |
| Whose | The bookkeeper's to do | What the accountant reads |

A transaction that is not important is in neither list. A transaction in
an account for reference is never in the first list, and is in the second
only when that account is chosen, as everywhere an organization's
transactions are listed (`docs/explorer.md`). A pending charge that has
since posted is in neither: the posted charge is the one that counts
(`docs/clearing.md`, 4).

**They are a choice of the transaction search**, not a search of their
own (`docs/explorer.md`). The search gains one choice, "For the accountant":
anything, more context needed, or context added. It sits beside "To check"
on an account's, an organization's and a project's lists, and combines with
every other choice as they combine with each other. Like the other choices
it is judged in Go, part by part, from the facts the read brings back. The
read gains three `EXISTS` for the facts it did not already bring (the
payee's mark, the explanation's state, the link to an account for
reference). Answered questions belong to another domain, so main hands the
ledger a function that says which transactions have one, as it hands it
the receipts' counts.

**Each row says why it is listed.** In both lists, a line under the
description gives the reasons it is important ("a check", "a transfer"),
and in the green list what was added ("a receipt", "a description"). That
is what the accountant reads the row for. The rows carry the reasons as
codes, and the template says them, each its own string.

### The page: "For the accountant"

`/orgs/{id}/accountant`, linked from the organization's page. Anyone who
can read the organization sees it, and it shows nothing a reader of the
same transactions could not already find in the search.

```
For the accountant
Checks, transfers and pass-through are what a bank statement
cannot explain on its own.

More context needed (12)    Context added (30)    Questions (2 open)
━━━━━━━━━━━━━━━━━━━━━━━━━━━  a yellow rule under the current tab
Months: Aug  Sep  Oct            Account ▾   Project ▾   [Show]

Oct 3   Check 1322                               −$400.00
        a check · no receipt yet
Oct 7   Transfer to savings                    −$2,000.00
        a transfer
```

- **Three tabs, each a link**, with no script. The first two are the lists,
  and the third is the organization's questions (4). Each tab says its
  count and its words.
- **Which tab opens first depends on the reader**: "More context needed"
  for somebody who may keep the books, "Context added" for everybody else.
  The address says which, so a link one person sends another opens the
  same tab.
- **The current tab has a rule under it** in its list's colour. That rule,
  about four lines of stylesheet, is all the new CSS the lists need; the
  colours are the app's own (`--warn-*` and `--ok-*`, already in light and
  dark).
- **Month, account and project** are the search's own controls, and the
  rows are the search's own rows, a hundred to a page, newest first.
- **When a list is empty**, it says what to do next:
  - yellow: "Nothing needs more context. Every check, transfer and
    pass-through has what the accountant needs." and a link to the green
    tab;
  - green: "Nothing is ready for the accountant yet. A transaction is ready
    once it has a description, a receipt, whom it was paid to, or a
    project." and a link to the yellow one.
- **The organization's page** has a card "For the accountant": a sentence,
  and a link to each list. It shows no counts. That page is opened all the
  time, and counting an organization's whole history takes about a second
  at fifty thousand transactions (`docs/explorer.md`, "Storage"). The
  counts are on the tabs.
- **A transaction's page** says, when it is important, which list it is in.
  Yellow gets one sentence per reason ("This is a check, so the accountant
  needs to know what it was for."), and, for whoever can act on it, "Add a
  description, a receipt or whom it was paid to." Green gets "Ready for the
  accountant." A transaction that is not important shows nothing. It is one
  line, so the volunteer who opened the page to attach a receipt has no more
  to read above it than before.

## 4. Questions

A **question** is a message somebody writes on a thing, which somebody else
answers. The accountant needs somewhere to ask "what was check 1322 for?"
that is not another email, and the bookkeeper needs somewhere to answer it
that is not another email either.

- **On one thing**: a transaction, a statement, a project or a payee.
- **A thread**: the question and its replies, oldest first, each with who
  wrote it, the role they hold there, and when.
- **Open or answered.** A question is answered by a reply from anybody but
  the person who asked, or when the asker marks it answered. It is never
  reopened; a new question is a new thread.
- **Plain text**, at most 2,000 characters, kept as written. No markup is
  accepted, and no address in it becomes a link.
- **Who**: asking, replying and marking answered take Note on the thing;
  reading takes reading it. A role on a project alone reaches a question
  about a transaction through that project's parts of it, as it reaches
  the transaction's receipts. Everybody else is told the question is not
  there (`ErrNotFound`), as for anything else they were not given.
- **Its own link**, `/questions/{id}`, readable only after signing in. It
  shows the thread and what it is about. Somebody with an urgent question
  pastes it into their own email, so the page shows the link in a box that
  can be selected or long-pressed on a phone, not only in the address bar.
- **What it is about shows only what the reader may see.** For a role on a
  project alone, that is the project's part of a charge, never the rest,
  and no link to a transaction page they cannot open.
- **In the history**, in the same SQL transaction, with `via` set when it
  came through a key.
- **An answered question is context added** for its transaction (2).
- **When a statement is removed**, the questions on its transactions, and
  on it, go with them, as receipt links do.

```
Back to Check 1322
Question                                              Open

  About: Oct 3 · Check 1322 · −$400.00 · Checking
  See the transaction

Ana, accountant · Oct 28
  What was check 1322 for?
Marta, bookkeeper · Oct 29
  The plumber, for the leak in the hall. The receipt is attached.

  [ reply                                    ]
  [Send reply]   [Mark answered]   (the second for the asker only)

Link to this question: [ https://…/questions/… ]
```

**On the thing's own page**, a "Questions" card at the bottom lists each
open thread in one line (its first line, who asked, and a link). Under it
is "Ask a question about this", folded, so the page does not grow for
somebody who came for something else. An open question is also mentioned
in one line at the top, so somebody following the weekly email's link sees
it first.

**The organization's questions** are the third tab of "For the
accountant": open or answered, and those you asked. Each row says what it
is about, its first line, who asked, and how long it has waited. When there
are none: "No open questions. Ask one from any transaction, statement,
project or payee."

**One verb through each flow**: "Ask a question" says "Question asked.",
"Send reply" says "Reply sent.", and "Mark answered" says "Marked answered."

**Storage**: `question_messages(id, scope_kind, scope_id, transaction_id,
statement_id, parent_id, author_id, body, state, created_at)`, STRICT, a
domain of its own (`questionbus`, `questiondb`). The thread keeps its own
kind of thing rather than widening `types.Scope`, which means "something a
role is given on". `transaction_id` and `statement_id` are set for those
two kinds and reference their tables `ON DELETE CASCADE`, which is how a
removed statement takes its questions.

## 5. The weekly email

Once a week, at the day and hour each person chose, an email of what was
added that they can now look at, with links. **No activity, no email.**

### Settings

On the profile page, a card "Weekly email". The person reads "weekly
email", never "digest".

```
Weekly email
[x] Send me a weekly email of what is new
Day  [Monday ▾]        Hour  [08:00 ▾]
Time zone  [America/New_York        ]
The next one is on Monday 3 November at 08:00, your time.
[Save]
```

- **On or off.** With no choice made, it is on for anybody who is an
  accountant, a bookkeeper or an owner of something, and off for everybody
  else.
- **Day and hour**, Monday at 08:00 unless chosen. Hours are written
  08:00, the same in all three languages.
- **Time zone**, an IANA name in `users.tz`, a later column. A box with a
  list of names to type into needs no script. A small script served by
  the app (the pages allow no script written into them) fills it from the
  browser at the first sign-in, and offers the browser's on the profile
  page when it is empty. Without script, it is UTC.
- **The sentence saying when the next one comes** is the most useful line
  on the card. It is how a wrong time zone is noticed. When the email is
  off, it says "You get no weekly email."
- **Language** is the person's own, as for every message.

Stored in `notification_prefs(user_id, digest_enabled, digest_weekday,
digest_hour, sent_up_to, unsubscribe_hash)`. A person with no row has the
defaults.

### What it says

Built from the history (`events`) and the questions since the person's
last email, over what they may read, **leaving out what they did
themselves**:

- **Where they are an accountant**: important transactions that gained
  context since the last email (they moved into "context added"), new
  answers to their questions, and questions waiting on them.
- **Where they keep the books or own it**: how many transactions still
  need more context, and questions waiting for their answer.
- **A statement imported, alone, is not a line.** The accountant already
  has the statement.

**Links only**: counts and links, never an amount, a payee or an image.
Every link goes to a page that asks for sign-in. It is plain text, as
every message the app sends is:

```
Subject: What is new this week, to 3 November

The house
4 transactions now have what the accountant needs:
https://…/orgs/…/accountant?context=added
1 question was answered:
https://…/questions/…

You get this email every Monday at 08:00 (America/New_York).
Change the day or the hour: https://…/account#weekly
Stop it: https://…/digest/stop?t=…
```

### How it is sent

- **A loop** in the server, shaped like the one that prunes old sign-in
  codes, sends to each person whose day and hour have come in their own
  time zone.
- **A cursor per person** (`sent_up_to`), so nothing is missed or repeated
  when a run is skipped or the server restarts. Somebody who joins midweek
  starts from the moment they joined, and gets no flood. Taking a week to
  send is one statement (`UPDATE … WHERE sent_up_to IS ?`), never a read
  and then a write. The cursor moves when there is nothing to say, too.
- **Stopping it**: the link opens a page with one sentence and a button,
  "Stop the weekly email", which posts. That is one tap more than the link,
  and that tap is what the scanners do not make. The message also carries
  `List-Unsubscribe` and `List-Unsubscribe-Post` headers, so a mail program
  that offers its own unsubscribe button stops it in one tap (RFC 8058);
  `foundation/mail` gains headers for it. Each email has a token of its
  own, and only its hash is kept. A link from an older email leads to the
  profile page after signing in.

## 6. Through the API

So that a person's Claude can work the lists with them (`docs/books-api.md`):

| Endpoint | Tool | Scope |
|---|---|---|
| `GET /api/v1/transactions?context=needed\|added` | `find_transactions` | `books:read`. The search's choice, with each row's `why` and `added` |
| `GET /api/v1/questions` | `list_questions` | `books:read`. On one thing, or the open ones in an organization, each with its `url` |
| `POST /api/v1/questions` | `ask_question` | `books:write`, and Note on the thing |
| `POST /api/v1/questions/{id}/replies` | `reply_to_question` | `books:write`, and Note |
| `POST /api/v1/questions/{id}/answered` | `mark_question_answered` | `books:write`, and Note |
| `GET /api/v1/me/weekly-email` | `get_weekly_email_settings` | `books:read`. The key's own person's, read only |

As everything done through a key, what Claude writes is marked "through
Claude" in the history and listed by `list_changes`. Claude's guide
(`mcpapp/guide.md`) learns the two lists in the pages' own words:
yellow is ours to do, green is ready for the accountant.

## Left out, on purpose

- **A flag across organizations.** Organizations as the books somebody
  keeps, and accounts for reference, already say it.
- **A seen or reviewed mark.** It is work for the accountant.
- **Notes beside questions.** A description of its own, a part's memo and
  a payee's note already exist; a fourth place to write would be a fourth
  place to look.
- **Email the moment a question is asked.** The weekly email, and the
  question's link pasted into the person's own email when it cannot wait.

## Build order

Seven pull requests after this one, each useful when merged:

1. **The read** (1, 2, 3): the search's choice, the reasons on each row,
   the function main hands the ledger for answered questions (nothing yet),
   and the choice in `find_transactions`. Nothing on a page changes yet.
2. **The page** (3): "For the accountant" with its two lists, the
   organization page's card, the transaction page's line, and the choice
   in the search's form.
3. **Questions, the books** (4): `questionbus`, `questiondb`, the history,
   and answered questions counted as context.
4. **Questions, the pages** (4): the thread and its link, the card on each
   thing's page, and the third tab.
5. **The weekly email's settings** (5): `users.tz`, `notification_prefs`, the
   profile's card, and the script for the time zone.
6. **The weekly email** (5): what it says, the loop that sends it, and stopping it.
7. **Through the API** (6): the question endpoints, the settings, and
   Claude's guide.

Then the tests that cross all of it (Verification), and the translations.

## As built

### The read (#153)

- **`ledgerbus/accountant.go`**: the reasons and the kinds of context as
  codes, judged in Go beside the search's other choices
  (`Criteria.Accountant`). The store reads three facts beside the
  transactions only when a list is asked for (`ledgerdb/explore.go`,
  `factsQuery`).
- **A payment to a reported payee is always in the green list**: naming
  its payee is itself context. Its reasons still say "reported".
- **A difference accepted without a note is not context**: it says only
  that the difference was let go.

### The page

- **The rows come first**, then the search's other choices, closed: the
  tab is the page's, not a choice made in the form, and on a phone the
  form is a screen of its own.
- **A yellow row says only why it is important**, not every kind of
  context it lacks: a row in that list lacks them all, and eight "no ..."
  under each row would bury the one word that matters.
- **The months offered are the six latest**, counted from today, and
  "Every month"; any other is a day or two in the form.
- **The two counts** are the same search twice, once for each list.
- **A transaction's page** says which list it is in through
  `ledgerbus.AccountingOf`, the same judgement on the one transaction.

### Questions, the books

- **Two tables, not one of messages**: `questions` (what it is about,
  where its history goes, whether it is answered, and the references
  that go with a removed statement) and `question_replies` (text and who
  wrote it). Only the question has what a thread has.
- **Home** is kept with each question: the account for a transaction or
  a statement, the project, or the payee list's owner. It is where the
  line of history is written, and where an organization's list of
  questions will start from.
- **Marking answered is the asker's alone.** Anybody else answers by
  replying, which does it in the same SQL statement that finds the
  question open, so that two replies at once answer it once. A reply
  after the answer is a reply.
- **The history** says "asked a question about ...", "replied to ...",
  "answered ...", naming the thing, never the question's words: a line
  of history is read by everyone who reads the account, and the words
  are the thread's.
### Questions, the pages

- **`questionapp`** has the question's own page and the forms that ask,
  reply and mark answered. The card on a thing's page and the line at its
  top are partials in `page/assets/app/base.html`, shared by `ledgerapp`
  (a transaction, a statement), `tenancyapp` (a project) and `payeeapp`
  (a payee) from the `questionbus.Summary` each asks for: a partial of
  any one app would have been copied into three, and no app imports
  another.
- **The third tab** is on `ledgerapp`'s "For the accountant", from
  `questionbus.InOrg`: open or answered, everybody's or yours.
- **A role on a project alone** reads a question about a charge as the
  project's part of it, the project named, with no link to the
  transaction (`questionbus.Thread`, `tenancybus.AccountNamed` for the
  account's name and currency and nothing more).
- **Dates are shown as days** until people have a time zone (5).

### The weekly email's settings

- **On by default for everybody**, not only for accountants, bookkeepers
  and owners as planned: an email with nothing in it is never sent, and
  only those roles' lines ever have anything in them, so the default
  reaches exactly those people without the user domain asking tenancy
  which roles somebody holds. The card's sentence says "if there is
  anything new for you".
- **`users.tz`** is a later column, written only by `SetZone`, in one
  statement: `UpdateUser` leaves it alone, so that saving a name never
  undoes a zone the sign-in page set a moment before.
  `notification_prefs` has the cursor and the token's hash already, for
  the email (6), so the table has one history.
- **The browser's zone** comes from `authapp/static/tz.mjs`, a served
  module (the pages allow no script written into them): every sign-in
  form carries it in a hidden field, kept only when the person has none;
  the profile offers it when the field is empty. Checked in Chrome by
  `make test-browser`.
- **The zone field suggests** a list of zones of the Americas, Europe and
  Africa; any IANA name is taken, since Go knows them all and cannot list
  them. `time/tzdata` is in the binary.
- **When the next one comes** is said under the form, in the person's
  zone, or in UTC with "UTC" when they have none yet.

### What the weekly email says

- **Remembered, not read from the history.** Attaching a receipt writes
  no line of history, on purpose, and the other ways of giving context
  name the transaction unevenly. So each email keeps what it saw: the
  transactions in each list for that person (`digest_seen`), and the next
  one's "new" is what is in a list now and was not then, however it got
  there. It is not a mark anybody sets; it is what the email told them,
  as its cursor is.
- **Organizations only**, as "For the accountant" is: an account with no
  organization has no lists page to link to.
- **Whose lines**: whoever may keep the books hears of transactions
  newly needing more context; an accountant who does not, of those newly
  ready; whoever may write, of questions others asked since and of
  answers others gave to theirs. A viewer hears nothing.
- **The first email only looks**: it sees the lists and says nothing of
  them, and looks back a week for questions, so that somebody given a
  role today hears of this week's and not a year's.

### Sending the weekly email

- **Every ten minutes, not every hour.** An hourly ticker drifts from the
  top of the hour, and the person chose an hour. A run with nothing due
  reads the users and their settings and nothing else, and taking a week
  is a claim, so running often never sends twice. There is no loop at all
  without a mail relay and a public address, so that cursors never move
  past emails nobody got.
- **Due** is: the person's account enabled and the email on; their hour
  this week has come in their own time zone (`userbus.Weekly.Last`); and
  it came after the last email they were sent and **less than six hours
  ago** (`userbus.Late`). Past that, the week waits for the next, which
  says everything since the last one that went, so a server that was down
  loses only the timing. The same window keeps an email from going at a
  strange hour to somebody who turns it back on midweek, or who was just
  given their first role.
- **Taken, then built.** `ClaimWeek` is the one `UPDATE … WHERE
  sent_up_to IS ?`, moving the cursor to the moment of sending (a person
  with no row gets one with the defaults first). An empty week moves it
  too and keeps what it saw. A message the relay refuses is logged; what
  it saw is not kept, so the next email tells its lists' news again, but
  its questions, read from the cursor, it does not.
- **The message** is `digestapp/mail/digest.txt`, in the reader's
  language: one block per organization, each line a count and the link to
  the list or the question, and a footer with the day and hour, the link
  to the profile's card and the link to stop it. The subject's date is the
  person's own, written `2006-01-02` like every date on a page, so that it
  needs no month names. The weekday in the footer is seven whole
  sentences, one per day.
- **Stopping it**: each email mints a token (`userbus.StopToken`) and
  keeps only its hash, replacing the last one's, so only the latest
  email's link works; an older one's page says so and links to the
  profile. `GET /digest/stop?t=` is a sentence and a button and changes
  nothing; `POST /digest/stop` stops it, with the token from the form or
  from the address, where a mail program's own button leaves it
  (`List-Unsubscribe-Post: List-Unsubscribe=One-Click`). Neither asks for
  sign-in, and the cross-origin guard lets the mail program's post
  through because it carries neither `Sec-Fetch-Site` nor `Origin`.
  Stopping keeps the day and the hour for when it is turned back on.

## Verification

- **Each reason and each kind of context**, in both lists, in a
  table-driven test of the ledger's search.
- **A transaction in an account for reference** is never "more context
  needed".
- **Isolation**: nobody reads another organization's lists, questions,
  question links or weekly email, through any route, the API, the MCP
  tools or a link in an email. Every new route is in the stranger walk
  (`app/sdk/muxer/tenancy_test.go`).
- **Roles**: every role on every kind of thing, for asking, replying,
  marking answered and reading. A role on a project alone reaches neither
  a transaction's other parts nor its page, through the lists, a question
  or the email.
- **The email**, with a clock moved by hand: it comes at the chosen hour in
  two time zones; nothing when there is nothing; nothing twice; nothing
  of the person's own doing; nothing to somebody who stopped it; and
  opening the link to stop it stops nothing.
- **Colour**: a test reads the stylesheet's colours and checks that the
  text on yellow and on green, and the hint lines, have a contrast of at
  least 4.5 to 1, and the tabs' rules at least 3 to 1, light and dark.
  Each state shows its words.
- **On a phone**: every new page at 360 px, light and dark, looked at
  before its pull request.
