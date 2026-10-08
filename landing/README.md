# pairmux landing site

Dependency-free static landing page for <https://pairmux.treeleaves30760.com>.
The documentation site is separate at <https://pairmux-docs.treeleaves30760.com>;
this directory does not build or deploy `website/`.

## Build and test

Use Node.js 22 or newer. No package installation is needed. From the repository root:

```sh
node --test landing/test/*.test.mjs
node landing/build.mjs
```

The generated site is `landing/dist/`. Sources are in `landing/src/`. The only
browser JavaScript progressively enhances the three copy buttons; commands remain
selectable when JavaScript or clipboard access is unavailable. The handoff example
is static, not a simulated live session, and its CLI commands are checked against
the root README.

`build.mjs` copies root `install.sh` and `install.ps1` **byte for byte** into the
output. Those root files are canonical; do not add installer copies under `src/`.
A missing, empty, or non-regular required input fails the build before touching an
existing output tree.

Output cleanup is deliberately limited. Inside this repository, only
`landing/dist` is accepted. A first build requires a nonexistent or empty real
directory; later builds require the `.pairmux-landing-output` marker and only the
allowlisted generated files. The script does not recursively delete the output
directory, does not traverse symlinks, and refuses unexpected files or directories.
A test or local caller can pass `--out-dir <absolute-directory>` for an empty or
previously generated directory **outside** this repository. On macOS, use a resolved
real path rather than a symlink such as `/tmp`.

Tests cover byte equality, metadata, project links, README CLI examples,
plain-text installer headers, static 404 behavior, output safety, missing-input
failure, and clipboard success/failure. A local file server may not implement
Cloudflare's `_headers` or 404 routing; verify actual response headers and status
codes on the Pages preview before releasing.

## Cloudflare Pages: Dashboard Git integration

This is a manual Dashboard setup, not a Wrangler or GitHub Actions deployment.
These instructions describe future deployment; running the build alone does not
publish anything.

1. In the Cloudflare Dashboard, open **Workers & Pages**, then **Create application**
   and choose **Pages**. Use **Import an existing Git repository** (Git integration).
2. Connect GitHub and select **`treeleaves30760/pairmux`**. Name the Pages project
   `pairmux` if that name is available; the Dashboard shows its assigned
   `<project>.pages.dev` hostname.
3. Configure the build as follows:

   | Setting | Value |
   | --- | --- |
   | Production branch | `main` |
   | Framework preset | `None` |
   | Root directory | Repository root; leave blank |
   | Build command | `node landing/build.mjs` |
   | Build output directory | `landing/dist` |
   | Build environment variable | `NODE_VERSION=22` (or a newer supported LTS) |

   Do not set the root directory to `landing`: the build needs both canonical
   installers from the repository root. Do not set output to `website/`.
4. Save and deploy only when the repository changes are approved and available on
   GitHub. Subsequent pushes to `main` trigger production builds. If branch previews
   are enabled, other branches produce preview URLs, not a new production site.
5. Review the preview: desktop and mobile layout, keyboard focus, manual-copy
   fallback, navigation links, readable installers, and a genuinely missing URL.
   Check both `/install.sh` and `/install.ps1` responses for
   `Content-Type: text/plain; charset=utf-8`, `X-Content-Type-Options: nosniff`, and
   revalidation cache policy. Unknown paths should return a **404**, not landing
   HTML with a 200 status. `_headers` and top-level `404.html` are copied into the
   build output; do not enable an SPA catch-all redirect.

## Register the custom hostname before DNS

In the Pages project, open **Custom domains**, select **Set up a custom domain**,
and enter **`pairmux.treeleaves30760.com`**. Complete this association **before**
manually creating any DNS record. A bare CNAME to a Pages project that has not
registered the hostname can fail with a Cloudflare 522.

- If the zone is managed in the same Cloudflare account, follow the Dashboard's
  confirmation flow; Cloudflare can create the CNAME automatically.
- If DNS is elsewhere, after registering the hostname, add the CNAME at that DNS
  provider: name `pairmux`, target the **actual `<project>.pages.dev` hostname shown
  by the Pages project**. Do not assume the project received `pairmux.pages.dev`.
- Wait for Pages to show the domain and TLS certificate as active, then check
  `https://pairmux.treeleaves30760.com/`, the installer endpoints, and an unknown URL.

`pairmux-docs.treeleaves30760.com` belongs to the documentation deployment, not this
Pages project. Do not redirect or register the docs hostname here. Canonical URLs,
Open Graph metadata, robots, and sitemap intentionally name the production landing
hostname even when viewing a preview.

## Package-source wording

The shell installer isolates the pairmux installation from inherited uv settings
and configuration, and resolves the platform wheel exclusively from public PyPI.
uv bootstrap and a Python download, when needed, use official Astral upstream
sources rather than PyPI. tmux 3.2+ is a separate OS dependency; Python 3.9+ is needed
for the uv tool environment. Windows uses WSL, not a native pairmux binary.

The manual `uvx --default-index https://pypi.org/simple pairmux@latest version` is a
quick run, not a persistent installation. `uv tool install --default-index
https://pypi.org/simple pairmux` is the persistent alternative. Both set the default
index but may still honor extra indexes and other source overrides in the user's
environment/configuration; do not describe those manual commands as source-isolated.

## Cloudflare references

- [Static HTML deployment](https://developers.cloudflare.com/pages/framework-guides/deploy-anything/)
- [Git integration](https://developers.cloudflare.com/pages/get-started/git-integration/)
- [Build configuration](https://developers.cloudflare.com/pages/configuration/build-configuration/)
- [Custom domains](https://developers.cloudflare.com/pages/configuration/custom-domains/)
- [Response headers](https://developers.cloudflare.com/pages/configuration/headers/)
- [Serving Pages / 404 behavior](https://developers.cloudflare.com/pages/configuration/serving-pages/)
