#!/usr/bin/env bash
#
# Exercises scripts/translate with claude replaced by a script that prints
# the environment it was started in, against an invented secrets.env and key.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LAUNCH="$REPO_DIR/scripts/translate"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then
		printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else
		printf '  FAIL %s\n' "$what"; fail=$((fail + 1))
	fi
}

cat > "$TMP/claude" <<'FAKE'
#!/usr/bin/env bash
printf 'URL=%s\nKEY=%s\nSMTP=%s\nARGS=%s\n' "${RECONCILE_URL:-}" "${RECONCILE_API_KEY:-}" "${SMTP_PASSWORD:-}" "$*"
FAKE
chmod +x "$TMP/claude"

export CLAUDE="$TMP/claude" SECRETS_ENV="$TMP/secrets.env" RECONCILE_KEY_FILE="$TMP/conf/reconcile/api-key"
unset RECONCILE_URL RECONCILE_API_KEY SMTP_PASSWORD APP_HOST

launch() { "$LAUNCH" "$@" > "$TMP/out" 2> "$TMP/err"; }
said() { grep -q -- "$1" "$TMP/out" "$TMP/err"; }

check "no secrets.env is refused" bash -c '! "$0" >/dev/null 2>&1' "$LAUNCH"

printf 'SMTP_PASSWORD=invented-mail-password\n' > "$SECRETS_ENV"
check "no APP_HOST is refused" bash -c '! "$0" >/dev/null 2>&1' "$LAUNCH"

printf 'APP_HOST=reconcile.example.invalid  # the public address\nSMTP_PASSWORD=invented-mail-password\n' > "$SECRETS_ENV"

check "the first run makes the key file and stops" bash -c '! "$0" >/dev/null 2>&1' "$LAUNCH"
check "and the file is private" test -z "$(find "$RECONCILE_KEY_FILE" \( -perm -g=r -o -perm -o=r \) -print)"
check "and the folder too" test -z "$(find "$(dirname "$RECONCILE_KEY_FILE")" -maxdepth 0 -perm -o=r -print)"
check "an empty key file is refused" bash -c '! "$0" >/dev/null 2>&1' "$LAUNCH"

printf 'not-a-key\n' > "$RECONCILE_KEY_FILE"
check "something that is not a key is refused" bash -c '! "$0" >/dev/null 2>&1' "$LAUNCH"

printf '# from the site, 2026-10-09\nrcn_testid.testsecret  \n' > "$RECONCILE_KEY_FILE"
chmod 644 "$RECONCILE_KEY_FILE"
check "a key others can read is refused" bash -c '! "$0" >/dev/null 2>&1' "$LAUNCH"

chmod 600 "$RECONCILE_KEY_FILE"
launch --model fable || true
check "the site is https and APP_HOST" said 'URL=https://reconcile.example.invalid$'
check "the key is the file's line, trimmed" said 'KEY=rcn_testid.testsecret$'
check "nothing else from secrets.env reaches claude" said 'SMTP=$'
check "claude's own options pass through" said 'ARGS=--model fable$'

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
