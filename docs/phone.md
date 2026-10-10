# From a phone: the installable app, checks that wait, and Claude reading them

A plan for step 9 (`docs/plan.md`, build order), after the books API
(`docs/books-api.md`). It comes from the way a treasurer actually gets a
check's image: not a scan named after its number, but a screenshot from the
bank's app on a phone, fifty at a time at the end of a month, each named
`Screenshot_20261010-143201.png`. Today none of them can be added
(`docs/shapes.md`, 3): the name says no number, and a number with no
transaction yet refuses the whole upload.

Three things stand between that phone and a check attached to the
transaction that paid it, and this plan takes them in order:

1. The images have to **get in**, from the phone's share sheet, many at
   once.
2. A check image whose number is not known yet, or whose check has not
   cleared, has to **wait** rather than be refused.
3. Somebody has to **read** each one, and on a phone at a month's end that
   is Claude, through the books API, with the bank's own figures checking
   what it read.

## Decisions (from Q&A, 2026-10-10)

| Topic | Decision |
|---|---|
| How images get in | **An installable app with a share target**, as stewards has (`stewards/app/domain/inboxapp`): "Reconcile" in Android's share sheet, the photos caught on the phone by a service worker and sent one a request, shrunk first. It serves every receipt, not only checks: it is the design's shop-counter test (`docs/design.md`) |
| A check image nobody has numbered | **Waits** in its account's inbox, as a check image, until a person or Claude says its number |
| A number with no transaction yet | **Waits** with its number, and is attached when an import brings the check. The check has not cleared, or its statement is not imported; neither is a mistake |
| Who reads a check | **Claude**, through two MCP tools: one that shows a receipt's image, one that says what a check is. The site does no OCR of its own |
| What Claude sees of a check | **The whole image**, the MICR line included. That line prints the routing and account number, which then reach claude.ai; it also prints the check's number, a second reading to hold the first to. The person's own Claude, acting as them, may see what they may see on the site |
| How a reading is checked | **By the bank's amount.** Claude's reading of a check is accepted only when the amount it read is the amount of the account's transaction with that number. A misread digit becomes a refusal Claude reports, never a check on the wrong transaction |
| A stack from several accounts (2026-10-10, issue #72) | **A person's own check inbox**, for the images of checks only, beside an account's check images, which stay. Each check is filed under its account afterwards: by Claude, from the account number on its face, or by the person, choosing the account (8) |

## 1. Checks that wait

A check image today is matched as it is added, or refused
(`receiptbus.AddChecks`). Under this plan it is matched as it is added
when it can be, and otherwise waits:

- **A receipt is a check image or it is not**, whether or not its number
  is known (`receipts.check_image`, a later column; every receipt with a
  `check_number` is one). A check image without a number is named "A
  check" in lists, never by its file's name.
- **Added under "Check images"**, a file whose name gives no number, with
  no number typed, is a check of its own, waiting. Files that name the
  same number are still one check's sides, and a typed number still makes
  all the files one check.
- **A number with one transaction** is attached at once, as now. **With
  none**, the image waits with its number and its payee. **With several**,
  it waits too, and the account's transactions with that number are its
  suggestions; a person chooses. Nothing in an upload is refused because
  of another check in it.
- **Numbered later**, on the image's own page: "Check number" and "Paid
  to", which match it as an upload would. Whoever may correct the
  receipt's details may number it; a number already matched is not
  changed there (detach it first, which says what is being undone).
- When it is attached, by any of these ways, it takes its transaction's
  date and amount, and its payee goes to the transaction, as now.

### As built (the first pull request)

- **A check image** is `Receipt.CheckImage`, stored as
  `receipts.check_image`; `Init` marks every receipt that already had a
  number, which before this were all the check images there were.
- **Added** by `AddChecks` as planned: nothing in an upload is refused
  for another check in it, and the inbox says how many were attached and
  how many wait. A typed number that is not digits still refuses the
  upload, since it is a typing mistake in the form.
- **Numbered** on its page with "Check number" (`NumberCheck`,
  `POST /receipts/{id}/number`), for a check image that is waiting. A
  check image attached is refused there; taking it off its transaction
  first says what is undone.
- **Several transactions** with its number are its suggestions, in its
  inbox and on its page.
- **Named** "A check" until it has a number, "Check 1176" after, and
  never by its file's name.

## 2. Matched when the check clears

An import that brings a check number a waiting image of the same account
already has attaches it, as typing the number would have. It is
`ledgerbus.Import`'s last step, after the statement is stored, and so the
bulk import and the API's `import_from_inbox` do it too. The ledger knows
nothing of receipts: main gives it a function to call with the account
(`ledgerbus.AfterImport`), and `receiptbus.MatchChecks` is that function.
Matching that fails is logged and never undoes the import, which has
already happened; the image is still waiting and can be numbered again.

### As built (the second pull request)

- `ledger.OnImport(receipts.MatchChecks)` in main, and in the tests that
  build the site, after both are made. `MatchChecks` looks at the
  account's waiting check images with a number, and attaches each whose
  number now has one transaction, as numbering it on its page would,
  payee and all. The importer is who attached it.
- Somebody who may import may attach receipts (a bookkeeper's role has
  both), so nothing is skipped for want of a permission in practice; if it
  were, nothing would be matched and nothing would fail.

## 3. The installable app

Ported from stewards, where it has carried thirty plant photos at a time
since its inbox was built:

- **A manifest** (name, icons at 192 and 512 pixels, `display:
  standalone`, start at `/`) linked from every page a signed-in person
  sees, and **a share target** for `image/*` and `application/pdf`.
- **A service worker** whose only address is the share target: it puts
  the shared files in the phone's Cache Storage and sends the person to a
  screen that asks **where they go**: a checking account's checks, or an
  account's or project's receipts, among those the person may add
  receipts to. The phone remembers the last choice.
- The server's handler for the share target only ever sees a share the
  worker missed, and asks for it again.
- These are the first scripts on the site (`script-src 'self'` already
  allows them). The site still works without them; a share is the one
  thing that needs them.

### As built (the third pull request)

- **The manifest** is receiptapp's, at `/receipts/app.webmanifest`, and
  the worker and the share script are at `/receipts/static/`, all served
  to anybody: Chrome reads the manifest without cookies, and the worker
  again on its own schedule. The worker is served with
  `Service-Worker-Allowed: /receipts/shared`, its scope.
- **The icons** are the site's, not the receipts': a white tick on the
  accent blue, drawn for it, at `/static/img/` (`page.Renderer.Images`),
  with the 192-pixel one as every page's favicon.
- **The share page** is `/receipts/share`. It offers, among the inboxes
  the person may add receipts to, a checking account's check images
  first, then its receipts, then each other account's and each
  project's. Its form says where before its files, so that the choice is
  asked about before a byte is kept, and is then that inbox's upload, or
  its check images'. The phone remembers the last choice (`localStorage`),
  so a month shared in batches goes to the same place without choosing
  again.
- **What the page says** about a share -- ready, gone, could not be put in
  the form, did not reach the page, too many for one upload -- is written
  by the server in the page's language, hidden until the script shows the
  one that happened.
- **The JavaScript tests** came with it, from stewards: `make test-js` (none
  yet) and `make test-browser`, which builds the server, signs in with the
  bootstrap secret, and shares to it from a headless Chrome. CI runs both,
  with Node 24.

## 4. A photo a request, shrunk on the phone

An upload takes at most 20 files a request (`docs/plan.md`, step 7).
Stewards' sender (`send.mjs`, `shrink.mjs`, `jpeg.mjs`) sends one photo a
request after shrinking it to what is worth keeping, so fifty screenshots
are fifty small requests, each of which can fail and be retried alone.
Check images and receipts go through the same uploads they do from a
form, so nothing about them changes on the server.

### As built (the fourth pull request)

- **`send.mjs`** on the share page sends each file to
  `POST /receipts/share/one`, where first and then the file, and opens the
  inbox when the last has an answer, with what happened in the query the
  inbox reads after an upload of its own (and `already`). Up to 60 files a
  share (`MaxShare`); the inbox's own form keeps its 20.
- **Sending again is safe**: a file whose bytes a receipt in the inbox
  has, not removed, is "already" and nothing is added
  (`receiptbus.Holds`). The page also remembers what has been answered,
  and Add after a failure sends only the rest.
- **A refusal** is a code -- `kind`, `big`, `none`, `to` -- that the page
  says in its own words, which the server writes on `#send-words` with
  their placeholders left in; a dropped connection or a server error is
  tried again for about five minutes, and a session that ran out stops
  the share and says so.
- **Shrinking** (`shrink.mjs`, `jpeg.mjs`) is stewards', unchanged: a
  camera JPEG past 4096 pixels is scaled to it, upright and with its EXIF;
  anything else, a screenshot included, goes as it is. Tested under Node
  (`make test-js`) and in Chrome.
- **Sides named after their check**, shared together, go one a request,
  so each is a check image of its own, both attached to the check; the
  inbox's form still makes them one. The accountant's package names each
  page apart.

## 5. Claude sees a receipt

`GET /api/v1/receipts/{receipt}/image?page=` and `get_receipt_image`,
`books:read`: a receipt's page as an image, at the large size
(`foundation/imaging`, 1600 pixels on its longer side), or a PDF's first
page as it is. It is the first tool that answers with something other than
JSON, so `mcpapp` changes once: an `image/*` answer becomes MCP image
content. `list_waiting_receipts` says which are check images and which
have their number.

### As built (the fifth pull request, before the fourth)

- Built before the sender (4), since twenty files a share already work
  and this is what lets Claude read them.
- **The page** is `receiptbus.File`, so whoever may see the receipt may
  see it; a JPEG or PNG is its large picture (a photo too small to have
  one is sent as it is), a WebP as it is, and a PDF or HEIC is refused
  with 415 and the receipt's url, where a person can look at it.
- **On `/mcp`** an answer whose type is `image/*` is an MCP image rather
  than text (`mcpapp.answer`); every other answer is unchanged.
- **`list_waiting_receipts`** gives `check_image` and `check` for the
  images of checks, and nothing more for any other receipt.

## 6. Claude reads a check

`POST /api/v1/receipts/{receipt}/check` `{number, payee, amount, date}` and
`read_check`, `books:write`. It numbers a waiting check image as a person
would on its page (1), and is refused, with nothing changed, when the
account's one transaction with that number has another amount. A check
that has not cleared keeps its number and waits for 2. The Claude guide
(`app/domain/mcpapp/guide.md`) gains the month's-end routine: list the
waiting check images, look at each, read it, and say which were matched,
which wait, and which were refused and why.

### As built (the sixth pull request)

- **`receiptbus.ReadCheck`** takes a `CheckReading`: number, payee,
  amount (required) and date. One transaction with the number and another
  amount is `AmountDiffers`, and nothing changes; several, the one with the
  amount read, if exactly one has it; none, the image waits with the
  number, amount, payee and date read.
- **Matching on import** is now held to an amount the image carries,
  read by Claude or typed by a person: an image whose amount is not the
  bank's waits for somebody to look again. Numbering on the image's page
  is a person's word and still takes the bank's amount over one typed.
- **The history** has a line for each reading, "read the image of check
  1176, for 120.00", which says the key it came through, and
  `list_changes` lists it.
- **The guide** (`app/domain/mcpapp/guide.md`) gains "The images of
  checks", and reading them is, with importing, what Claude does without
  asking first: the bank's amount checks each reading.

## 7. What else a check says, and the checks outstanding

Issue #71, from reading the first month of real checks: an accountant
preparing 1099s needs, for each check, who was paid, how much and **what
for**, and which year it belongs to, which is the year it was written. The
image says both; the bank says neither. A check written on 23 September
and cleared on the 24th showed the 24th, and its memo line ("Cleaning",
"Summer work") had nowhere to go.

### As built (the seventh pull request)

- **The memo and the day written** are a check image's
  (`receipts.check_memo`, `receipts.check_written_on`, `Details.Memo` and
  `WrittenOn`), read by Claude (`read_check`'s `memo` and `date`) or typed
  on the image's page under "Memo" and "Date written". The receipt's own
  date is still the day it cleared once attached; the day written is kept
  apart from it.
- **On the transaction** they are written beside the payee, as the payee is
  (`transactions.check_memo`, `transactions.check_written_on`,
  `ledgerbus.SetWritten`, with a line of history): when the image is
  attached, when an import brings a check whose image was read before it
  cleared, and when the image's page corrects them. The transaction's date
  stays the day it cleared; its page says "The check was written on … and
  cleared on …". Like the payee, a note and not money: whoever may attach
  receipts may write it, in a reconciled period too.
- **Shown**: the memo beside the payee on the month list, the sorting page,
  the transaction's page and a project's book; the payee now on the
  statement's list of what is not sorted and in a project's book too.
- **Not the description.** The memo is not copied into the transaction's
  own description (issue #70): it is shown already, and that description
  is the person's to write. Claude's guide says so.
- **The checks outstanding** (`receiptbus.OutstandingChecks`): an
  account's check images with a number that are on no transaction, oldest
  written first, with the total of those whose amount is known and how many
  have none. On the account's month-by-month page, all of them; on a
  statement's page, those written by the end of its period (or on a day not
  read), beside its closing balance; to Claude, in `get_account_months`.
- **The accountant's package** gains "Memo on the check" and "Written on",
  after "Our description", as its last columns.
- **Not built**: payees as records of their own, marked as 1099
  contractors with a W-9 on file, and a year's payments per payee. That
  needs a plan of its own: how two spellings of one payee become one, and
  whose the W-9 flag is.

## 8. A person's own check inbox

Every way in so far asks for the account first. A treasurer with three
checking accounts has one stack of checks at a month's end, and sorting it
by account before sharing it is the work the stack was meant to save --
when every check already says its account, in the line of digits at its
foot.

**Why a person's inbox, and why only checks.** An inbox that only its
uploader can see is a place where a receipt can be stranded: forgotten,
or left behind by somebody who loses their role, where nobody else -- not
the treasurer, not the site administrator -- will ever find it. That is
the design's test failing a month later (`docs/design.md`). So ordinary
receipts keep going into an account's or a project's inbox, where the
treasurer sees what a volunteer put there. A check is the exception: its
account is printed on it, and its uploader, or their Claude, files it the
same day. An account's check images stay too, for somebody with several
accounts and no Claude who would rather choose there.

**Why not a receipt with a third kind of home.** A receipt's home is an
account or a project, held by a CHECK in the table that SQLite can drop
only by rebuilding it, and every page that names a receipt's inbox would
learn a kind of inbox only one person may see. The statement inbox
already has the right shape (`docs/books-api.md`, "The inbox"): a
person's list of files, each becoming something in an account when it is
filed.

- **Getting in**: "Your checks, from any account" first on the share page,
  for anybody who may add receipts to an account; a form on the inbox's
  own page, `/checks`, linked from the front page and from an account's
  check images. Sending again is safe, by content.
- **Filing by Claude**: `read_check` on one of these takes the account
  number as printed. Its last four digits are compared with those the
  site keeps of each account the person may add receipts to: one account,
  and it is filed there and read as a check image of that account, held
  to the bank's amount; none, or several, or only accounts the person may
  read, and nothing moves, with the reason. Everything that can refuse is
  asked before anything moves.
- **Filing by a person**: on `/checks`, an account for each, and a number
  if they like, as adding it to the account's check images would.
- **Nobody else** sees them, the site administrator included, until they
  are filed; then they are receipts of the account like any other.

### As built

- **`check_inbox`** (`receiptdb/inbox.go`): an image, whose it is, when it
  was added, removed and filed, and the receipt it became. A new table,
  not a later column. A filed item keeps pointing at its receipt, so the
  page can say where each went.
- **`receiptbus/inbox.go`**: `AddToCheckInbox`, `CheckInbox`,
  `CheckInboxHolds`, `InboxCheckFile`, `SetInboxCheckRemoved` (while it
  waits), `FileInboxCheck` and `ReadInboxCheck`. Filing is one
  transaction that claims the item with `UPDATE … WHERE filed_at IS NULL`,
  so the person's filing and their Claude's at the same moment make one
  receipt; the second is `ErrFiled`.
- **The account number** goes no further than the comparison. It is not
  stored or logged, and a refusal names the account by its last four
  digits, as the site does. **The routing number is not asked for**: the
  site keeps none for an account, so it would be a field that checks
  nothing.
- **The history**: filing writes `receipt.filed` in the account's,
  "filed the image of check 1176 here from their own checks", with the key
  it came through; Claude's reading adds its `receipt.check_read` line
  after it.
- **The API** keeps one routine for Claude rather than adding tools:
  `list_waiting_receipts` lists these after the receipts, `your_checks`
  true, with an id `get_receipt_image` and `read_check` take as they take
  a receipt's; `read_check` gains `account_number`, needed for these
  only.
- **The pages** are under `/checks`: `/receipts/checks/{id}/image` would
  overlap `/receipts/{id}/files/{n}` with neither more specific, which the
  muxer refuses.

## Build order

Pull requests after this plan, each useful when merged:

1. **Checks that wait** (1).
2. **Matched when the check clears** (2).
3. **The installable app**, with the share landing in the existing forms,
   20 files at a time (3).
4. **A photo a request** (4).
5. **Claude sees a receipt** (5).
6. **Claude reads a check** (6).
7. **What else a check says**, and the checks outstanding (7), from issue #71.
8. **A person's own check inbox** (8), after the plan, from issue #72.
