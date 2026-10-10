#!/usr/bin/env bash
#
# scripts/data-guard against a throwaway repository. Every number below is
# invented, and assembled at run time so that this file does not itself
# contain the shapes it tests for.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUARD="$REPO_DIR/scripts/data-guard"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else printf '  FAIL %s\n' "$what"; fail=$((fail + 1)); fi
}

cd "$TMP"
git init -q .
git config user.email test@example.invalid
git config user.name test

refuses() { ! "$GUARD" "$@" >/dev/null 2>&1; }
accepts() { "$GUARD" "$@" >/dev/null 2>&1; }
reset()   { git rm -rq --cached . >/dev/null 2>&1 || true; rm -rf ./*; }

echo "by name"

printf 'date,amount\n' > export.csv; git add export.csv
check "a CSV export is refused" refuses staged
reset

mkdir -p business/importing/testdata
printf 'date,amount\n' > business/importing/testdata/invented.csv
git add -f business/importing/testdata/invented.csv
check "an invented one in testdata/ is not" accepts staged
reset

printf 'x' > receipt.jpg; git add receipt.jpg
check "a receipt photo is refused" refuses staged
reset

mkdir -p app/sdk/page/assets/app/img
printf 'x' > app/sdk/page/assets/app/img/icon-192.png; git add app/sdk/page/assets/app/img/icon-192.png
check "the site's own icon is not" accepts staged
reset

mkdir -p app/sdk/page/assets/app/img/receipts
printf 'x' > app/sdk/page/assets/app/img/receipts/a.png; git add app/sdk/page/assets/app/img/receipts/a.png
check "a photo beside the icons is" refuses staged
reset

printf 'x' > secrets.env; git add -f secrets.env
check "secrets.env is refused, even forced past .gitignore" refuses staged
reset

echo
echo "by content"

card="4$(printf '%03d' 111) 1111 1111 1111"
printf 'const sample = "%s"\n' "$card" > a.go; git add a.go
check "a card-number shape is refused" refuses staged
reset

printf 'const n = 123_456_789_012\n' > a.go; git add a.go
check "a test constant with underscores is accepted" accepts staged
reset

digits="$(printf '%s%s' 98765 4321012)"
printf 'const acct = "%s"\n' "$digits" > a.go; git add a.go
check "an account-number shape warns but is not refused" accepts staged
"$GUARD" staged 2>"$TMP/err" >/dev/null || true
check "and the warning says why" grep -q 'account or routing number' "$TMP/err"
reset

printf 'API_KEY = "%s"\n' "$(printf 'abcd%.0s' 1 2 3 4 5)" > a.py; git add a.py
check "a hard-coded credential is refused" refuses staged
reset

printf '%s\n' "-----BEGIN OPENSSH PRIVATE""-----" | sed 's/PRIVATE-/PRIVATE KEY-/' > k.txt; git add k.txt
check "a private key is refused" refuses staged
reset

check "nothing staged is nothing to refuse" accepts staged

echo
echo "the whole tree, for CI"

printf 'x' > statement.pdf; git add statement.pdf; git commit -qm one --no-verify
check "a committed statement is caught however it got there" refuses tracked
git rm -q statement.pdf; git commit -qm two
check "and a clean tree passes" accepts tracked

echo
check "this repository itself is clean" bash -c 'cd "$0" && "$1" tracked' "$REPO_DIR" "$GUARD"

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
