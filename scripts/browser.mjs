// A headless Chrome for the browser tests (lifted from stewards), driven over the DevTools protocol
// with what Node has built in -- a WebSocket and a child process -- and no
// npm package. Puppeteer and Playwright would each be a dependency tree, a
// download of their own browser, and a lockfile, for the dozen commands
// these tests use.
//
// Chrome is the one on the machine. CHROME names it; otherwise the usual
// names are looked for on PATH. A GitHub runner has google-chrome. A machine
// without one skips the browser tests and says so, except in CI, where a
// missing browser is a failure rather than a quiet pass.
//
// Nothing here waits without a limit. The first run in CI waited 42 minutes
// in silence on a command Chrome never answered, because a command's promise
// only settled when an answer came. Now every command has a time limit, a
// page that crashes fails the commands waiting on it, Chrome going away fails
// them all, and each of those failures carries the end of Chrome's own
// output. A browser test that cannot finish says so in seconds.
import { spawn } from "node:child_process";
import { accessSync, constants, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";

const names = ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"];

// findChrome is the path of a Chrome to run, or "" when there is none.
export function findChrome() {
  if (process.env.CHROME) {
    return process.env.CHROME;
  }

  for (const dir of (process.env.PATH || "").split(delimiter)) {
    for (const name of names) {
      try {
        accessSync(join(dir, name), constants.X_OK);
        return join(dir, name);
      } catch {
        // not here
      }
    }
  }

  return "";
}

// skip is the reason to skip browser tests on this machine, or false to run
// them: always run in CI, so that there they fail rather than vanish.
// stage writes one line of progress to stderr: what a test's setup has got
// to, so that a run that stops can be seen to have stopped somewhere.
export function stage(what) {
  process.stderr.write(`  [${new Date().toISOString().slice(11, 19)}] ${what}\n`);
}

export function skip() {
  if (process.env.CI || findChrome()) {
    return false;
  }

  if (!warned) {
    warned = true;
    console.warn("browser tests SKIPPED: no Chrome on this machine. Install one, or set CHROME to its path");
  }

  return "no Chrome on this machine: install one, or set CHROME to its path";
}

let warned = false;

// launch starts Chrome and resolves to {page, close}. page() opens a tab.
export async function launch() {
  const chrome = findChrome();
  if (!chrome) {
    throw new Error("no Chrome found: install one, or set CHROME to its path");
  }

  const profile = mkdtempSync(join(tmpdir(), "reconcile-chrome-"));
  const args = [
    "--headless=new",
    "--remote-debugging-port=0",
    `--user-data-dir=${profile}`,
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-extensions",
    "--disable-background-networking",
    "--window-size=412,915",
  ];

  // GitHub's Ubuntu runners do not allow Chrome the user namespaces its
  // sandbox is built from. The pages it opens here are this app's own.
  if (process.env.CI) {
    args.push("--no-sandbox");
  }

  const proc = spawn(chrome, [...args, "about:blank"], { stdio: ["ignore", "ignore", "pipe"] });

  // The end of everything Chrome says, kept for the error that needs it.
  let said = "";
  proc.stderr.on("data", (d) => {
    said = (said + d).slice(-6000);
  });
  const tail = () => (said.trim() ? `\nChrome said, at the end:\n${said.trim()}` : "\nChrome said nothing.");

  const url = await new Promise((done, fail) => {
    const timer = setTimeout(() => fail(new Error(`Chrome did not start in 20 s${tail()}`)), 20_000);
    const ready = () => {
      const m = /DevTools listening on (ws:\/\/\S+)/.exec(said);
      if (m) {
        clearTimeout(timer);
        proc.stderr.off("data", ready);
        done(m[1]);
      }
    };
    proc.stderr.on("data", ready);
    proc.once("exit", (code) => {
      clearTimeout(timer);
      fail(new Error(`Chrome exited (${code}) before it was ready${tail()}`));
    });
  });

  const cdp = await connect(url, tail);
  proc.once("exit", (code, signal) => cdp.abort(`Chrome exited (${code ?? signal})`));

  return {
    page: () => openPage(cdp),
    tail,
    async close() {
      try {
        await cdp.send("Browser.close", {}, undefined, 5000);
      } catch {
        // already gone
      }
      cdp.close();
      await new Promise((done) => {
        if (proc.exitCode !== null) return done();
        proc.on("exit", done);
        setTimeout(() => {
          proc.kill("SIGKILL");
          done();
        }, 5000);
      });
      rmSync(profile, { recursive: true, force: true });
    },
  };
}

// connect is one WebSocket to the browser, over which every tab is reached
// by its session id.
//
// Every command waits limit ms for its answer, 30 s unless said otherwise,
// and fails with tail() -- the end of Chrome's own output -- when it does not
// get one, when its page crashes, or when Chrome goes away.
async function connect(url, tail) {
  const ws = new WebSocket(url);
  await new Promise((done, fail) => {
    ws.addEventListener("open", done, { once: true });
    ws.addEventListener("error", () => fail(new Error(`could not reach Chrome at ${url}${tail()}`)), { once: true });
  });

  let next = 1;
  let gone = "";
  const waiting = new Map();
  const listeners = new Set();

  const settle = (id, fn) => {
    const w = waiting.get(id);
    if (!w) return;
    waiting.delete(id);
    clearTimeout(w.timer);
    fn(w);
  };

  const abort = (why, sessionId) => {
    for (const [id, w] of waiting) {
      if (sessionId === undefined || w.sessionId === sessionId) {
        settle(id, () => w.fail(new Error(`${w.method}: ${why}${tail()}`)));
      }
    }
  };

  ws.addEventListener("message", (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && waiting.has(msg.id)) {
      settle(msg.id, (w) => (msg.error ? w.fail(new Error(`${w.method}: ${msg.error.message}`)) : w.done(msg.result)));
    } else if (msg.method === "Inspector.targetCrashed") {
      abort("the page crashed", msg.sessionId);
    } else if (msg.method) {
      for (const fn of listeners) fn(msg);
    }
  });

  ws.addEventListener("close", () => {
    gone = gone || "the connection to Chrome closed";
    abort(gone);
  });

  return {
    send(method, params = {}, sessionId, limit = 30_000) {
      if (gone) {
        return Promise.reject(new Error(`${method}: ${gone}${tail()}`));
      }

      const id = next++;
      return new Promise((done, fail) => {
        const timer = setTimeout(
          () => settle(id, () => fail(new Error(`${method}: no answer from Chrome in ${limit / 1000} s${tail()}`))),
          limit,
        );
        waiting.set(id, { done, fail, method, sessionId, timer });
        ws.send(JSON.stringify({ id, method, params, sessionId }));
      });
    },
    listen(fn) {
      listeners.add(fn);
    },
    abort(why) {
      gone = why;
      abort(why);
    },
    close() {
      ws.close();
    },
  };
}

