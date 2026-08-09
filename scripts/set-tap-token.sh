#!/bin/sh

# Store (or rotate) HOMEBREW_TAP_GITHUB_TOKEN, the credential the release
# workflow's final step uses to push Casks/pairmux.rb to the Homebrew tap.
#
# This is a script rather than a documented sequence of commands because the
# procedure has two traps, both of which fail *silently*, and pasting commands
# one at a time walks into them:
#
#   1. A token carried between commands in an environment variable is lost
#      whenever each command runs in its own shell — an agent's shell-out, a
#      new terminal tab, a `!` prefix. The variable reads as empty, not unset,
#      so nothing errors.
#   2. gh treats an empty GH_TOKEN as "none supplied" and falls back to the
#      ambient login. A verification step therefore keeps passing while testing
#      an entirely different credential, and `gh secret set` cheerfully stores
#      an empty secret. Nothing notices until a release reaches the tap push,
#      which happens after PyPI has published and the release is public —
#      neither of which can be redone for a tag.
#
# So the whole procedure runs in one process with no handoff: the token is
# proved against the tap before it is stored, and proved again from Actions
# afterwards, because only Actions can read what was actually stored.
#
# Usage:
#   scripts/set-tap-token.sh [--no-verify]
#
#   --no-verify   store without dispatching the Tap credential workflow. The
#                 local read/write proof still runs; only the check of the
#                 stored value is skipped.
#
# Overridable for a fork or a renamed tap:
#   PAIRMUX_REPO       repository holding the secret (default treeleaves30760/pairmux)
#   PAIRMUX_TAP_REPO   tap repository            (default treeleaves30760/homebrew-pairmux)

set -eu

REPO=${PAIRMUX_REPO:-treeleaves30760/pairmux}
TAP=${PAIRMUX_TAP_REPO:-treeleaves30760/homebrew-pairmux}
SECRET=HOMEBREW_TAP_GITHUB_TOKEN
CASK=Casks/pairmux.rb
PROBE=.release-token-check
WORKFLOW=tap-credential.yml

VERIFY=1
for arg in "$@"; do
  case "$arg" in
    --no-verify) VERIFY=0 ;;
    -h|--help) sed -n '3,33p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $arg (try --help)" >&2; exit 2 ;;
  esac
done

die() { echo "set-tap-token: $*" >&2; exit 1; }

command -v gh >/dev/null 2>&1 || die "gh is not installed"

# Two different identities are in play. The ambient login writes the secret and
# needs admin on the repository; the token being stored only ever touches the
# tap. Confirming the first now keeps a rejected `gh secret set` from arriving
# after the token has already been typed.
gh auth status >/dev/null 2>&1 || die "gh is not logged in (run: gh auth login)"
gh api "repos/$REPO" --jq '.permissions.admin' 2>/dev/null | grep -qx true ||
  die "the logged-in account cannot write secrets on $REPO"

PROBE_SHA=

restore_tty() { stty echo 2>/dev/null || true; }

cleanup() {
  restore_tty
  # A probe left behind outlives this run and every later one, so remove it
  # even when the write proof is what failed.
  if [ -n "$PROBE_SHA" ]; then
    GH_TOKEN="$TOKEN" gh api --method DELETE "repos/$TAP/contents/$PROBE" \
      -f message='chore: drop-release-token-check' -f sha="$PROBE_SHA" >/dev/null 2>&1 || true
    PROBE_SHA=
  fi
}
trap cleanup EXIT INT TERM

cat <<EOF
Storing $SECRET on $REPO, for pushing to $TAP.

The token must be a fine-grained PAT whose resource owner is the tap's owner,
whose repository access selects only $TAP, and whose repository
permissions grant Contents: Read and write. Nothing else is used.
EOF

# Read from the terminal, not stdin: the prompt must work when this script is
# itself piped or run by a tool.
[ -r /dev/tty ] || die "no terminal to read the token from"
printf '\nPaste the token (input is hidden), then press Enter: ' >&2
stty -echo 2>/dev/null || true
IFS= read -r TOKEN < /dev/tty || die "no token read"
restore_tty
printf '\n' >&2

[ -n "$TOKEN" ] || die "empty token — nothing stored (this is trap 1 above)"
case $TOKEN in
  *[!!-~]*) die "token contains whitespace or control characters; re-copy it" ;;
esac

echo "checking the token can read $CASK ..."
GH_TOKEN="$TOKEN" gh api "repos/$TAP/contents/$CASK" --jq .sha >/dev/null 2>&1 || die \
  "the token cannot read $CASK in $TAP.
  A 403 means it is valid but lacks access: check that the PAT selects that
  repository and grants Contents. A 401 means the value itself is wrong."

# Read access is not enough: a Contents: Read token passes the check above and
# still cannot push the cask, which is the only thing this credential is for.
echo "checking the token can write ..."
PROBE_SHA=$(
  GH_TOKEN="$TOKEN" gh api --method PUT "repos/$TAP/contents/$PROBE" \
    -f message='chore: verify-release-token' \
    -f content="$(printf ok | base64)" --jq .content.sha
) || die "the token can read $TAP but not write to it: grant Contents: Read and write"
cleanup # removes the probe; leaves the tap exactly as it was
echo "read and write proved against $TAP"

# --body would put the token in argv, where ps and shell history can see it.
printf %s "$TOKEN" | gh secret set "$SECRET" --repo "$REPO"
echo "stored $SECRET on $REPO"

if [ "$VERIFY" -eq 0 ]; then
  echo "skipping the Actions-side check (--no-verify); run it with:"
  echo "  gh workflow run $WORKFLOW --repo $REPO"
  exit 0
fi

# The local proof says the token works. It says nothing about what landed in
# the secret, which is the half that failed last time and the half only Actions
# can see.
echo "dispatching $WORKFLOW to check the stored value ..."
before=$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --limit 1 --json databaseId --jq '.[0].databaseId // ""')
gh workflow run "$WORKFLOW" --repo "$REPO"

run=$before
i=0
while [ "$run" = "$before" ]; do
  i=$((i + 1))
  [ "$i" -le 30 ] || die "the dispatched run never appeared; check the Actions tab"
  sleep 2
  run=$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --limit 1 --json databaseId --jq '.[0].databaseId // ""')
done

gh run watch "$run" --repo "$REPO" --exit-status ||
  die "the stored secret failed its check — see the run above, then re-run this script"
echo "the stored $SECRET reads and writes the tap. Release is unblocked."
