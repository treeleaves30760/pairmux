# Releasing pairmux

This repository owns the pairmux CLI, native binaries, Python wheel wrappers,
RPM packages, installers, landing page, and documentation site. The companion
`pairmux-skills` repository owns the canonical Agent Skill.

**v0.5.3 migration.** This release retires pairmux APT / `.deb` distribution
and makes the website installer use PyPI. Existing tags, published PyPI versions,
and historical checksums are not rewritten. The seven-asset rule below applies
to new builds, not historical release manifests.

**v0.5.3 published on 2026-10-08** from commit `0d14566`.
[Release run 37760804396](https://github.com/treeleaves30760/pairmux/actions/runs/37760804396)
completed validation, GitHub publication, the four-wheel PyPI upload and Homebrew
update. Public downloads match the preserved validated bytes. Isolated public
PyPI `uvx`, persistent uv installation and the website installer passed version
and `doctor` checks; the unpinned installer resolved 0.5.3. The official Homebrew
tap fetch/checksum and macOS ARM64 binary also passed. Clean-machine Homebrew
installation, actual WSL installation and Linux RPM installation were not
performed locally; CI verified the Linux wheel runtime and RPM file listing.

**2026-10-08 deployment status:** the Cloudflare landing and both installers are
live and verified. The public APT repository/Pages and 16 historical `.deb`
assets have been removed; all 56 surviving assets and historical tags are
unchanged. Documentation DNS and deployment succeeded at that checkpoint,
but its GitHub Pages HTTPS certificate was then pending. The operator explicitly
approved proceeding with v0.5.3 without waiting for it. This historical status
is superseded by the 2026-10-09 HTTPS confirmation below; the
[APT migration guide source](./website/docs/migrating-from-apt.md) remains available.

**v0.6.0 preparation.** Persistent `uv tool install pairmux` is the primary install
path. The new `pairmux update` command is limited to the verified running uv tool
installation and reinstalls the latest compatible stable wheel from public PyPI,
resetting old pins and source policy. Keep public guidance gated to v0.6.0+ until
publication; v0.5.3 and earlier need `uv tool install --upgrade pairmux` first.
Do not move v0.5.3, revisit APT deletion, or unblock this release by silently
marking deferred documentation HTTPS complete.

**Local preparation checks (2026-10-08):** Go vet/race and tmux integration,
25 packaging tests, offline installer mocks/ShellCheck, 24 landing tests and
browser checks, docs audit/typecheck/build, manpage lint, and GoReleaser config
validation passed. The fresh unpublished snapshot contains seven native assets,
four wheels, and zero Debian packages. Its version is 0.6.0 but metadata still
names the existing v0.5.3 tag and commit `6327d42`; it is build-test evidence,
**not publication input**. The local `actionlint@v1.7.7` execution was blocked by
permissions at that preparation stage. On 2026-10-09 the operator explicitly
authorized the fixed `github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` source for
local and CI execution, along with public PyPI/GitHub/Homebrew publication and
official Canonical Ubuntu/Fedora platform acceptance. The local fixed-source
workflow lint passed after that authorization; this is not a permission bypass.
No v0.6.0 tag or publication had occurred at that stage.

**Updater completion checks (2026-10-09):** the real offline `uvintegration`
suite and shared fail-closed gate passed all six cases on macOS ARM64 with uv
0.11.7, Python 3.14.3 and Go 1.25.2: pinned upgrade, same-version native binary
replacement, regular-file and foreign-symlink refusals, hostile configuration
isolation, and rejection of a wheelhouse containing only a lower stable version
and a higher prerelease. The gate also failed as intended when the suite was
absent or required tools were unspecified. Ordinary/tagged Go vet and race tests,
tmux integration, 25 packaging tests, installer mocks/ShellCheck, 24 landing tests,
release configuration validation and docs typecheck/build passed; skill copies
remain byte-identical. The user's installed v0.5.2 binary and receipt hashes are
unchanged. [CI run 37827182087](https://github.com/treeleaves30760/pairmux/actions/runs/37827182087)
verified all six real cases with uv 0.11.16 on Ubuntu x86-64/Python 3.12.3 and
macOS ARM64/Python 3.14.7 at commit `18be0f9`; the full PR checks, including
fixed-version actionlint, were green. The earlier local completion checks made
no commit, push, tag or publication. Documentation
HTTPS remains deferred; the newer fixed-source actionlint authorization above
superseded its historical execution restriction for those checks. A later
permission check again blocked a local actionlint run, even after the original
source-specific consent was located. At that checkpoint the new block remained
unresolved; no agent or CI/publication was used to bypass it. Previously green
checks remain historical evidence, not validation of later workflow edits.

**Renewed authorization (2026-10-09):** the operator explicitly reauthorized
fixed-source `github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` execution locally
and in CI, and instructed completion of the v0.6.0 release. The local exact-source
version and workflow lint checks passed after this new authorization. Final-head
CI and publication must still succeed before they are recorded as complete.
The operator also completed documentation HTTPS; a public read of the Getting
Started page succeeded. Documentation HTTPS is no longer a deferred prerequisite.
Companion skills PR #2 was merged after its final-head CI and independent reviews
passed, with explicit authorization to proceed without another human reviewer.

## Release channels

| Channel | Implementation | Release operation |
| --- | --- | --- |
| GitHub Releases | GoReleaser builds four macOS/Linux `.tar.gz` archives, two Linux `.rpm` packages, and `checksums.txt`: **seven native assets**. | Push a canonical SemVer tag. The existing workflow verifies and stages the exact artifacts before publishing the release. No `.deb` is produced. |
| PyPI | Four platform wheels wrap the same verified Go binaries; wheel installation requires Python >= 3.9. | Configure the `PYPI_TOKEN` repository secret or migrate to Trusted Publishing. The tag workflow uploads only the four verified wheels. Smoke-test persistent `uv tool install` and `pairmux update` first, plus optional `uvx` quick runs. |
| Website installer | Root `install.sh` installs the pairmux wheel from `https://pypi.org/simple` through uv, bootstrapping official Astral uv and a compatible managed Python if needed. `install.ps1` delegates into WSL. | Rebuild the landing site whenever either root installer changes; test the public `/install.sh` and `/install.ps1` endpoints. The Bash installer no longer selects GitHub archives or consumes `checksums.txt`. |
| RPM files | GoReleaser emits installable `.rpm` release assets for Linux x86-64 and ARM64, with a tmux >= 3.2 dependency. | No extra work for direct package downloads. Verify checksums and smoke-test a local RPM; there is no Yum repository. |
| Homebrew tap | **Active.** GoReleaser renders the cask (binary + manpage, `depends_on formula: tmux`, quarantine-stripping postflight) during the build; the release workflow pushes `Casks/pairmux.rb` to [`homebrew-pairmux`](https://github.com/treeleaves30760/homebrew-pairmux) after the release goes public. Prereleases are skipped. | Smoke-test `brew install --cask treeleaves30760/pairmux/pairmux` + `pairmux doctor` on a clean machine after each stable release. |

The public entry points are [Home](https://pairmux.treeleaves30760.com),
[PyPI](https://pypi.org/project/pairmux/), and
[Docs](https://pairmux-docs.treeleaves30760.com/). GitHub remains the repository,
issue tracker, release archive, and source of the changelog.

## Release checklist

1. Sync `../pairmux-skills/skills/pairmux/` into `skills/pairmux/` and confirm
   that `diff -ru` reports no differences.
2. Update `ChangeLog.md`: move `[Unreleased]` entries into the next dated
   version and add its release comparison link. Do not call a version published
   until its tag workflow and public release verification complete.
3. Run the local validation suite from the repository root:

   ```sh
   gofmt -w .
   go vet ./...
   go test -race -count=1 ./...
   PAIRMUX_TEST_UV="$(command -v uv)" \
   PAIRMUX_TEST_PYTHON="$(command -v python3)" \
     ./scripts/test-update-uv.sh
   go test -race -tags integration -count=1 ./test/...
   python3 -m unittest discover -s packaging/pypi -p 'test_*.py'
   shellcheck install.sh scripts/*.sh
   ./scripts/test-install.sh
   ./scripts/validate-commit-subjects.sh --self-test
   goreleaser check
   goreleaser release --snapshot --clean --skip=publish
   npm --prefix website ci
   npm --prefix website run audit
   npm --prefix website run typecheck
   npm --prefix website run build
   node landing/build.mjs
   ```

   The opt-in `uvintegration` tests require the supplied installed uv/Python and
   use local test wheels in isolated HOME/config/cache/tool/bin directories,
   without tmux or package/Python downloads. `test-update-uv.sh` first verifies
   the exact tagged suite exists, so a missing test or a mock-only name match
   cannot pass the gate. A test-only runner checks the production updater argv
   before redirecting installation to that local wheelhouse; it is not a
   production source override. CI is configured to run this with uv 0.11.16 on
   macOS/Linux and again in release validation. The older fixture contains the
   new updater with an older version stamp: real v0.5.3 has no update command, so
   public bootstrap is a separate post-publication check.

   Use only a fresh build output. Verify **four archives + two RPMs + one
   checksum file = seven native assets**, four wheels, and zero `.deb` files.
   The release workflow retains its build-once / verify / publish pipeline;
   it validates wheel installation against the staged artifacts before
   publishing, not against a version that is not yet on PyPI.
4. Verify GitHub release permissions, PyPI secret presence, and the
   stable-release Homebrew tap credential (see below). `uv publish --dry-run`
   validates local distributions, not the PyPI token's upload authorization;
   treat prior successful uploads as historical evidence only. Confirm actual
   publishing authorization from the upload result, and preserve the original
   artifacts for recovery if any wheel is accepted. For the v0.5.3 migration,
   the landing and migration guide source are available and the former APT
   publisher is retired. Documentation HTTPS was deferred during that release
   and confirmed complete on 2026-10-09; do not infer new CLI publication from
   website availability.
5. Create and push the next annotated SemVer tag from validated `main`.
   Confirm that tag and PyPI version do not already exist. `v0.5.3` is already
   published and must not be recreated or moved.
6. Watch the tag workflow. It stages **seven** verified native assets in a
   draft GitHub release, publishes the four verified wheels to PyPI, makes
   the GitHub release public, then updates Homebrew for stable tags. Confirm
   each stage; do not replace this ordering with a separate rebuild.
7. On clean or isolated systems, smoke-test persistent `uv tool install pairmux`
   and, from v0.6.0, `pairmux update` followed by `version`/`doctor`. Also test the
   website installer, optional uvx quick runs, a direct RPM, and Homebrew.
   Start an older-release bootstrap check with `uv tool install 'pairmux==0.5.3'`,
   then `uv tool install --upgrade pairmux`, and only then `pairmux update`.
   Substitute a new release pin only **after** its upload succeeds. A same-version
   update reports `refreshed`, not a write-free no-op; a failed uv operation may
   have changed the environment, so do not promise automatic rollback.
   Verify wheel metadata has the new Home/Docs domains and the GitHub
   Repository URL. Use isolated HOME/tool/cache directories so an existing
   installation or cache does not stand in for the release under test.
   Never publish artifacts from an old local `dist/` directory.

### Published-release platform acceptance

After the stable release and tap publication succeed, dispatch the
`Published release acceptance` workflow with its exact canonical `vX.Y.Z` tag.
It consumes public artifacts only; it never rebuilds or publishes. It records
release/PyPI sources and checksums, and requires the installed binary to match
the selected native bytes, an exact version, healthy JSON `doctor`, and a live
isolated terminal command with `done`, exit 0 and one unique marker line.

- Public uv on Ubuntu/macOS: fresh isolated persistent install, public v0.5.3
  bootstrap followed by uv upgrade and same-version `refreshed`, and the public
  pinned Bash installer. The actual upgraded version is matched to its verified
  native/wheel bytes even for historical tags or differing channel-latest values;
  missing/inconsistent public provenance fails closed. The refresh probe retains
  the old canonical executable open, requires a distinct replacement inode and
  unchanged held bytes, then verifies the replacement hash/version. A healthy
  `refreshed` JSON response alone cannot pass. Public installer hashes are logged;
  checkout/deployed bytes are checked separately because website deployment can lag.
- Homebrew on fresh macOS: real tap/cask installation, tap commit and checksum,
  tmux dependency metadata and whether tmux was already installed, plus refusal
  of non-uv `pairmux update` without changing the binary.
- RPM: real `dnf` installation in official Fedora 44 x86-64 userspace on an
  Ubuntu Docker host, resolving tmux from pairmux's dependency. This is not
  bare-metal Fedora or ARM64 RPM runtime coverage.
- WSL: Windows 2025 importing checksum-verified official Canonical Ubuntu 24.04
  as an owned **WSL1** distribution, a non-root default user, production
  `install.ps1`, and Linux runtime/cleanup. Unsupported runner features fail;
  mocks and WSL2 are not substitutes.

All jobs preserve logs/provenance even on failure. The new helpers passed
local model-free tests and the common runtime probe against the unchanged
installed v0.5.2, but actual hosted/public channel acceptance is still pending.
The workflow passed exact-source actionlint after the renewed authorization;
it has not yet been dispatched. Do not represent prepared checks as passes.
Review also reproduced and fixed relative uv discovery with `PATH=.` and
`GODEBUG=execerrdot=0`; lookup results are now rejected before path resolution,
with zero subprocess calls in the regression. Post-fix local Go vet/race and real tmux integration,
all six real offline uv cases, 25 packaging tests, 20 acceptance-helper tests,
installer mocks/ShellCheck, 24 landing tests/build, and GoReleaser configuration
checks passed. The helper regressions include no-op refresh, held-byte mutation,
wrong actual-version bytes under channel-latest disagreement, and unverifiable
historical upgrades; focused post-fix review found no remaining actionable defect.
Those results do not replace pending final-head hosted CI. At `aea81e2`,
[CI run 37880838961](https://github.com/treeleaves30760/pairmux/actions/runs/37880838961)
passed both real uv 0.11.16 suites and Windows parsing but failed Linux ShellCheck
on the runtime-helper argument guard and a Linux tmux test's temporary HOME
teardown race. The guard now uses an equivalent explicit conditional; the race
requires owned pane writers to finish before temporary-directory cleanup. The
failed run is not a release gate pass. The separately authorized
[tap credential run 37886587406](https://github.com/treeleaves30760/pairmux/actions/runs/37886587406)
passed read/write and probe deletion, with its final survivor check confirming
cleanup. The public Bash installer still lagged the checkout's ambiguous-uv
listing guard at that checkpoint; verify deployment bytes again after `main`
publication rather than treating the local build as deployed.

### Rotating `HOMEBREW_TAP_GITHUB_TOKEN`

Run `scripts/set-tap-token.sh`. Do not do this by hand: the procedure has two
traps that both fail *silently*, and v0.3.0 hit both.

- A token handed between commands in an environment variable is lost whenever
  each command runs in its own shell — an agent's shell-out, a new tab, a `!`
  prefix. It then reads as empty rather than unset.
- `gh` treats an empty `GH_TOKEN` as "none supplied" and falls back to the
  ambient login, so the verification kept passing against a different
  credential entirely, and `gh secret set` stored an empty secret.

The script runs the whole thing in one process: it proves the token can read
**and write** the tap before storing it (read access alone is not enough — a
`Contents: Read` token passes a read check and still cannot push the cask),
stores it over stdin so it never reaches argv, then dispatches the **Tap
credential** workflow, because only Actions can see what was actually stored.
That workflow (`gh workflow run tap-credential.yml`) is also the standalone
check to run any time; it removes the probe file it writes. The release
preflight repeats the read check for stable tags, so a broken token fails the
run before anything is published rather than after PyPI.

## Homebrew (activated 2026-08-01)

Homebrew installs pairmux and the hard tmux >= 3.2 runtime dependency in the
same command. How it is wired:

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
deliberately decoupled from shipping the tap: the cask still installs from
checksummed GitHub tarballs, independently of the PyPI-based `install.sh`.

## Recovering a partially published release

Wheels embed the Go binary whose build stamps `mod_timestamp` from the commit,
so **artifacts are not reproducible across commits** and PyPI forbids filename
reuse. Consequences, learned on v0.2.0:

- Once the PyPI step has succeeded, **never delete/re-point the tag, rebuild
  the published wheels, or rerun the full release workflow**. A retag/rebuild
  can produce different bytes and PyPI rejects reused filenames with
  "File already exists". Complete the remaining steps manually with the
  original validated artifacts instead: undraft with
  `gh release edit vX.Y.Z --draft=false`, download the run's
  `validated-homebrew-cask` artifact, verify its sha256 values against the
  release's `checksums.txt`, and push it to the tap with the contents API
  (same call as the workflow step). If new bytes are needed, publish a new
  version rather than changing the old tag or PyPI version.
- Deleting a release's tag flips the published release back to **draft**;
  restoring the tag does not un-draft it.
- A failed publish job cannot simply be re-run: its preflight refuses a tag
  whose release already exists and is no longer a draft. Complete by hand.
- v0.3.0 hit exactly this with the tap token (`403 Resource not accessible by
  personal access token`) *after* PyPI and the public release had both
  succeeded. The manual completion above worked unchanged: download
  `validated-homebrew-cask`, match its four sha256 values against the release's
  `checksums.txt`, PUT it with the contents API, read back and byte-compare,
  then `brew fetch --cask` to confirm. The preflight added afterwards is what
  stops that ordering from recurring.
- Smoke-test after recovery: `brew tap treeleaves30760/pairmux` and
  `brew fetch --cask treeleaves30760/pairmux/pairmux` must verify checksums.

## Websites and installer delivery

The two sites have separate deployment paths:

- **Cloudflare Pages landing:** `https://pairmux.treeleaves30760.com`, using
  Git integration on `main`, repository-root build command
  `node landing/build.mjs`, output `landing/dist`. The build copies the root
  `install.sh` and `install.ps1`; there is no second installer implementation.
  Build watch paths must include both installers as well as `landing/**`.
- **GitHub Pages docs:** `https://pairmux-docs.treeleaves30760.com/`, built
  from `website/` by `.github/workflows/docs.yml`. Docusaurus uses `baseUrl: '/'`
  and preserves the existing page slugs and strict broken-link checks.

Follow [website/README.md](./website/README.md#deployment-and-custom-domains)
for coordinated custom-domain registration, DNS, HTTPS, and deploy verification.
Cloudflare Pages needs its custom domain registered in the project before a
CNAME can serve it. GitHub Actions Pages ignores repository `CNAME` files;
configure the docs custom domain in Pages settings/API instead.

## APT / Debian retirement

v0.5.3 removes `.deb` generation and APT distribution guidance, but retains
RPMs, tarballs, Homebrew, PyPI, and the `apt install tmux` dependency path.
The [migration guide](https://pairmux-docs.treeleaves30760.com/migrating-from-apt)
covers the former source, origin pin, keyring package/file, and PATH shadowing.
The installer does not run sudo or clean up system-package configuration.

The external retirement was completed on 2026-10-08 after the operator approved
proceeding without waiting for documentation HTTPS. The publisher was disabled
and its waiting run cancelled before its Pages and public repository were
removed. Only the 16 approved historical `.deb` assets were deleted; all 56
surviving release assets and historical tags were verified unchanged. Dated
retirement notices were appended to the original release notes, pointing to the
[migration guide source](./website/docs/migrating-from-apt.md). The local
`pairmux-apt` clone remains an offline backup, not a complete backup of Issues,
Actions or secrets; third-party forks and caches cannot be removed by this process.

Keep historical checksums, other release assets, and published PyPI versions
unchanged. Old checksums may still list `.deb` hashes as a historical record;
do not apply the new seven-asset rule retroactively or re-publish an existing
PyPI version.