// openPage opens a tab, phone-sized, and gives the few things a test does
// with it. Every console error and uncaught exception is kept in errors, so
// a test can say there were none: a page whose script the header policy
// blocked looks, to everything else, like a page that works.
async function openPage(cdp) {
  const { targetId } = await cdp.send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await cdp.send("Target.attachToTarget", { targetId, flatten: true });
  const send = (method, params, limit) => cdp.send(method, params, sessionId, limit);

  const errors = [];
  cdp.listen((msg) => {
    if (msg.sessionId !== sessionId) return;
    if (msg.method === "Runtime.exceptionThrown") {
      errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
    } else if (msg.method === "Log.entryAdded" && msg.params.entry.level === "error") {
      errors.push(msg.params.entry.text);
    } else if (msg.method === "Runtime.consoleAPICalled" && msg.params.type === "error") {
      errors.push(msg.params.args.map((a) => a.value ?? a.description).join(" "));
    }
  });

  await send("Inspector.enable"); // for Inspector.targetCrashed
  await send("Page.enable");
  await send("Runtime.enable");
  await send("Log.enable");
  await send("Network.enable");
  await send("DOM.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: 412, height: 915, deviceScaleFactor: 2, mobile: true });

  let touching = false;

  const page = {
    send,
    errors,

    // evaluate runs expr in the page, awaiting it if it is a promise, and
    // gives back its value as JSON would. Two minutes, for drawing and
    // shrinking a 50 MP picture on a slow runner; never longer.
    async evaluate(expr, limit = 120_000) {
      const { result, exceptionDetails } = await send(
        "Runtime.evaluate",
        { expression: expr, awaitPromise: true, returnByValue: true },
        limit,
      );
      if (exceptionDetails) {
        throw new Error(`in the page: ${exceptionDetails.exception?.description || exceptionDetails.text}`);
      }
      return result.value;
    },

    // goto loads url and waits for the page to have loaded.
    async goto(url) {
      await send("Page.navigate", { url });
      await page.waitFor(`document.readyState === "complete" && location.href !== "about:blank"`, 15_000);
    },

    // waitFor polls expr in the page until it is truthy, and gives back its
    // value; a navigation in the meantime is waited through.
    async waitFor(expr, timeout = 15_000) {
      const end = Date.now() + timeout;
      let last;
      while (Date.now() < end) {
        try {
          last = await page.evaluate(expr, 10_000);
          if (last) return last;
        } catch (err) {
          last = err.message; // mid-navigation: try again
        }
        await new Promise((done) => setTimeout(done, 50));
      }
      throw new Error(`waited ${timeout} ms for ${expr}; last: ${JSON.stringify(last)}`);
    },

    // setFiles chooses files, by their paths on this machine, in a file input.
    async setFiles(selector, files) {
      const { root } = await send("DOM.getDocument");
      const { nodeId } = await send("DOM.querySelector", { nodeId: root.nodeId, selector });
      if (!nodeId) throw new Error(`no ${selector} on the page`);
      await send("DOM.setFileInputFiles", { nodeId, files });
    },

    // on calls fn with the params of every event of this method the page
    // sends: Fetch.requestPaused, for a test that stands between the page
    // and the server.
    on(method, fn) {
      cdp.listen((msg) => {
        if (msg.sessionId === sessionId && msg.method === method) fn(msg.params);
      });
    },

    // tap touches the middle of what selector names, as a finger does: the
    // events a phone sends rather than click(), because a tap moves the
    // focus before it clicks, and a script that listens for the focus
    // leaving sees the difference.
    async tap(selector) {
      if (!touching) {
        await send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 1 });
        touching = true;
      }

      const at = await page.evaluate(`(() => {
        const e = document.querySelector(${JSON.stringify(selector)});
        if (!e) return null;
        e.scrollIntoView({ block: "center" });
        const b = e.getBoundingClientRect();
        return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
      })()`);
      if (!at) throw new Error(`no ${selector} on the page`);

      await send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [at] });
      await send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    },

    // type puts text where the focus is, as a phone's keyboard does: one
    // input event, not a key at a time.
    async type(text) {
      await send("Input.insertText", { text });
    },

    // press presses a key: Enter, Escape, ArrowDown or ArrowUp.
    async press(key) {
      const codes = { Enter: 13, Escape: 27, ArrowUp: 38, ArrowDown: 40 };
      const down = { key, code: key, windowsVirtualKeyCode: codes[key], nativeVirtualKeyCode: codes[key] };
      await send("Input.dispatchKeyEvent", { type: key === "Enter" ? "keyDown" : "rawKeyDown", ...down, ...(key === "Enter" ? { text: "\r" } : {}) });
      await send("Input.dispatchKeyEvent", { type: "keyUp", ...down });
    },

    async setCookie(cookie) {
      await send("Network.setCookie", cookie);
    },

    async screenshot() {
      const { data } = await send("Page.captureScreenshot", { format: "png" });
      return Buffer.from(data, "base64");
    },
  };

  return page;
}
