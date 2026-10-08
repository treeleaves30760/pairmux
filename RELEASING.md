# Releasing pairmux

This repository owns the pairmux CLI, native binaries, Python wheel wrappers,
RPM packages, installers, landing page, and documentation site. The companion
`pairmux-skills` repository owns the canonical Agent Skill.

**Upcoming release: v0.5.3.** This migration retires pairmux APT / `.deb`
distribution and makes the website installer use PyPI. Existing tags, published
PyPI versions, and historical checksums are not rewritten. The seven-asset
rule below applies to new builds, not historical release manifests.

## Release channels

| Channel | Implementation | Release operation |
| --- | --- | --- |
| GitHub Releases | GoReleaser builds four macOS/Linux `.tar.gz` archives, two Linux `.rpm` packages, and `checksums.txt`: **seven native assets**. | Push a canonical SemVer tag. The existing workflow verifies and stages the exact artifacts before publishing the release. No `.deb` is produced. |
| PyPI | Four platform wheels wrap the same verified Go binaries; wheel installation requires Python >= 3.9. | Configure the `PYPI_TOKEN` repository secret or migrate to Trusted Publishing. The tag workflow uploads only the four verified wheels. Smoke-test both `uvx` quick runs and persistent `uv tool install`. |
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
2. Update `ChangeLog.md`: move `[Unreleased]` entries into a dated version and
   add the release comparison link. v0.5.3 is not published until its tag and
   workflow complete; keep the migration entries Unreleased until release time.
3. Run the local validation suite from the repository root:

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
   npm --prefix website ci
   npm --prefix website run audit
   npm --prefix website run typecheck
   npm --prefix website run build
   node landing/build.mjs
   ```

   Use only a fresh build output. Verify **four archives + two RPMs + one
   checksum file = seven native assets**, four wheels, and zero `.deb` files.
   The release workflow retains its build-once / verify / publish pipeline;
   it validates wheel installation against the staged artifacts before
   publishing, not against a version that is not yet on PyPI.
4. Verify GitHub release permissions, the PyPI credential preflight, and the
   stable-release Homebrew tap credential (see below). For v0.5.3, confirm the
   new websites and APT migration guide are available and the former APT
   publisher has been retired before tagging. Website/domain setup is an
   explicit deployment task, not a side effect of pushing the release tag.
5. Create and push an annotated SemVer tag from validated `main`. For the
   upcoming release, use `v0.5.3` with a subject such as
   `chore: release-v0-5-3`.
6. Watch the tag workflow. It stages **seven** verified native assets in a
   draft GitHub release, publishes the four verified wheels to PyPI, makes
   the GitHub release public, then updates Homebrew for stable tags. Confirm
   each stage; do not replace this ordering with a separate rebuild.
7. On clean or isolated systems, smoke-test `uvx pairmux version`,
   `uvx pairmux doctor`, `uv tool install pairmux`, the website's `install.sh`,
   a direct RPM, and Homebrew. For release-specific uv checks, use
   `uvx --from 'pairmux==0.5.3' pairmux version` and
   `uv tool install 'pairmux==0.5.3'` **after** that version is published.
   Verify wheel metadata has the new Home/Docs domains and the GitHub
   Repository URL. Use isolated HOME/tool/cache directories so an existing
   installation or cache does not stand in for the release under test.
   Never publish artifacts from an old local `dist/` directory.

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

The external retirement must be coordinated after the new sites and guide are
available: stop the old publisher before retiring its Pages/repository and
removing only the approved historical `.deb` assets. Keep historical tags,
checksums, other release assets, and published PyPI versions unchanged. Old
checksums may still list `.deb` hashes as a historical record; do not apply the
new seven-asset rule retroactively or re-publish an existing PyPI version.
