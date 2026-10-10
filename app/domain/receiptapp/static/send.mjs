// The share page's sender (docs/phone.md, 4). It sends the files chosen --
// or shared, which share.mjs puts in the same file input -- one request at
// a time, each photo shrunk first if it is larger than it needs to be
// (shrink.mjs), says how far it has got, and keeps the form still while it
// does. Lifted from stewards' send screen.
//
// Without it the form posts the whole share at once, which is one upload:
// at most twenty files, minutes of nothing to see, and one dropped signal
// losing all of it. Everything here improves on a form that works without
// it: if this file does not load, only the progress and the larger shares
// are lost.
//
// Sending again is always safe. A file the inbox has already is recognised
// by its bytes and counted as "there already", never added twice
// (receiptbus.Holds), so every way this can fail ends in the same advice:
// press Add again. That holds for a shrunk photo too: the same photo
// shrinks to the same bytes in the same browser, which
// shrink_browser_test.mjs checks rather than assumes.
//
// What it says is the page's, in the page's language: the server writes
// each sentence on #send-words, with its {placeholders} left for this to
// fill. A refusal from the server is a code (kind, big, none, to), never a
// sentence, and is said in the page's words for it.
import { shrink } from "./shrink.mjs";

(() => {
  const form = document.querySelector("form[data-share]");
  if (!form || !form.dataset.send || !window.fetch || !window.FormData) {
    return;
  }

  const input = form.querySelector('input[type="file"]');
  const button = form.querySelector('button[type="submit"]');
  const status = document.getElementById("sending");
  const words = document.getElementById("sending-words");
  const bar = document.getElementById("sending-bar");
  const result = document.getElementById("sent");
  const max = Number(form.dataset.max) || 60;
  const label = button.textContent;
  const said = document.getElementById("send-words")?.dataset || {};

  // t is one of the page's sentences, its placeholders filled. A name the
  // page did not give comes back as the name, which is wrong to read and
  // easy to see, rather than as nothing at all.
  const t = (name, values = {}) =>
    (typeof said[name] === "string" ? said[name] : name).replace(/\{([a-zA-Z]+)\}/g, (all, k) => (k in values ? String(values[k]) : all));

  // How long to wait before trying a file again, each time it fails for
  // want of a signal or a server: about five minutes in all, and then it
  // stops and says so. Stewards learned the five minutes from a weak
  // signal whose uploads broke off for three.
  const waits = [2000, 5000, 10000, 20000, 30000, 30000, 30000, 30000, 30000, 30000, 30000, 30000];

  // The files this page has had an answer for, kept or there already, so
  // that pressing Add again after a failure sends only the ones that did
  // not arrive. By the File itself, which is the same object for as long
  // as it sits in the input.
  const delivered = new WeakSet();

  // A little longer than the server gives one upload to arrive.
  const patience = 11 * 60 * 1000;

  let busy = false;
  let awake = null;

  const stay = (ev) => {
    ev.preventDefault();
    ev.returnValue = "";
  };

  form.addEventListener("submit", async (ev) => {
    if (busy) {
      ev.preventDefault();
      return;
    }

    const chosen = Array.from(input.files || []);
    const to = form.querySelector('input[name="to"]:checked')?.value;
    if (chosen.length === 0 || !to) {
      return; // the browser's own "required" says what to do
    }

    ev.preventDefault();
    clear(result);

    if (chosen.length > max) {
      say(result, "notice", t("tooMany", { count: chosen.length, max }));
      return;
    }

    const files = chosen.filter((f) => !delivered.has(f));

    lock(true);

    let kept = 0;
    let attached = 0;
    let already = chosen.length - files.length;
    const refused = [];
    let stop = null;

    for (let i = 0; i < files.length; i++) {
      progress(i, files.length, t("sending", { n: i + 1, count: files.length }));

      const answer = await send(to, files[i], i, files.length);

      if (answer.outcome === "kept") {
        kept++;
        attached += answer.attached ? 1 : 0;
        delivered.add(files[i]);
      } else if (answer.outcome === "already") {
        already++;
        delivered.add(files[i]);
      } else if (answer.file) {
        refused.push({ name: files[i].name, problem: answer.file });
      } else {
        stop = answer.stop;
        break;
      }
    }

    if (!stop && refused.length === 0) {
      // All of them: the inbox says how many, as it does after an upload
      // sent without this script.
      progress(files.length, files.length, t("opening"));
      window.removeEventListener("beforeunload", stay);
      window.location.assign(done(to, kept, attached, already));
      return;
    }

    lock(false);

    if (kept > 0) {
      say(result, "notice ok", t("countKept", { count: kept }));
    }

    if (already > 0) {
      say(result, "notice ok", t("countAlready", { count: already }));
    }

    if (refused.length > 0) {
      const box = say(result, "notice", t("countNot", { count: refused.length }));
      const list = document.createElement("ul");
      for (const r of refused) {
        const item = document.createElement("li");
        const name = document.createElement("strong");
        name.textContent = r.name;
        item.append(name, `: ${r.problem}`);
        list.append(item);
      }
      box.append(list);
    }

    if (stop) {
      say(result, "notice", stop);
    } else {
      // Every file had its answer, so choosing them again would only send
      // the refused ones to be refused again.
      input.value = "";
    }

    result.scrollIntoView({ block: "start" });
  });

  // Said once the form is this script's to send, for the browser tests:
  // a module loads when it likes, and an Add pressed before then is the
  // form's own post, which is right for a person and a race for a test.
  form.dataset.sending = "ready";

  // done is the inbox the files went to, saying what happened, in the
  // query the inbox reads after an upload of its own: an account's or a
  // project's, or the person's own checks.
  function done(to, kept, attached, already) {
    if (to === "mine") {
      return `/checks?${new URLSearchParams({ done: "added", n: kept, already })}`;
    }

    const [kind, id] = to.split(":");
    const inbox = `/${kind === "project" ? "projects" : "accounts"}/${encodeURIComponent(id)}/receipts`;
    const q = kind === "checks"
      ? new URLSearchParams({ done: "checks", n: attached, waiting: kept - attached, already })
      : new URLSearchParams({ done: "added", n: kept, already });

    return `${inbox}?${q}`;
  }

  // send is one file, tried again after a pause when the signal or the
  // server fails. It answers {outcome, attached}, {file: why it was not
  // kept}, or {stop: why the rest cannot go}.
  async function send(to, file, i, n) {
    // Once, before any try: shrinking the same photo again gives the same
    // bytes, but there is no need to spend the phone's time finding out.
    const photo = await shrink(file);

    // Where first: the server asks about it before it keeps a byte.
    const body = new FormData();
    body.append("to", to);
    body.append("files", photo, file.name);

    for (let attempt = 0; ; attempt++) {
      let answer = null;

      try {
        const res = await fetch(form.dataset.send, {
          method: "POST",
          body,
          credentials: "same-origin",
          headers: { Accept: "application/json" },
          signal: AbortSignal.timeout ? AbortSignal.timeout(patience) : undefined,
        });
        answer = await read(res);
      } catch {
        answer = null; // no signal, or no answer in time
      }

      if (answer) {
        return answer;
      }

      if (attempt >= waits.length) {
        return { stop: t("gone") };
      }

      const at = { n: i + 1, count: n };
      progress(i, n, attempt < 3 ? t("dropped", at) : t("dropping", at));
      await pause(waits[attempt]);
    }
  }

  // pause waits ms, or less when the phone says it is back online.
  function pause(ms) {
    return new Promise((resolve) => {
      const back = () => {
        clearTimeout(timer);
        resolve();
      };
      const timer = setTimeout(() => {
        window.removeEventListener("online", back);
        resolve();
      }, ms);
      window.addEventListener("online", back, { once: true });
    });
  }

  // read turns an answer into one of send's, or null for "try again".
  async function read(res) {
    const json = (res.headers.get("Content-Type") || "").startsWith("application/json");

    if (res.redirected || (res.ok && !json) || (res.status === 403 && !json)) {
      // The session ran out while sending: a write without one is sent to
      // the sign-in page.
      return { stop: t("signedOut") };
    }

    if (!json) {
      if (res.status === 413) {
        return { file: t("big") };
      }
      if (res.status >= 500) {
        return null; // the proxy in front of the app, busy or restarting
      }
      return { stop: t("refused", { status: res.status }) };
    }

    const body = await res.json();

    if (res.ok) {
      return { outcome: body.outcome, attached: !!body.attached };
    }
    if (res.status >= 500) {
      return null;
    }

    const code = body.error && body.error.problem;
    if (body.error && body.error.field === "files") {
      return { file: t(code) };
    }

    // Not about one file -- where it goes -- so the same for every one
    // after it.
    return { stop: t("to") };
  }

  function lock(on) {
    busy = on;
    form.setAttribute("aria-busy", on ? "true" : "false");

    for (const el of form.elements) {
      el.disabled = on;
    }

    button.textContent = on ? t("sendingOn") : label;
    status.hidden = !on;

    if (on) {
      status.scrollIntoView({ block: "center", behavior: "smooth" });
      window.addEventListener("beforeunload", stay);
      keepAwake();
    } else {
      window.removeEventListener("beforeunload", stay);
      if (awake) {
        awake.release().catch(() => {});
        awake = null;
      }
    }
  }

  // keepAwake asks the phone not to dim its screen while sending: a phone
  // that locks itself pauses the page, and the share with it.
  function keepAwake() {
    if (navigator.wakeLock) {
      navigator.wakeLock.request("screen").then((lock) => {
        if (busy) {
          awake = lock;
        } else {
          lock.release().catch(() => {});
        }
      }, () => {});
    }
  }

  function progress(sent, n, text) {
    words.textContent = text;
    bar.max = n;
    bar.value = sent;
  }

  function say(where, kind, text) {
    const p = document.createElement("div");
    p.className = kind;
    p.setAttribute("role", kind === "notice" ? "alert" : "status");
    p.textContent = text;
    where.append(p);
    where.hidden = false;
    return p;
  }

  function clear(where) {
    where.replaceChildren();
    where.hidden = true;
  }
})();
