# reconcile — product and build plan

Agreed on 2026-10-07. This is the document that decides arguments about
behaviour: when a decision here changes, change it here in the same pull
request, and say why.

## Context

People who handle a nonprofit's money day to day (the "boots on the ground")
get statements from their accountants only monthly, quarterly or yearly. The
app is meant to close that gap. They upload bank and credit-card statements,
the app reads them into accounts, and the transactions can be justified with
receipts, split across categories and projects, reconciled each month against
the statement, and handed to the accountant. A **project** (such as a World
Youth Day pilgrimage) gathers income and expense splits from one or more
accounts, so anyone can see whether it is coming out even.

**The mission** (README.md, 2026-10-08): understand the money between the
accountant's reports and talk with the accountant better; never replace the
bookkeeping software or the accountant. Every part of every charge is one of
four kinds -- income, expense, transfer, pass-through -- and only income and
expenses are operations. See "Kinds of money" below.

It is general-purpose and public, but used mostly by Schoenstatt entities and
friends. Jeff is the site administrator, but **no one has access to every
account by default**. Anyone can sign up and create organizations and
accounts, and access is delegated by email address.

The repository started as a scaffold (commit `78d4eb6`): a Go server with
`/healthz`, push-to-deploy to the shared Hetzner konsoleH account, and
Apache-2.0. The build order below says what comes next.

### Decisions already made (from Q&A)

| Topic | Decision |
|---|---|
| Org ↔ account | **Organization is optional.** An account belongs to an org or stands alone (personal) |
| Who creates | **Anyone signed in** can create orgs and accounts |
| Sign-up | **Open.** Any email can request a code; a user is created on first sign-in |
| Statement reading | **Deterministic parsers only, no AI.** Start with **generic CSV**, then OFX/QFX. **PDF later** (open question below) |
| Accountant | **Both** a read-only role and a downloadable export package |
| Projects | **Splits.** A transaction has one or more parts, each with an amount, a category and an optional project |
| Categories | **One list per organization**. A personal account keeps its own list |
| Receipts at close | **Never required.** Welcome, but they never block a month |
| Uploads | **Many files per upload, and many files per receipt** (photos and PDFs) |
| Languages | **English, Spanish, Portuguese.** Translations live in a DB table, the templates call `t`, and Claude fills pending translations through an API later |

---

## What gets reused (do not rewrite)

