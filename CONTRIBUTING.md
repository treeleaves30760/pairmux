# Contributing

## Commit subjects

Every new commit subject must use this exact form:

```text
<type>: <kebab-case-description>
```

The allowed types are `feat`, `doc`, `fix`, and `chore`. Use exactly one space
after the colon. Keep the description lowercase and hyphen-separated, without
spaces or consecutive hyphens. The complete subject must be at most 72
characters.

Valid examples:

```text
feat: add-socket-isolation
doc: explain-release-flow
chore: pin-ci-actions
fix: reject-unsafe-terminal-names
```

Examples such as `feat/add-socket-isolation`, `feat:add-socket-isolation`,
`chores: pin-ci-actions`, `docs: update-guide`, and
`fix: Trailing-Hyphen-` are invalid.

## Branch names

Work must happen on a typed branch with this form:

```text
<type>/<kebab-case-major>
```

Use the same four types. The branch suffix should name the major workstream,
not repeat the full commit subject. Examples:

```text
feat/terminal-lifecycle
doc/release-guide
chore/ci-pins
fix/socket-collision
```

Create the typed branch from an up-to-date `main`, commit there, and merge it
back locally only after validation. A merge commit must follow the same subject
format:

```sh
git switch main
git pull --ff-only
git switch -c feat/terminal-lifecycle
# make and validate changes
git commit -m 'feat: harden-terminal-lifecycle'
git switch main
git merge --no-ff feat/terminal-lifecycle \
  -m 'chore: merge-terminal-lifecycle'
```

## Validation

Run the dependency-free validator before pushing:

```sh
./scripts/validate-commit-subjects.sh --self-test
./scripts/validate-commit-subjects.sh --subject 'fix: reject-unsafe-names'
./scripts/validate-commit-subjects.sh --branch 'fix/socket-collision'
./scripts/validate-commit-subjects.sh --commit HEAD
./scripts/validate-commit-subjects.sh --range main HEAD
```

The default invocation validates `HEAD`. CI validates the commits introduced
by a push or pull request and validates the pull request's source branch.
