#!/usr/bin/env bash
#
# The front end is one small file and it is the only thing between a healthy
# binary and a person, so the handful of things it must do are pinned here.
#
# Taken from stewards, which took it from mass-intentions, where it was written after 2026-09-22: the front
# page was a 404 for everybody while /healthz answered 200 and the deploy
# reported success. Apache mapped "/" onto the docroot, mod_dir substituted a
# DirectoryIndex, and the Go server was asked for a file it has no route for --
# so every path worked except the first one anybody types.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="$REPO_DIR/deploy/htaccess.template"

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else printf '  FAIL %s\n' "$what"; fail=$((fail + 1)); fi
}

# What deploy.sh writes, so the assertions below are about the real file.
RENDERED="$(sed 's/__APP_PORT__/8461/' "$TEMPLATE")"

has() { printf '%s\n' "$RENDERED" | grep -Eq "$1"; }

# The line number a rule lands on, for the ordering assertions; empty when
# there is no such rule, which the check using it then reports, rather than
# set -e ending the script before it says which.
where() { printf '%s\n' "$RENDERED" | grep -nE "$1" | head -1 | cut -d: -f1 || true; }

echo "the front end"

check "nothing is left to substitute" bash -c "! printf '%s' \"\$0\" | grep -q '__'" "$RENDERED"

check "mod_dir does not answer for the front page" has '^DirectoryIndex disabled$'
check "and the index it would have asked for is proxied to the front page" \
	has '^RewriteRule \^index\\\.html\$ http://127\.0\.0\.1:8461/ \[P'

check "everything else is proxied to the loopback" \
	has '^RewriteRule \^\(\.\*\)\$ http://127\.0\.0\.1:8461/\$1 \[P'

# The port rendered above is the one the binary listens on by default. If the
# two drift, Apache proxies to a port nothing answers and every page is a 502.
check "the port is the binary's default" \
	grep -q '"127.0.0.1:8461"' "$REPO_DIR/cmd/reconcile/config.go"

echo
echo "order, which is the whole of what a rewrite file is"

acme="$(where '^RewriteRule \^\\\.well-known/')"
oauth="$(where '^RewriteCond %\{REQUEST_URI\} !\^/\\\.well-known/oauth-')"
https="$(where 'https://%\{HTTP_HOST\}')"
index="$(where '\^index')"
catchall="$(where '\^\(\.\*\)\$ http://127')"

before() { [ -n "$1" ] && [ -n "$2" ] && [ "$1" -lt "$2" ]; }

# ACME first. A certificate that cannot renew takes the site down months later,
# for a reason nobody connects to this file.
check "the ACME path is exempted before anything else rewrites" before "$acme" "$catchall"

# The one exception to it: the OAuth discovery documents go to the app, or
# Claude cannot find where to sign in. A RewriteCond binds to the rule right
# after it, so it must be the line before the ACME rule and nowhere else.
check "the OAuth discovery documents are the one exception, on the ACME rule itself" \
	bash -c '[ -n "$0" ] && [ "$0" -eq $(( $1 - 1 )) ]' "$oauth" "$acme"
check "and the exception names them exactly" \
	has '^RewriteCond %\{REQUEST_URI\} !\^/\\\.well-known/oauth-\(authorization-server\|protected-resource\)\(/\|\$\)$'

check "the index rule comes before the catch-all that would swallow it" before "$index" "$catchall"

check "http is redirected to https before anything is proxied" before "$https" "$catchall"

echo
echo "and the deploy writes it"

# The half of the 2026-09-22 lesson this file cannot enforce on its own: that
# every deploy reinstalls it, rather than only a one-time install, so the file
# on the server cannot drift from the one in git.
check "every push to main deploys" \
	grep -q 'run: deploy/deploy.sh deploy' "$REPO_DIR/.github/workflows/deploy.yml"

# A line that is a call and nothing else. Matching the bare word would find the
# comment in cmd_deploy explaining why the call is there, and pass on the
# comment after the call was deleted -- which is how the same assertion in
# mass-intentions was caught passing on nothing.
check "and every deploy reinstalls the front end" \
	bash -c "sed -n '/^cmd_deploy() {/,/^}/p' '$REPO_DIR/deploy/deploy.sh' | grep -qE '^[[:space:]]*install_htaccess[[:space:]]*$'"

check "the deploy workflow runs only on main and by hand, never for a pull request" \
	bash -c '! grep -qE "pull_request" "$0"' "$REPO_DIR/.github/workflows/deploy.yml"

check "installing it by hand only passes once the public address behaves" \
	bash -c "sed -n '/^cmd_htaccess() {/,/^}/p' '$REPO_DIR/deploy/deploy.sh' | grep -qE '^[[:space:]]*verify_front_end[[:space:]]*$'"

check "a deploy asks for the front page, not only /healthz" \
	bash -c "sed -n '/^health_public() {/,/^}/p' '$REPO_DIR/deploy/deploy.sh' | grep -q 'https://\$APP_HOST/\"'"

# What deploy.sh installs is this template with the port in it, byte for byte.
# Rendered from a throwaway credentials file, so nothing real is read.
fixture="$(mktemp)"
trap 'rm -f "$fixture"' EXIT
printf 'DEPLOY_SSH_HOST=example.invalid\nDEPLOY_SSH_USER=u\nAPP_HOST=h.invalid\nAPP_PORT=8461 \nAPP_DOCROOT=d\n' > "$fixture"

check "deploy.sh installs exactly the rendered template" \
	bash -c 'diff <(SECRETS_ENV="$0" "$1/deploy/deploy.sh" render-htaccess) <(printf "%s\n" "$2")' \
	"$fixture" "$REPO_DIR" "$RENDERED"

printf 'DEPLOY_SSH_HOST=example.invalid\nDEPLOY_SSH_USER=u\nAPP_HOST=h.invalid\nAPP_PORT=84;51\nAPP_DOCROOT=d\n' > "$fixture"
check "a port that is not a number is refused rather than written into a rewrite rule" \
	bash -c '! SECRETS_ENV="$0" "$1/deploy/deploy.sh" render-htaccess >/dev/null 2>&1' "$fixture" "$REPO_DIR"

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
