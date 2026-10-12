#!/usr/bin/env bash
#
# The deploy's moving parts, each tested as shipped.
#
# Every function under test is extracted from deploy/deploy.sh or
# deploy/supervise.sh with sed and eval, rather than reimplemented here, so the
# test is of what deploys and not of a copy that can drift. Nothing reaches the
# server: `remote` and `remote_in_app` run their commands in a temporary
# directory standing in for APP_DIR.
#
# Predicates are called by name and never through `bash -c`. A shell function
# is not exported to a subshell, so `bash -c 'f && exit 1; exit 0'` reports
# "command not found" and passes whatever f does -- mass-intentions' first
# supersede test did exactly that in four of its five assertions.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY="$REPO_DIR/deploy/deploy.sh"
SUPERVISE="$REPO_DIR/deploy/supervise.sh"
BACKUP="$REPO_DIR/deploy/backup.sh"

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else printf '  FAIL %s\n' "$what"; fail=$((fail + 1)); fi
}

TMP="$(mktemp -d)"
cleanup() {
	# Any process a test started, so a failed assertion cannot leave a sleep
	# running on a CI runner.
	jobs -p | xargs -r kill 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT

extract() { sed -n "/^$2() {/,/^}/p" "$1"; }
body() { extract "$DEPLOY" "$1"; }

log()  { :; }
ok()   { :; }
bad()  { :; }
warn() { :; }

APP=reconcile
APP_DIR="$TMP/app"
Q_DIR="$(printf '%q' "$APP_DIR")"
mkdir -p "$APP_DIR"

remote()        { bash -c "$1"; }
remote_in_app() { ( cd "$APP_DIR" && bash -c "$1" ); }

# ------------------------------------------------------------------ rollback

echo "rolling back"

eval "$(body do_rollback)"

# supervise.sh, faked to the one behaviour that matters: it refuses to start a
# binary whose contents say it is bad, which is what the real one does by way
# of the binary exiting and the pid check failing.
cat > "$APP_DIR/supervise.sh" <<'FAKE'
#!/usr/bin/env bash
case "$1" in
start) grep -q '^bad' ./reconcile && exit 1; echo running > ./state ;;
stop)  rm -f ./state ;;
esac
exit 0
FAKE
chmod +x "$APP_DIR/supervise.sh"

up() { [ -f "$APP_DIR/state" ]; }
holds() { grep -q "$1" "$APP_DIR/reconcile"; }

# A good release, live, and recorded as known good by the deploy that shipped it.
printf 'good v1\n' > "$APP_DIR/reconcile"
cp -p "$APP_DIR/reconcile" "$APP_DIR/reconcile.last-good"

# Two failed deploys in a row. Each copies what is in place into .prev first,
# as cmd_deploy does, so by the second .prev is itself bad -- which is why .prev
# alone is not enough.
for v in 2 3; do
	cp -p "$APP_DIR/reconcile" "$APP_DIR/reconcile.prev"
	printf 'bad v%s\n' "$v" > "$APP_DIR/reconcile"
	do_rollback >/dev/null 2>&1
done

check "two failed deploys in a row still roll back to a binary that works" holds 'good v1'
check "and it is running" up
check "the failed binary is kept for a person to look at" test -f "$APP_DIR/reconcile.failed"
check "the known-good copy is kept, not consumed" test -f "$APP_DIR/reconcile.last-good"

deploy_body() { body cmd_deploy; }
guarded_start() { deploy_body | grep -q 'if ! remote_in_app "./supervise.sh start"; then'; }
start_rolls_back() { deploy_body | sed -n '/if ! remote_in_app "\.\/supervise.sh start"; then/,/^	fi/p' | grep -qE '^[[:space:]]*do_rollback$'; }
health_rolls_back() { deploy_body | sed -n '/if ! health_loopback; then/,/^	fi/p' | grep -qE '^[[:space:]]*do_rollback$'; }

check "the start is guarded, so set -e cannot end the script before the rollback" guarded_start
check "a start that fails rolls back" start_rolls_back
check "a start that does not answer rolls back" health_rolls_back

rm -f "$APP_DIR"/reconcile* "$APP_DIR/state" "$APP_DIR/supervise.sh"

# ------------------------------------------------------------------ cron

