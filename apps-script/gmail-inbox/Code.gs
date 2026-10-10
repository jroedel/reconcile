/**
 * Sends the statements your email brings to your inbox on Reconcile, where
 * they wait for you, or your Claude, to import them.
 *
 * It runs in your own Google account, as you, once an hour. It looks only
 * at mail you have labelled "reconcile" (a Gmail filter does that), sends
 * each PDF, CSV, OFX or QFX attachment it has not sent before, and labels
 * the conversation "reconcile/sent" so you can see it went. Reconcile
 * recognises a file it already has by its content, so sending one twice
 * adds nothing.
 *
 * It holds no address and no key: both are Script Properties you set
 * (Project Settings, Script Properties):
 *
 *   RECONCILE_URL  the site's address, such as https://books.example.invalid
 *   RECONCILE_KEY  a key made at <site>/account/keys for "A script that
 *                  sends statements to your inbox". It may only send files;
 *                  it cannot read anything.
 *
 * Then run install() once from the editor, which asks for permission to
 * read your mail and starts the hourly run. README.md beside this file says
 * the rest. Nothing here changes or deletes a message but the label.
 */

const LABEL = 'reconcile';
const SENT_LABEL = 'reconcile/sent';
const KINDS = /\.(pdf|csv|ofx|qfx)$/i;
const DAYS = 60;

// Gmail ids of messages already sent, with when, kept in the script's own
// properties: a conversation can gain next month's statement after this
// month's was sent, so the label on it cannot be what says "done".
const SENT_KEY = 'sent';

// Set when the site refused the key, so that an expired key is one email to
// you rather than one an hour.
const REFUSED_KEY = 'refusedAt';

/** Starts the hourly run. Run it once; running it again changes nothing. */
function install() {
  ScriptApp.getProjectTriggers()
    .filter((t) => t.getHandlerFunction() === 'sendStatements')
    .forEach((t) => ScriptApp.deleteTrigger(t));

  ScriptApp.newTrigger('sendStatements').timeBased().everyHours(1).create();

  sendStatements();
}

/** One run: everything labelled and not yet sent. */
function sendStatements() {
  const props = PropertiesService.getScriptProperties();
  const site = (props.getProperty('RECONCILE_URL') || '').replace(/\/+$/, '');
  const key = (props.getProperty('RECONCILE_KEY') || '').trim();

  if (!site || !key) {
    throw new Error('Set RECONCILE_URL and RECONCILE_KEY under Project Settings, Script Properties.');
  }

  const sent = JSON.parse(props.getProperty(SENT_KEY) || '{}');
  const sentLabel = GmailApp.getUserLabelByName(SENT_LABEL) || GmailApp.createLabel(SENT_LABEL);
  const threads = GmailApp.search('label:' + LABEL + ' has:attachment newer_than:' + DAYS + 'd', 0, 100);

  for (const thread of threads) {
    let all = true;

    for (const message of thread.getMessages()) {
      const id = message.getId();
      if (sent[id]) {
        continue;
      }

      const outcome = sendMessage(site, key, message);

      if (outcome === 'refused') {
        refused(props, site);
        return;
      }

      if (outcome === 'done') {
        sent[id] = Date.now();
      } else {
        all = false;
      }
    }

    if (all) {
      thread.addLabel(sentLabel);
    }
  }

  // Forget what is older than anything the search can find again.
  const cutoff = Date.now() - (DAYS + 30) * 24 * 60 * 60 * 1000;
  for (const id of Object.keys(sent)) {
    if (sent[id] < cutoff) {
      delete sent[id];
    }
  }

  props.setProperty(SENT_KEY, JSON.stringify(sent));
  props.deleteProperty(REFUSED_KEY);
}

/**
 * Sends one message's statement attachments: 'done' when every one is in
 * the inbox or was refused for good (too big, empty), 'retry' when one
 * could not be sent this time, 'refused' when the site refused the key.
 */
function sendMessage(site, key, message) {
  let outcome = 'done';

  for (const file of message.getAttachments({ includeInlineImages: false })) {
    if (!KINDS.test(file.getName())) {
      continue;
    }

    const url = site + '/api/v1/inbox?name=' + encodeURIComponent(file.getName()) +
      '&source=' + encodeURIComponent('gmail:' + message.getId());

    let res;
    try {
      res = UrlFetchApp.fetch(url, {
        method: 'post',
        contentType: file.getContentType() || 'application/octet-stream',
        payload: file.getBytes(),
        headers: { Authorization: 'Bearer ' + key },
        muteHttpExceptions: true,
        followRedirects: false,
      });
    } catch (e) {
      console.warn('Could not reach the site for ' + file.getName() + ': ' + e);
      outcome = 'retry';
      continue;
    }

    const code = res.getResponseCode();

    if (code === 401 || code === 403) {
      return 'refused';
    }

    if (code === 200 || code === 201) {
      console.log(file.getName() + ': ' + JSON.parse(res.getContentText()).outcome);
    } else if (code === 400 || code === 413) {
      // Not a file the inbox takes, and sending it again will not change
      // that: said in the log, and not tried again.
      console.warn(file.getName() + ' was refused: ' + res.getContentText());
    } else {
      console.warn(file.getName() + ' could not be sent (' + code + '); trying again next hour.');
      outcome = 'retry';
    }
  }

  return outcome;
}

/** Tells you, once, that the key no longer works. */
function refused(props, site) {
  console.error('The site refused the key. Make a new one at ' + site + '/account/keys and put it in RECONCILE_KEY.');

  if (props.getProperty(REFUSED_KEY)) {
    return;
  }

  props.setProperty(REFUSED_KEY, String(Date.now()));

  const me = Session.getEffectiveUser().getEmail();
  GmailApp.sendEmail(me, 'Your Gmail script cannot send statements to Reconcile',
    'The site refused the key your Gmail script uses, so the statements in your email are not reaching your inbox there.\n\n' +
    'Keys for a script last a year, and a key can be revoked. Make a new one at ' + site + '/account/keys ' +
    '("A script that sends statements to your inbox"), and put it in the script\'s RECONCILE_KEY property.\n\n' +
    'Nothing is lost: the script sends what it missed once the key works again.');
}