Three sibling repos already contain most of the mechanics. All three are
Jeff's own code. **eumaeus has no LICENSE file; the plan assumes he is
willing to relicense the lifted parts under Apache-2.0.** Copy code only,
never local data or configs (eumaeus' fixtures are invented and safe).

| Need | Lift from | Notes |
|---|---|---|
| SQLite, schema checks, `AddColumn`, `Restrict` | `stewards/foundation/sqldb/sqldb.go` | Verbatim |
| SMTP sender, `Recorder` for tests and dev | `stewards/foundation/mail/mail.go` | Verbatim |
| CSRF origin check, `MaxBody`, content-type guards, JSON helpers | `stewards/foundation/web/{forms,json}.go` | Verbatim (the scaffold lacks them) |
| `ID`, `Email` | `stewards/business/types/{id,email}.go` | Verbatim |
| Page renderer (layout, hashed CSS/JS, buffered render) | `stewards/app/sdk/page/render.go`, `funcs.go` | Swap `Steward` for `User`, swap EN/ES `Text` for `t` |
| Session cookie, `Authenticate` and `Require` middleware, `SafeNext` | `stewards/app/sdk/mid/session.go` | Rename `StewardFrom` to `UserFrom` |
| **Six-digit emailed code + pending cookie, try counting, backup codes, bootstrap** | `mass-intentions/business/domain/user/userbus/{userbus,credential}.go`, `app/domain/authapp/pending.go` | Code sign-in is in mass-intentions; stewards only has emailed links |
| Credential format `<id>.<secret>`, sha256, constant-time compare | `stewards/.../userbus/credential.go` | Same scheme for codes, sessions, backup codes and later API keys |
| Grants by email, invitation flow | `dropin-forms/business/domain/access/accessbus/accessbus.go`, `app/domain/peopleapp/peopleapp.go` | Model to adapt; this app adds scopes and inheritance |
| File store (write to temp, fsync, rename; safe names) | `stewards/business/domain/photo/stores/photofs/photofs.go` | Becomes `filestore` |
| Upload route shape (own muxer branch, `MaxBody`, `MultipartOnly`, longer deadlines) | `stewards/app/sdk/muxer/muxer.go`, `app/domain/photoapp` | |
| Money (`Amount int64` cents, `Parse`, `Display`) | `eumaeus/business/types/money/money.go` | Verbatim |
| Import port and records | `eumaeus/business/domain/importing/importbus` | Add account and actor |
| CSV with mapping, `Detect`, header `Fingerprint`, date formats | `eumaeus/.../importing/sources/csvsource` | Mapping stored per account in the DB, not in TOML |
| OFX/QFX 1.x and 2.x, FITID | `eumaeus/.../importing/sources/ofxsource` | Verbatim |
| Balance verification (running balance is the truth; figure, balance and ending mismatches) | `eumaeus/.../importing/sources/statement/{statement,table,trace}.go` | Verbatim. Used for CSV with a balance column now, PDF adapters later |
| Dedupe (`Hash`, `AssignOccurrences`, partial unique index on external_id, `FindDuplicates`) | `eumaeus/business/domain/ledger/ledgerbus` | |
| Per-account, per-month coverage | `eumaeus/.../ledgerbus/coverage.go` | Drives the reconcile overview |
| Pre-commit check for account-number shapes | `eumaeus/scripts/pre-commit` | Important in a public repo |
| API keys, OAuth, MCP (later phase) | `stewards/app/sdk/mid/apikey.go`, `foundation/oauth`, `app/domain/mcpapp` | For the translation API and Claude |

---

## Domain model

All IDs are `types.ID`. Money is `money.Amount` (cents, signed: money in is
positive). Times are Unix milliseconds. Every table is `STRICT`.

### Kinds of money (decided 2026-10-08)

Two lenses, kept apart. **Cash** is what the bank says: money in and money
out of an account, the statement, reconciling. **Operations** is what the
money was, and it is a property of each part (split), through its category:

| Kind | What it is | In the totals |
|---|---|---|
| `income` | Money that came to the organization | Income, signed: a donation returned is negative income |
| `expense` | Money the organization spent | Expenses, signed: a refund is a negative expense, never income |
| `transfer` | Money moving between the organization's own accounts (the card paid from checking) | Neither. An organization's transfers should net to zero; a gap is a leg not imported, or money in transit |
| `passthrough` | Money that went through but was never the organization's (a personal charge and its repayment; a collection forwarded to the diocese) | Neither. Each pass-through category should come back to zero; a balance is somebody who still owes it, or a collection not yet forwarded |

- **The kind is not the sign.** A part's kind is what it was; its sign is
  only which way the money moved.
- **The kind is on the category**, an explicit column, so sorting a part is
  still one choice, from a list grouped by kind. Reports read the column,
  never a category's name. A category made before kinds has none ("not said
  yet") until its owner chooses one; parts in it stay outside income and
  expenses until then.
- **A new list starts with** "Transfers between our accounts" (transfer) and
  "Personal, repaid" (pass-through), renamable like any other.
- Reconciling stays a cash check. The kinds never change a balance.

**Identity**
- `users(id, email UNIQUE, name, lang, site_admin, enabled, created_at, updated_at)`. The email can change, and everything references `id`.
- `signin_codes`, `sessions`, `backup_codes`: as in mass-intentions.
- `email_changes(id, user_id, new_email, code_hash, tries, expires_at, used_at)`. A code is sent to the **new** address, and the old address is told afterwards.

**Tenancy and access**
- `orgs(id, name, created_by, created_at, archived_at)`
- `accounts(id, org_id NULL, name, kind[checking|savings|card|cash|other], last4, currency DEFAULT 'USD', opened_on, archived_at)`. With `org_id` NULL it is a personal account.
- `projects(id, org_id NULL, name, starts_on, ends_on, note, archived_at)`
- `grants(id, scope_kind[org|account|project], scope_id, user_id NULL, email NULL, role, granted_by, created_at)`. Exactly one of `user_id` or `email` is set. A grant to an email with no user waits. When that address signs up, or a user verifies a change to it, the grant is claimed in one statement: `UPDATE grants SET user_id=?, email=NULL WHERE email=?`.
- **Roles** (cumulative: holding two is holding everything either allows. Not a ladder -- an accountant exports and a contributor uploads receipts, and neither does the other's job, so each role is a set of permissions in `tenancybus/role.go`):

  | Role | Can |
  |---|---|
  | `owner` | everything, including grants, rename, archive, and categories; there can never be zero owners |
  | `bookkeeper` | import statements, edit splits, categories and projects on transactions, reconcile, export |
  | `contributor` | see transactions; upload and attach receipts; add notes |
  | `accountant` | read everything, export, add notes |
  | `viewer` | read |

- **Inheritance:** a role on an org applies to all its accounts and projects. A role on a project shows **only that project's splits**, with their receipts, never the whole account.
- **Site admin** (Jeff, set by bootstrap) sees the user list and the names of orgs and accounts, and can disable users. **No data access without a grant.**
- `events(id, actor_id, scope_kind, scope_id, action, detail_json, at)`: an append-only audit trail of who changed what, which accountants will want.

**Ledger**
- `statements(id, account_id, file_id, format[csv|ofx], period_start, period_end, opening NULL, closing NULL, checked[balances|totals|none], added, already, imported_by, imported_at)`. There is no "failed": a statement that does not balance is never stored. `added` and `already` are what the import found, kept because the second is not recoverable afterwards.
- `transactions(id, account_id, statement_id, posted_on, description, amount, balance_after NULL, external_id, hash, occurrence)`. Unique on `(account_id, external_id)` where external_id is set; non-unique on `hash`.
- `splits(id, transaction_id, position, amount, category_id NULL, project_id NULL, memo, rule_id NULL)`. `rule_id` is the sorting rule that sorted the part, cleared when a person saves the transaction (`docs/sorting.md`); not a foreign key, so that removing a rule leaves its mark on what it sorted. The business layer enforces that the parts sum to the transaction, each the same way round as it and none zero. A new transaction gets one split for the full amount, and `ledgerdb.Init` gives one to any transaction without. A split goes with its transaction when a statement is removed. Putting money into a project takes Bookkeep on the account *and* on the project; a split already in a project stays there for a bookkeeper of the account who cannot see the project. Money into or out of a project is a line in the project's history (`split.added`, `split.removed`); re-sorting categories is not written into the account's, which it would flood.
- `categories(id, org_id NULL, account_id NULL, name, kind, created_by, created_at, archived_at)`. `kind` is income, expense, transfer or passthrough, or '' for one made before kinds (above). Exactly one owner, either an org or a personal account; an account in an org has no list of its own. Names are unique within a list, whatever their case. Archived, never deleted.
- `csv_mappings(account_id, fingerprint, mapping, updated_by, updated_at)`. Once mapped, the same header is read with the same columns; the preview is still shown, because it is where the balance check is seen.

**Receipts and files**
- `files(id, sha256, size, content_type, name, uploaded_by, uploaded_at)`: one row per upload, because who uploaded it is part of what it is. The bytes go to `APP_DIR/files/<sha256>` (0700, `[files] dir`), once per content, and are served only through the app, after the domain that links the file to a scope has said who may read it. A file may be read into a statement only by the person who uploaded it.
- `receipts(id, account_id NULL, project_id NULL, uploaded_by, spent_on, amount NULL, merchant, note, created_at, removed_at NULL)`. Exactly one of `account_id` and `project_id` is set: that is the receipt's **home**, the inbox it was put in. A receipt holds **one or more files**.
- `receipt_files(receipt_id, position, file_id)`
- `receipt_links(receipt_id, transaction_id, linked_by, linked_at)`. Many-to-many, because one receipt can cover two charges and one charge can have several receipts. The link goes with its transaction (`ON DELETE CASCADE`), so removing a statement puts its receipts back to waiting rather than losing them.
- A receipt with no transaction is **waiting**. It sits in its home inbox until it is matched. Removing one is a mark (`removed_at`), allowed only while it waits, and can be undone; nothing that was uploaded is deleted.
- **Who sees a receipt:** anyone who can read its home inbox, or the account of a transaction it is on, or a project holding a split of one. So a pilgrim given only the project sees what they uploaded and which charge it went to, and the treasurer sees it from the card. **Who attaches it:** someone with the Receipts permission on the transaction's account, or on a project of its splits.

**Reconciliation**
- `reconciliations(statement_id PK, account_id, period_start, period_end, note, reconciled_by, reconciled_at)`. A row is the statement reconciled; reopening deletes it, with an event that keeps the reason. The period is the one on the bank's paper statement: it includes the file's own and may reach up to 31 days beyond it either side, because a CSV states no period and its first and last rows are rarely the 1st and the 31st.
- **The lock is on the period, not the statement.** While a period of an account is reconciled, no transaction posted in it can have its splits changed, no import may add one to it, and no statement that brought one in may be removed. Statements overlap (a card's June lists what May brought in), so a lock on one statement's own rows would leave the month open through the other. Receipts are not locked: they change no figure, and are welcome late.

**Translations**
- `ui_strings(key PK, en, context, first_seen, last_seen)` and `ui_translations(key, lang[es|pt], text, status[pending|draft|approved], updated_by, updated_at)`.

---

## How the main flows work

**Sign-in (code).** Enter an email and receive a six-digit code, valid for 15 minutes with 5 tries; a cookie holds the pending sign-in. An unknown email creates the user only when the code is correct, and the page reads the same whether or not the address is known. Backup codes (10 × 16 characters) can be generated on the profile page and used instead of a code. Sessions last 90 days in an `__Host-` cookie. The one-time `/sign-in/first` with a bootstrap secret makes Jeff the site admin.

**Delegation.** An owner types an email and picks a role. The app sends an invitation saying who granted what. The invitation contains **no** sign-in link; the person signs in normally, as in dropin-forms. Owners can change or revoke grants, but the last owner cannot be removed.

**Importing a statement.**
1. Upload the file to an account.
2. Sniff the format (OFX header → `ofxsource`; otherwise CSV).
3. For CSV, look up the header fingerprint in the account's `csv_mappings`. If it is not found, show a **mapping screen** prefilled by `csvsource.Detect` (date, description, amount or debit/credit, balance, date format, invert), with a preview of the first 10 parsed rows. Save the mapping on confirm.
4. Verify:
   - **With a balance column**, every row's balance must follow from the one before, so a missing row is caught and named. The rows are tried oldest-first and newest-first, and the balance as money held and as money owed; a failure is reported in the reading the dates and the account's kind make likeliest. (eumaeus' idea; its `statement` package was built for PDF tables and was not copied.)
   - **With opening and closing balances** (OFX `LEDGERBAL`, or typed in by the user), check opening + Σ = closing.
   - **With neither**, the statement is `unverified`, and the UI says so plainly.
5. Dedupe against existing transactions (FITID, then hash + occurrence), and show "N new, M already here". A failed verification imports nothing and explains which row broke.
6. Store the original file as a `file` linked to the statement.

**Receipts.** Uploading works from a phone: the file picker allows multiple files and the camera. Each file becomes its own receipt unless the person ticks "these pages are one receipt", and a note can go with them. The date, amount and shop are typed afterwards on the receipt's page, by whoever has the paper in front of them -- asking for them at the counter is what would make the minute three. Receipts can be uploaded onto a transaction directly, or into the inbox. **Match suggestions** pair waiting receipts with transactions (exact amount, date within ±5 days); they are worked out whenever the waiting list or a transaction is shown, so a statement imported later finds receipts uploaded earlier, and a person confirms each pair; nothing is auto-attached. Accepted types are JPEG, PNG, WebP, HEIC and PDF, sniffed from the first bytes (`http.DetectContentType`, plus the `ftyp` brands for HEIC, which it does not know) and never trusted from the extension; a file of any other kind is named back to the person and not kept. Files are streamed to disk and never decoded whole, because the host kills processes at about 300 MB. A photo is shown in the page; a PDF is only ever a download, so nothing uploaded runs as a document on this site.

**Projects.** The project page shows income, expense and net (whether it comes out even), a breakdown by category and month, and the splits it holds, from any account. Assigning a split to a project requires bookkeeper rights on the transaction's account. Budgets come later.

**Reconcile (end of month).** Each account has a month-by-month page. A month is *reconciled* when every day of it is in a reconciled period, *imported* when every day is in some statement, *partial* when some days are in none (and it names them), *missing* when no statement reaches it, and *still going* while it is the current month. A month with no transactions looks the same as one nobody imported in a list of transactions; this page is how to tell them apart. The days of the first month before the account's first statement are not a gap. Opening a statement shows:
- the verification result, and the closing balance to compare with the paper (stated, typed, or read from the last row when the file balanced row by row)
- its period's transactions, money in and out, and those with no category, which do not block reconciling but cannot be sorted afterwards without reopening
- waiting receipts that may belong: put into the account's inbox and dated in the period, or for the amount of one of its transactions
- a "Mark reconciled" form with the period (bookkeeper), or, once reconciled, who did it and when, and a "Reopen" form that asks why

Receipts are never required.

**Export (accountant package).** For an account and a period of months (from the month-by-month page, or a statement's own period from its page), or for a whole project (from its book), the export is a zip containing:
- `transactions.csv`, one row per split: date, account, description, the split's amount, currency, the whole transaction's amount, category, project, memo, its receipts' paths in the zip, the statement file it came from, and the day its period was reconciled (empty if it was not). UTF-8 with a byte-order mark so that Excel reads accents; a text cell that starts with `=`, `+`, `-` or `@` gets a leading apostrophe so that it is never run as a formula. The header row is words, so it goes through `t` (an app's `text/*.txt`).
- a `receipts/` folder of files named `YYYY-MM-DD_amount_merchant_n.ext`: the date, amount and shop typed on the receipt, or else the transaction's date, amount and description; `n` is the page. A receipt on two charges is in the zip once.
- for an account, a `statements/` folder with the original file of every statement whose period touches the export's. A project's package has none: a role on a project is no window into its accounts.

Owners, bookkeepers and accountants can download it (the Export permission). It is streamed, a file at a time, with thirty minutes to send; nothing is written to the history, since nothing changed, and the log keeps who downloaded what.

**Translations.**
- Templates call `{{t "Upload receipts"}}` (keyed by English text plus an optional context: `{{tc "verb" "Close"}}`), and Go-side copy calls `i18n.T(ctx, "…")`.
- At startup the app walks the parse trees of the embedded templates and a registry of Go strings, and upserts every key into `ui_strings`. New keys get `pending` rows for es and pt.
- Translations are cached in memory. A missing or pending translation falls back to English.
- A user's `lang`, or else `Accept-Language`, picks the language.
- A later phase adds `/api/v1/translations?status=pending` + `PUT`, behind an API key with a translator scope, plus an MCP tool, so Claude can fetch and fill them. A person approves the results on a review screen.

---

## Build order (each step is one or more PRs; a merge deploys)

1. **Plan in the repo.** *(this PR)*
   - Add `docs/plan.md` (this plan) and `docs/design.md` (UI principles: phone-first, plain words).
   - Fill in CLAUDE.md's "what this app is for".
   - Add `scripts/data-guard` (from eumaeus' pre-commit) as a pre-commit hook and a CI step, and the rule "no real statements, account numbers or receipts in the repo; fixtures are invented".
2. **Foundations.**
   - Copy in `sqldb`, `mail`, `web/forms.go` + `json.go`, `types/{id,email}`, and `money`.
   - Port the page renderer and the session middleware.
   - Turn `/healthz` into the schema check.
   - Config gains `[db]`, `[files]`, `[server] base_url`, `[mail]`, and `[auth] bootstrap_secret`.
   - `scripts/secrets` gains the runtime keys (SMTP\_*, MAIL_FROM, BOOTSTRAP_SIGNIN_SECRET); on the server, mail goes through the host's local Exim at `localhost:25`, which signs it.
   - `deploy.sh` regains stewards' hard-link snapshot of `files/` beside each database backup.
   - Add the i18n table, the `t` function, and template-key extraction.
3. **Identity.** Code sign-in, sessions, sign-out, bootstrap, profile (name, language, email change with verification), backup codes. In dev, codes go to the log through `mail.Recorder` when no relay is set and `base_url` is loopback.
4. **Tenancy.**
   - Orgs, personal and org accounts, projects (shell only), and grants with email invitations.
   - A single `tenancybus.AccessTo(ctx, user, scope)` answers every check (organizations, accounts, projects and grants share one domain, so that making an organization and its owner is one transaction); business methods take the actor and filter by it.
   - Site admin user list.
   - The events table.
   - **Cross-tenant isolation tests**: user A can never read B's org, account, project, file or export, through any route.
5. **Import.** Statements, the CSV mapping screen and saved mappings, OFX/QFX, verification, dedupe, the transaction list by month, and the statement file stored and downloadable. Removing a statement takes its transactions with it. The list's filters by category, project and receipt arrive with those things, in steps 6 and 7.
6. **Categories, splits, projects.** Per-org and personal category lists, the split editor, project assignment, the project dashboard (totals, by category, by month, every part, per currency), and the month list's "not sorted yet" filter.
7. **Receipts.** Multi-file upload (direct and inbox), receipt viewer, attach and detach, match suggestions (same amount within ±5 days, from the accounts the person may attach receipts on) on the waiting list and the transaction page. Uploads stream part by part, 20 MB a file, 20 files and 120 MB a request, ten minutes. Pages show a photo's smaller pictures (step 9, below).
8. **Reconcile and export.** In two pull requests: first the month-by-month page, the statement reconcile screen, and locking and reopening (audited); then the export zip (`exportbus`, `exportapp`).
9. **Later, separately planned:**
   - The translations API, an MCP tool for Claude, and a review screen; then ES/PT go live (planned in `docs/translations.md`)
   - **Kinds of money** (above), before the sorting rules, which build on them: `categories.kind`; the category list grouped by kind and the split editor's choice too; the project book's income, expenses and net, with transfers and pass-through on lines of their own; an operations line beside the month list's cash; the balances that should come back to zero (each pass-through category, an organization's transfers); a "Kind" column in the accountant's package
   - PDF statements: one generic reader over poppler's `pdftotext -layout`, checked by running balances or the document's own totals (planned in `docs/pdf-statements.md`)
   - Budgets: a project's, and an organization's year by category, income and expenses, set by owners (planned in `docs/budgets.md`)
   - Categorization rules and suggestions (eumaeus `categorizebus`; planned in `docs/sorting.md`). Rules (`rulebus`, `sort_rules`, applied at import and from the rules page), then suggestions and "Sort this month" (`/accounts/{id}/sort`)
   - Thumbnails (stewards `imaging`, now `foundation/imaging`): a JPEG or PNG receipt has a small picture for lists and a large one for its page, upright and without EXIF, made the first time a page asks and kept beside the original by its hash (`files/sized/`). The original is unchanged, still a tap away, and is what the accountant's package holds. PDF, WebP and HEIC are shown or offered as they are

## Open items (do not block steps 1–8)

- **PDF statements.** The server has poppler's `pdftotext` (checked 2026-10-09: 22.12); see `docs/pdf-statements.md`.
- **Public hostname and From address.** Both go in `secrets.env`, never in a tracked file. The From address must be on a domain the Hetzner host DKIM-signs for, or sign-in codes land in spam and nobody can sign in.
- **Disk.** Receipts and statements accumulate on the shared host. `make prod-status` should report the size of `files/`.

## Verification

- Each step: `make test` (unit tests with `-race`, lint, shell tests) and `make go-check PKG=…` on every package touched; `make vuln-check`; and CI green before merge.
- Business tests run against real SQLite in `t.TempDir()`, with a hand-moved clock (the stewards pattern). The single-use code claims and grant claims are tested under concurrency.
- Importers use **invented** fixtures only: a CSV with a balance column, one with debit/credit columns, OFX 1.x SGML and 2.x XML, and statements broken on purpose (dropped row, wrong sign, ending mismatch). Re-importing an overlapping export must add nothing.
- Access: a table-driven test of every role on every scope for every route, plus the isolation suite in step 4.
- End to end, locally (`make run`, codes in the log): sign up → create an org → add an account → import a CSV → split a charge into a project → upload three photos as one receipt → accept the match → reconcile → download the export and open the zip. After a deploy, run `make prod-status`.
