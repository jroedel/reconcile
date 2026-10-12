#!/usr/bin/env bash
#
# The database's backups and the uploaded files' snapshots, taken every night
# and kept by age.
#
#   backup.sh nightly   take one now, beside the running server, then prune
#   backup.sh prune     only prune: the deploy calls this after its own backup
#
# Why nightly, and why by age. Until this, a backup was taken only by a deploy,
# and the newest 14 were kept. On a day of many merges that reached back less
# than a day (2026-10-11: 28 deploys the day before), so damage noticed the
# next morning was in every backup there was; and a week with no deploys had
# no backup at all. Kept by age, the backups reach back a fixed distance
# however often the app is deployed:
#
#   - everything from the last two days, for "what did I just do";
#   - the newest of each day for thirty days, for "it was fine last week";
#   - the newest of each month after that, for good, for "what did the books
#     say at the end of March". The database is a few megabytes, and a
#     snapshot of the files is hard links, which cost a file's size only once
#     the file itself is gone.
#
# The database is copied by the binary itself (reconcile -backup), which takes
# a consistent copy while the server runs (foundation/sqldb.Snapshot). Not cp:
# the database is in WAL mode, and a copy of the .db alone is missing its
# latest writes without saying so. Not sqlite3, which shared hosting does not
# promise to have.
#
# Silent when all is well, because cron emails whatever a job prints; see
# supervise.sh for what 288 emails a day did to the one that mattered. What it
# does print is a failure, and worth reading.
set -euo pipefail

# The backups hold everybody's books. On shared hosting "other" is the other
# customers on the machine.
umask 077

cd "$(dirname "${BASH_SOURCE[0]}")"

APP=reconcile
LOCKFILE="$APP.lock"

# STAMP is the shape of every name this script makes and the only shape it
# ever deletes: a name it did not make is somebody's, and is left alone.
STAMP='[0-9]{8}T[0-9]{6}Z'

# doomed reads backup names on stdin and prints those the rule above lets go.
# $1 and $2 are the stamps of two days and of thirty days ago.
#
# The stamps sort as they read, so "newest of each day" is the first of that
# day in reverse order, and so is the newest of each month. A backup moving
# from one band to the next can only be let go, never kept back: each day's
# newest stays its day's newest until it is old enough to be its month's.
doomed() {
	local recent="$1" daily="$2" name stamp key
	local -A seen=()

	while read -r name; do
		stamp="$(grep -oE "$STAMP" <<<"$name" | tail -1)" || continue
		[ -n "$stamp" ] || continue

		if [[ ! "$stamp" < "$recent" ]]; then
			continue
		elif [[ ! "$stamp" < "$daily" ]]; then
			key="day ${stamp:0:8}"
		else
			key="month ${stamp:0:6}"
		fi

		if [ -n "${seen[$key]:-}" ]; then
			printf '%s\n' "$name"
		else
			seen[$key]=1
		fi
	done < <(sort -r)
}

# prune lets go of the database backups and the files' snapshots the rule
# does not keep, each kind on its own. BACKUP_NOW, in seconds since 1970, is
# for scripts/deploy-test.sh, which cannot wait for days to pass.
prune() {
	[ -d backups ] || return 0

	local now recent daily
	now="${BACKUP_NOW:-$(date -u +%s)}"
	recent="$(date -u -d "@$((now - 2 * 86400))" +%Y%m%dT%H%M%SZ)"
	daily="$(date -u -d "@$((now - 30 * 86400))" +%Y%m%dT%H%M%SZ)"

	local name
	find backups -maxdepth 1 -type f -regextype posix-extended -regex "backups/$APP-$STAMP\.db" -printf '%f\n' \
		| doomed "$recent" "$daily" \
		| while read -r name; do rm -f "backups/$name" "backups/$name-wal"; done

	find backups -maxdepth 1 -type d -regextype posix-extended -regex "backups/files-$STAMP" -printf '%f\n' \
		| doomed "$recent" "$daily" \
		| while read -r name; do rm -rf "backups/$name"; done
}

# nightly takes a backup beside the running server, then prunes.
#
# Under the supervisor's lock, as a deploy's backup is: a deploy stops the
# app, backs up and swaps the binary holding it, and a nightly backup in the
# middle of that would run whichever binary was in place, and prune while the
# deploy's own backup was being written. Five minutes is longer than any
# deploy holds it.
nightly() {
	exec 9>"$LOCKFILE"
	flock -w 300 9 || { echo "the nightly backup waited five minutes for the lock and gave up; nothing was backed up" >&2; exit 1; }

	# Before the first deploy that made one, there is nothing to keep.
	[ -f "$APP.db" ] || return 0

	mkdir -p backups && chmod 700 backups

	local stamp; stamp="$(date -u +%Y%m%dT%H%M%SZ)"

	if ! ./"$APP" -config config.toml -backup "backups/$APP-$stamp.db"; then
		rm -f "backups/$APP-$stamp.db"
		echo "the nightly backup of the database failed; the message above says why" >&2
		exit 1
	fi

	# The files as the deploy snapshots them (deploy.sh, backup_script):
	# hard links, which the file store never writes through.
	if [ -d files ] && ! cp -al files "backups/files-$stamp"; then
		rm -rf "backups/files-$stamp"
		echo "WARNING: the files could not be snapshotted tonight; the database was backed up" >&2
	fi

	prune
}

case "${1:-}" in
nightly) nightly ;;
prune)   prune ;;
*) echo "usage: backup.sh {nightly|prune}" >&2; exit 2 ;;
esac
