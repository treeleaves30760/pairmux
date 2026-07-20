# Website

This website is built using [Docusaurus](https://docusaurus.io/), a modern static website generator.

## Installation

```bash
npm ci
```

The checked-in `package-lock.json` is the dependency source of truth. Node.js 20 or newer is required.

## Local Development

```bash
npm run start
```

This command starts a local development server and opens up a browser window. Most changes are reflected live without having to restart the server.

## Build

```bash
npm run typecheck
npm run build
```

The build synchronizes `website/docs/changelog.md` from the repository's `ChangeLog.md`, then generates static content in `build/`. Preview that production output locally with:

```bash
npm run serve
```

## Deployment

Deployment is handled by [`.github/workflows/docs.yml`](../.github/workflows/docs.yml). A push to `main` that changes the website, `ChangeLog.md`, or the workflow runs `npm ci`, the dependency audit, typecheck, and production build, then publishes the `build/` artifact with GitHub Actions' Pages deployment.

The workflow can also be started manually with `workflow_dispatch`. This repository does not use a local `npm run deploy` step or a `gh-pages` branch for its normal deployment path.
