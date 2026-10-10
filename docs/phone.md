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
nothing of receipts: main gives it a function to call with the account and
what was imported (`ledgerbus.Imported`), and receiptbus is that function.
Matching that fails is logged and never undoes the import, which has
already happened; the image is still waiting and can be numbered again.

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

## 4. A photo a request, shrunk on the phone

An upload takes at most 20 files a request (`docs/plan.md`, step 7).
Stewards' sender (`send.mjs`, `shrink.mjs`, `jpeg.mjs`) sends one photo a
request after shrinking it to what is worth keeping, so fifty screenshots
are fifty small requests, each of which can fail and be retried alone.
Check images and receipts go through the same uploads they do from a
form, so nothing about them changes on the server.

## 5. Claude sees a receipt

`GET /api/v1/receipts/{receipt}/image?page=` and `get_receipt_image`,
`books:read`: a receipt's page as an image, at the large size
(`foundation/imaging`, 1600 pixels on its longer side), or a PDF's first
page as it is. It is the first tool that answers with something other than
JSON, so `mcpapp` changes once: an `image/*` answer becomes MCP image
content. `list_waiting_receipts` says which are check images and which
have their number.

## 6. Claude reads a check

`POST /api/v1/receipts/{receipt}/check` `{number, payee, amount, date}` and
`read_check`, `books:write`. It numbers a waiting check image as a person
would on its page (1), and is refused, with nothing changed, when the
account's one transaction with that number has another amount. A check
that has not cleared keeps its number and waits for 2. The Claude guide
(`app/domain/mcpapp/guide.md`) gains the month's-end routine: list the
waiting check images, look at each, read it, and say which were matched,
which wait, and which were refused and why.

## Build order

Six pull requests after this plan, each useful when merged:

1. **Checks that wait** (1).
2. **Matched when the check clears** (2).
3. **The installable app**, with the share landing in the existing forms,
   20 files at a time (3).
4. **A photo a request** (4).
5. **Claude sees a receipt** (5).
6. **Claude reads a check** (6).
