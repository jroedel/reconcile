# Translations: Claude fills them, a person looks them over

The plan for step 9's first item (`docs/plan.md`, build order): an API and an
MCP server through which Claude fills the pending Spanish and Portuguese
strings, which go live as they arrive, and a screen on which a person looks
them over afterwards.

## Where things stand

Every word on a page already goes through `t` (CLAUDE.md, "Words on a page").
At startup the renderer reads every `t` out of the templates and
`translationbus.Register` records each string in `ui_strings`, with a pending
row per language in `ui_translations` (status `pending`, `draft` or
`approved`). The catalogue shows a draft as soon as it exists and English for
a pending string. About 680 `t` calls are in the templates today, a few
hundred distinct strings, all pending in both languages.

So nothing about *showing* translations changes. What is missing is a way to
*write* them, and a person to check what was written.

## Decisions (from Q&A, 2026-10-08)

| Topic | Decision |
|---|---|
| Claude's translations | **Trusted.** They are shown on the pages as soon as they are written. Reviewing is a look over them afterwards, from the web, not a gate they wait at |
| Who reviews | **The site administrator, and people the administrator makes translators** for Spanish, Portuguese or both. Nobody else: anyone may sign up here, so "every signed-in user" (stewards' rule) would let a stranger rewrite the interface |
| How Claude connects | **Both in this step:** personal API keys with a small `/api/v1` and a Claude Code skill, *and* the MCP server with OAuth so claude.ai can connect. Lifted from stewards |
| Which Spanish and Portuguese | **Latin American Spanish with *tú*; Brazilian Portuguese with *você*.** Plain, as the English is |
| Agents and production | **A narrow exception in CLAUDE.md:** an agent may call the public `/api/v1` translation endpoints, only when a person asks it to, with a key the person put in an environment variable. Nothing else about production changes: no ssh, no deploy, no `make prod-*` |

## What gets lifted from stewards

Stewards keeps one row per original text, keyed by a hash, with a `checked`
flag and a send-back note; reconcile keys by `(context, en)` with three
statuses and two languages. So the screens, the key and OAuth machinery and
the MCP server come across nearly whole, and the translation rules are
re-fitted to reconcile's tables rather than copied.

| Need | From stewards | Notes |
|---|---|---|
| API keys: `<prefix>_<id>.<secret>`, sha256 of the secret, constant-time compare, 90 days, 5 live per person, name, `last_used_at` hourly | `business/domain/user/userbus/apikey.go`, `credential.go`; `userdb` `api_keys` | Prefix `rcn_` |
| Bearer middleware that never reads the cookie, `RequireKey` with `WWW-Authenticate` | `app/sdk/mid/apikey.go` | |
| Keys screen: make (secret shown once), list, revoke | `app/domain/stewardapp/keys.go` | `/account/keys`, linked from the account page for those who translate; a 404 to anybody else |
| Endpoints declared once as `Endpoint` structs that mount the routes, build the `GET /api/v1` index and name the MCP tools | `app/domain/apiapp` | |
| MCP over streamable HTTP, tools generated from the index, calls replayed in-process with the caller's bearer | `app/domain/mcpapp` | Official SDK `github.com/modelcontextprotocol/go-sdk`, the one new dependency |
| OAuth 2.1, S256 PKCE, authorization code only, Client ID Metadata Documents fetched only from Anthropic's hosts, the token an ordinary key with `client` set | `app/domain/oauthapp`, `foundation/oauth`, `userdb` `oauth_grants` | |
| Placeholder check: the set of `{name}`s must be the same | `translationbus/pending.go` | Also refuses markup the English does not have |
| Review screen: original, translation, who wrote it, where it appears, "Looks right", "Change it", "Send back to Claude", "All N above look right"; works without JavaScript | `app/domain/translationapp`, `templates/steward-translations.html` | |
| The skill and its curl wrapper; the MCP server's guide | `.claude/skills/stewards-api/SKILL.md` (translating section), `scripts/stewards-api`, `mcpapp/guide.md` | Rewritten for this app's words and languages |

## Storage

All in existing stores except one new table; every later column arrives as
`AddColumn` beside its `CREATE`, with the old-schema test CLAUDE.md requires.

- `ui_strings` gains **`pages TEXT NOT NULL DEFAULT ''`**: the templates a
  string appears on (`statement.html`, `signin-code.txt`), written at every
  `Register`. The renderer's extraction already knows the file; this is the
  "where" that tells Claude and the reviewer whether "Close" is a button or a
  month-end.
- `ui_translations` gains **`origin TEXT NOT NULL DEFAULT ''`** (`claude` or
  `person`) and **`note TEXT NOT NULL DEFAULT ''`** (a reviewer's reason for
  sending it back). `updated_by` is already there.
- **`translators(user_id, lang, granted_by, granted_at)`**, primary key
  `(user_id, lang)`, in `translationdb`. The site administrator is not listed;
  they may review every language.
- **`api_keys`** and **`oauth_grants`** in `userdb`, as stewards has them.

## The rules (translationbus)

- **Claude's translations are live at once.** Through the API a translation
  becomes `draft`, `origin = claude`, and is shown on pages straight away (the
  catalogue reloads after every batch). `draft` means only "no person has
  looked at it yet", never "not ready". Each item stands alone: a refusal does
  not stop the rest.
- **Refused:** an unknown string or language; empty text, or longer than four
  times the English plus 100 characters; a set of `{placeholders}` that differs
  from the English's (order may change, as word order does); `<` or `>` the
  English does not have; and any row a person has approved, unless sent
  unchanged.
- **A person looks over them, afterwards.** On the review screen, a reviewer
  of that language can:
  - *Looks right*: `approved`, the note cleared.
  - *Change it*: their text, checked by the same rules, `approved`,
    `origin = person`.
  - *Send back*: a note (up to 500 characters), the row stays `draft` and goes
    first in Claude's next list.
- **What Claude is asked for:** pending rows and sent-back drafts, sent-back
  first, of strings the running binary still uses (`last_seen` at the latest
  startup), with their context, pages, the current text and note, and a
  glossary of the approved translations of the app's own terms (below).
- **Who may hold a key** is the same as who may review: the administrator
  and translators. A key acts as its person and reaches nothing but
  `/api/v1`, which holds nothing but translations. **No endpoint under
  `/api/v1` may ever read an organization, account, project, statement,
  receipt or export**; a later API for those is a plan of its own, with
  scopes. This is the line that keeps a key on a laptop from being a key to
  somebody's books.

## The API

Bearer `rcn_…` only; a cookie is never read. JSON, 64 KB a request.

- `GET /api/v1`: the index: every endpoint, its method, its parameters,
  and its MCP tool name.
- `GET /api/v1/translations/pending?lang=es&limit=50` (at most 200), tool
  `list_pending_translations`:
  `{"lang":"es","pending":[{"context":"","en":"…","pages":["statement.html"],"current":"…","note":"…"}],"remaining":312,"glossary":[{"en":"Reconcile","text":"Conciliar"}]}`
- `PUT /api/v1/translations`, tool `put_translations`, at most 200 items:
  `{"lang":"es","translations":[{"context":"","en":"…","text":"…"}]}` →
  `{"results":[{"en":"…","context":"","outcome":"created|updated|unchanged|refused","problem":"placeholders"}]}`
- `GET /api/v1/translations?lang=es&status=draft|approved&limit=100&offset=0`
  (at most 500), tool `list_translations`: for reading back what is there.

Each batch writes one log line: who, which language, how many of each
outcome.

## The pages

- **Profile**: "Keys for Claude" (administrator and translators only): make
  one with a name, see its secret once, see when each was last used, revoke.
  A connection made through claude.ai is listed here too, by its client's
  name, and revoked the same way.
- **`/translations`** (administrator and translators): one language at a time,
  the drafts to check first, 50 a page, each with the English, the
  translation, who wrote it, the pages it is on and any note, and the three
  actions. Counts at the top: pending, to check, sent back, approved. A
  translator sees only their languages.
- **Admin page**: translators: add by email address and language, remove.
- **OAuth consent** (`/oauth/authorize`): "claude.ai wants to translate
  Reconcile as you", for the administrator and translators; anyone else is
  told they cannot connect.

## Style, for the skill and the MCP guide

- Spanish: Latin American, *tú*, plain. Portuguese: Brazilian, *você*, plain.
  Short sentences, no exclamation marks, as `docs/design.md` asks of the
  English.
- Keep every `{placeholder}` exactly, moved to where the sentence needs it.
- Money words follow what a parish treasurer in Latin America or Brazil says,
  and the glossary keeps them the same everywhere once approved. A first
  glossary for the skill to start from, for a reviewer to correct:

  | English | Spanish | Portuguese |
  |---|---|---|
  | statement | estado de cuenta | extrato |
  | reconcile / reconciled | conciliar / conciliado | conciliar / conciliado |
  | receipt | comprobante | comprovante |
  | category | categoría | categoria |
  | project | proyecto | projeto |
  | money in / money out | entradas / salidas | entradas / saídas |
  | bookkeeper | contador auxiliar | auxiliar de contabilidade |
  | accountant | contador | contador |
  | owner | responsable | responsável |
  | sorted / not sorted yet | clasificado / sin clasificar | classificado / não classificado |

- A string that is a fragment ("{n} parts") is translated as a fragment; a
  doubt is reported in the summary rather than guessed in the text.

## CLAUDE.md

A new rule beside section 6:

> **One exception: translations.** An agent may call the public `/api/v1`
> translation endpoints, and only those, when a person asks it to, with the
> key in `RECONCILE_API_KEY` and the site in `RECONCILE_URL` — set by that
> person in their environment, never written to a file. What it writes is
> live at once; it never overwrites a translation a person has approved or
> corrected, because that is the person's word on it.

and `.claude/settings.json` keeps denying everything else it denies now.

## Build order

Two pull requests, Claude's way in first, so that the pages are in Spanish
and Portuguese as soon as possible:

1. **Claude's way in.**
   - The schema changes and their old-schema test: `pages` from the
     extraction, and `origin` and `note`.
   - API keys and their screen, `/account/keys`, for the administrator.
   - `/api/v1` and its three endpoints.
   - The MCP server and OAuth.
   - The skill, `scripts/reconcile-api`, the MCP guide, and the CLAUDE.md
     exception.

   After it deploys, the administrator makes a key or connects claude.ai,
   asks Claude to translate, and the pages are in Spanish and Portuguese.
2. **Looking them over.**
   - The translators table and its admin page; translators may then hold keys
     too.
   - The review rules and `/translations`.

"Spanish and Portuguese go live" is not a switch: the language chooser is
already there, and a translation is shown the moment it is written. They are
live when Claude has filled them, after the first pull request.

## Verification

- `translationbus`:
  - every refusal (placeholders, markup, length, approved, unknown, stale);
  - send-back ordering;
  - a translator limited to their languages;
  - the catalogue reloading after a batch.
- `translationdb` and `userdb`: `Init` over the schema as it stood before,
  written out literally.
- `app/sdk/muxer`:
  - the review flow end to end;
  - a key that reaches `/api/v1` and nothing else, with no cookie accepted on
    `/api/v1`;
  - a signed-in non-translator refused the screen, the keys and the consent;
  - every new route in the stranger walk.
- MCP: the tools listed match the index; a call without a bearer gets 401 with
  the resource metadata; a call with one writes drafts.
- OAuth: PKCE required; a client from a host that is not Anthropic's refused;
  a code used twice refused; a non-translator refused at consent.
- `make vuln-check`, for the new dependency.
