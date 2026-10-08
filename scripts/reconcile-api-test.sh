#!/usr/bin/env bash
#
# Exercises scripts/reconcile-api with curl replaced on PATH by a script that
# says what it was asked, so nothing reaches a network. What is checked is
# what the script refuses before curl, and that the key never reaches curl's
# command line.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API="$REPO_DIR/scripts/reconcile-api"

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

# The fake curl prints its arguments, and the header file it was handed.
mkdir "$TMP/bin"
cat > "$TMP/bin/curl" <<'FAKE'
#!/usr/bin/env bash
printf 'ARGS %s\n' "$*"
prev=""
for a in "$@"; do
	if [ "$prev" = --header ] && [ "${a#@}" != "$a" ]; then
		printf 'HEADER %s\n' "$(cat "${a#@}")"
	fi
	prev="$a"
done
FAKE
chmod +x "$TMP/bin/curl"
export PATH="$TMP/bin:$PATH"

unset RECONCILE_URL RECONCILE_API_KEY

refuses() { ! "$API" "$@" >/dev/null 2>&1; }
answers() { "$API" "$@" >/dev/null 2>&1; }

check "no URL is refused" refuses GET /api/v1

export RECONCILE_URL=https://reconcile.example.invalid/

check "the index needs no key" answers GET /api/v1
check "anything else does" refuses GET '/api/v1/translations/pending?lang=es'

export RECONCILE_API_KEY=rcn_testid.testsecret

check "a path outside the API" refuses GET /account
check "an endpoint that is not translations" refuses GET /api/v1/accounts
check "a method the API does not take" refuses DELETE /api/v1/translations
check "a site in the clear" env RECONCILE_URL=http://reconcile.example.invalid bash -c '! "$0" GET /api/v1 >/dev/null 2>&1' "$API"

out="$("$API" GET '/api/v1/translations/pending?lang=es')"

check "the address is the site's, without a doubled slash" grep -q 'https://reconcile.example.invalid/api/v1/translations/pending?lang=es' <<<"$out"
check "the key is sent as a header" grep -q '^HEADER Authorization: Bearer rcn_testid.testsecret$' <<<"$out"
check "the key is not on curl's command line" bash -c '! grep "^ARGS" | grep -q testsecret' <<<"$out"

printf '%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