echo
echo "the watchdog and the nightly backup, on a crontab shared with mass-intentions and stewards"

export CRONFILE="$TMP/crontab.txt"
mkdir -p "$TMP/bin"
cat > "$TMP/bin/crontab" <<'FAKE'
#!/usr/bin/env bash
if [ "${1:-}" = "-l" ]; then
	[ -s "$CRONFILE" ] || exit 1
	cat "$CRONFILE"
else
	cat "$1" > "$CRONFILE"
fi
FAKE
chmod +x "$TMP/bin/crontab"
PATH="$TMP/bin:$PATH"

eval "$(grep '^CRON_MARKER=' "$DEPLOY")"
eval "$(body install_cron)"

# What mass-intentions installs, verbatim in shape, and a line of somebody
# else's, which a shared account will have.
cat > "$CRONFILE" <<'CRON'
0 3 * * * /home/someone/backup.sh
@reboot /home/u/intentions/supervise.sh start production # mass-intentions
*/5 * * * * /home/u/intentions/supervise.sh start production # mass-intentions
0 6 * * 0 cd /home/u/intentions && ./intentions -config config.toml -distribute # mass-intentions
@reboot cd /home/u/stewards && ./supervise.sh start # stewards
*/5 * * * * cd /home/u/stewards && ./supervise.sh watch # stewards
CRON

for _ in 1 2 3; do install_cron; done

count() { grep -c -- "$1" "$CRONFILE" || true; }
ours() { grep -c ' # reconcile$' "$CRONFILE" || true; }

check "three deploys leave exactly our three lines" test "$(ours)" -eq 3
check "one of them is the watchdog, every five minutes" \
	grep -q "^\*/5 \* \* \* \* cd $Q_DIR && ./supervise.sh watch # reconcile$" "$CRONFILE"
check "one starts it at boot" \
	grep -q "^@reboot cd $Q_DIR && ./supervise.sh start # reconcile$" "$CRONFILE"
check "and one backs it up every night" \
	grep -q "^23 3 \* \* \* cd $Q_DIR && ./backup.sh nightly # reconcile$" "$CRONFILE"
check "every mass-intentions line is left alone" test "$(count '# mass-intentions')" -eq 3
check "and every stewards line" test "$(count '# stewards$')" -eq 2
check "and so is somebody else's" test "$(count '/home/someone/backup.sh')" -eq 1

# The other direction. mass-intentions' install_cron removes every line matching
# this before writing its own -- copied here from its deploy.sh, because the
# repository is not available to CI. If ours matched it, its next deploy would
# delete our watchdog and nothing would restart this app again.
MASS_INTENTIONS_STALE='# mass-intentions|supervise\.sh start (production|rehearsal)'
survives_theirs() { [ "$(grep -Ev "$MASS_INTENTIONS_STALE" "$CRONFILE" | grep -c ' # reconcile$')" -eq 3 ]; }
check "mass-intentions' next deploy leaves our watchdog in place" survives_theirs

# stewards finds its own lines by the marker " # stewards" at the end of the
# line, exactly as we find ours, so the two can only collide if one marker
# ends the other.
STEWARDS_STALE=' # stewards$'
survives_stewards() { [ "$(grep -Ev "$STEWARDS_STALE" "$CRONFILE" | grep -c ' # reconcile$')" -eq 3 ]; }
check "and so does stewards' next deploy" survives_stewards

# One of ours at a path we no longer use. Found by the marker wherever it
# points, so a second watchdog cannot run an old binary for ever.
echo "*/5 * * * * cd /old/path && ./supervise.sh start # reconcile" >> "$CRONFILE"
install_cron
check "a line of ours at an old path is removed" bash -c '! grep -q /old/path "$0"' "$CRONFILE"

# The watchdog line as deploys before watch wrote it, which is what the
# server has until the first deploy of this one: replaced, not kept beside.
echo "*/5 * * * * cd $Q_DIR && ./supervise.sh start # reconcile" >> "$CRONFILE"
install_cron
check "the old, talkative watchdog is replaced" test "$(count 'supervise.sh start # reconcile$')" -eq 1

# ------------------------------------------------------------------ supersession

echo
echo "supersession"

eval "$(body superseded)"
fresh() { ! superseded; }

