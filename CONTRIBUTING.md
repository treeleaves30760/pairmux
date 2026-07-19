# Contributing

## Commit subjects

Every new commit subject must use this exact form:

```text
<type>:<kebab-case-description>
```

The allowed types are `feat`, `doc`, `chores`, and `fix`. Keep the description
lowercase and hyphen-separated, without spaces or consecutive hyphens. The
complete subject must be at most 72 characters.

Valid examples:

```text
feat:add-socket-isolation
doc:explain-release-flow
chores:pin-ci-actions
fix:reject-unsafe-terminal-names
```

Examples such as `feat/add-socket-isolation`, `feat: add-socket-isolation`,
`docs:update-guide`, and `fix:Trailing-Hyphen-` are invalid.

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
chores/ci-pins
fix/socket-collision
```

## Validation

Run the dependency-free validator before pushing:

```sh
./scripts/validate-commit-subjects.sh --self-test
./scripts/validate-commit-subjects.sh --subject 'fix:reject-unsafe-names'
./scripts/validate-commit-subjects.sh --branch 'fix/socket-collision'
./scripts/validate-commit-subjects.sh --commit HEAD
./scripts/validate-commit-subjects.sh --range main HEAD
```

The default invocation validates `HEAD`. CI validates the commits introduced
by a push or pull request and validates the pull request's source branch.
