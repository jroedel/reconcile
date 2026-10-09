# Working in this repository

`AGENTS.md` is a link to this file, so every agent reads the same rules.

This app helps the people who handle a nonprofit's money day to day
understand it between the accountant's reports, and talk with the
accountant about it; it does not replace the bookkeeping software or the
accountant (`README.md`, which says what income, an expense, a transfer and
pass-through are). They upload bank and card
statements, which are read and checked against their own balances; they
attach receipts, split transactions across categories and **projects** (a
pilgrimage, a building fund) that draw from several accounts, reconcile each
month, and hand the accountant a package. Anyone may sign up; nobody, not
even the site administrator, sees an account they were not given.

**Read `docs/plan.md` before changing behaviour**, and `docs/design.md` before
changing anything a person sees. The plan's build order says what comes next.
The design's test decides most arguments: a volunteer at a shop counter can
photograph three receipts into the right project in under a minute, and a
month later the treasurer can tell which charge each belongs to.

**Statements, receipts and account numbers never enter this repository.**
Fixtures are invented and live in a `testdata/` directory; `scripts/data-guard`
refuses the rest, as the pre-commit hook (`make hooks`) and in CI.

It is written in Go, it is a **public** repository under the Apache-2.0
license, and it ships by a push to `main` that CI deploys to the Hetzner
konsoleH account shared with `/opt/projects/mass-intentions` and
`/opt/projects/stewards`. The scaffolding -- Makefile, deploy, secrets, CI,
these rules -- was imported from stewards; where a rule is about this
repository rather than about Go, it is marked as such.