git init -q --bare "$TMP/origin.git"
git clone -q "$TMP/origin.git" "$TMP/work" 2>/dev/null
git -C "$TMP/work" config user.email test@example.invalid
git -C "$TMP/work" config user.name test
git -C "$TMP/work" commit -q --allow-empty -m first
git -C "$TMP/work" branch -M main
git -C "$TMP/work" push -q origin main

SAVED_REPO_DIR="$REPO_DIR"
REPO_DIR="$TMP/work"

check "a checkout at main's tip is not superseded" fresh

git -C "$TMP/work" commit -q --allow-empty -m second
git -C "$TMP/work" push -q origin main
git -C "$TMP/work" reset -q --hard HEAD~1
check "a checkout behind main's tip is superseded" superseded

REPO_DIR="$TMP/no-such-repo"
check "an unanswerable question deploys rather than silently skipping" fresh

REPO_DIR="$TMP/work"
DEPLOY_ALLOW_BRANCH=1
check "a deliberate branch deploy is never superseded" fresh
unset DEPLOY_ALLOW_BRANCH

REPO_DIR="$SAVED_REPO_DIR"

# ------------------------------------------------------------------ supervise

echo
echo "which process is the server"

eval "$(extract "$SUPERVISE" running)"
PIDFILE="$TMP/reconcile.pid"
is_running() { running >/dev/null; }
not_running() { ! is_running; }

# named starts sleep under the argv[0] it is given, and returns once the
# process carries it. Until bash reaches its exec, /proc/<pid>/cmdline still
# says "bash -c ...": checked at once, the server was sometimes not
# recognised (a deploy failed on it, 2026-10-08), and an impostor was "not
# taken for the server" without its name ever being looked at.
named() {
	local name="$1" pid tries=0
	bash -c 'exec -a "$0" sleep 30' "$name" &
	pid="$!"
	until [ "$(tr '\0' '\n' < "/proc/$pid/cmdline" 2>/dev/null | head -1)" = "$name" ]; do
		tries=$((tries + 1))
		[ "$tries" -le 100 ] || { echo "  $name never started" >&2; break; }
		sleep 0.05
	done
	printf '%s\n' "$pid" > "$PIDFILE"
}

# The server as run.sh leaves it: argv[0] is ./reconcile.
named ./reconcile
check "the server is recognised by its argv[0]" is_running

# Everything else in the directory has "reconcile" in its path, and none of it is
# the server. A substring match, as in mass-intentions, takes each of these for it.
for impostor in "$APP_DIR/supervise.sh" "/home/u/reconcile/run.sh" "vim reconcile.log"; do
	named "$impostor"
	check "\"$impostor\" is not taken for the server" not_running
done

printf '999999\n' > "$PIDFILE"
check "a pid that is not alive is not running" not_running

jobs -p | xargs -r kill 2>/dev/null || true

# ------------------------------------------------------------------ the front page

echo
echo "who answered the front page"

eval "$(body answered_by_app)"

app_headers="HTTP/2 404
content-security-policy: default-src 'none'; style-src 'self'
content-type: text/plain; charset=utf-8"

apache_headers="HTTP/2 404
content-type: text/html; charset=iso-8859-1
server: Apache"

app_answered()    { answered_by_app <<<"$app_headers"; }
apache_answered() { answered_by_app <<<"$apache_headers"; }
not_apache()      { ! apache_answered; }

check "the app's own 404 counts as the app answering" app_answered
check "Apache's 404 does not" not_apache

# ------------------------------------------------------------------ backup

echo
echo "backups"

eval "$(body backup_script)"

# backup.sh as shipped, which the snippet prunes through. Its clock is
# BACKUP_NOW: the backups below are stamped 2026-09-30, and are "now" until
# the clock is moved on.
cp "$BACKUP" "$APP_DIR/backup.sh"
utc() { command date -u -d "$1" +%s; }
export BACKUP_NOW; BACKUP_NOW="$(utc 2026-09-30T12:00:00Z)"

# The stamp comes from `date`, which is replaced so that four backups in the
# same second have four names.
# The count is kept in a file: backup_script is called inside $(...), a
# subshell, and a variable incremented there never reaches the next call.
echo 0 > "$TMP/stamps"
date() { local n; n=$(( $(cat "$TMP/stamps") + 1 )); echo "$n" > "$TMP/stamps"; printf '20260930T00000%dZ' "$n"; }

