#!/usr/bin/env bash
#
# Exercises scripts/secrets against a throwaway credentials file and a fake gh.
#
# Nothing here reaches GitHub or the server. gh is replaced on PATH by a script
# that answers from files in a temporary directory, and every host is under
# .invalid, which RFC 2606 guarantees never resolves -- so the ssh and HTTPS
# sections fail at DNS, immediately, which is itself one of the things tested.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS="$REPO_DIR/scripts/secrets"

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

# Never the real secrets.env: every run below points at the fixture.
export SECRETS_ENV="$TMP/secrets.env"

cat > "$SECRETS_ENV" <<'ENV'
DEPLOY_SSH_HOST=example.invalid
DEPLOY_SSH_USER=someuser
DEPLOY_SSH_PORT=2222
APP_HOST=reconcile.example.invalid
APP_PORT=8461
APP_DIR=reconcile
APP_DOCROOT=public_html/reconcile.example.invalid/public
KEEP_BACKUPS=14
DEV_ADDR="127.0.0.1:18461"
ENV

# A real throwaway key, so the decode-and-inspect path is exercised rather than
# mocked. It never leaves this temporary directory.
ssh-keygen -q -t ed25519 -f "$TMP/testkey" -C "test" -N ""
printf 'DEPLOY_SSH_KEY_B64=%s\n' "$(base64 -w0 < "$TMP/testkey")" >> "$SECRETS_ENV"
printf 'DEPLOY_KNOWN_HOSTS_B64=%s\n' "$(printf '[example.invalid]:2222 ssh-ed25519 AAAAC3Nz\n' | base64 -w0)" >> "$SECRETS_ENV"

# The fake gh. Logged in; the secrets it lists are the lines of gh-secrets, the
# variables are the name=value lines of gh-vars. Anything that tries to *set* a
# value is recorded, so a test can assert nothing was sent.
mkdir -p "$TMP/bin"
#
# A variable set is stored trimmed, because that is what GitHub did with the
# "14 " that first exposed the parser: it kept "14", and the read-back then
# disagreed with what was sent.
cat > "$TMP/bin/gh" <<FAKE
#!/usr/bin/env bash
case "\$1 \$2" in
"auth status")   exit 0 ;;
"secret list")   cat "$TMP/gh-secrets" 2>/dev/null; exit 0 ;;
"variable list")
	# Lagging, while gh-stale counts down: the list shows the snapshot from
	# before, as GitHub's does for a few seconds after a set.
	n="\$(cat "$TMP/gh-stale" 2>/dev/null || echo 0)"
	if [ "\$n" -gt 0 ]; then
		echo "\$((n - 1))" > "$TMP/gh-stale"
		cat "$TMP/gh-vars.before"
	else
		cat "$TMP/gh-vars" 2>/dev/null
	fi
	exit 0 ;;
"variable set")
	echo "\$*" >> "$TMP/gh-writes"
	value="\$(cat)"
	value="\$(printf '%s' "\$value" | sed 's/^[[:space:]]*//; s/[[:space:]]*\$//')"
	touch "$TMP/gh-vars"
	sed -i "/^\$3=/d" "$TMP/gh-vars"
	printf '%s=%s\n' "\$3" "\$value" >> "$TMP/gh-vars"
	exit 0 ;;
*)               cat > /dev/null; echo "\$*" >> "$TMP/gh-writes"; exit 0 ;;
esac
FAKE
chmod +x "$TMP/bin/gh"

# sleep is on PATH too, so the read-back's waits cost nothing here; each call
# is recorded so a test can see that it waited.
printf '#!/bin/sh\necho "$1" >> "%s/slept"\n' "$TMP" > "$TMP/bin/sleep"
chmod +x "$TMP/bin/sleep"
export PATH="$TMP/bin:$PATH"

run() { "$SECRETS" "$@" > "$TMP/out" 2>&1; }
said() { grep -q -- "$1" "$TMP/out"; }

echo "render"
"$SECRETS" render production > "$TMP/prod.toml"
"$SECRETS" render local      > "$TMP/local.toml"

