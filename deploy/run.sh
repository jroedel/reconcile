#!/usr/bin/env bash
#
# Launch the server in the foreground. supervise.sh backgrounds it.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# Every file this process creates is private to the account. SQLite makes its
# database, -wal and -shm with 0666 masked by the umask, and the account's
# default leaves them world-readable -- where "world" on shared hosting is the
# other customers on the machine.
umask 077

APP=reconcile

# The pid is written here, before exec, rather than by the caller recording $!.
# The caller's $! is the pid of the setsid/nohup wrapper, not of the process
# that ends up holding the port, so a supervisor that trusted it would kill the
# wrapper and leave the server running. exec keeps this pid for the binary.
printf '%s\n' "$$" > "$APP.pid"

exec "./$APP" -config config.toml