run_backup() { ( cd "$APP_DIR" && bash -c "$(backup_script)
echo after-the-snippet" ); }

out="$(run_backup)"
said_no_db()    { grep -q 'no database yet' <<<"$out"; }
carried_on()    { grep -q 'after-the-snippet' <<<"$out"; }
check "with no database yet, it says so" said_no_db
check "and does not end the command it is part of" carried_on

printf 'db\n' > "$APP_DIR/reconcile.db"
for _ in 1 2 3 4; do run_backup >/dev/null; done

kept() { find "$APP_DIR/backups" -name 'reconcile-*.db' | wc -l | tr -d ' '; }
check "four backups the same morning are all kept, being less than two days old (saw $(kept))" test "$(kept)" -eq 4
check "the backups directory is private" test "$(stat -c %a "$APP_DIR/backups")" = 700

# The files: snapshotted beside each backup, as links rather than copies.
# Invented files, never a real statement or receipt.
mkdir -p "$APP_DIR/files"
printf 'an invented receipt\n' > "$APP_DIR/files/aaaa"
printf 'an invented statement\n' > "$APP_DIR/files/bbbb"

out="$(run_backup 2>&1)"
snap="$(find "$APP_DIR/backups" -maxdepth 1 -name 'files-*' | sort -r | head -1)"

snapshotted()   { test -n "$snap" && test -f "$snap/aaaa" && test -f "$snap/bbbb"; }
same_inode()    { test "$(stat -c %i "$snap/aaaa")" = "$(stat -c %i "$APP_DIR/files/aaaa")"; }
said_snapshot() { grep -q 'files snapshotted to backups/files-' <<<"$out"; }
check "the files are snapshotted" snapshotted
check "as hard links, not copies" same_inode
check "and it says so" said_snapshot

rm "$APP_DIR/files/aaaa"
survives() { test "$(cat "$snap/aaaa")" = 'an invented receipt'; }
check "a file removed afterwards survives in the snapshot" survives

for _ in 1 2 3; do run_backup >/dev/null 2>&1; done
snaps() { find "$APP_DIR/backups" -maxdepth 1 -name 'files-*' | wc -l | tr -d ' '; }

# A week on, the next deploy's backup prunes the old ones: of a day a week
# ago, only its newest is kept.
newest="$(find "$APP_DIR/backups" -name 'reconcile-*.db' | sort -r | head -1)"
BACKUP_NOW="$(utc 2026-10-07T12:00:00Z)"
( cd "$APP_DIR" && ./backup.sh prune )
check "a week later, one backup of that day is kept (saw $(kept))" test "$(kept)" -eq 1
check "and it is the day's newest" test -f "$newest"
check "and one snapshot of the files (saw $(snaps))" test "$(snaps)" -eq 1
BACKUP_NOW="$(utc 2026-09-30T12:00:00Z)"

# A host whose cp cannot link: the snapshot warns, the database is backed up
# all the same, and the command the snippet is part of carries on.
mkdir -p "$TMP/nocp"
printf '#!/bin/sh\nfor a in "$@"; do [ "$a" = -al ] && { echo "cp: cannot create hard link" >&2; exit 1; }; done\nexec /bin/cp "$@"\n' > "$TMP/nocp/cp"
chmod +x "$TMP/nocp/cp"

out="$( ( cd "$APP_DIR" && PATH="$TMP/nocp:$PATH" bash -c "$(backup_script)
echo after-the-snippet" ) 2>&1 )"

warned()       { grep -q 'WARNING: the files could not be snapshotted' <<<"$out"; }
db_backed_up() { grep -q 'backed up to backups/reconcile-' <<<"$out"; }
no_half_snap() { test "$(snaps)" -eq 1; }
check "a snapshot that fails warns" warned
check "and the database is backed up all the same" db_backed_up
check "and nothing half-made is left" no_half_snap
check "and the command carries on" grep -q 'after-the-snippet' <<<"$out"
unset -f date

# ------------------------------------------------------------------ kept by age

echo
echo "which backups are kept"

eval "$(extract "$BACKUP" doomed)"
STAMP='[0-9]{8}T[0-9]{6}Z'

# Invented names, a year of them, against a clock at 2026-10-12 00:00: two
# days ago is the 10th, thirty days ago the 12th of September.
let_go="$(doomed 20261010T000000Z 20260912T000000Z <<'NAMES'
reconcile-20261011T230000Z.db
reconcile-20261011T010000Z.db
reconcile-20261010T120000Z.db
reconcile-20261009T200000Z.db
reconcile-20261009T100000Z.db
reconcile-20260920T030000Z.db
reconcile-20260901T120000Z.db
reconcile-20260901T080000Z.db
reconcile-20260815T030000Z.db
reconcile-20260801T030000Z.db
reconcile-20250105T030000Z.db
reconcile-not-one-of-ours.db
NAMES
)"

goes()  { grep -qx "reconcile-$1.db" <<<"$let_go"; }
stays() { ! goes "$1"; }
check "everything from the last two days stays" stays 20261011T010000Z
check "  however many there are in a day" stays 20261010T120000Z
check "of an older day, its newest stays" stays 20261009T200000Z
check "  and the others go" goes 20261009T100000Z
check "a day alone in its last thirty stays" stays 20260920T030000Z
check "past thirty days, the newest of the month stays" stays 20260901T120000Z
check "  and the rest of that month goes" goes 20260901T080000Z
check "  in every month" goes 20260801T030000Z
check "a month's newest stays for good" stays 20250105T030000Z
check "a name it did not make is never let go" stays not-one-of-ours
check "and nothing else goes (saw $(grep -c . <<<"$let_go"))" test "$(grep -c . <<<"$let_go")" -eq 3

# ------------------------------------------------------------------ nightly

echo
echo "the nightly backup"

NIGHT="$TMP/night"
mkdir -p "$NIGHT/files"
cp "$BACKUP" "$NIGHT/backup.sh"

# The binary, faked to what backup.sh asks of it: -backup writes the file it
# is given, or fails when told to.
cat > "$NIGHT/reconcile" <<'FAKE'
#!/usr/bin/env bash
[ -f ./fail ] && { echo "reconcile: the database is locked" >&2; exit 1; }
while [ $# -gt 0 ]; do [ "$1" = -backup ] && { printf 'snapshot\n' > "$2"; exit 0; }; shift; done
exit 2
FAKE
chmod +x "$NIGHT/reconcile"

night() { ( cd "$NIGHT" && ./backup.sh nightly ) 2>&1; }

out="$(night)"; status=$?
nothing_yet() { [ -z "$out" ] && [ ! -d "$NIGHT/backups" ]; }
check "with no database yet, it does nothing and says nothing" nothing_yet

printf 'db\n' > "$NIGHT/reconcile.db"
printf 'an invented receipt\n' > "$NIGHT/files/cccc"
out="$(night)"; status=$?
nightly_db()    { find "$NIGHT/backups" -name 'reconcile-*.db' | grep -q .; }
nightly_files() { find "$NIGHT/backups" -path '*/files-*/cccc' | grep -q .; }
nightly_mode()  { test "$(stat -c %a "$(find "$NIGHT/backups" -name 'reconcile-*.db' | head -1)")" = 600; }
check "it backs the database up" nightly_db
check "and snapshots the files" nightly_files
check "privately" nightly_mode
check "and says nothing, so cron sends no mail" test -z "$out"
check "and succeeds" test "$status" -eq 0

touch "$NIGHT/fail"
sleep 1
before="$(find "$NIGHT/backups" -name 'reconcile-*.db' | wc -l)"
out="$(night)" && status=0 || status=$?
said_why()    { grep -q 'the database is locked' <<<"$out" && grep -q 'nightly backup of the database failed' <<<"$out"; }
nothing_half() { test "$(find "$NIGHT/backups" -name 'reconcile-*.db' | wc -l)" -eq "$before"; }
check "a backup that fails says why, so cron mails it" said_why
check "and fails" test "$status" -ne 0
check "and leaves nothing half-made" nothing_half
rm -f "$NIGHT/fail"
unset BACKUP_NOW

# ------------------------------------------------------------------ pull

echo
echo "pulling a copy to this machine"

eval "$(body sha256_of)"
eval "$(body cmd_pull_backup)"

# The server is a directory here, scp is cp, and what needs ssh or secrets.env
# is already done.
SERVER="$TMP/server"
mkdir -p "$SERVER/files/sized"
load_config() { :; }
need_app_dir() { :; }
ssh_setup() { :; }
die() { echo "DIED: $*" >&2; exit 1; }
pull_file() { cp "$SERVER/$1" "$2"; }
pull_in_server() { APP_DIR=. ; remote_in_app() { ( cd "$SERVER" && bash -c "$1" ); }; cmd_pull_backup "$@"; }

cp "$NIGHT/reconcile" "$SERVER/reconcile"
# Invented files, each named by its content's SHA-256 as the file store names
# them, and two things that are not uploads at all.
for content in 'an invented receipt' 'an invented statement' 'a third invented file'; do
	printf '%s\n' "$content" > "$SERVER/files/x"
	mv "$SERVER/files/x" "$SERVER/files/$(sha256_of "$SERVER/files/x")"
done
printf 'a picture made again from its photo\n' > "$SERVER/files/sized/thumb.jpg"
printf 'half an upload\n' > "$SERVER/files/.upload-123"

PULLED="$TMP/pulled"
out="$( (pull_in_server "$PULLED") 2>&1 )" && status=0 || status=$?

pulled_db()    { find "$PULLED" -maxdepth 1 -name 'reconcile-*.db' | grep -q .; }
db_private()   { test "$(stat -c %a "$(find "$PULLED" -maxdepth 1 -name 'reconcile-*.db' | head -1)")" = 600; }
no_leftover()  { ! find "$SERVER/backups" -name '*.pull.db' | grep -q .; }
three_files()  { test "$(find "$PULLED/files" -type f | wc -l)" -eq 3; }
named_right()  { for f in "$PULLED"/files/*; do [ "$(sha256_of "$f")" = "$(basename "$f")" ] || return 1; done; }
check "the pull succeeds" test "$status" -eq 0
check "it brings the database down" pulled_db
check "privately" db_private
check "and leaves no snapshot behind on the server" no_leftover
check "it brings the three files, and not the pictures or a half-made upload" three_files
check "each the content its name says" named_right
check "into a private directory" test "$(stat -c %a "$PULLED")" = 700

out="$( (pull_in_server "$PULLED") 2>&1 )"
first="$(find "$PULLED/files" -type f | sort | head -1)"
other="$(find "$PULLED/files" -type f | sort | tail -1)"
inode="$(stat -c %i "$other")"
rm "$first"
( pull_in_server "$PULLED" ) >/dev/null 2>&1
came_back() { test -f "$first" && [ "$(sha256_of "$first")" = "$(basename "$first")" ]; }
untouched() { [ "$(stat -c %i "$other")" = "$inode" ]; }
check "a second pull brings back a file that is missing here" came_back
check "and leaves the ones already here as they are" untouched

inside="$( (pull_in_server "$REPO_DIR/pulled-by-mistake") 2>&1 )" && status=0 || status=$?
refused_repo() { [ "$status" -ne 0 ] && grep -q 'inside the repository' <<<"$inside"; }
check "a destination inside the repository is refused" refused_repo
check "and nothing is left there" test ! -e "$REPO_DIR/pulled-by-mistake"
rm -rf "$REPO_DIR/pulled-by-mistake"

# ------------------------------------------------------------------ the lock

echo
echo "the swap holds the supervisor's lock"

eval "$(body locked)"

swap_is_locked() { deploy_body | grep -q 'locked "./supervise.sh stop$'; }
swap_backs_up() { deploy_body | sed -n '/locked "\.\/supervise.sh stop$/,/die/p' | grep -q 'backup_script'; }
swap_renames()  { deploy_body | sed -n '/locked "\.\/supervise.sh stop$/,/die/p' | grep -q "^mv \$APP.new \$APP"; }

check "stop, backup and rename are one locked command" swap_is_locked
check "the backup is inside it" swap_backs_up
check "and so is the rename" swap_renames

# While a locked command runs, a watchdog start -- which takes the same lock --
# must wait rather than start anything.
locked "sleep 2" &
sleep 0.5
watchdog_waits() { ! ( cd "$APP_DIR" && flock -n reconcile.lock true ); }
check "a watchdog start during the swap cannot take the lock" watchdog_waits
wait

watchdog_free() { ( cd "$APP_DIR" && flock -n reconcile.lock true ); }
check "and can once the swap is over" watchdog_free

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
