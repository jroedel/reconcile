# Using Reconcile from Claude

Claude on claude.ai can keep your books on Reconcile as you: say how a
month's statements checked and what is still to sort, import what your
email brought, sort it, write sorting rules and try them first, attach
receipts, and work out what an amount is made of. It sees what you can see
on the site and nothing else, it changes only what your role lets you
change, and it answers with the app's own figures rather than its own
arithmetic.

It proposes and you decide: it says what it would sort, which rule it
would write and what that rule would catch, which receipt goes on which
charge, and makes the change when you say yes. Importing is the exception:
when you ask it to import, it imports what checks against its own figures,
and leaves the rest waiting with the reason.

Some things stay yours, on the site, and Claude has no way to do them:
reconciling a month, reopening one, removing a statement or a receipt,
accepting the difference left in an explanation, and entries by hand. It
cannot download a statement or a receipt either.

## Connecting it

Once, on claude.ai in a browser:

1. **Add Reconcile as a connector.** In *Settings → Connectors*, choose
   *Add custom connector*. Name it `Reconcile`, and give it the site's
   address followed by `/mcp`, such as `https://books.example.invalid/mcp`.
   On a Team or Enterprise plan, an owner of the plan may need to add it
   first, under the organization's settings; you then connect to it
   yourself.
2. **Connect.** Choose *Connect*. Claude opens Reconcile, which asks you to
   sign in if you are not already, and then asks whether claude.ai may keep
   your books as you. Choose *Allow*, and you are back on claude.ai. To let
   it read and change nothing, tick *Only let it read* first.
3. **Turn it on in a chat.** In a conversation, Reconcile is among the
   connectors in the tools menu under the message box. Claude asks before
   it uses a tool the first time. The reading tools are safe to allow
   always; for the ones that change something, being asked each time is a
   second look before anything is saved, and allowing them always is fine
   once you trust how it works.

Once connected on the web, it works the same in the Claude apps on a phone.

If you also translate the site, the same connection translates: the consent
page says so.

## Things to ask

Start with the month in front of you, in your own words:

- "Do this month's books." Claude imports what your email brought, says
  how each statement checked, proposes how to sort what is left and which
  rules would save you the work next month, and, once you agree, sorts,
  writes the rules and attaches the receipts that match. It ends with what
  it changed and what is left for you, with the links.

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
  which holders' months are in. With your yes it gathers the charges into
  the payment's explanation, and says the difference the app gives and what
  may account for it.
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

## Checking what it did

Everything Claude changes is marked on the site until you look at it:

- A transaction it sorted says *by Claude* on the month list, and the month
  says how many are *sorted by a program, to check*, with a link that shows
  only those. Saving the transaction yourself, changed or not, says you
  checked it, and the mark goes.
- A rule it wrote says *by Claude* on the rules page, until you save it
  there.
- Every change is in the history of what it touched, marked as done
  through Claude.

Ask Claude "what did you change today?" and it reads the same history.
Anything it got wrong can be put right on the page, as anything else is, or
by asking Claude to undo it.

## Ending it

The connection is a key that lasts 90 days, listed on *Your account → Your
API keys* as "connected from Claude". *Revoke* ends it at once. When it runs
out, Claude asks you to connect again. Removing the connector on claude.ai
stops Claude using it, but revoking it on the site is what ends the key.

## Other programs

A program of your own, such as Claude Code on your computer, can do the
same with a key: on *Your API keys*, make one for **Reading your books**,
or **Keeping your books** to let it change what you may, and send it as `Authorization: Bearer <key>` to the site's `/api/v1`, which
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