check "production listens on the loopback, on APP_PORT" \
	grep -q '^addr = "127.0.0.1:8461"$' "$TMP/prod.toml"
check "local uses DEV_ADDR and loses its quotes" \
	grep -q '^addr = "127.0.0.1:18461"$' "$TMP/local.toml"
check "with no runtime values, there is nothing but [server] and [log]" \
	bash -c '! grep -E "^\[" "$0" | grep -vqxE "\[(server|log)\]"' "$TMP/prod.toml"

# The two characters that end a TOML string early, in a value that reaches the
# config. There is no runtime secret yet to carry them; the first one will be
# a password, which is exactly the kind of value that holds both.
{ cat "$SECRETS_ENV"; printf "DEV_ADDR='a\"b\\\\c'\n"; } > "$TMP/escape.env"
SECRETS_ENV="$TMP/escape.env" "$SECRETS" render local > "$TMP/escape.toml"
check "a quote and a backslash in a value are escaped" \
	grep -qxF 'addr = "a\"b\\c"' "$TMP/escape.toml"

echo
echo "the rendered config is one the binary accepts"
if go -C "$REPO_DIR" build -o "$TMP/reconcile" ./cmd/reconcile 2>/dev/null; then
	accepts() { "$TMP/reconcile" -config "$1" -check >/dev/null; }

	check "production config passes the binary's own -check" accepts "$TMP/prod.toml"
	check "and so does local" accepts "$TMP/local.toml"
	check "and so does one with escapes in it, read back as written" \
		bash -c '"$0" -config "$1" -check | grep -qF "listening on   a\"b\\c"' "$TMP/reconcile" "$TMP/escape.toml"
	check "and -check names the address the deploy greps for" \
		bash -c '"$0" -config "$1" -check | grep -qx "listening on *127.0.0.1:8461"' "$TMP/reconcile" "$TMP/prod.toml"
else
	echo "  skip the binary would not build here"
fi

echo
echo "secrets stay where they belong"
# The policy is three lists in scripts/secrets. A key in two of them would be a
# credential that push sends somewhere it must not go, so it is asserted rather
# than reviewed.
check "no key is in two destination groups at once" \
	bash -c '
	    eval "$(grep -E "^(DEPLOY_KEYS|VAR_KEYS|RUNTIME_KEYS)=" "'"$SECRETS"'")"
	    printf "%s\n" "${DEPLOY_KEYS[@]}" "${VAR_KEYS[@]}" "${RUNTIME_KEYS[@]}" \
	        | sort | uniq -d | grep -q . && exit 1
	    exit 0'

check "every key in the example file belongs to a group, or is local" \
	bash -c '
	    eval "$(grep -E "^(DEPLOY_KEYS|VAR_KEYS|RUNTIME_KEYS)=" "'"$SECRETS"'")"
	    known=" ${DEPLOY_KEYS[*]} ${VAR_KEYS[*]} ${RUNTIME_KEYS[*]} "
	    for k in $(sed -n "s/^\([A-Z][A-Z0-9_]*\)=.*/\1/p" "'"$REPO_DIR"'/secrets.env.example"); do
	        case "$k" in DEV_*) continue ;; esac
	        case "$known" in *" $k "*) ;; *) echo "$k goes nowhere" >&2; exit 1 ;; esac
	    done'

# gh sets a value from stdin only when --body is absent; `--body -` stores the
# literal string "-". Nothing in the script may use that form.
check "no gh secret or variable is set with --body" \
	bash -c '! grep -E "gh (secret|variable) set[^|]*--body" "'"$SECRETS"'"'

echo
echo "the key lives in the file, not at a path"
check "a base64 key round-trips byte-identical" \
	bash -c '
	    eval "$(grep "^DEPLOY_SSH_KEY_B64=" "$SECRETS_ENV")"
	    printf "%s" "$DEPLOY_SSH_KEY_B64" | base64 -d | cmp -s - "'"$TMP"'/testkey"'

