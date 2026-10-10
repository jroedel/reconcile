# Statements from Gmail to Reconcile

`Code.gs` is a Google Apps Script that sends the statements your email
brings to your inbox on Reconcile (`docs/books-api.md`, "The inbox"). It
runs in your own Google account, as you, once an hour. Google needs to
approve nothing, and Reconcile never holds a key to your mail.

What it sends waits under **Import several statements** until you import it,
or until your Claude does. It only sends; it cannot read anything on the site.

## Setting it up

### 1. Make a key

1. On the site, open *Your account → Programs → Your API keys*.
2. Under *Make a key*, choose **A script that sends statements to your
   inbox**, and name it something like "Gmail script".
3. Choose *Make a key*, and copy the key that starts `rcn_`. It is shown
   once; if you lose it, revoke it and make another. It lasts a year.

### 2. Label your statements

The script reads only mail labelled `reconcile`. It never guesses which mail
is a statement.

1. In Gmail, search for the emails your statements come in: from your bank,
   or from whoever sends you theirs, for example
   `from:statements@bank.example.invalid has:attachment filename:pdf`.
2. Choose the filter icon at the right of the search box, then
   *Create filter*.
3. Tick *Apply the label*, choose *New label…*, and call it `reconcile`.
4. Tick *Also apply filter to matching conversations*, so that what came in
   the last 60 days is sent too. Then *Create filter*.

You can also label one email by hand. A label belongs to the whole
conversation, so any other PDF, CSV, OFX or QFX attached in it is sent too.

### 3. Make the script

1. Open [script.google.com](https://script.google.com), signed in as the
   Gmail account the statements arrive in, and choose *New project*.
2. Rename it from "Untitled project" to something like "Reconcile Gmail
   inbox".
3. Open `Code.gs` in this folder on GitHub, choose *Raw*, and copy all of
   it. In the editor, replace everything in `Code.gs` with it, and save
   (Ctrl+S, or ⌘S).

### 4. Give it the address and the key

1. Choose the gear, *Project Settings*, in the left-hand bar.
2. Under *Script Properties*, choose *Add script property* twice:
   - `RECONCILE_URL`: the site's address, such as
     `https://books.example.invalid`
   - `RECONCILE_KEY`: the key from step 1
3. Choose *Save script properties*.

Neither goes in the code, so a new version of `Code.gs` can be pasted over
the old one without them.

### 5. Start it

1. Go back to the editor (`< >` in the left-hand bar).
2. In the list of functions at the top, choose `install`, then *Run*.
3. Google asks for permission. Choose *Review permissions* and your account.
   If it says "Google hasn't verified this app", choose *Advanced*, then
   *Go to Reconcile Gmail inbox*: it is your own script. Then *Allow*.

It asks to read and label your mail, to connect to an outside service (the
site), to send email as you (only to you, once, if the key stops working),
and to run on a schedule. The first run happens straight away, and then
every hour.

On a Google Workspace account, the administrator may have limited what
scripts may do. If authorizing is refused, or every run says the site could
not be reached, ask them.

## Telling it worked

- The editor's *Execution log* lists each file and what the site said:
  `received`, `already_waiting` or `already_imported`.
- *Triggers* (the alarm clock in the left-hand bar) shows `sendStatements`,
  hourly.
- In Gmail, a conversation whose statements were all sent has the label
  `reconcile/sent`.
- On the site, *Import several statements* says how many statements came in
  by email. *Check them and import* shows each with the account proposed for
  it: choose one where none was found, then *Import the ready ones*. One that
  needs a closer look keeps a link to its own page; *Put aside* takes one off
  the list without importing it.

## Day to day

- **To send now** rather than at the next hour, choose `sendStatements` in
  the list of functions, then *Run*.
- **Sending twice is harmless.** The site knows a file by its content, and
  the script remembers what it sent.
- **What it cannot send:** attachments not named `.pdf`, `.csv`, `.ofx` or
  `.qfx`; mail older than 60 days; and emails that only link to the bank's
  website. Upload those by hand. A PDF with a password arrives, but the site
  cannot read it.
- **When the key ends or is revoked**, the script emails you once and sends
  nothing until you put a new key in `RECONCILE_KEY`. Nothing is lost: it
  sends what it missed the next hour after that.
- **A file the inbox does not take** (larger than 10 MB, or empty) is logged
  and not tried again.
- **To update it**, paste the new `Code.gs` over the old one and save. The
  hourly run and the properties stay.
- **To stop it**, delete its trigger under *Triggers*, or revoke the key on
  the site.
- **Do not share the project.** Whoever can open it can read the key in its
  properties. The key only sends files to your inbox, but it is yours.
