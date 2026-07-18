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

The builder maps goreleaser's `goos_goarch` to a PyPI platform tag:

| goreleaser (`goos_goarch`) | wheel platform tag |
| --- | --- |
| `darwin_arm64` | `macosx_11_0_arm64` |
| `darwin_amd64` | `macosx_10_12_x86_64` |
| `linux_amd64`  | `manylinux_2_17_x86_64.manylinux2014_x86_64` |
| `linux_arm64`  | `manylinux_2_17_aarch64.manylinux2014_aarch64` |

Windows is intentionally unsupported (use WSL). The compressed manylinux tags
expand to two `Tag:` lines each in the wheel's `WHEEL` metadata, per spec.

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
not a PyPI tag. `--version` accepts a normalized PEP 440 version (`0.1.0`,
`0.1.0.dev1`); a leading `v` (as in a git tag `v0.1.0`) is stripped.

Wheels are written to `--out-dir` (default `dist/`) as
`pairmux-<version>-py3-none-<platform-tag>.whl`.

`--check` re-opens each wheel after building and re-verifies every `RECORD`
hash/size, that `RECORD`'s own row is empty, and that the script kept its exec
bit. Build fails (non-zero exit) if any check fails.

### CI invocation (after goreleaser)

goreleaser produces one binary per build under
`<dist>/<build>_<goos>_<goarch>[_<variant>]/pairmux` (the default `goamd64: v1`
adds a `_v1` suffix, e.g. `pairmux_linux_amd64_v1`). Point the builder at that
directory and it scans for the four supported platforms, ignoring anything else
(Windows builds, `checksums.txt`, `artifacts.json`, archives, ...):

```bash
python3 packaging/pypi/build_wheels.py --version "$VERSION" \
    --dist-dir dist/goreleaser --check
```

This is the exact call the release workflow makes. It leaves four wheels in
`dist/`. A workflow step would look roughly like:

```yaml
# after the goreleaser step has populated dist/goreleaser/
- name: Build PyPI wheels
  run: |
    python3 packaging/pypi/build_wheels.py \
      --version "${GITHUB_REF_NAME#v}" \
      --dist-dir dist/goreleaser --check

- name: Publish to PyPI
  env:
    UV_PUBLISH_TOKEN: ${{ secrets.PYPI_TOKEN }}
  run: uv publish dist/*.whl
```

> Keep goreleaser's output directory (`dist/goreleaser/`) separate from the
> wheel output directory (`dist/`), so `uv publish dist/*.whl` matches only the
> wheels and not goreleaser's archives.

## Publishing

```bash
uv publish dist/*.whl
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

The PyPI name `pairmux` is reserved (a `0.0.1` placeholder exists); real
releases are `0.1.0`+. Wheels for a version already on PyPI cannot be
re-uploaded — bump the version.

## Linting

```bash
uvx check-wheel-contents dist/*.whl
```

Expect one finding: **`W007: Wheel library is empty`**. That is correct and
benign for this model — a binary-in-scripts wheel deliberately has no
`purelib`/`platlib` content. ruff/uv wheels trip the same check. Do not "fix" it.