echo
echo "check, with everything on GitHub"
printf '%s\n' DEPLOY_SSH_HOST DEPLOY_SSH_USER DEPLOY_SSH_PORT DEPLOY_SSH_KEY_B64 DEPLOY_KNOWN_HOSTS_B64 > "$TMP/gh-secrets"
cat > "$TMP/gh-vars" <<'VARS'
APP_HOST=reconcile.example.invalid
APP_PORT=8461
APP_DIR=reconcile
APP_DOCROOT=public_html/reconcile.example.invalid/public
KEEP_BACKUPS=14
VARS

check "a complete file and a matching GitHub pass" run check
check "the host key is recognised in its [host]:port form" said "pinned for \[example.invalid\]:2222"

echo
echo "check, catching what is wrong"
sed -i 's/^APP_PORT=8461$/APP_PORT=9999/' "$TMP/gh-vars"
check "a variable GitHub holds differently is a failure" bash -c '! "$0" check >/dev/null 2>&1' "$SECRETS"
sed -i 's/^APP_PORT=9999$/APP_PORT=8461/' "$TMP/gh-vars"

sed '/^DEPLOY_SSH_KEY_B64$/d' "$TMP/gh-secrets" > "$TMP/gh-secrets.partial"
cp "$TMP/gh-secrets" "$TMP/gh-secrets.full"
cp "$TMP/gh-secrets.partial" "$TMP/gh-secrets"
check "a secret missing from GitHub is a failure" bash -c '! "$0" check >/dev/null 2>&1' "$SECRETS"
cp "$TMP/gh-secrets.full" "$TMP/gh-secrets"

sed "s/^DEPLOY_SSH_KEY_B64=.*/DEPLOY_SSH_KEY_B64=not!valid!base64/" "$SECRETS_ENV" > "$TMP/bad.env"
check "a corrupt key is refused before a deploy uses it" \
	bash -c '! SECRETS_ENV="$1" "$0" check >/dev/null 2>&1' "$SECRETS" "$TMP/bad.env"

sed "s|^APP_DIR=.*|APP_DIR=public_html/reconcile|" "$SECRETS_ENV" > "$TMP/exposed.env"
SECRETS_ENV="$TMP/exposed.env" "$SECRETS" check > "$TMP/out" 2>&1 || true
check "an APP_DIR inside public_html is refused" said "is inside public_html"

check "a missing credentials file is an error, not a default" \
	bash -c '! SECRETS_ENV=/nonexistent "$0" check >/dev/null 2>&1' "$SECRETS"

# A credentials file is not a script. Sourcing one would run this.
cp "$SECRETS_ENV" "$TMP/hostile.env"
printf 'APP_HOST=$(touch %s/pwned)\n' "$TMP" >> "$TMP/hostile.env"
SECRETS_ENV="$TMP/hostile.env" "$SECRETS" render production > /dev/null 2>&1 || true
check "secrets.env is parsed, not executed" test ! -e "$TMP/pwned"

echo
echo "status"
# The server and the public address are under .invalid, so status must say
# not ready -- and it must say so after running every section, not at the first
# one that fails. A readiness report that stops early is run six times.
check "is not ready when the server cannot be reached" bash -c '! "$0" status >/dev/null 2>&1' "$SECRETS"
run status || true
check "reports the server section" said "the server"
check "names the unreachable server" said "could not reach someuser@example.invalid"
check "gets past it to the public check" said "does not resolve. Add an A record"
check "gets past that to the pipeline" said "the deploy pipeline"
check "and does not call it ready while deploy.sh cannot ship the binary" \
	bash -c 'grep -q "^cmd_deploy()" "$0/deploy/deploy.sh" || grep -q "does not ship the binary yet" "$1"' "$REPO_DIR" "$TMP/out"
check "ends with one verdict" said "not ready: "

echo
echo "nothing was sent"
check "check and status never set a GitHub secret or variable" test ! -e "$TMP/gh-writes"

