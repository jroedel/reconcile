---
name: reconcile-api
description: Translate the Reconcile site's interface into Spanish and Portuguese through its live translation API, as the person whose key is in RECONCILE_API_KEY. Use when the person asks to translate what is waiting, or to revise some translations — not for developing the app.
---

# Translating Reconcile through its API

(Claude on claude.ai uses the same API as tools on `/mcp`, and is given the
same guidance from `app/domain/mcpapp/guide.md`. A rule changed here is
changed there too.)

Reconcile helps the people who handle a parish's or a community's money day
to day: volunteers on a pilgrimage photographing receipts at a shop counter,
and the treasurer who reconciles each month and hands the accountant a
package. Its interface is written in English; you fill in its Spanish and
Portuguese. Everything you send is on the **live site** at once.

This is the one thing an agent may do to production (CLAUDE.md, "One
exception: translations"): call the translation endpoints, when the person
asks, and nothing else. Never as a test, never while developing the app, and
never to check a change you made to the code.

## Start from the index, every time

```sh
scripts/reconcile-api GET /api/v1
```

It lists every endpoint, the fields each takes and the rules, built from the
running binary's own route table; trust it over this file. Read it before the
first call in a session.

## The key and the site

`scripts/reconcile-api` reads the key from `RECONCILE_API_KEY` and the site
from `RECONCILE_URL`, both in the environment the person started Claude
Code in -- with `make translate`, which reads them from where the person
keeps them -- and hands the key to curl without putting it on a command
line.

- Never print it, echo it, write it to a file, or put its value in a command.
  Refer to it only as `$RECONCILE_API_KEY`, and only through the script.
- Never look for a key anywhere else, never run `make translate` or
  `scripts/translate`, and never read `secrets.env`, `config.toml` or
  `~/.config/reconcile/` (CLAUDE.md §6). If the script says a variable is
  not set, or the API answers 401 or 403, stop and tell the person: a
  translator makes a key at `/account/keys` on the site, for "Translating
  this site", puts it in
  `~/.config/reconcile/api-key`, and starts Claude Code with
  `make translate`.
- Never write the site's address into a file here. The repository is public.

## What you can and cannot do

- **Translations only.** Nothing under `/api/v1` reaches anybody's
  organizations, accounts, statements, receipts or exports, and nothing
  should.
- **What you send is shown at once.** The person trusts your translations
  and looks them over afterwards. Do the work as if nobody will.
- **A translation a person approved or corrected is refused** unless sent
  unchanged. Tell the person what you would change; they change it on the
  review screen.

## Translating

"Translate what's waiting", for Spanish (`es`) or Portuguese (`pt`), means:

1. **List it:**

   ```sh
   scripts/reconcile-api GET '/api/v1/translations/pending?lang=es&limit=100'
   ```

   Each item is a string of the interface: its English (`en`), a `context`
   that tells apart two strings with the same English (usually empty), and
   `pages`, the template files it is written in. Open those files in
   `app/domain/*/templates/` when a string's sense is not clear from its
   words: you have the source, and it shows exactly where the words sit.
2. **Translate the batch** and send it, as
   `{"lang": "es", "translations": [{"context", "en", "text"}]}` with
   `context` and `en` exactly as given. Write the batch to a file in your
   scratchpad, not in the repository:

   ```sh
   scripts/reconcile-api PUT /api/v1/translations --json @"$SCRATCH/es-1.json"
   ```

   The person asking is the yes; there is nothing to show first. Each result
   is `created`, `updated`, `unchanged` or `refused` with a `problem`. Ask
   again until `remaining` is 0.
3. **Report** how many you translated, and name any you were unsure of, so
   the person looks at those first.

### How it should read

- **Spanish: Latin American, with tú.** **Portuguese: Brazilian, with você.**
  Plain and short, as the English is: no exclamation marks, the app speaks as
  the app, never as a person. A volunteer at a shop counter reads it on a
  phone; a treasurer at a desk needs to trust every word.
- **The money words** are what a parish treasurer in Latin America or Brazil
  says, and the same on every page. Use the glossary the pending list
  returns; until a term is in it, start from these:

  | English | Spanish | Portuguese |
  |---|---|---|
  | statement | estado de cuenta | extrato |
  | reconcile / reconciled | conciliar / conciliado | conciliar / conciliado |
  | receipt | comprobante | comprovante |
  | category | categoría | categoria |
  | project | proyecto | projeto |
  | money in / money out | entradas / salidas | entradas / saídas |
  | transaction | movimiento | movimentação |
  | bookkeeper | contador auxiliar | auxiliar de contabilidade |
  | accountant | contador | contador |
  | owner | responsable | responsável |
  | sorted / not sorted yet | clasificado / sin clasificar | classificado / não classificado |

- **Keep every `{placeholder}` exactly as it is**, untranslated: the app
  fills it in. Move it where the sentence needs it: "{n} receipts waiting for
  a match" is "{n} comprobantes esperando a ser asociados" or wherever the
  number reads naturally.
- **No markup.** A translation is text; `<` and `>` the English does not have
  are refused.
- **Read `pages`.** A short string is often a button, a heading or a badge:
  "Close" on a month-end page is a month being closed, not a window. A
  string that is a fragment ("{n} parts", "not sorted yet") is translated as
  a fragment that fits where it is shown.
- **One sent back** has the reviewer's `note` and the translation there now
  (`current`). Translate it again with the note in mind, or send the same
  words if they are right, which answers the note; say which in your report.
- **Dates and amounts** are filled in by the app; leave their placeholders
  alone and do not write currency symbols of your own.
- Translate only when the person asks. To revise some at their request,
  `GET '/api/v1/translations?lang=es&status=draft'` shows what is there, with
  `status` draft or approved.

## When something is refused

A refusal of the whole request is `{"error": {"field": "…", "problem": "…"}}`,
and a refused translation in a batch has its `problem`; either is a sentence
saying what to fix. Fix that and send again. If the sentence does not tell
you how, stop and tell the person rather than trying variations.
