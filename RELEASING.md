# Releasing pairmux

This repository owns the pairmux CLI, native binaries, Python wheel wrappers,
Linux packages, install script, and documentation site. The companion
`pairmux-skills` repository owns the canonical Agent Skill.

## Release channels

| Channel | Implementation | Release operation |
| --- | --- | --- |
| GitHub Releases | GoReleaser builds four macOS/Linux archives, four `.deb`/`.rpm` packages, and checksums. | Push a canonical SemVer tag. The workflow verifies and stages the exact artifacts before publishing the release. |
| PyPI | Four platform wheels wrap the same verified Go binaries. Linux wheel installation is smoke-tested. | Configure the `PYPI_TOKEN` repository secret or migrate to Trusted Publishing. The tag workflow uploads only the four verified wheels. |
| Direct installer | `install.sh` selects a GitHub archive and installs atomically. | Smoke-test the public URL on macOS and Linux after every release. |
| Debian/RPM files | GoReleaser emits installable `.deb` and `.rpm` release assets. | No extra work for direct package downloads. |
| APT repository | **Live.** The [`pairmux-apt`](https://github.com/treeleaves30760/pairmux-apt) repository signs and publishes metadata rebuilt from every stable release to GitHub Pages. | Operate per `pairmux-apt/OPERATIONS.md` (key custody, rebuild workflow). |
| Homebrew tap | **Active.** GoReleaser renders the cask (binary + manpage, `depends_on formula: tmux`, quarantine-stripping postflight) during the build; the release workflow pushes `Casks/pairmux.rb` to [`homebrew-pairmux`](https://github.com/treeleaves30760/homebrew-pairmux) after the release goes public. Prereleases are skipped. | Smoke-test `brew install --cask treeleaves30760/pairmux/pairmux` + `pairmux doctor` on a clean machine after each stable release. |

## Release checklist

1. Sync `../pairmux-skills/skills/pairmux/` into `skills/pairmux/` and confirm
   that `diff -ru` reports no differences.
2. Update `ChangeLog.md`: move `[Unreleased]` entries into a dated version and
   add the release comparison link.
3. Run the local validation suite:

   ```sh
   gofmt -w .
   go vet ./...
   go test -race -count=1 ./...
   go test -race -tags integration -count=1 ./test/...
   python3 -m unittest discover -s packaging/pypi -p 'test_*.py'
   shellcheck install.sh scripts/*.sh
   ./scripts/test-install.sh
   ./scripts/validate-commit-subjects.sh --self-test
   goreleaser check
   goreleaser release --snapshot --clean --skip=publish
   ```

4. Verify the GitHub repository settings and PyPI credential preflight.
5. Create and push an annotated SemVer tag from `main`, for example
   `git tag -a v0.1.0 -m 'chore: release-v0-1-0'`.
6. Watch the tag workflow. Confirm the draft release contains nine native
   assets and PyPI contains four wheels before making the release visible.
7. Smoke-test `uv tool install pairmux`, `install.sh`, and one `.deb` on clean
   systems. Never publish artifacts from an old local `dist/` directory.

## Homebrew (activated 2026-08-01)

Homebrew is the first-class path on macOS — the platform where the
interactive-terminal use case is most common — and the only channel that
installs the hard tmux >= 3.2 runtime dependency in the same command. How it
is wired:

- The tap repo is [`treeleaves30760/homebrew-pairmux`](https://github.com/treeleaves30760/homebrew-pairmux);
  a repo-scoped PAT lives in the `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret
  (the workflow's default `GITHUB_TOKEN` cannot push cross-repo).
- This pipeline uses GoReleaser as a **builder only** (`--skip=publish`), so
  the cask config's `skip_upload: auto` never publishes from GoReleaser
  itself. Instead the validate job preserves the rendered
  `dist/homebrew/Casks/pairmux.rb` as an artifact, and the publish job pushes
  it to the tap via the GitHub contents API **after** the release goes
  public — never for prerelease tags — then reads it back and byte-compares.
- After each stable release, smoke-test on a clean machine:
  `brew install --cask treeleaves30760/pairmux/pairmux`, then
  `pairmux version` and `pairmux doctor` (confirms tmux arrived and that no
  Gatekeeper prompt appears; the cask's postflight strips the quarantine
  attribute from the fetched binary).

Signing and notarization of the macOS binaries remain recommended for a wider
stable release, together with an SBOM and build provenance, but they are
deliberately decoupled from shipping the tap: the cask installs from the same
checksummed tarball `install.sh` already uses.

## APT repository

The signed APT repository lives in
[`pairmux-apt`](https://github.com/treeleaves30760/pairmux-apt): key custody,
metadata generation from every non-draft stable release, atomic Pages
publishing, and container-tested key enrollment are documented in that repo's
`OPERATIONS.md`. Nothing APT-specific remains in this repository's release
flow beyond producing the `.deb` assets.
