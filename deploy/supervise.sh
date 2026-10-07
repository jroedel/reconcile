#!/usr/bin/env bash
#
# Keep the server running, without systemd.
#
# The target is a konsoleH account with no root and no user D-Bus, so
# `systemctl --user` cannot work. Supervision is cron plus this script: `start`
# is idempotent and is the @reboot line, and `watch` -- start, but silent when
# there is nothing to do -- runs every five minutes as the watchdog.
#
# Why watch is silent. Cron emails whatever a job prints, so a watchdog that
# says "already running as 86229" sends 288 identical emails a day, from root
# at the host, unsigned. That buried the one email that matters -- the app was
# down and has been started, or would not start -- and taught Gmail to file
# every one of them as spam (2026-10-02). watch prints only when it acted or
# failed, so an email from it is news.
#
# Taken from stewards, which took it from mass-intentions less its rehearsal
# instance, and with one
# difference that matters on a shared crontab: this script takes no instance
# argument. mass-intentions' deploy removes every cron line matching
# "supervise.sh start production" before writing its own, so a line of ours
# in that shape would be deleted by the other project's next deploy.
set -euo pipefail

# The log holds one line per request. On shared hosting "other" is the other
# customers on the machine, and there is no reason for them to have any of it.
umask 077

cd "$(dirname "${BASH_SOURCE[0]}")"

APP=reconcile
PIDFILE="$APP.pid"
LOGFILE="$APP.log"
LOCKFILE="$APP.lock"

# The umask governs files created from here on, and cannot reach one that
# already exists -- a redirect appends to the old file and keeps its mode.
for f in "$PIDFILE" "$LOGFILE" "$LOCKFILE"; do
	[ -e "$f" ] && chmod 600 "$f" 2>/dev/null || true
done

# running() checks the pid is alive AND is this program.
#
# A pidfile on its own is a claim, not a fact: after a reboot the number in it
# is very likely some other process, and acting on that would kill a stranger.
#
# The test is the process's argv[0], exactly, and not a substring of its
# command line as in mass-intentions. Every path in this directory contains
# "reconcile", so a substring match would take this very script, or a shell
# someone has open in the directory, for the server.
running() {
	local pid argv0
	[ -f "$PIDFILE" ] || return 1
	pid="$(cat "$PIDFILE" 2>/dev/null)" || return 1
	[ -n "$pid" ] || return 1
	kill -0 "$pid" 2>/dev/null || return 1

	argv0="$(tr '\0' '\n' < "/proc/$pid/cmdline" 2>/dev/null | head -1)" || return 1
	[ "$argv0" = "./$APP" ] || return 1

	printf '%s' "$pid"
}

do_start() {
	local pid
	if pid="$(running)"; then
		echo "already running as $pid"
		return 0
	fi

	# The lock serialises against the five-minute watchdog firing in the middle
	# of a deploy's restart, which would otherwise start a second copy that
	# then loses the race for the port and dies confusingly.
	exec 9>"$LOCKFILE"
	if ! flock -w 30 9; then
		echo "another start is in progress" >&2
		return 1
	fi

	if pid="$(running)"; then
		echo "came up while we waited, as $pid"
		return 0
	fi

	# 9>&- closes the inherited lock fd in the child, so the lock is released
	# when this script exits rather than being held for the life of the server.
	setsid nohup ./run.sh </dev/null >>"$LOGFILE" 2>&1 9>&- &

	sleep 1

	if pid="$(running)"; then
		echo "started as $pid"
	else
		echo "did not start; the last of $LOGFILE:" >&2
		tail -n 20 "$LOGFILE" >&2 || true
		return 1
	fi
}

# do_watch is do_start for cron: nothing at all when the app is already up,
# and start's own words when it is not, which are worth an email.
do_watch() {
	running >/dev/null && return 0
	echo "the app was not running"
	do_start
}

do_stop() {
	local pid
	if ! pid="$(running)"; then
		echo "not running"
		rm -f "$PIDFILE"
		return 0
	fi

	# SIGINT, which the binary treats as a request to shut down gracefully.
	kill -INT "$pid" 2>/dev/null || true

	for _ in $(seq 1 30); do
		running >/dev/null || { rm -f "$PIDFILE"; echo "stopped"; return 0; }
		sleep 1
	done

	echo "did not stop in 30s; killing it" >&2
	kill -KILL "$pid" 2>/dev/null || true
	rm -f "$PIDFILE"
}

do_status() {
	local pid
	if pid="$(running)"; then
		echo "running as $pid"
	else
		echo "not running"
		return 1
	fi
}

case "${1:-}" in
start)   do_start ;;
watch)   do_watch ;;
stop)    do_stop ;;
restart) do_stop; do_start ;;
status)  do_status ;;
*) echo "usage: supervise.sh {start|watch|stop|restart|status}" >&2; exit 2 ;;
esac
