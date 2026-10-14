# Documentation website

This directory contains the [Docusaurus](https://docusaurus.io/) documentation
site at **https://pairmux-docs.treeleaves30760.com/**. The separate
**https://pairmux.treeleaves30760.com** homepage lives in the private
`treeleaves30760/pairmux-website` repository and serves this repository's root
installers. This site's local build does not change external Pages settings or
DNS.

## Installation

Run these commands from `website/`:

```bash
npm ci
```

The checked-in `package-lock.json` is the dependency source of truth. Node.js
20 or newer is required; CI uses Node.js 24.

## Local development

```bash
npm run start
```

This command starts a local development server and opens a browser window.
Most changes are reflected live without restarting the server.

## Build

```bash
npm run typecheck
npm run build
```

The build synchronizes `website/docs/changelog.md` from the repository's
`ChangeLog.md`, then generates static content in `build/`. Do not hand-edit the
generated changelog page. Preview production output locally with:

```bash
npm run serve
```

Docusaurus is in docs-only mode. `url` is the docs origin and `baseUrl` is `/`,
not `/pairmux/`; internal slugs such as `/`, `/cli-reference`, `/concepts`,
`/skills`, `/guides/human-collaboration`, and `/changelog` are unchanged.
Broken links, anchors, and Markdown links remain build errors. Do not relax
those gates to make a domain migration pass.

## Deployment and custom domains

| Site | Hosting | Build | Output |
| --- | --- | --- | --- |
| Home and installers | Cloudflare Pages project `pairmux-website`, deployed by GitHub Actions | `ci` workflow in `treeleaves30760/pairmux-website` | Managed in that repository |
| Documentation | GitHub Actions Pages | `npm run build` from `website/` | `website/build` |

### Homepage

The homepage and the public `/install.sh` and `/install.ps1` are built and
deployed from `treeleaves30760/pairmux-website`; its build fetches the root
installers from this repository's `main`. See
[RELEASING.md](../RELEASING.md#websites-and-installer-delivery) for
redeploying it after an installer change. The in-repository `landing/` build
and its Cloudflare Pages Git integration were retired on 2026-10-15.

### GitHub Pages docs

Deployment is handled by [`.github/workflows/docs.yml`](../.github/workflows/docs.yml).
A push to `main` affecting `website/`, `ChangeLog.md`, or the workflow runs
`npm ci`, the dependency audit, typecheck, and production build, then publishes
the `build/` artifact through GitHub Actions Pages. Pull requests run the
validation build but do **not** deploy. `workflow_dispatch` supports manual
runs; there is no local `npm run deploy` step or `gh-pages` branch in the
normal deployment path. The deploy job first checks that Pages is bound to
`pairmux-docs.treeleaves30760.com`; otherwise it leaves the existing production
site intact and emits a notice. After the Dashboard/DNS handoff, dispatch
**Docs** again to publish the root-baseUrl artifact.

Coordinate the domain switch with the root-baseUrl build:

1. Verify domain ownership in the GitHub owner's Pages settings using the
   exact TXT challenge supplied by GitHub. Keep that verification record.
2. Configure `pairmux-docs.treeleaves30760.com` as the repository's **Pages
   custom domain** in Settings/API, with GitHub Actions as the publishing
   source. **Actions publishing ignores repository `CNAME` files**; adding
   one is neither required nor sufficient to bind this domain.
3. In Cloudflare DNS, create a **DNS-only** CNAME:
   `pairmux-docs` → `treeleaves30760.github.io`. The target has no protocol,
   `/pairmux` path, or Pages project name.
4. Deploy the Docusaurus build with
   `url: 'https://pairmux-docs.treeleaves30760.com'` and `baseUrl: '/'`
   in the coordinated change window. Switching only DNS or only `baseUrl`
   leaves an inconsistent site.
5. Once DNS verification and certificate provisioning complete, enable
   **Enforce HTTPS**. Check the root, deep links, assets, 404s, and the old
   `https://treeleaves30760.github.io/pairmux/` URLs for correct redirects
   without loops. Update the GitHub repository homepage to the homepage URL
   after verification.

Local commands below perform only read-only endpoint checks after deployment:

```bash
dig +short pairmux.treeleaves30760.com
dig +short pairmux-docs.treeleaves30760.com CNAME
curl -fsSI https://pairmux.treeleaves30760.com/install.sh
curl -fsSI https://pairmux.treeleaves30760.com/install.ps1
curl -fsSI https://pairmux-docs.treeleaves30760.com/cli-reference
curl -fsSI https://pairmux-docs.treeleaves30760.com/guides/human-collaboration
curl -sSIL https://treeleaves30760.github.io/pairmux/cli-reference
```

Check the installer bodies as well as their headers and inspect browser deep
links on desktop and mobile. If account permissions or authentication block
custom-domain setup, hand off that step to the operator; a successful local
build is not evidence that a site or domain is live.

References: [Cloudflare custom domains](https://developers.cloudflare.com/pages/configuration/custom-domains/),
[GitHub Pages custom domains](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site),
[GitHub Actions and CNAME](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/troubleshooting-custom-domains-and-github-pages#cname-errors).
