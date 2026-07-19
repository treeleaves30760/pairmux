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
| Debian/RPM files | GoReleaser emits installable `.deb` and `.rpm` release assets. | No extra work for direct package downloads. These files do not constitute an APT or Yum repository. |
| APT repository | Not implemented. | Choose hosting and supported distributions, sign repository metadata, publish indexes, document key enrollment, and test `apt update` plus `apt install pairmux`. |
| Homebrew | Deferred. | Sign and notarize macOS binaries, verify them through Gatekeeper, then add a tap/formula workflow. |

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

## APT roadmap

APT requires a signed repository in addition to the `.deb` artifact. Complete
these as a separate milestone:

- Select managed hosting or a static repository generator such as `aptly` or
  `reprepro`, and define supported suites and architectures.
- Keep the repository signing key outside the source tree; document public-key
  distribution and rotation.
- Generate `Packages`, `Release`, and `InRelease` metadata from the verified
  `.deb` files produced by the tag workflow.
- Publish metadata and packages atomically, retaining old versions for rollback.
- Add container tests that enroll the key, run `apt update`, install pairmux,
  and verify `pairmux version` matches the tag.

Artifact signing, an SBOM, and build provenance are recommended before a wider
stable release even though they do not block the first public release.