**How to write Go here** is section 0 below and
`.claude/skills/writing-go/SKILL.md`, following
[ardanlabs/kronk](https://github.com/ardanlabs/kronk) by way of the sibling
projects.

**How to move around this repository** is sections 1–5. They close loops that
are a property of Go and of agents rather than of any one repository: a
compiler diagnostic already knows where the problem is, and gopls already knows
where a symbol is declared. When one of them stops paying here, delete it
rather than obey it out of habit.

**Who does what** is section 6, and it is the part that is enforced rather
than asked for.

## 0. Touching Go: load the skill, then run the check

**Before reading, writing or modifying any `.go` file**, load
`.claude/skills/writing-go/SKILL.md` — the modern-Go rules for the version in
`go.mod`. This is kronk's mandatory-skill rule and it is the one directive here
that is not negotiable.

**After modifying any `.go` file**, on the package you changed:

```sh
make go-check PKG=./cmd/reconcile
```

which is `gofmt -s -w`, `go vet`, `staticcheck`, `go build ./...` and the
package's tests. All must pass. **Fix the code; do not suppress the
diagnostic.**

One package rather than `./...`, deliberately. This module is new enough that
`staticcheck ./...` is clean; keep it that way and the bound costs nothing, and
the day it is not clean the rule is already in place.

`make dev-tools` installs `staticcheck` and `gopls`, both pinned.

Three things kronk says that are worth repeating verbatim: be concise; verify an
API from the live toolchain or the docs rather than from recall; double-check
the arguments of a tool call before submitting it.

## 1. A diagnostic is an address. Go to it.

`go build`, `go vet`, `go test` and `gofmt` all report `file.go:line:col`. That
is the answer to "where", and no search is needed to find it. Read the region
around the reported line directly — `sed -n '<line-8>,<line+8>p'` on the file it
named — rather than grepping for the identifier it mentions.

## 2. Find a Go symbol with `make sym`, not with grep

```sh
make sym NAME=Serve
make sym NAME=web.Serve
make outline FILE=foundation/web/serve.go
```

Both are `gopls`, which answers from the same type information the compiler
uses: it knows which `Store` is a method on which type, and it brings the doc
comment with it. There is also a gopls MCP server in some setups
(`mcp__gopls__go_search`, `go_symbol_references`, `go_file_context`); when it is
available it is the same answer without the shell.

**grep is still right** for everything that is not a Go symbol: a config key, a
SQL column, a string in a template, a word in the plan. The rule is about the
declaration of an identifier, which is what gopls is exact about.

## 3. One verify command, and its output is already filtered

```sh
make test-unit     # the tests, with -race. No network.
make lint          # go vet + gofmt check
make test          # both, plus the shell tests
make vuln-check    # govulncheck. Needs the network.
make go-check PKG= # the after-editing-Go loop, on one package
make release       # the static linux/amd64 binary the server runs
```

`make test-unit` counts the passing packages instead of listing them, so **there
is nothing to pipe it through**. A filter written in a hurry is one that
eventually hides a `FAIL`; if pass-noise ever comes back, fix `scripts/go-test`
rather than the command line.

## 4. Formatting is part of verifying, not a step of its own

`make lint` fails when something is not gofmt-clean and names the files.
`make fmt` fixes them. Running `gofmt -l` speculatively is not useful.

## 5. Where things live

```
cmd/reconcile/                      the server's main, and its config
app/domain/<x>app/                  HTTP handlers and view types. No business rules.
app/sdk/                            the plumbing under them: muxer, page, mid
business/domain/<x>/<x>bus/         the rules. This is where behaviour is.
business/domain/<x>/stores/<x>db/   storage, per domain
business/types/                     small value types: ID, Email, money.Amount
foundation/                         no domain knowledge: web, sqldb, logger, imaging, pdftext
deploy/                             how it reaches the server. Run by CI, not by agents
scripts/                            how this is built and checked
```

So far the domains are `translation` (the interface's strings), `user` (who
signs in and how), `tenancy` (organizations, accounts, projects, and who may
do what on each), `event` (the history, written by the domain that made
the change, in its transaction), `file` (what people upload, bytes kept once
per content outside anything Apache serves), `importing` (reading a bank's
CSV, OFX or PDF into records, knowing nothing of accounts), `category` (each
organization's list, or a personal account's, each category one of the four
kinds of money), `ledger` (statements,
transactions and their splits: checking, deduplicating, storing, sorting,
and a project's book; reconciling, which locks a period; applying
sorting rules; and explaining one amount by the others that make it up), `rule` (an account's sorting rules: text to look for and
what a charge that has it is sorted into), `budget` (what owners expect
a project's, or an organization's year's, income and expenses to be,
compared with what happened), `receipt`
(the photos and PDFs that justify a charge, waiting in an account's or
project's inbox until a person matches them) and `export` (the
accountant's zip, read across the others and stored nowhere). The pages
over them are `homeapp`, `authapp`, `tenancyapp`, `ledgerapp`,
`categoryapp`, `ruleapp`, `budgetapp`, `receiptapp`, `exportapp`, `adminapp` and
`translationapp` (the review screen); a program's ways
in are `apiapp` (`/api/v1`, translations only), `mcpapp` (the same as tools
on `/mcp`) and `oauthapp` (how Claude on claude.ai gets a key). The rest is
where the plan's domains go when they arrive.

**Access is asked of tenancybus, every time.** A business method that reads
or changes something in an organization takes the actor and answers
`ErrNotFound` for a scope they hold no role on -- the same answer as one that
does not exist, so a page never confirms somebody else's organization is
there. `app/sdk/muxer/tenancy_test.go` walks every route as a stranger; a new
route goes in its lists.

The rule that makes the layering worth having: **an App package never imports
another App package, and nothing in `foundation/` knows a domain word.** When
two apps need the same thing it moves down a layer.

A question about *behaviour* starts in `business/domain/…bus`. A question about
*a status code or a form field* starts in `app/domain/…app`. A question about
*how it is stored* starts in the matching `stores/…db`.

## 6. Who does what: agents write PRs, people merge and run production

**Here, and not negotiable.**

- **The human in the loop is the pull request, at a minimum.** Everything in
  `git` is a pull request on a branch. An agent opens it; a person reviews and
  merges it. Nothing is committed or pushed to `main` directly, and an agent
  never merges its own PR.
- **A merge to `main` is a deploy.** CI runs the checks and, if they pass,
  ships to the server. So the review is the last gate there is — a PR
  description says what the change does to the running app, not only to the
  code.
- **Agents never touch the production server.** No `ssh`, `scp` or `rsync` to
  it, no `deploy/deploy.sh`, and none of the `make deploy*` or `make prod-*`
  targets. Those exist for a person at a terminal; `make` is how that person
  talks to production, and it is the only way. If a task seems to need
  something from production — a log line, the state of the database — say
  what you need and why, and let the person run the target and paste the
  answer.
- **Agents never set GitHub secrets or variables**, and never read
  `secrets.env` or `config.toml`. `DEPLOY_*` are GitHub secrets, `APP_*` are
  GitHub variables, and runtime credentials go to the server over ssh from a
  person's machine, never through CI.
- **This repository is public.** No hostname, account name, server path,
  credential or real record goes into a tracked file, a test fixture, a
  commit message or a PR description. They belong in `secrets.env`. Fixtures
  are invented, and hosts in tests are under `.invalid`.

**One exception: translations.** An agent may call the public `/api/v1`
translation endpoints, and only those, when a person asks it to, with the
key in `RECONCILE_API_KEY` and the site in `RECONCILE_URL` — the
environment the person started Claude Code in, with `make translate`
(`scripts/translate`): it takes the site from `secrets.env`'s `APP_HOST`
and the key from `~/.config/reconcile/api-key`, a file of the person's
outside the repository. An agent never runs it and never reads either
file, and never writes the key or the site's address anywhere. What it writes is
live at once; it never overwrites a translation a person has approved or
corrected, because that is the person's word on it. `scripts/reconcile-api`
is the way, and `.claude/skills/reconcile-api` says how to do it well
(`docs/translations.md`). That is a translator using the site, not an agent
operating the server: never as a test, never while developing, and never to
check a change.

`.claude/settings.json` denies these commands, so the rule holds even when it
is forgotten. Do not work around a denial — a denied command is the answer.

## When there is a database

These are the storage conventions from mass-intentions and stewards, where
each one was paid for. Plain SQLite through `modernc.org/sqlite`
(`foundation/sqldb`), pure Go so the release stays static. `deploy.sh` backs
up `reconcile.db` and snapshots `files/` on every deploy.

- **No migration tool.** Each store owns `Init(ctx, db)` holding idempotent
  `CREATE TABLE IF NOT EXISTS … STRICT` DDL, and exports
  `Expected sqldb.Expected`. `main` calls every `Init` in foreign-key order and
  then `sqldb.CheckSchema` once. A later column arrives as an `ALTER … DEFAULT`
  beside the `CREATE`, so a fresh database gets it from one and an existing one
  from the other.
- **Nothing that mentions a later column may sit in the `CREATE` block.** An
  index on one goes in a second `Exec` *after* the `AddColumn` call. On a fresh
  database it builds and every test passes; on a database that predates the
  column it runs against a column that is not there yet. This took
  mass-intentions' production down through four deploys, and no test could see
  it, because every test starts from an empty directory.
- **A constraint removed from a `CREATE` block is still there on every database
  that already exists.** SQLite has no `ALTER` that drops one; removing a
  `CHECK` means rebuilding the table, guarded by a `sqlite_master` lookup.
- **A store with a later column owns a test that runs `Init` over the schema as
  it stood before** — the old DDL written out literally, not derived from the
  new one.
- **`/healthz` re-checks the schema on every call**, rather than pinging
  (`app/sdk/health`). The deploy rolls a release back on what it says, and a
  binary rolled back onto a newer schema must report unhealthy. A new store
  adds its `Init` to `prepare` and its `Expected` to `expectedSchema`, both in
  `cmd/reconcile/main.go`.
- **Timestamps are Unix milliseconds in INTEGER columns**, never text.
- **A single-use claim is one statement**, `UPDATE … WHERE x IS NULL` or
  `INSERT … ON CONFLICT DO NOTHING`, never a read followed by a write.

## Words on a page

Every word a person reads goes through `t`, in the template:
`{{t "Upload receipts"}}`, `{{t "{count} waiting" "count" .N}}`, and
`{{tc "month-end" "Close"}}` when one English word needs two translations.
The English is the key, so it must be a literal: the renderer reads every
`t` out of the parse trees at startup and refuses to start on one it cannot
read (`app/sdk/page/extract.go`). Each string is registered in `ui_strings`
with a pending Spanish and Portuguese translation, which Claude fills
through the translation API and a translator looks over at `/translations`
(`docs/translations.md`). A sentence is
translated whole; never build one by joining fragments, and never put markup
inside one — two strings and the link between them.

Handlers pass data, not sentences. A rule's error reaches a person through a
template that says it, not as the error's own text: authapp's handlers set
`view.Problem = "bad-email"` and `templates/partials/problems.html` says it.
Mail works the same way: `mail/<name>.txt` in an app defines "subject" and
"body" with `t`, and `page.Renderer.Mail` writes it in the reader's language.
So do words that land anywhere else, such as a spreadsheet's header row:
`text/<name>.txt`, written by `page.Renderer.Text`.

## House style, in one paragraph

Comments explain **why**, at length, and in prose — including what was tried and
rejected, and what a reader would otherwise assume. Match the density of the
file being edited rather than the density of a tutorial. Error sentences that
reach a person say what to do about it and never name a Go package.
