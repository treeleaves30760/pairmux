# packaging/pypi — PyPI platform wheels for pairmux

pairmux is a Go binary, but `uv tool install pairmux`, `uvx pairmux version`,
and `pipx install pairmux` should still Just Work. We get that the same way
**ruff** and **uv** do: publish one **platform wheel per OS/arch** (macOS,
Linux and Windows, each on x86-64 and ARM64), each carrying the prebuilt native
binary in the wheel's *scripts* section. No build backend on the release path,
no compilation on the user's machine, and **no sdist**. A separate, in-tree
backend lets people [install from a source checkout](#install-from-a-source-checkout)
without PyPI; it never produces release artifacts.

## Install from public PyPI

With [uv](https://docs.astral.sh/uv/getting-started/installation/) installed:

```bash
uv tool install pairmux
pairmux version
pairmux doctor
```

Ensure uv's tool executable directory is on PATH and check `command -v pairmux`
for older commands shadowing it. This ordinary persistent install uses your
regular environment and uv configuration, including index overrides, and may
reuse cache; it is not an exclusive-public-PyPI source policy.

### Update (v0.6.0+)

Starting with **v0.6.0**, update the currently running, verified persistent uv
tool installation, including one created by the Bash/WSL installer:

```bash
pairmux update
```

**v0.5.3 and older have no `update` command.** First use
`uv tool install --upgrade pairmux` with your normal configuration to obtain
v0.6.0+ when available, then use the new command.

The updater needs uv installed. It clears all inherited `UV_*` settings,
ignores uv configuration, and reinstalls the latest compatible **stable wheel**
from **`https://pypi.org/simple`**, with no persistent cache or source builds.
Old exact pins, constraints, indexes, and extras are replaced. It never selects
below the running version's numeric core; recognized alpha/beta/rc builds may
advance to that core's final or a later stable release, while dev/snapshot
builds are refused.

A version change returns `updated`; a same-version reinstall returns `refreshed`,
not a no-op. Failures return `E_UPDATE` with a recovery hint, without guaranteed
rollback. Recover with the original manager, or `uv tool install --upgrade pairmux`
for uv installs (normal configuration applies). Homebrew, RPM, pipx/pip,
manual/development installs, and temporary uvx runs are unchanged; tmux is
untouched. There are no command-specific options, automatic updates, sudo, or
uv bootstrap/update. Global `--json` is supported.

### Optional temporary run with uvx

```bash
uvx pairmux version
uvx pairmux doctor
```

`uvx` remains supported. It uses a temporary tool environment, which uv may cache,
or reuses an existing uv-managed installation; it does not promise a fresh
download or the latest version every time. Temporary runs cannot use
`pairmux update`.

### Alternative installer

```bash
curl -fsSL https://pairmux.treeleaves30760.com/install.sh | bash
```

The installer persistently installs with uv, clearing inherited `UV_*` settings
and ignoring uv configuration. It uses **`https://pypi.org/simple`**, no persistent
cache, and wheels only. The pipe executes downloaded code; download and review
[the installer](https://pairmux.treeleaves30760.com/install.sh) first if preferred.
The pairmux wheel comes from [PyPI](https://pypi.org/project/pairmux/); when the
installer needs uv or a managed Python, those may come from Astral upstream,
not PyPI. Python 3.9+ is required for wheel installation, not for executing the
native binary. Supported targets are macOS 12+, glibc 2.17+ Linux and Windows
10 version 1809+ (ConPTY), each on x86-64 or ARM64.

tmux is optional. On macOS and Linux pairmux uses tmux (3.2+, installed
separately) when it is present and its built-in multiplexer otherwise; Windows
always uses the built-in multiplexer. `PAIRMUX_BACKEND=builtin` or
`PAIRMUX_BACKEND=tmux` overrides the automatic choice. Neither the wheel nor
the installers supply tmux.

On Windows, the PowerShell installer applies the same PyPI-only uv policy
natively; set `$env:PAIRMUX_INSTALL_TARGET = 'wsl'` first to install into WSL
instead:

```powershell
irm https://pairmux.treeleaves30760.com/install.ps1 | iex
```

[Homepage](https://pairmux.treeleaves30760.com) ·
[Documentation](https://pairmux-docs.treeleaves30760.com/) ·
[Source and issues](https://github.com/treeleaves30760/pairmux)

## The model

* **Binary-in-wheel.** Each wheel contains the executable at
  `pairmux-<ver>.data/scripts/pairmux` (`pairmux.exe` in Windows wheels).
  Installers (pip, uv, pipx) copy files from a wheel's `*.data/scripts/`
  directory straight into the target environment's `bin/` (`Scripts\` on
  Windows), so the binary lands on `PATH` with its exec bit intact.
* **Per-platform, `py3-none-<platform>` tags.** The wheel is platform-specific
  (`Root-Is-Purelib: false`) but Python-agnostic — any CPython/PyPy ≥ 3.9 can
  install it, because there is no Python code to run.
* **No sdist.** An sdist would tempt a source build (and a Go toolchain) on the
  user's box. We only ship wheels. PyPI is happy to host wheels with no sdist.
* **Shebang safety.** pip/uv rewrite a `scripts/` file's shebang *only* when it
  starts with `#!python`. A native Mach-O/ELF/PE binary starts with its own
  magic bytes, so it is copied verbatim — never mangled.

### Platform mapping

The builder maps GoReleaser's `goos_goarch` to a PyPI platform tag:

| GoReleaser (`goos_goarch`) | wheel platform tag |
| --- | --- |
| `darwin_arm64` | `macosx_12_0_arm64` |
| `darwin_amd64` | `macosx_12_0_x86_64` |
| `linux_amd64`  | `manylinux_2_17_x86_64.manylinux2014_x86_64` |
| `linux_arm64`  | `manylinux_2_17_aarch64.manylinux2014_aarch64` |
| `windows_amd64` | `win_amd64` |
| `windows_arm64` | `win_arm64` |

The compressed manylinux tags expand to two `Tag:` lines each in the wheel's
`WHEEL` metadata, per spec. Windows tags carry no OS-version floor; ConPTY
needs Windows 10 version 1809 or newer.

## Project metadata and PyPI description

`build_wheels.py` owns the Core Metadata headers written into every wheel.
[`DESCRIPTION.md`](./DESCRIPTION.md) is the single source for the user-facing
Markdown rendered on the PyPI project page; the root README and this packaging
guide are not uploaded as the Description. `HOMEPAGE` points to the public
site, `DOCUMENTATION` to the docs site, and `REPOSITORY` remains GitHub;
`ISSUES` and `CHANGELOG` derive from `REPOSITORY`, not from `HOMEPAGE`.

Keep every link in `DESCRIPTION.md` absolute so it works outside the GitHub
repository view. Metadata tests verify the content type, source file, project
links, the absence of a tmux requirement, and install examples. Before
publishing, also ask PyPI's renderer to check the completed wheels:

```bash
uvx --from twine==6.2.0 twine check --strict dist/wheels/*.whl
```

The publish job runs this pinned renderer check again on the exact downloaded
wheel artifacts before reading release credentials or staging GitHub assets.

PyPI release files are immutable. Editing the description does not change an
already-published project version; the new metadata appears when the next
version's wheels are uploaded.

## `build_wheels.py`

A dependency-free, stdlib-only (`zipfile`/`hashlib`/`base64`) Python 3.9+ script
that writes the `.whl` zips by hand: `METADATA`, `WHEEL`, and a `RECORD` with
correct `sha256=` (urlsafe-base64, no padding) hashes and byte sizes, plus the
binary with `0755` encoded in the zip entry's `external_attr`. There is **no
build backend** (no setuptools/maturin/hatch).

### Manual invocation

Explicit `--binary` / `--platform` pairs (repeatable):

```bash
python3 packaging/pypi/build_wheels.py --version 0.1.0 \
    --binary dist/pairmux_darwin_arm64/pairmux   --platform darwin_arm64 \
    --binary dist/pairmux_linux_amd64_v1/pairmux --platform linux_amd64 \
    --check
```

`--platform` takes a goreleaser **`goos_goarch`** token (e.g. `darwin_arm64`),
not a PyPI tag. `--version` accepts canonical three-part SemVer (`v0.1.0` or
`v0.2.0-rc.1`; the lowercase `v` is optional). Prereleases map to normalized
PEP 440 wheel versions such as `0.2.0rc1`. Ambiguous forms, leading zeroes,
local versions, and shortened releases are rejected.

Wheels are written to `--out-dir` (default `dist/`) as
`pairmux-<version>-py3-none-<platform-tag>.whl`.

`--check` re-opens each wheel after building and re-verifies every `RECORD`
hash/size, that `RECORD`'s own row is empty, and that the script kept its exec
bit. Build fails (non-zero exit) if any check fails.

Native headers are checked before any wheel is emitted:

- Darwin inputs must be thin, little-endian 64-bit Mach-O binaries whose CPU
  architecture and deployment-target load command exactly match the
  `macosx_*` wheel tag.
- Linux inputs must be little-endian ELF64 executables (or PIE executables)
  whose machine architecture matches the x86-64 or aarch64 manylinux tag.
- Windows inputs must be PE32+ images (`MZ` header, `PE\0\0` signature,
  optional-header magic `0x20b`) marked as executables, not DLLs, for the
  console subsystem, with machine `0x8664` for `win_amd64` or `0xAA64` for
  `win_arm64`. The wheel entry is named `pairmux.exe`.

This rejects swapped cross-build artifacts, GUI-subsystem or 32-bit Windows
builds, and prevents a tag from claiming an older macOS release than the
bundled binary actually supports. A platform tag without a header check is
refused rather than packaged unverified.

### CI invocation

GoReleaser produces one binary per build under
`<dist>/<build>_<goos>_<goarch>[_<variant>]/pairmux` (for example,
`pairmux_linux_amd64_v1` and `pairmux_darwin_arm64_v8.0`), or `pairmux.exe`
for Windows (`pairmux_windows_amd64_v1`, `pairmux_windows_arm64_v8.0`). Point
the builder at that directory and it scans for the six supported platforms,
ignoring anything else (`checksums.txt`, `artifacts.json`, archives, ...):

```bash
python3 packaging/pypi/build_wheels.py --version "$VERSION" \
    --dist-dir dist --out-dir dist/wheels --check
```

This is the exact directory layout and builder call used by the release
workflow. Publishing is intentionally ordered after validation:

1. The read-only `validate` job runs Go/Python/shell/workflow checks and
   race-enabled unit and tmux integration tests.
2. A single `goreleaser release --skip=publish` invocation builds six native
   binaries, four `.tar.gz` archives (macOS/Linux), two `.zip` archives
   (Windows), two RPM packages, and one checksum manifest. `verify_release.py`
   checks the structured artifact manifest, archive contents, binary identity,
   filenames, every checksum, and that the rendered Homebrew cask references
   only the four macOS/Linux tarballs, before staging **nine public assets**.
   Unexpected Debian packages or checksum rows are rejected.
3. The wheel builder consumes those same six binaries and emits exactly six
   wheels. The just-built Linux x86-64 wheel is smoke-tested with pip, persistent
   `uv tool install`, and temporary `uvx --from` using exact tag-version
   comparisons. uv is bootstrapped as the pinned `uv==0.11.16` official PyPI
   binary package in a disposable venv; tool/bin/cache/HOME directories are
   isolated. Both uv paths use the local wheel with `--offline`, `--no-index`,
   `--no-build`, `--no-config`, and the runner's existing Python with downloads
   disabled. Validation never requests a not-yet-published version or public
   PyPI's latest pairmux. A `windows-latest` job then runs the preserved bytes:
   it pip-installs the `win_amd64` wheel into a fresh venv with no index, runs
   `pairmux.exe version` from it and from the checksum-verified
   `windows_amd64` zip, and requires both to report the tag version and to be
   byte-identical. Publishing waits for that job.
4. The native assets, wheels, and rendered Homebrew cask are preserved separately.
   The publish job downloads those exact bytes and never invokes GoReleaser or
   a compiler.
5. After description rendering and credential preflight, GitHub assets are first
   uploaded to a draft release. PyPI receives the exact verified wheels; only
   after that succeeds is the GitHub release made visible. The rendered cask is
   then published to the Homebrew tap for stable tags only.

The validation step is equivalent to:

```yaml
- name: Build PyPI wheels
  run: |
    python3 packaging/pypi/build_wheels.py \
      --version "$GITHUB_REF_NAME" \
      --dist-dir dist \
      --out-dir dist/wheels --check
    test "$(find dist/wheels -maxdepth 1 -name '*.whl' | wc -l | tr -d ' ')" -eq 6
```

> Keep GoReleaser's build directories under `dist/` and wheel output under
> `dist/wheels/`, so the uploaded artifact contains only verified wheel files.

## Install from a source checkout

For people who can clone this repository but cannot install pairmux from PyPI.
The repository-root `pyproject.toml` declares an in-tree PEP 517 backend,
[`pairmux_build_backend.py`](./pairmux_build_backend.py), with **no build
dependencies** (`requires = []`), so pip and uv need no index at all. It reuses
`build_wheels.py` for metadata, header checks and the wheel writer, but always
produces a host-only **development** wheel; it is never part of a release.

**With Go 1.25+** ([go.dev/dl](https://go.dev/dl/)) on PATH, from the clone:

```bash
python -m pip install .                    # into the active environment
uv tool install --offline .                # or: a persistent uv tool
uv tool install --offline --reinstall .    # after git pull, to rebuild
```

`pipx install .` uses the same backend, but pipx first installs its own shared
pip from an index, so it is not an offline path on a fresh machine.

`pip install git+https://github.com/treeleaves30760/pairmux` also works where
GitHub is reachable but PyPI is not (pip clones with tags, so the version is
derived as below). The backend runs `go build -trimpath -buildvcs=false
-ldflags "-s -w -X …/internal/version.Version=<version>"` with
`CGO_ENABLED=0` and the interpreter's GOOS/GOARCH into a temporary directory
outside the checkout. It downloads nothing itself; Go resolves its toolchain as
you configured it.

**Without Go**, download your platform's archive from
[GitHub Releases](https://github.com/treeleaves30760/pairmux/releases), verify
it against `checksums.txt`, extract `pairmux` (`pairmux.exe` on Windows), and
point `PAIRMUX_PREBUILT_BINARY` at its **absolute** path:

```bash
PAIRMUX_PREBUILT_BINARY="$PWD/../pairmux-download/pairmux" python -m pip install .
```

```powershell
# Windows PowerShell, from the clone, after downloading the release zip
# pairmux_<version>_windows_amd64.zip next to it and checking its SHA256:
Expand-Archive ..\pairmux_<version>_windows_amd64.zip -DestinationPath ..\pairmux-download
$env:PAIRMUX_PREBUILT_BINARY = (Resolve-Path ..\pairmux-download\pairmux.exe).Path
python -m pip install .            # or: uv tool install --offline .
Remove-Item Env:PAIRMUX_PREBUILT_BINARY
```

The binary is packaged byte-for-byte after the same Mach-O/ELF/PE header checks
the release wheels use, against the installing interpreter's platform (an
x86-64 Python under Rosetta needs the darwin amd64 binary). It is never
executed during the build. `pairmux version` then reports that binary's own
stamp (the release version for a release binary), while the wheel keeps the
development version below.

**Version.** `git describe` against the latest canonical `vX.Y.Z` tag yields
`X.Y.Z.post<N>.dev0+g<12-hex sha>`, with `.dirty` appended to the local label
for uncommitted changes to tracked files: `0.6.0.post2.dev0+gdc23e7f011a2`
is two commits after v0.6.0. Even the tagged commit itself builds as
`X.Y.Z.post0.dev0+g<sha>`. The `.dev` segment and the `+` local label make the
build sort after its base release, impossible to upload to PyPI (local versions
are refused) and impossible to confuse with a published wheel. The same string
is stamped into the binary, and `pairmux update` refuses it as not a canonical
release; reinstall from the clone instead. Without usable git metadata (a
source archive, a vendored copy, no `git`) the version is `0.0.0.dev0+unknown`,
or `0.0.0.dev0+g<sha>` for history without release tags.

`PAIRMUX_VERSION` overrides the version with a canonical SemVer tag (`v1.0.1`,
`v1.0.1-rc.1`) or a canonical PEP 440 version (`1.0.1.dev3+mybuild`); anything
else is rejected. A release-shaped override (no `.dev`, no `+`) is honored with
a warning, because it gives up the distinction above. The installer
`install.ps1` reads the same variable as a release pin, so clear it before a
source build unless you mean it.

**Wheel tags.** The host's own tag, read from the binary: `macosx_<minos>_<arch>`
from the Mach-O deployment target, `win_amd64`/`win_arm64`, or on Linux the
host-only `linux_x86_64`/`linux_aarch64`. A locally built static binary has not
been audited for manylinux, `linux_*` installs on glibc and musl distributions
alike, and PyPI refuses `linux_*` uploads: one more guard against publishing a
development build.

**Not supported, with an explanation:** `build_sdist` (there is no sdist, as
for the release; `uv build` needs `--wheel`), editable installs (a compiled
binary has nothing to link), and `--config-settings` (use the two environment
variables). The checkout must contain `pyproject.toml`, so tags up to v0.6.0
cannot be installed this way. uv may reuse a wheel it built from the directory,
so the root `pyproject.toml` lists Go sources, embedded assets, packaging files,
git commit/tags and both variables as uv cache keys; a changed file behind an
unchanged `PAIRMUX_PREBUILT_BINARY` path still needs `--reinstall`.

## Publishing

```bash
uv publish dist/wheels/*.whl
```

`uv publish` authenticates with a **PyPI API token** via the `UV_PUBLISH_TOKEN`
environment variable (username defaults to `__token__`), or with an explicit
`--token`. Store the token as a GitHub Actions secret and map it in the step's
`env:`.

**Required CI secret:**

| Secret name | Value | Used as |
| --- | --- | --- |
| `PYPI_TOKEN` | a PyPI API token (starts with `pypi-`) | `UV_PUBLISH_TOKEN: ${{ secrets.PYPI_TOKEN }}` |

(You can name the secret whatever you like; `PYPI_TOKEN` is the convention this
README and the workflow snippet use. Alternatively, PyPI **Trusted Publishing**
via OIDC needs no stored token — `uv publish` with `--trusted-publishing
automatic` — but that requires configuring a publisher on PyPI and is out of
scope here.)

The PyPI distribution name is `pairmux`. Confirm project ownership and configure
the repository's `PYPI_TOKEN` before pushing a release tag. Publishing uses
PyPI's simple index as a duplicate check, so a workflow retry can skip exact
wheel files already accepted while the matching GitHub release is still a
draft. If PyPI succeeds but a later step fails, preserve the already-published
wheel bytes and follow the recovery procedure in
[`RELEASING.md`](../../RELEASING.md); do not retag or rebuild that version.

## Linting

```bash
uvx check-wheel-contents dist/wheels/*.whl
```

Expect one finding: **`W007: Wheel library is empty`**. That is correct and
benign for this model — a binary-in-scripts wheel deliberately has no
`purelib`/`platlib` content. ruff/uv wheels trip the same check. Do not "fix" it.
