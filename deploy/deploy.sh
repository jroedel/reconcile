#!/usr/bin/env bash
#
# Put reconcile on the Hetzner konsoleH account it shares with mass-intentions
# and stewards.
#
# The target is shared hosting with no root, no systemd and no Docker: ssh,
# cron and a static binary are what everything here is built out of. Adapted
# from mass-intentions' deploy.sh, which paid for most of what follows; where a
# step is here because of something that went wrong there, the comment says so.
#
# Run from CI on every push to main, and by a person through `make deploy`,
# `make deploy-htaccess` and the `make prod-*` targets. Never by an agent
# (CLAUDE.md section 6; .claude/settings.json denies it).
#
# There is no separate one-time install, unlike mass-intentions. Everything an
# install would do -- the directories, the supervisor, cron, the front end --
# is idempotent and runs on every deploy, so the first push to main is the
# install, and a server rebuilt from nothing is one re-run of the workflow away.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_ENV="${SECRETS_ENV:-$REPO_DIR/secrets.env}"

APP=reconcile

log()  { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m !!\033[0m %s\n' "$*" >&2; }
ok()   { printf '\033[32m  ok\033[0m %s\n' "$*"; }
bad()  { printf '\033[31m   X\033[0m %s\n' "$*"; }
die()  { printf '\033[31mX\033[0m %s\n' "$*" >&2; exit 1; }

require() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but is not installed"; }

# shellcheck source=../scripts/env.sh
. "$REPO_DIR/scripts/env.sh"

# ------------------------------------------------------------------ config

# In CI the values arrive as environment variables; on a person's machine they
# come from secrets.env. Same names either way. The file is read only when it
# exists, and a runner never has one.
load_config() {
	[ ! -f "$SECRETS_ENV" ] || parse_env "$SECRETS_ENV"

	: "${DEPLOY_SSH_HOST:?DEPLOY_SSH_HOST is not set. On your machine it belongs in secrets.env; in CI it is a repository secret}"
	: "${DEPLOY_SSH_USER:?DEPLOY_SSH_USER is not set}"
	: "${APP_HOST:?APP_HOST is not set}"
	: "${APP_PORT:?APP_PORT is not set}"
	: "${APP_DOCROOT:?APP_DOCROOT is not set}"

	DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"
	KEEP_BACKUPS="${KEEP_BACKUPS:-14}"

	[[ "$APP_PORT" =~ ^[0-9]+$ ]] || die "APP_PORT is \"$APP_PORT\"; it must be a port number"
	[[ "$KEEP_BACKUPS" =~ ^[0-9]+$ ]] && [ "$KEEP_BACKUPS" -ge 1 ] \
		|| die "KEEP_BACKUPS is \"$KEEP_BACKUPS\"; it must be a whole number of at least 1"
}

# need_app_dir is separate from load_config because the front end does not
# need it, and `make deploy-htaccess` predates APP_DIR being a GitHub variable.
need_app_dir() {
	: "${APP_DIR:?APP_DIR is not set. It belongs in secrets.env and, for CI, in the GitHub variables: make deploy-send-secrets}"
	Q_DIR="$(printf '%q' "$APP_DIR")"
}

# ssh_setup decodes the key and the pinned host key into a directory removed
# on exit. There is no unpinned fallback: a deploy that trusts whatever answers
# on port 22 is a deploy that can be handed somebody else's shell.
SSH_TMP="$(mktemp -d)"
chmod 700 "$SSH_TMP"
trap 'rm -rf "$SSH_TMP"' EXIT
SSH_OPTS=()

ssh_setup() {
	require ssh

	printf '%s' "${DEPLOY_SSH_KEY_B64:?DEPLOY_SSH_KEY_B64 is not set. Run: make deploy-keygen}" \
		| base64 -d > "$SSH_TMP/key" 2>/dev/null \
		|| die "DEPLOY_SSH_KEY_B64 is not valid base64. Run: make deploy-status"
	chmod 600 "$SSH_TMP/key"

	grep -q 'BEGIN OPENSSH PRIVATE KEY' "$SSH_TMP/key" \
		|| die "DEPLOY_SSH_KEY_B64 decodes to something that is not an ssh private key"

	printf '%s' "${DEPLOY_KNOWN_HOSTS_B64:?DEPLOY_KNOWN_HOSTS_B64 is not set; a deploy must be pinned. Run: make deploy-known-hosts}" \
		| base64 -d > "$SSH_TMP/known_hosts" 2>/dev/null \
		|| die "DEPLOY_KNOWN_HOSTS_B64 is not valid base64. Run: make deploy-known-hosts"

	SSH_OPTS=(
		-i "$SSH_TMP/key"
		-o "UserKnownHostsFile=$SSH_TMP/known_hosts"
		-o StrictHostKeyChecking=yes
		-o ConnectTimeout=15
		-o BatchMode=yes
	)
}

remote()        { ssh "${SSH_OPTS[@]}" -p "$DEPLOY_SSH_PORT" "$DEPLOY_SSH_USER@$DEPLOY_SSH_HOST" "$@"; }
remote_in_app() { remote "cd $Q_DIR && $1"; }
push_file()     { scp -q "${SSH_OPTS[@]}" -P "$DEPLOY_SSH_PORT" "$1" "$DEPLOY_SSH_USER@$DEPLOY_SSH_HOST:$2"; }

# ------------------------------------------------------------------ guards

# assert_app_dir_is_private refuses an APP_DIR Apache could serve. It holds
# config.toml, the database and the backups, and whether a stranger can fetch
# them must not depend on an .htaccess that a later change can undo.
assert_app_dir_is_private() {
	case "$APP_DIR" in
	public_html|public_html/*|*/public_html|*/public_html/*|"$APP_DOCROOT"|"$APP_DOCROOT"/*)
		die "APP_DIR ($APP_DIR) is inside public_html, where Apache could serve the database. Put it beside public_html, such as ~/reconcile"
		;;
	esac
}

# assert_not_exposed proves over HTTPS that what must not be public is not.
#
# Checked from outside rather than reasoned about from the layout, because the
# question is what Apache serves. A connection failure is NOT a pass: an
# unresolvable hostname would otherwise make every one of these look safe,
# which is the most dangerous way for a security check to succeed.
assert_not_exposed() {
	local problems=0 code

	for path in config.toml "$APP.db" "$APP.log" run.sh supervise.sh secrets.env; do
		code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "https://$APP_HOST/$path" 2>/dev/null)" || code=""

		case "$code" in
		200)     bad "https://$APP_HOST/$path is publicly readable"; problems=$((problems + 1)) ;;
		''|000)  die "https://$APP_HOST did not answer, so nothing could be checked -- which is not the same as safe" ;;
		*)       ok "$path is not served ($code)" ;;
		esac
	done

	[ "$problems" -eq 0 ] || die "$problems file(s) are exposed. Nothing was deployed"
}

# ensure_main stops a person deploying a branch or a dirty tree by accident.
# CI sets SKIP_GIT_CHECK: a runner's checkout is a detached HEAD, and its
# trigger already is "a commit on main".
ensure_main() {
	[ -z "${SKIP_GIT_CHECK:-}" ] || return 0
	[ -z "${DEPLOY_ALLOW_BRANCH:-}" ] || return 0

	local branch; branch="$(git -C "$REPO_DIR" rev-parse --abbrev-ref HEAD)"
	[ "$branch" = "main" ] || die "on branch $branch; deploys come from main. Set DEPLOY_ALLOW_BRANCH=1 to override"

	git -C "$REPO_DIR" diff --quiet && git -C "$REPO_DIR" diff --cached --quiet \
		|| die "the working tree has uncommitted changes; what would be deployed is not what is committed"
}

# superseded reports whether main has moved past the commit being deployed.
#
# Merging three pull requests in a minute queues three deploys, and one that
# has already started carries on and ships a commit superseded while it built.
# cancel-in-progress would stop it, and is the wrong tool: cancelled between
# the rename and the start, it leaves the app down until cron notices.
#
# Failing open is deliberate: if the tip cannot be read, deploy. A deploy that
# silently does nothing because it could not reach GitHub is worse than one
# extra release.
superseded() {
	[ -z "${DEPLOY_ALLOW_BRANCH:-}" ] || return 1

	local mine tip
	mine="$(git -C "$REPO_DIR" rev-parse HEAD 2>/dev/null)" || return 1
	tip="$(git -C "$REPO_DIR" ls-remote origin refs/heads/main 2>/dev/null | cut -f1)" || return 1

	[ -n "$mine" ] && [ -n "$tip" ] || return 1
	[ "$mine" != "$tip" ]
}

# ------------------------------------------------------------------ health

health_loopback() {
	for _ in $(seq 1 20); do
		if remote "curl -sf --max-time 5 http://127.0.0.1:$APP_PORT/healthz" >/dev/null 2>&1; then
			ok "the app answers on the loopback"
			return 0
		fi
		sleep 2
	done

	return 1
}

# health_public asks what a visitor gets, for two paths.
#
# /healthz must be 200. "/" must be answered by the app rather than by Apache,
# and that is the front-page lesson from mass-intentions on 2026-09-22: a
# healthy binary behind an Apache that turned "/" into a request for a file,
# and a deploy that said "deployed" over a site whose first page was a 404.
#
# "Answered by the app" is not "200", because there is no front page yet and
# the app's own answer to "/" is a 404. It is the app's Content-Security-Policy
# header, which Apache never sends: this binary sets it on every response, the
# 404 included, and the .htaccess deliberately sets no headers at all.
health_public() {
	local ok=0 code headers

	code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 "https://$APP_HOST/healthz" 2>/dev/null)" || code=""
	printf '     %-48s %s\n' "https://$APP_HOST/healthz" "${code:-unreachable}"
	[ "$code" = "200" ] || ok=1

	headers="$(curl -s -D - -o /dev/null --max-time 20 "https://$APP_HOST/" 2>/dev/null)" || headers=""
	if answered_by_app <<<"$headers"; then
		printf '     %-48s %s\n' "https://$APP_HOST/" "answered by the app"
	else
		printf '     %-48s %s\n' "https://$APP_HOST/" "NOT answered by the app"
		ok=1
	fi

	return "$ok"
}

# answered_by_app reads response headers on stdin.
answered_by_app() { grep -qi "^content-security-policy: default-src 'none'"; }

# ------------------------------------------------------------------ front end

render_htaccess() {
	sed "s/__APP_PORT__/$APP_PORT/" "$REPO_DIR/deploy/htaccess.template"
}

# install_htaccess writes the front end into the docroot.
#
# The docroot is not created here. konsoleH creates it when the subdomain is
# added, and making it ourselves would turn "the subdomain points somewhere
# else" into a file written to a directory nothing serves, silently. Through a
# .new file and a rename, so Apache never reads half a file.
install_htaccess() {
	local docroot; docroot="$(printf '%q' "$APP_DOCROOT")"

	render_htaccess | remote "
		[ -d $docroot ] || { echo 'no such docroot' >&2; exit 3; }
		cat > $docroot/.htaccess.new && chmod 644 $docroot/.htaccess.new && mv $docroot/.htaccess.new $docroot/.htaccess
	" || die "could not install into $APP_DOCROOT. If it said 'no such docroot', add the subdomain in konsoleH with that docroot, or correct APP_DOCROOT"

	ok "$APP_DOCROOT/.htaccess proxies to 127.0.0.1:$APP_PORT"

	# Reported, not removed: deleting files from a live web directory is a
	# person's call, not the deploy's.
	local stray
	stray="$(remote "ls -A $docroot" | grep -vx '.htaccess' || true)"
	[ -z "$stray" ] || warn "the docroot also holds: $(tr '\n' ' ' <<<"$stray")-- only .htaccess belongs there"
}

# verify_front_end asks the internet whether Apache reads our rules. http must
# redirect to https, which only our rules do; /healthz must be the app, or the
# proxy with nothing behind it yet. Apache's own 403 or 404 means it is serving
# the directory as files and has not read the rules at all.
verify_front_end() {
	require curl

	local http code
	http="$(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}' --max-time 15 "http://$APP_HOST/" || true)"

	case "$http" in
	"301 https://$APP_HOST/"*) ok "http redirects to https" ;;
	*) die "http://$APP_HOST/ answered \"$http\", not a redirect to https. Apache is not reading $APP_DOCROOT/.htaccess: check that the subdomain's docroot in konsoleH is exactly $APP_DOCROOT" ;;
	esac

	code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "https://$APP_HOST/healthz" || true)"

	case "$code" in
	200)     ok "https://$APP_HOST/healthz answers 200: the app is live behind the proxy" ;;
	502|503) ok "https://$APP_HOST/ proxies to 127.0.0.1:$APP_PORT; nothing answers there yet ($code)" ;;
	*)       die "https://$APP_HOST/healthz answered $code. Expected 200 from the app, or 502/503 from the proxy with nothing behind it" ;;
	esac
}

cmd_htaccess() {
	load_config
	ssh_setup

	log "installing the front end on $DEPLOY_SSH_HOST"
	install_htaccess
	verify_front_end
}

# ------------------------------------------------------------------ cron

# install_cron makes `supervise.sh start` the boot launcher and `supervise.sh
# watch` the five-minute watchdog. watch is start that prints nothing when the
# app is already up, because cron emails whatever a job prints; see
# supervise.sh for the 288 emails a day that cost.
#
# The crontab is shared with mass-intentions and stewards, which is the reason
# for both rules below, and scripts/deploy-test.sh holds each of them against
# the other projects' lines. stewards marks its lines "# stewards", so the two
# markers never match each other.
#
#   Ours are found by their marker and nothing else. Matching on the path was
#   mass-intentions' first version, and it left a watchdog running a binary
#   from an old directory for ever after APP_DIR changed.
#
#   Ours never look like theirs. mass-intentions removes every line matching
#   "supervise.sh start (production|rehearsal)" before writing its own, so
#   ours call start with no instance argument, and its next deploy leaves them
#   where they are.
CRON_MARKER='# reconcile'

install_cron() {
	local lines="@reboot cd $Q_DIR && ./supervise.sh start $CRON_MARKER
*/5 * * * * cd $Q_DIR && ./supervise.sh watch $CRON_MARKER"

	remote "crontab -l 2>/dev/null | grep -v ' $CRON_MARKER\$' > /tmp/reconcile-cron.\$\$ || true
	        printf '%s\n' \"$lines\" >> /tmp/reconcile-cron.\$\$
	        crontab /tmp/reconcile-cron.\$\$ && rm -f /tmp/reconcile-cron.\$\$"

	ok "the watchdog is scheduled"
}

# ------------------------------------------------------------------ backup and rollback

# backup_script prints the shell that copies the database, for a caller to run
# on the server while the app is stopped. There is no database yet, and until
# there is it says so and does nothing; it is here so that the first store is
# backed up from its first deploy rather than from the day somebody remembers.
#
# Stopped, because a live SQLite database in WAL mode keeps its latest writes
# in the -wal file, and a copy of the .db alone opens perfectly and is missing
# them. A clean shutdown checkpoints the WAL into the .db, so the copy taken
# afterwards is whole; the -wal is copied too, in case the shutdown was not
# clean. sqlite3's .backup is used when the host has it, and is safe either way.
#
# A snippet rather than a remote call of its own, so that it can run inside the
# same locked command as the stop and the swap -- see cmd_deploy. No `exit` in
# it for that reason: an exit here would end the swap as well.
backup_script() {
	local stamp; stamp="$(date -u +%Y%m%dT%H%M%SZ)"

	cat <<-SH
		if [ ! -f $APP.db ]; then
			echo 'no database yet, so nothing to back up'
		else
			mkdir -p backups && chmod 700 backups
			if command -v sqlite3 >/dev/null 2>&1; then
				sqlite3 $APP.db ".backup 'backups/$APP-$stamp.db'"
			else
				cp -p $APP.db backups/$APP-$stamp.db
				[ ! -f $APP.db-wal ] || cp -p $APP.db-wal backups/$APP-$stamp.db-wal
			fi
			# Newest first by the stamp in the name, never by mtime: cp -p
			# gives every copy the database's own mtime, so two deploys with
			# no write between them make backups that tie, and ls -t could
			# then rotate out the one just taken.
			ls -1 backups/$APP-*.db 2>/dev/null | sort -r | tail -n +$((KEEP_BACKUPS + 1)) | while read -r old; do
				rm -f "\$old" "\$old-wal"
			done
			echo "backed up to backups/$APP-$stamp.db, keeping $KEEP_BACKUPS"
		fi
	SH
}

# locked runs a snippet on the server holding supervise.sh's own lock.
#
# The watchdog is cron running `supervise.sh watch` every five minutes, and
# when the app is down it starts it, taking this lock. Without the lock, a
# watchdog firing between a deploy's stop and its rename starts the OLD binary;
# the deploy's own start then finds it "already running", the health check
# passes against the old code, and the deploy reports success having shipped
# nothing. mass-intentions has that
# window. Here a watchdog in it waits for the lock, and finds the new binary.
locked() {
	remote_in_app "exec 9>$APP.lock && flock -w 60 9 || { echo 'could not take the lock' >&2; exit 1; }
$1"
}

# do_rollback puts back the most recent binary that both started and answered.
#
# .last-good in preference to .prev. .prev is whatever was in place before this
# deploy, which after one failed deploy is itself the failed one -- so in
# mass-intentions three failed deploys in a row left nothing good to roll back
# onto. Copied rather than moved, so it is still there for the deploy after next.
do_rollback() {
	remote_in_app "./supervise.sh stop" || true

	remote_in_app "mv $APP $APP.failed 2>/dev/null || true
	               if [ -f $APP.last-good ]; then
	                       cp -p $APP.last-good $APP
	               elif [ -f $APP.prev ]; then
	                       mv $APP.prev $APP
	               fi"

	remote_in_app "./supervise.sh start" || true
}

# ------------------------------------------------------------------ deploy

cmd_deploy() {
	load_config
	need_app_dir
	ensure_main
	ssh_setup
	require curl

	assert_app_dir_is_private

	log "checking that nothing private is public"
	assert_not_exposed

	log "building"
	make -C "$REPO_DIR" --no-print-directory release

	log "uploading"
	remote "mkdir -p $Q_DIR && chmod 700 $Q_DIR"
	push_file "$REPO_DIR/$APP-linux-amd64" "$APP_DIR/$APP.new"
	push_file "$REPO_DIR/deploy/run.sh"       "$APP_DIR/run.sh"
	push_file "$REPO_DIR/deploy/supervise.sh" "$APP_DIR/supervise.sh"
	remote_in_app "chmod 700 $APP.new run.sh supervise.sh"

	# Pre-flight: the NEW binary reads the LIVE config while the old one is
	# still serving. A config it will not accept is then a deploy that swaps
	# nothing, rather than a process that dies after the rename with the
	# previous one already gone.
	log "pre-flight: does the new binary accept the config on the server?"
	remote_in_app "[ -f config.toml ]" \
		|| die "there is no config.toml in $APP_DIR on the server. Run 'make deploy-send-secrets' from your machine, then re-run this deploy"

	local summary
	summary="$(remote_in_app "./$APP.new -config config.toml -check")" \
		|| die "the new binary refuses the config on the server. Run 'make deploy-send-secrets' and try again"

	# And does it listen where Apache proxies? A config.toml from an older
	# secrets.env could name a different port, and the binary would start
	# happily behind a proxy pointed somewhere else.
	grep -q "^listening on *127.0.0.1:$APP_PORT\$" <<<"$summary" \
		|| die "config.toml on the server does not listen on 127.0.0.1:$APP_PORT, where Apache proxies. Run 'make deploy-send-secrets'"
	ok "it does, and it listens on 127.0.0.1:$APP_PORT"

	# The last moment at which doing nothing is free: built, uploaded and
	# pre-flighted, and nothing on the server touched yet.
	if superseded; then
		log "a newer commit is already on main; its deploy follows this one"
		remote_in_app "rm -f $APP.new" || true
		log "stopping here. Nothing on the server was changed"
		return 0
	fi

	# The front end and the schedule on every deploy, not only once. Both are
	# small, both are in git, and a server holding an older copy of either is a
	# fault nobody notices until somebody else does -- in mass-intentions, a
	# cron line added to the install and never reaching the host.
	log "front end"
	install_htaccess

	log "schedule"
	install_cron

	# Stop, back up and swap as one locked command; see locked(). mv rather
	# than cp over the running file: overwriting a binary that is executing
	# gives ETXTBSY, and a rename is atomic.
	log "stopping, backing up and swapping"
	locked "./supervise.sh stop
$(backup_script)
[ ! -f $APP ] || cp -p $APP $APP.prev
mv $APP.new $APP" || die "the swap did not complete. The app may be stopped: run 'make prod-restart', then re-run the deploy"

	# Guarded. `set -e` is on, so an unguarded start that fails ends the script
	# here -- before the health check, and therefore before the rollback the
	# health check exists to trigger. That is what happened to mass-intentions
	# on 2026-09-21.
	if ! remote_in_app "./supervise.sh start"; then
		bad "the new binary did not start; rolling back"
		do_rollback
		die "rolled back to the last binary that was known good"
	fi

	log "health"
	if ! health_loopback; then
		bad "the new binary did not answer after the swap; rolling back"
		do_rollback
		die "rolled back to the last binary that was known good"
	fi

	# Known good: it started, and it answered. Kept apart from .prev, which
	# every deploy overwrites, a failed one included.
	remote_in_app "cp -p $APP $APP.last-good" || warn "could not keep a copy of the binary that is known good"
	remote_in_app "printf '%s\n' '$(git -C "$REPO_DIR" rev-parse HEAD)' > deployed-commit.txt" || true

	log "from outside"
	health_public || warn "a public check failed. The binary is healthy on the loopback, so this is the front end rather than the release; it was NOT rolled back"

	rm -f "$REPO_DIR/$APP-linux-amd64"
	log "deployed"
}

# ------------------------------------------------------------------ status

cmd_status() {
	load_config
	need_app_dir
	ssh_setup

	log "the process"
	remote_in_app "./supervise.sh status" || bad "the app is not running"

	log "from outside"
	health_public || bad "the public check failed"

	log "what is live"
	remote_in_app "cat deployed-commit.txt 2>/dev/null || echo '(nothing deployed yet)'"

	log "the database"
	remote_in_app "ls -lh $APP.db 2>/dev/null || echo '(no database yet)'"
	remote_in_app "ls -1 backups/*.db 2>/dev/null | sort -r | head -3 || true"

	log "nothing private is public"
	assert_app_dir_is_private
	assert_not_exposed

	log "the log"
	remote_in_app "grep -c 'level=ERROR' $APP.log 2>/dev/null || echo 0" | sed 's/^/     errors logged: /'
	remote_in_app "tail -n 5 $APP.log 2>/dev/null || true"
}

cmd_logs()    { load_config; need_app_dir; ssh_setup; remote_in_app "tail -n ${1:-80} $APP.log"; }
cmd_restart() { load_config; need_app_dir; ssh_setup; remote_in_app "./supervise.sh restart"; }

# A backup by hand stops the app for the length of a copy, for the reason
# backup_script gives.
cmd_backup() {
	load_config; need_app_dir; ssh_setup
	locked "./supervise.sh stop
$(backup_script)"
	remote_in_app "./supervise.sh start"
}

# ------------------------------------------------------------------ dispatch

usage() {
	cat <<-USAGE
		usage: deploy/deploy.sh <command>

		  deploy           build, ship, pre-flight, swap, health-check, roll back on failure
		  status           what is live, and whether it is well
		  logs [n]         the last n lines of the server's log (80)
		  backup           back up the database now (stops the app for a moment)
		  restart          restart the app
		  htaccess         install the Apache front end only, then check it from outside
		  render-htaccess  print the .htaccess that would be installed, touch nothing
	USAGE
}

case "${1:-}" in
deploy)          shift; cmd_deploy "$@" ;;
status)          shift; cmd_status "$@" ;;
logs)            shift; cmd_logs "$@" ;;
backup)          shift; cmd_backup "$@" ;;
restart)         shift; cmd_restart "$@" ;;
htaccess)        shift; cmd_htaccess "$@" ;;
render-htaccess) shift; load_config; render_htaccess ;;
*)               usage; exit 2 ;;
esac
