# Printed checks

Issue #166. A program on the treasurer's own computer, `reconcile-checks`
(its own repository), prints checks on pre-printed check stock. Two
computers print from one stack. Each check it prints is sent here: the
image of it as printed, and what it printed — the number, amount, payee,
the day written and the memo. The check then waits among the account's
checks outstanding until the statement that pays it is imported, and is
attached to its charge the way a check image read by Claude is
(`docs/phone.md`, 6 and 7). The bookkeeper and the accountant see payee,
memo and date written beside the money leaving, and nobody photographs the
check.

The site never sees a full account number. The stock is pre-printed, the
site keeps only an account's last four digits, and the image the program
sends shows the account as `XXXXXX1234`. Accounts are named by their id.

## The rules (`receiptbus/sent.go`)

- **Sending** (`SendCheck`) needs every part but the memo. A check is drawn
  on a checking account that is in use: not a card, not one kept for
  reference, not archived (`Printable`, `ErrNotChecking`). Sending needs the
  role that attaches receipts in the account.
- **Attached at once** when the account has a transaction that paid the
  number for the amount, and its payee, memo and day written are written on
  that transaction too. **Refused** (`AmountDiffers`, nothing kept) when the
  bank paid the number for another amount: the wrong stock was loaded, or
  the amount was mistyped. **Outstanding** otherwise.
- **Sent twice** — the program was not sure the first arrived — is
  recognised by what the check says: the same number, amount, payee (ignoring
  case and spaces) and day. The answer is the check made the first time,
  and nothing is kept. Not by the file: the same check drawn again is new
  bytes. **The same number saying anything else** is another check, refused
  with the one that has the number (`NumberUsed`).
- **A photo of the same check**, numbered before anybody said its amount,
  is completed by the sending rather than refused: the printed image
  becomes its next page (`docs/phone.md`, 9).
- **Void** (`VoidCheck`, by number; `SetCheckVoided`, on the check's page):
  a spoiled check is kept with its number, so the accountant can see why the
  number was skipped. It is out of the checks outstanding, matched to
  nothing, and not read or numbered again (`ErrVoided`). Its number is free
  to print again. A check that has cleared cannot be voided (`ErrAttached`).
  On its page, a person who voided one by mistake can take the mark off,
  unless its number has been printed again since.
- **Asking about a number** (`CheckNumberState`) before printing on it gives
  one of: free; outstanding, attached or void, with the check; or cleared,
  meaning the bank paid it and no image is here, with the bank's
  transactions for it (usually one). It reads no more than sending would
  refuse with, for one number at a time.
- **The numbers used** (`CheckNumbers`) are the highest, and the five
  highest, from the account's check images (void ones too) and from the
  bank's transactions, compared as numbers. The program proposes the next
  one; the person confirms it against the paper. The site is not a
  sequence generator: a counter check or a second checkbook makes one stray
  number, which the list shows for what it is.

## The API (`apiapp/checks.go`)

Routes behind the `checks` scope (`docs/books-api.md`, "Scopes"), which
`books:write` includes. None of them is a tool on `/mcp`: sending is a file,
and a file cannot pass through a conversation. Every request sends
`Authorization: Bearer <key>`. A refusal is the API's usual
`{"error": {"field", "problem"}}`, where `problem` is a sentence. A refusal
a program must tell apart also carries `code`, and what it needs to act on.

| Call | Answer |
|---|---|
| `GET /api/v1/checks/accounts` | `{accounts: [{id, name, last4, currency, organization, highest, last_numbers}]}`: checking accounts in use where the person may add receipts. `highest` is `""` when none has been used, and `last_numbers` holds the five highest, highest first |
| `GET /api/v1/accounts/{account}/payees?q=` | `{payees: [{name}]}`, at most 20, whose name or any spelling contains `q`. Empty when the person cannot see the organization's payees |
| `POST /api/v1/accounts/{account}/checks?number=&amount=&payee=&written_on=YYYY-MM-DD&memo=&name=`, the image as the body (PNG, JPEG, WebP, HEIC or PDF, at most 20 MB) | `201 {outcome: "outstanding" \| "attached", check}` · `200 {outcome: "already_there", check}` · `409 code number_used, check` · `409 code amount_differs, bank, sent` · `422 code not_checking`. `400` for a field missing or malformed, before the image is kept |
| `GET /api/v1/accounts/{account}/checks?state=outstanding\|all` | `{checks: [check]}`: the person's own checks in the account, newest written first. Outstanding by default |
| `GET /api/v1/accounts/{account}/checks/{number}` | Always `200 {number, state: "free" \| "outstanding" \| "attached" \| "void" \| "cleared", check?, cleared?: [{amount, date}]}`. `404` means only that the key does not reach the account |
| `POST /api/v1/accounts/{account}/checks/{number}/void` | `200 {check}` · `409 code cleared` · `404 code no_check` |

A `check` is `{id, number, amount, payee, written_on, memo, state:
"outstanding" | "attached" | "void", attached_to: [{id, url}], url}`.
Amounts are unsigned strings, such as `"12.34"`.

**A 404 is never a free number.** On every route here, as everywhere in the
API, `404` answers both "no such account" and "an account this key does not
reach", so that a stranger learns nothing. A program that took a 404 for
"free" would print on every number once its key lost the account.

### For the program

- **Signing in**: until OAuth for native programs arrives (#166, the last
  pull request), the person makes a key on `/account/keys` for "A program
  that prints checks" and pastes it, with the site's address, into the
  program's settings. On any `401`, ask for a key again.
- **Before printing**, propose `highest + 1` and let the person confirm it
  against the paper. Then ask `GET …/checks/{number}`: print only on `free`,
  and if the person goes ahead anyway, the send's `409` still catches the
  clash.
- **After printing**, send. Offline, queue the send and the voids in order,
  and send them again when the connection comes back: a repeat is
  `already_there`, and voiding a void check answers it as it is.
- **A misprint**, such as a jam or a typo seen too late, is sent and then
  voided.
- **Never** send or print a full account number.
- **Taking a void mark off** is the person's, on the check's page. There is
  no route for it, as there is none for restoring a removed receipt.

## On the site

- A waiting check's page has **Void this check**, and once it is voided, **It
  is not void**. The account's receipts page lists **Void checks** apart,
  after the matched ones.
- **The history**: "sent check 1190 for 12.34 to Hilltop Plumbing, as
  printed", "voided check 1190", "took the void mark off check 1190", each
  with the key it came through.

## Not yet

- **The accountant's package** does not list void checks yet. They are on
  the account's receipts page.
- **Signing in through OAuth** as a native program is the next pull
  request.
