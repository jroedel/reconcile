// A server of this repository's for a browser test: built, started on a
// free port with a database and files directory of its own, and signed
// into with its one-time bootstrap secret, as the first person signs in.
// Lifted from stewards.
//
// Shared by the browser tests that need the real thing -- the share
// page's script and the header policy it runs under -- rather than each
// starting its own the same way.
import { execFileSync, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { stage } from "./browser.mjs";

const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const freePort = () =>
  new Promise((done) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => done(port));
    });
  });

// startServer gives back the server's base address, the session cookie of
// the person signed in, its directory, log() for everything it has written
// so far, post() for a form sent as that person, and stop().
export async function startServer(name) {
  const dir = mkdtempSync(join(tmpdir(), `reconcile-${name}-`));
  let appLog = "";
  let app;

  const stop = () => {
    app?.kill();
    rmSync(dir, { recursive: true, force: true });
  };

  try {
    stage("building the server");
    execFileSync("go", ["build", "-o", join(dir, "reconcile"), "./cmd/reconcile"], { cwd: repo, stdio: "inherit", timeout: 180_000 });

    const port = await freePort();
    const base = `http://127.0.0.1:${port}`;
    const secret = randomBytes(24).toString("hex");
    writeFileSync(
      join(dir, "config.toml"),
      `[server]\naddr = "127.0.0.1:${port}"\nbase_url = "${base}"\n` +
        `[db]\npath = "${join(dir, "reconcile.db")}"\n[files]\ndir = "${join(dir, "files")}"\n` +
        `[auth]\nbootstrap_secret = "${secret}"\n`,
    );

    stage("starting it");
    app = spawn(join(dir, "reconcile"), ["-config", join(dir, "config.toml")], { stdio: ["ignore", "pipe", "pipe"] });
    app.stdout.on("data", (d) => (appLog += d));
    app.stderr.on("data", (d) => (appLog += d));

    for (let i = 0; ; i++) {
      try {
        if ((await fetch(`${base}/healthz`, { signal: AbortSignal.timeout(2000) })).ok) break;
      } catch {
        // not listening yet
      }
      if (i > 100) throw new Error(`the server did not start:\n${appLog}`);
      await new Promise((done) => setTimeout(done, 100));
    }

    stage("signing in");
    const signIn = await fetch(`${base}/sign-in/first`, {
      method: "POST",
      redirect: "manual",
      signal: AbortSignal.timeout(10_000),
      headers: { "Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin" },
      body: new URLSearchParams({ email: "treasurer@example.org", secret }),
    });
    const cookie = signIn.headers.getSetCookie().map((c) => /^__Host-session=([^;]+)/.exec(c)).find(Boolean)?.[1];
    if (!cookie) throw new Error(`the bootstrap sign-in gave no session: ${signIn.status}\n${appLog}`);

    // post sends a form as the person signed in, as one of the site's own
    // pages would, and gives back where it was sent.
    const post = async (path, form) => {
      const res = await fetch(`${base}${path}`, {
        method: "POST",
        redirect: "manual",
        signal: AbortSignal.timeout(10_000),
        headers: { Cookie: `__Host-session=${cookie}`, "Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin" },
        body: new URLSearchParams(form),
      });
      if (res.status !== 303) throw new Error(`POST ${path}: ${res.status}\n${await res.text()}`);
      return res.headers.get("Location");
    };

    return { base, cookie, dir, log: () => appLog, post, stop };
  } catch (err) {
    stop();
    throw err;
  }
}

// sessionCookie is the cookie for page.setCookie, so Chrome is that person
// too.
export const sessionCookie = (base, cookie) => ({ name: "__Host-session", value: cookie, url: `${base}/`, secure: true, httpOnly: true, path: "/" });
