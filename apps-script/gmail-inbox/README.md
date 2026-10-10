# Statements from Gmail to Reconcile

`Code.gs` is a Google Apps Script that sends the statements your email
brings to your inbox on Reconcile (`docs/books-api.md`, "The inbox"). It
runs in your own Google account, as you, once an hour. Google needs to
approve nothing, and Reconcile never holds a key to your mail.

What it sends waits under **Import several statements** until you import it,
or until your Claude does. It only sends; it cannot read anything on the site.

## Setting it up

1. **Label your statements.** In Gmail, make a filter for the emails your
   statements come in (from your bank, or from whoever sends you theirs) that
   applies the label `reconcile`. The script never guesses which mail is a
   statement: it reads only what has that label.
2. **Make a key.** On the site, at *Your account → Your API keys*, make a key
   for **A script that sends statements to your inbox**, named something like
   "Gmail script". Copy it; it is shown once. It lasts a year.
3. **Make the script.** At [script.google.com](https://script.google.com),
   make a new project, and paste `Code.gs` over what is there.
4. **Give it the address and the key.** In the project's *Project Settings*,
   under *Script Properties*, add `RECONCILE_URL` (the site's address, such as
   `https://books.example.invalid`) and `RECONCILE_KEY` (the key). Neither
   goes in the code.
5. **Start it.** In the editor, choose `install` and *Run*. Google asks for
   permission to read and label your mail, to connect to the site, and to
   send you an email; allow it. The first run happens straight away, and then
   every hour.

## Telling it worked

- The site's *Import several statements* page says how many statements came
  in by email, with a link to check and import them.
- In Gmail, a conversation whose statements were all sent has the label
  `reconcile/sent`.
- The script's *Executions* page logs each file and what the site said:
  `received`, `already_waiting` or `already_imported`.

## When something goes wrong

- **The key ended or was revoked.** The script emails you once, then stops
  sending until you put a new key in `RECONCILE_KEY`. Nothing is lost: it
  sends what it missed the next hour after that.
- **A file the inbox does not take** (larger than 10 MB, or empty) is logged
  and not tried again. Upload it by hand.
- **The site could not be reached.** It tries again the next hour.

It looks back 60 days, and attachments named `.pdf`, `.csv`, `.ofx` or
`.qfx` only. A statement that is not one of those is left alone.
