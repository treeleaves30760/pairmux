# Contributing

## Commit subjects

Every new commit subject must use this exact form:

```text
<type>/<description>
```

The allowed types are `feat`, `doc`, `chores`, and `fix`. The description must
be nonempty, must not start or end with whitespace, and the complete subject
must be at most 72 characters. Existing history is not checked retroactively.

Valid examples:

```text
feat/add socket isolation
doc/explain the release flow
chores/pin CI actions
fix/reject unsafe terminal names
```

Invalid examples include `feature/add isolation`, `docs/update guide`, `feat/`,
and `fix:wrong separator`.

Run the dependency-free validator locally before pushing:

```sh
./scripts/validate-commit-subjects.sh --self-test
./scripts/validate-commit-subjects.sh --subject 'fix/reject unsafe names'
./scripts/validate-commit-subjects.sh --commit HEAD
./scripts/validate-commit-subjects.sh --range main HEAD
```

The default invocation validates `HEAD` and does not require a remote:

```sh
./scripts/validate-commit-subjects.sh
```

Amend an invalid local subject with `git commit --amend` before pushing. CI
validates only commits introduced by the current pull request or push.
