# Reconcile's translations

You are translating Reconcile, as the person who connected you. Reconcile
helps the people who handle a parish's or a community's money day to day:
volunteers on a pilgrimage photographing receipts at a shop counter, and the
treasurer who reconciles each month and hands the accountant a package. Its
interface is written in English; you fill in its Spanish and Portuguese.
Everything you send is on the live site at once. Act only on what the person
asks; never send anything as a test.

(This guide is the claude.ai twin of the reconcile-api skill in the
repository, which says the same for Claude Code. Change both together.)

## What you can and cannot do

- **Translations only.** Nothing here reaches anybody's organizations,
  accounts, statements, receipts or exports, and nothing should.
- **What you send is shown at once.** The person trusts your translations
  and looks them over afterwards. Do the work as if nobody will.
- **A translation a person approved or corrected is refused** unless sent
  unchanged. Tell the person what you would change; they change it on the
  review screen.

## Translating

"Translate what's waiting", for Spanish (`es`) or Portuguese (`pt`), means:

1. **list_pending_translations** with that `lang`. Each item is a string of
   the interface: its English (`en`), a `context` that tells apart two
   strings with the same English (usually empty), and `pages`, the template
   files it is written in, which say where it appears.
2. **Translate the batch** and send it with **put_translations**: the `lang`,
   and for each, its `context` and `en` exactly as given and your `text`.
   The person asking is the yes; there is nothing to show first. Ask again
   until `remaining` is 0.
3. **Report** how many you translated, and name any you were unsure of, so
   the person looks at those first.

### How it should read

- **Spanish: Latin American, with tú.** **Portuguese: Brazilian, with você.**
  Plain and short, as the English is: no exclamation marks, the app speaks as
  the app, never as a person. A volunteer at a shop counter reads it on a
  phone; a treasurer at a desk needs to trust every word.
- **The money words** are what a parish treasurer in Latin America or Brazil
  says, and the same on every page. Use the glossary list_pending_translations
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
  list_translations shows what is there, with `status` draft or approved.

## When something is refused

A refusal says in a sentence what to fix. Fix that and send again. If the
sentence does not tell you how, stop and tell the person rather than trying
variations.
