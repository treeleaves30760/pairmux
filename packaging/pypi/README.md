# packaging/pypi — PyPI platform wheels for pairmux

pairmux is a Go binary, but `uv tool install pairmux` and `pipx install pairmux`
should still Just Work. We get that the same way **ruff** and **uv** do: publish
one **platform wheel per OS/arch**, each carrying the prebuilt native binary in
the wheel's *scripts* section. No build backend, no compilation on the user's
machine, and **no sdist**.

## The model

* **Binary-in-wheel.** Each wheel contains the executable at
  `pairmux-<ver>.data/scripts/pairmux`. Installers (pip, uv, pipx) copy files
  from a wheel's `*.data/scripts/` directory straight into the target
  environment's `bin/`, so the binary lands on `PATH` with its exec bit intact.
* **Per-platform, `py3-none-<platform>` tags.** The wheel is platform-specific
  (`Root-Is-Purelib: false`) but Python-agnostic — any CPython/PyPy ≥ 3.9 can
  install it, because there is no Python code to run.
* **No sdist.** An sdist would tempt a source build (and a Go toolchain) on the
  user's box. We only ship wheels. PyPI is happy to host wheels with no sdist.
* **Shebang safety.** pip/uv rewrite a `scripts/` file's shebang *only* when it
  starts with `#!python`. A native Mach-O/ELF binary starts with its own magic
  bytes, so it is copied verbatim — never mangled.

### Platform mapping

The builder maps GoReleaser's `goos_goarch` to a PyPI platform tag:

| GoReleaser (`goos_goarch`) | wheel platform tag |
| --- | --- |
| `darwin_arm64` | `macosx_12_0_arm64` |
| `darwin_amd64` | `macosx_12_0_x86_64` |
| `linux_amd64`  | `manylinux_2_17_x86_64.manylinux2014_x86_64` |
| `linux_arm64`  | `manylinux_2_17_aarch64.manylinux2014_aarch64` |

Windows is intentionally unsupported (use WSL). The compressed manylinux tags
expand to two `Tag:` lines each in the wheel's `WHEEL` metadata, per spec.

## Project metadata and PyPI description

`build_wheels.py` owns the Core Metadata headers written into every wheel.
[`DESCRIPTION.md`](./DESCRIPTION.md) is the single source for the user-facing
Markdown rendered on the PyPI project page; the root README and this packaging
guide are not uploaded as the Description.

Keep every link in `DESCRIPTION.md` absolute so it works outside the GitHub
repository view. Metadata tests verify the content type, source file, project
links, runtime requirement, and install examples. Before publishing, also ask
PyPI's renderer to check the completed wheels:

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

This rejects swapped cross-build artifacts and prevents a tag from claiming an
older macOS release than the bundled binary actually supports.

### CI invocation

GoReleaser produces one binary per build under
`<dist>/<build>_<goos>_<goarch>[_<variant>]/pairmux` (for example,
`pairmux_linux_amd64_v1` and `pairmux_darwin_arm64_v8.0`). Point the builder at
that directory and it scans for the four supported platforms, ignoring anything else
(Windows builds, `checksums.txt`, `artifacts.json`, archives, ...):

```bash
python3 packaging/pypi/build_wheels.py --version "$VERSION" \
    --dist-dir dist --out-dir dist/wheels --check
```

This is the exact directory layout and builder call used by the release
workflow. Publishing is intentionally ordered after validation:

1. The read-only `validate` job runs Go/Python/shell/workflow checks and
   race-enabled unit and tmux integration tests.
2. A single `goreleaser release --skip=publish` invocation builds four native
   binaries, four archives, four Linux packages, and one checksum manifest.
   `verify_release.py` checks the structured artifact manifest, archive contents,
   binary identity, filenames, and every checksum before staging nine public assets.
3. The wheel builder consumes those same four binaries, emits exactly four
   wheels, and smoke-tests both the Linux wheel and Debian package with an exact
   tag-version comparison.
4. The native assets and wheels are preserved separately. The publish job
   downloads those exact bytes and never invokes GoReleaser or a compiler.
5. GitHub assets are first uploaded to a draft release. PyPI receives the exact
   verified wheels; only after that succeeds is the GitHub release made visible.

The validation step is equivalent to:

```yaml
- name: Build PyPI wheels
  run: |
    python3 packaging/pypi/build_wheels.py \
      --version "$GITHUB_REF_NAME" \
      --dist-dir dist \
      --out-dir dist/wheels --check
    test "$(find dist/wheels -maxdepth 1 -name '*.whl' | wc -l | tr -d ' ')" -eq 4
```

> Keep GoReleaser's build directories under `dist/` and wheel output under
> `dist/wheels/`, so the uploaded artifact contains only verified wheel files.

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
draft.

## Linting

```bash
uvx check-wheel-contents dist/wheels/*.whl
```

Expect one finding: **`W007: Wheel library is empty`**. That is correct and
benign for this model — a binary-in-scripts wheel deliberately has no
`purelib`/`platlib` content. ruff/uv wheels trip the same check. Do not "fix" it.
