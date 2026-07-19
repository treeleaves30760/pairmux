#!/bin/sh

set -eu

MAX_SUBJECT_LENGTH=72

usage() {
  cat <<'EOF'
Usage:
  scripts/validate-commit-subjects.sh --self-test
  scripts/validate-commit-subjects.sh --subject SUBJECT
  scripts/validate-commit-subjects.sh --branch BRANCH
  scripts/validate-commit-subjects.sh --commit REVISION
  scripts/validate-commit-subjects.sh --range BASE_REVISION HEAD_REVISION

With no arguments, the validator checks HEAD. A valid subject is at most 72
characters and has the form feat|doc|fix|chore: kebab-case. Branch names use
the form feat|doc|fix|chore/kebab-case.
EOF
}

subject_error() {
  subject=$1

  case "$subject" in
    feat:\ *|doc:\ *|fix:\ *|chore:\ *)
      description=${subject#*: }
      ;;
    *)
      printf '%s' "expected feat|doc|fix|chore followed by ': '"
      return 0
      ;;
  esac

  case "$description" in
    ''|-*|*-|*--*|*[!a-z0-9-]*)
      printf '%s' 'description must be lowercase kebab-case'
      return 0
      ;;
  esac

  if [ "${#subject}" -gt "$MAX_SUBJECT_LENGTH" ]; then
    printf 'subject is longer than %s characters' "$MAX_SUBJECT_LENGTH"
    return 0
  fi

  return 1
}

branch_error() {
  branch=$1
  case "$branch" in
    feat/*|doc/*|fix/*|chore/*) description=${branch#*/} ;;
    *)
      printf '%s' "expected feat|doc|fix|chore followed by '/'"
      return 0
      ;;
  esac
  case "$description" in
    ''|-*|*-|*--*|*[!a-z0-9-]*)
      printf '%s' 'branch description must be lowercase kebab-case'
      return 0
      ;;
  esac
  return 1
}

validate_subject() {
  subject=$1
  label=$2

  if reason=$(subject_error "$subject"); then
    printf 'invalid commit subject (%s): %s\n  %s\n' "$label" "$reason" "$subject" >&2
    return 1
  fi
}

validate_branch() {
  branch=$1
  if reason=$(branch_error "$branch"); then
    printf 'invalid branch name: %s\n  %s\n' "$reason" "$branch" >&2
    return 1
  fi
}

run_self_test() {
  failures=0

  for subject in \
    'feat: add-socket-isolation' \
    'doc: explain-release-flow' \
    'chore: pin-ci-actions' \
    'fix: reject-unsafe-names' \
    'feat: x'
  do
    if subject_error "$subject" >/dev/null; then
      printf 'self-test: expected valid: %s\n' "$subject" >&2
      failures=$((failures + 1))
    fi
  done

  for subject in \
    'feature: add-socket-isolation' \
    'docs: explain-release-flow' \
    'chores: pin-ci-actions' \
    'feat:' \
    'feat:add-socket-isolation' \
    'feat: add socket isolation' \
    'feat: Add-socket-isolation' \
    'fix: trailing-hyphen-' \
    'fix: double--hyphen' \
    'feat/add-socket-isolation' \
    'fix' \
    ' fix: reject-unsafe-names'
  do
    if ! subject_error "$subject" >/dev/null; then
      printf 'self-test: expected invalid: %s\n' "$subject" >&2
      failures=$((failures + 1))
    fi
  done

  long_subject='feat: '
  index=0
  while [ "$index" -lt 67 ]; do
    long_subject="${long_subject}x"
    index=$((index + 1))
  done
  if ! subject_error "$long_subject" >/dev/null; then
    printf 'self-test: expected overlong subject to be invalid\n' >&2
    failures=$((failures + 1))
  fi

  for branch in \
    'feat/socket-isolation' \
    'doc/release-flow' \
    'chore/pin-actions' \
    'fix/unsafe-names'
  do
    if branch_error "$branch" >/dev/null; then
      printf 'self-test: expected valid branch: %s\n' "$branch" >&2
      failures=$((failures + 1))
    fi
  done
  for branch in 'feat:socket-isolation' 'feature/socket-isolation' 'feat/' 'feat/Bad-name' 'fix/two--parts'; do
    if ! branch_error "$branch" >/dev/null; then
      printf 'self-test: expected invalid branch: %s\n' "$branch" >&2
      failures=$((failures + 1))
    fi
  done

  if [ "$failures" -ne 0 ]; then
    printf 'commit subject validator self-test failed (%s case(s))\n' "$failures" >&2
    return 1
  fi

  printf 'commit subject validator self-test passed\n'
}

validate_commit() {
  revision=$1
  commit=$(git rev-parse --verify "${revision}^{commit}") || {
    printf 'unable to resolve commit: %s\n' "$revision" >&2
    return 1
  }
  subject=$(git show -s --format=%s "$commit")
  validate_subject "$subject" "$commit"
}

validate_range() {
  base=$(git rev-parse --verify "${1}^{commit}") || {
    printf 'unable to resolve base commit: %s\n' "$1" >&2
    return 1
  }
  head=$(git rev-parse --verify "${2}^{commit}") || {
    printf 'unable to resolve head commit: %s\n' "$2" >&2
    return 1
  }
  commits=$(git rev-list --reverse "${base}..${head}")

  if [ -z "$commits" ]; then
    printf 'no commit subjects to validate in %s..%s\n' "$base" "$head"
    return 0
  fi

  failures=0
  total=0
  for commit in $commits; do
    total=$((total + 1))
    subject=$(git show -s --format=%s "$commit")
    if ! validate_subject "$subject" "$commit"; then
      failures=$((failures + 1))
    fi
  done

  if [ "$failures" -ne 0 ]; then
    printf '%s invalid commit subject(s) found\n' "$failures" >&2
    return 1
  fi

  printf 'validated %s commit subject(s)\n' "$total"
}

if [ "$#" -eq 0 ]; then
  set -- --commit HEAD
fi

case "$1" in
  --self-test)
    [ "$#" -eq 1 ] || { usage >&2; exit 2; }
    run_self_test
    ;;
  --subject)
    [ "$#" -eq 2 ] || { usage >&2; exit 2; }
    validate_subject "$2" literal
    printf 'commit subject is valid\n'
    ;;
  --branch)
    [ "$#" -eq 2 ] || { usage >&2; exit 2; }
    validate_branch "$2"
    printf 'branch name is valid\n'
    ;;
  --commit)
    [ "$#" -eq 2 ] || { usage >&2; exit 2; }
    validate_commit "$2"
    printf 'commit subject is valid\n'
    ;;
  --range)
    [ "$#" -eq 3 ] || { usage >&2; exit 2; }
    validate_range "$2" "$3"
    ;;
  -h|--help)
    usage
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
