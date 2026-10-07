# Designing what a person sees

Read this before changing anything a person sees. `docs/plan.md` decides what
the app does; this decides how it feels to use it.

## Who is using it

Two kinds of people, and the first one matters more.

- **The person on the ground.** A parish volunteer, a youth leader on a
  pilgrimage, a sister who keeps the house's card. They have a phone, a
  receipt in their hand, and no training in bookkeeping. Their whole job here
  is: get the receipt in, say what it was for, and see how much is left.
- **The person who answers for the money.** The treasurer, the business
  manager, the accountant. They import statements, split and categorise,
  reconcile at the end of the month, and download the package. They are at a
  desk, and they need to trust every number.

The test for a screen: **a volunteer who has never seen the app, standing at a
shop counter, can photograph three receipts into the right project in under a
minute, and a month later the treasurer can tell from the screen alone which
charge each one belongs to.**

## Principles

- **Phone first.** Every screen works one-handed at 360 px wide before it is
  arranged for a desk. Upload is a big button that opens the camera and the
  photo library, and takes several files at once.
- **Plain words.** "Money in" and "money out", not debit and credit. "Waiting
  for a match", not unreconciled. A term an accountant needs — reconciled,
  category, statement — is used exactly, and the first time it appears on a
  screen it says what it means in a few words.
- **Every number says where it came from.** A balance names the statement it
  was read from. A total names what it adds up. A transaction shows its
  statement, its receipts and who changed it last. The point of the app is
  that an accountant can believe it.
- **Verified, or plainly not.** A statement that checked out against its
  balances says so; one that could not be checked says that too, in words,
  rather than hiding the difference behind a colour.
- **Nothing is lost by accident.** Uploads are never discarded silently. A
  mistake is undone with the button next to it, not by asking somebody. A
  reconciled month is locked, and unlocking it asks for a reason that is kept.
- **Works without JavaScript.** Every flow is a form that posts and a page
  that answers. Script makes uploading several photos nicer, and is never
  required for it. No build step, no npm (the same rule as stewards).
- **Money looks like money.** Always two decimals, the currency symbol, money
  out in the same colour everywhere *and* with a minus sign, so it reads in
  black and white and to a screen reader. Amounts align on the decimal point
  in tables.
- **Every word goes through `t`.** No English sentence is written into a
  template or a handler without it, so Spanish and Portuguese can follow. A
  sentence is translated whole; never build one by joining fragments.
- **Errors say what to do.** "That file is a photo of a receipt, but the
  account needs a statement. Statements come from your bank's website as CSV."
  — never a status code, never a Go package name.

## Language

The app speaks as **the app**, never as a named person. It addresses the
reader as "you". Short sentences. No exclamation marks.
