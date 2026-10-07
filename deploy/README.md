# Deploying

Nothing here is run by an agent (see `CLAUDE.md` §6). This directory holds
what a person, or CI, uses to put the app on the Hetzner konsoleH account it
shares with mass-intentions and stewards.

The repository is public, so the hostname, the account and the paths are not
written in any tracked file. They live in `secrets.env` (see
`secrets.env.example`), and reach CI as GitHub secrets and variables through
`make deploy-send-secrets`.

## First time

1. In konsoleH, add the subdomain with a docroot of its own (that is
   `APP_DOCROOT`), turn on Let's Encrypt for it, and point its A record at
   the server.
2. `cp secrets.env.example secrets.env` and fill in what you can.
3. `make deploy-keygen`: install the public key on the account, and paste
   the `DEPLOY_SSH_KEY_B64=` line into `secrets.env`.
4. `make deploy-known-hosts`: compare the fingerprint with konsoleH's, and
   paste the `DEPLOY_KNOWN_HOSTS_B64=` line.
5. `make deploy-send-secrets`: GitHub secrets and variables, and
   `config.toml` onto the server.
6. `make deploy-status`, until it says "ready".
7. Push to `main`, or re-run the deploy workflow from the Actions tab.

Keep `secrets.env` in Bitwarden.

## Where things live on the server

| What | Where | Why |
|---|---|---|
| Docroot for `APP_HOST` | `APP_DOCROOT` | Holds `.htaccess` and nothing else |
| The binary, `config.toml`, later the database | `APP_DIR`, **outside** `public_html` | So Apache can never serve the database or the config |
| The Go server | `127.0.0.1:8461` | Loopback only. Apache proxies to it; TLS is Apache's job |

## The front end

`htaccess.template` is the whole of Apache's configuration for this app: it
proxies every request to the loopback port with `mod_rewrite`'s `[P]` flag,
because shared hosting does not allow `ProxyPass` in `.htaccess`.
`scripts/htaccess-test.sh` pins the rules that matter and their order.

It is written into the docroot from this template, with `__APP_PORT__`
substituted, and never edited by hand on the server:

- **On every push to `main`**, by the `ship` job in
  `.github/workflows/deploy.yml`.
- **By hand**, with `make deploy-htaccess`.

Either way `deploy/deploy.sh htaccess` then checks from outside that Apache
is reading it: `http://` must redirect to `https://`, and `/healthz` must be
the app (200) or the proxy with nothing behind it yet (502/503). An Apache
403 or 404 there means the file was written to a directory the subdomain
does not serve -- check the docroot in konsoleH against `APP_DOCROOT`.

## Deploying

A push to `main` deploys. Until the `APP_HOST` variable is set on GitHub, the
deploy workflow skips itself rather than failing. The `ship` job in
`.github/workflows/deploy.yml` runs `deploy/deploy.sh deploy`, which in order:

1. refuses an `APP_DIR` inside `public_html`, and checks over HTTPS that
   `config.toml`, the database, the log and the scripts are not served;
2. builds the static binary and uploads it beside the running one;
3. runs the **new** binary's `-check` against the **live** `config.toml`, and
   confirms it listens where Apache proxies -- so a config it would refuse is
   a deploy that changes nothing;
4. stops if a newer commit has reached `main` meanwhile (its own deploy
   follows);
5. reinstalls `.htaccess` and the cron watchdog;
6. under the supervisor's lock: stops the app, backs up `reconcile.db` and
   snapshots `files/` (keeping `KEEP_BACKUPS` of each), and swaps the binary;
7. starts it, and waits for `/healthz` on the loopback. If it does not start
   or does not answer, it **rolls back** to the last binary that did;
8. checks from outside that `/healthz` is 200 and `/` is answered by the app.

There is no separate install step: every deploy creates what it needs, so the
first push is the install. What it cannot create is `config.toml`, which a
person puts there with `make deploy-send-secrets`.

On the server, in `APP_DIR`:

| File | What |
|---|---|
| `reconcile` | the binary. `.prev` is the one before, `.last-good` the latest that started and answered, `.failed` the latest that did not |
| `config.toml` | from `make deploy-send-secrets`; never edited on the server |
| `reconcile.db` | the database, mode 0600 |
| `files/` | uploaded statements and receipts, one file per content hash. Mode 0700, served only through the app |
| `backups/` | per deploy, a copy of the database and a snapshot of `files/`, the newest `KEEP_BACKUPS` of each kept. The snapshots are hard links, so a file is stored once however many hold it. On the same disk, so a copy off the server is still a person's to take |
| `run.sh`, `supervise.sh` | start the binary, and keep it running from cron |
| `reconcile.log` | the server's log |
| `deployed-commit.txt` | the commit that is live |

The watchdog is two crontab lines ending in `# reconcile`. The crontab is
shared with mass-intentions and stewards, and each project's deploy leaves the
others' lines alone -- `scripts/deploy-test.sh` holds that.

## By hand

| Command | What |
|---|---|
| `make prod-status` | the process, the public checks, what is live, backups, the size of `files/`, the log |
| `make prod-logs N=200` | the tail of the log |
| `make prod-backup` | a backup now (stops the app for a second or two) |
| `make prod-restart` | restart |
| `make deploy` | deploy from your machine, from a clean `main` |
| `make deploy-htaccess` | the front end only |