echo
echo "a mismatch shows both sides"
sed -i 's/^KEEP_BACKUPS=14$/KEEP_BACKUPS=15/' "$TMP/gh-vars"
run check || true
check "check names what secrets.env says and what GitHub has" \
	said "KEEP_BACKUPS differs: secrets.env says 14, GitHub has 15"
sed -i 's/^KEEP_BACKUPS=15$/KEEP_BACKUPS=14/' "$TMP/gh-vars"

echo
echo "the file is read the way a person reads it"
# Everything that makes a value look like 14 to a person and not to a parser:
# a trailing space, a Windows line ending, an inline comment, and a quoted
# value with a comment after it. Each must reach GitHub and the config as the
# bare value, and push must then agree with what GitHub kept.
{
	sed '/^\(KEEP_BACKUPS\|APP_PORT\|APP_DIR\|DEV_ADDR\)=/d' "$SECRETS_ENV"
	printf 'KEEP_BACKUPS=14 \n'
	printf 'APP_PORT=8461\r\n'
	printf 'APP_DIR=reconcile   # beside public_html, never in it\n'
	printf 'DEV_ADDR="127.0.0.1:18461"  # a quoted value, then a comment\n'
} > "$TMP/messy.env"

SECRETS_ENV="$TMP/messy.env" "$SECRETS" render production > "$TMP/messy-prod.toml"
SECRETS_ENV="$TMP/messy.env" "$SECRETS" render local > "$TMP/messy-local.toml"

check "a carriage return does not reach the config" \
	grep -qx 'addr = "127.0.0.1:8461"' "$TMP/messy-prod.toml"
check "a quoted value keeps exactly what is between its quotes" \
	grep -qx 'addr = "127.0.0.1:18461"' "$TMP/messy-local.toml"

# A value that must keep its spaces still can, by being quoted.
{ cat "$TMP/messy.env"; printf 'DEV_ADDR="  spaced  "\n'; } > "$TMP/spaced.env"
SECRETS_ENV="$TMP/spaced.env" "$SECRETS" render local > "$TMP/spaced.toml"
check "a quoted value keeps its spaces" \
	grep -qx 'addr = "  spaced  "' "$TMP/spaced.toml"

rm -f "$TMP/gh-vars"
SECRETS_ENV="$TMP/messy.env" "$SECRETS" push > "$TMP/out" 2>&1 && pushed=yes || pushed=no
check "push survives the round trip through a GitHub that trims" test "$pushed" = yes
check "and GitHub holds the bare values" \
	bash -c 'grep -qx "KEEP_BACKUPS=14" "$0" && grep -qx "APP_PORT=8461" "$0" && grep -qx "APP_DIR=reconcile" "$0"' "$TMP/gh-vars"

echo
echo "a GitHub that lags behind a set"
# The bug behind "I have to run deploy-send-secrets twice": a changed value is
# read back before GitHub's list reflects it. KEEP_BACKUPS was 13 on GitHub and
# is 14 in the file; the list goes on saying 13 for two reads.
sed 's/^KEEP_BACKUPS=.*/KEEP_BACKUPS=13/' "$TMP/gh-vars" > "$TMP/gh-vars.before"
cp "$TMP/gh-vars.before" "$TMP/gh-vars"
echo 2 > "$TMP/gh-stale"
rm -f "$TMP/slept"

SECRETS_ENV="$TMP/messy.env" "$SECRETS" push > "$TMP/out" 2>&1 && pushed=yes || pushed=no
check "one push is enough when GitHub catches up within the wait" test "$pushed" = yes
check "it said it was waiting, rather than failing" said "has not caught up"
check "and it waited" test -s "$TMP/slept"

# A value that never arrives is still a failure, and names both sides.
cp "$TMP/gh-vars.before" "$TMP/gh-vars"
echo 99 > "$TMP/gh-stale"
SECRETS_ENV="$TMP/messy.env" "$SECRETS" push > "$TMP/out" 2>&1 && pushed=yes || pushed=no
check "a value GitHub never shows is still a failure" test "$pushed" = no
check "which names what was sent and what GitHub has" said "sent 14, GitHub has 13"
rm -f "$TMP/gh-stale"

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
