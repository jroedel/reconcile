# Using Reconcile from Claude

Claude on claude.ai can read your books on Reconcile as you: how a month's
statements checked, what is still to sort, what a sorting rule would do
before you save it, and what an amount is made of. It sees what you can see
on the site and nothing else, and it answers with the app's own figures
rather than its own arithmetic.

For now it only reads. It cannot change anything in your books, import a
statement, or download a statement or receipt; when it would sort a charge,
save a rule or import from your inbox, it says so and links you to the page
where you do it. (Sorting, rules, receipts and importing through Claude
come next, `docs/books-api.md`. Reconciling a month, reopening one, and
removing a statement or receipt stay yours, on the site.)

## Connecting it

Once, on claude.ai in a browser:

1. **Add Reconcile as a connector.** In *Settings → Connectors*, choose
   *Add custom connector*. Name it `Reconcile`, and give it the site's
   address followed by `/mcp`, such as `https://books.example.invalid/mcp`.
   On a Team or Enterprise plan, an owner of the plan may need to add it
   first, under the organization's settings; you then connect to it
   yourself.
2. **Connect.** Choose *Connect*. Claude opens Reconcile, which asks you to
   sign in if you are not already, and then asks whether claude.ai may read
   your books as you. Choose *Allow*, and you are back on claude.ai.
3. **Turn it on in a chat.** In a conversation, Reconcile is among the
   connectors in the tools menu under the message box. Claude asks before
   it uses a tool the first time; every Reconcile tool reads only, so
   allowing them always is safe.

Once connected on the web, it works the same in the Claude apps on a phone.

If you also translate the site, the same connection translates: the consent
page says so.

## Things to ask

Start with the month in front of you, in your own words:

- "How did September's import go?" Claude reads your inbox and each
  account's months: which statements checked against their balances, which
  months are missing or partly covered, and what is not sorted yet.
- "What's left to sort in the checking account this month?" Each charge
  with what the app would suggest, and the categories and projects there are
  to choose from.
- "Would a rule for *grocery* sorted to Food catch the right charges?"
  Claude tries the rule without saving it, and says what it would sort, what
  a longer rule sorts instead, and where it would disagree with another.
- "What did the card payment on 3 October cover?" Claude finds the payment
  and its explanation, last month's for comparison, the charges that may
  belong in it, and on a card whose statements come one file per holder,
  which holders' months are in. It says the difference the app gives, and
  what may account for it.
- "Which receipts are waiting for a match?" Each with the charges it may
  belong to.
- "How does the pilgrimage stand?" A project's income and expenses, from
  every account it draws on.

Everything Claude mentions comes with a link to its page, so you can see
where a number came from.

With the [Gmail script](../apps-script/gmail-inbox/README.md) sending your
statements to your inbox, "what came in by email?" is where a month starts.

## What it cannot see

Only what you have been given. An organization, account or project you hold
no role on is, to Claude, not there: it is told "not found", as for one that
does not exist. Asking Claude for somebody else's account is not a way
round that, and neither is anything written in a bank's description or a
receipt, which Claude is told to read as data, never as instructions.

## Ending it

The connection is a key that lasts 90 days, listed on *Your account → Your
API keys* as "connected from Claude". *Revoke* ends it at once. When it runs
out, Claude asks you to connect again. Removing the connector on claude.ai
stops Claude using it, but revoking it on the site is what ends the key.

What Claude did through the key is in the history of each thing it touched,
marked as done through it.

## Other programs

A program of your own, such as Claude Code on your computer, can read the
same with a key: on *Your API keys*, make one for **Reading your books**,
and send it as `Authorization: Bearer <key>` to the site's `/api/v1`, which
lists everything it takes. The key reaches only what you may reach
yourself; keep it out of any file that is shared or committed.

## When something goes wrong

- **"This site connects only to Claude."** The link came from another
  program, and nothing was sent to it. Start again from claude.ai.
- **Claude says something is not found** that you can see on the site.
  Make sure you are connected as the same person you sign in as; the
  consent page names the email.
- **Claude stops working after a few months.** The key ran out or was
  revoked. Disconnect and connect again in *Settings → Connectors*.
