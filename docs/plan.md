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

**Identity**
- `users(id, email UNIQUE, name, lang, site_admin, enabled, created_at, updated_at)`. The email can change, and everything references `id`.
- `signin_codes`, `sessions`, `backup_codes`: as in mass-intentions.
- `email_changes(id, user_id, new_email, code_hash, tries, expires_at, used_at)`. A code is sent to the **new** address, and the old address is told afterwards.

**Tenancy and access**
- `orgs(id, name, created_by, created_at, archived_at)`
- `accounts(id, org_id NULL, name, kind[checking|savings|card|cash|other], last4, currency DEFAULT 'USD', opened_on, archived_at)`. With `org_id` NULL it is a personal account.
- `projects(id, org_id NULL, name, starts_on, ends_on, note, archived_at)`
- `grants(id, scope_kind[org|account|project], scope_id, user_id NULL, email NULL, role, granted_by, created_at)`. Exactly one of `user_id` or `email` is set. A grant to an email with no user waits. When that address signs up, or a user verifies a change to it, the grant is claimed in one statement: `UPDATE grants SET user_id=?, email=NULL WHERE email=?`.
- **Roles** (cumulative, highest wins):

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
- `statements(id, account_id, file_id, format[csv|ofx|pdf], period_start, period_end, opening, closing, verification[verified|partial|unverified|failed], verification_note, imported_by, imported_at)`
- `transactions(id, account_id, statement_id, posted_on, description, amount, balance_after NULL, external_id, hash, occurrence)`. Unique on `(account_id, external_id)` where external_id is set; non-unique on `hash`.
- `splits(id, transaction_id, amount, category_id NULL, project_id NULL, memo)`. The business layer enforces that the parts sum to the transaction. A new transaction gets one split for the full amount.
- `categories(id, org_id NULL, account_id NULL, name, archived_at)`. Exactly one owner, either an org or a personal account.
- `csv_mappings(account_id, fingerprint, mapping_json)`. Once mapped, the same header imports with no questions.

**Receipts and files**
- `files(id, sha256 UNIQUE, size, content_type, original_name, uploaded_by, uploaded_at)`. Bytes go to `APP_DIR/files/<sha256>` (0700), are deduped by content, and are served only through the app after an access check.
- `receipts(id, account_id NULL, project_id NULL, uploaded_by, date NULL, amount NULL, merchant, note, created_at)`. A receipt holds **one or more files**.
- `receipt_files(receipt_id, file_id, position)`
- `transaction_receipts(transaction_id, receipt_id)`. Many-to-many, because one receipt can cover two charges and one charge can have several receipts.
- A receipt with no transaction is **waiting**. It sits in the account's (or project's) receipt inbox until it is matched.

**Reconciliation**
- `reconciliations(id, statement_id UNIQUE, state[open|reconciled], reconciled_by, reconciled_at, note)`. While a statement is reconciled, its transactions and splits are locked. A bookkeeper or owner can reopen it, giving a reason, and the reopening is recorded as an event.

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
   - **With a balance column**, run eumaeus' `statement.Build` + `Verify`. Every row must follow from the one before, so a missing row is caught.
   - **With opening and closing balances** (OFX `LEDGERBAL`, or typed in by the user), check opening + Σ = closing.
   - **With neither**, the statement is `unverified`, and the UI says so plainly.
5. Dedupe against existing transactions (FITID, then hash + occurrence), and show "N new, M already here". A failed verification imports nothing and explains which row broke.
6. Store the original file as a `file` linked to the statement.

**Receipts.** Uploading works from a phone: the file picker allows multiple files and the camera. Each file becomes its own receipt unless the person ticks "these pages are one receipt". The uploader can optionally type the date, amount and merchant. Receipts can be uploaded onto a transaction directly, or into the inbox. After every import, **match suggestions** pair waiting receipts with transactions (exact amount, date within ±5 days), and a person confirms each pair; nothing is auto-attached. Accepted types are JPEG, PNG, WebP, HEIC and PDF, sniffed with `http.DetectContentType` and not trusted from the extension. Files are streamed to disk and never decoded whole, because the host kills processes at about 300 MB.

**Projects.** The project page shows income, expense and net (whether it comes out even), a breakdown by category and month, and the splits it holds, from any account. Assigning a split to a project requires bookkeeper rights on the transaction's account. Budgets come later.

**Reconcile (end of month).** A per-account grid of months shows whether each one is covered by a statement, verified, reconciled, or missing (eumaeus coverage). Opening a statement shows:
- the verification result
- transactions with no category
- unmatched receipts dated in the period
- a "Mark reconciled" button, which locks the period

Receipts are never required.

**Export (accountant package).** For an account and period, or for a project, the export is a zip containing:
- `transactions.csv`, one row per split: date, account, description, amount, category, project, memo, receipt filenames
- a `receipts/` folder of files named `YYYY-MM-DD_amount_merchant_n.ext`

The accountant role can download it.

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
   - A single `accessbus.Role(ctx, user, scope)` answers every check; business methods take the actor and filter by it.
   - Site admin user list.
   - The events table.
   - **Cross-tenant isolation tests**: user A can never read B's org, account, project, file or export, through any route.
5. **Import.** Statements, the CSV mapping screen and saved mappings, OFX/QFX, verification, dedupe, the transaction list (filter by month, category, project, has-receipt), and the statement file stored.
6. **Categories, splits, projects.** Per-org and personal category lists, the split editor, project assignment, the project dashboard.
7. **Receipts.** Multi-file upload (direct and inbox), receipt viewer, attach and detach, match suggestions after import.
8. **Reconcile and export.** The coverage grid, the statement reconcile screen, locking and reopening (audited), and the export zip.
9. **Later, separately planned:**
   - The translations API, an MCP tool for Claude, and a review screen; then ES/PT go live
   - PDF statements
   - Budgets per project and category
   - Categorization rules and suggestions (eumaeus `categorizebus`)
   - Thumbnails (stewards `imaging`)

## Open items (do not block steps 1–8)

- **PDF statements.** These need poppler's `pdftotext` on the host, or text extraction in the browser with a served pdf.js. Someone should run `command -v pdftotext` on the server before step 9.
- **Public hostname and From address.** Both go in `secrets.env`, never in a tracked file. The From address must be on a domain the Hetzner host DKIM-signs for, or sign-in codes land in spam and nobody can sign in.
- **Disk.** Receipts and statements accumulate on the shared host. `make prod-status` should report the size of `files/`.

## Verification

- Each step: `make test` (unit tests with `-race`, lint, shell tests) and `make go-check PKG=…` on every package touched; `make vuln-check`; and CI green before merge.
- Business tests run against real SQLite in `t.TempDir()`, with a hand-moved clock (the stewards pattern). The single-use code claims and grant claims are tested under concurrency.
- Importers use **invented** fixtures only: a CSV with a balance column, one with debit/credit columns, OFX 1.x SGML and 2.x XML, and statements broken on purpose (dropped row, wrong sign, ending mismatch). Re-importing an overlapping export must add nothing.
- Access: a table-driven test of every role on every scope for every route, plus the isolation suite in step 4.
- End to end, locally (`make run`, codes in the log): sign up → create an org → add an account → import a CSV → split a charge into a project → upload three photos as one receipt → accept the match → reconcile → download the export and open the zip. After a deploy, run `make prod-status`.
