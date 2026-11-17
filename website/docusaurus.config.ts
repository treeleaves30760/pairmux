import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const siteUrl = 'https://pairmux-docs.treeleaves30760.com';
const homeUrl = 'https://pairmux.treeleaves30760.com';
const repoUrl = 'https://github.com/treeleaves30760/pairmux';

// Site-wide fallback description. Every doc sets its own `description:` front
// matter, which overrides this through react-helmet on that page.
const siteDescription =
  'Documentation for pairmux, the CLI that gives AI coding agents real, persistent terminals (tmux or a built-in PTY multiplexer) with live human handoff.';

// Structured data for search engines. Static on every page: it describes the
// site and the software, not the individual page. No softwareVersion on
// purpose — it would go stale between releases.
const structuredData = {
  '@context': 'https://schema.org',
  '@graph': [
    {
      '@type': 'WebSite',
      '@id': `${siteUrl}/#website`,
      url: `${siteUrl}/`,
      name: 'pairmux documentation',
      description: siteDescription,
      inLanguage: 'en',
      about: {'@id': `${homeUrl}/#software`},
    },
    {
      '@type': 'SoftwareApplication',
      '@id': `${homeUrl}/#software`,
      name: 'pairmux',
      url: homeUrl,
      description:
        'A CLI that gives AI coding agents real, persistent terminals with blocking, observable commands and live human handoff.',
      applicationCategory: 'DeveloperApplication',
      operatingSystem: 'macOS, Linux, Windows',
      license: 'https://opensource.org/licenses/MIT',
      isAccessibleForFree: true,
      offers: {'@type': 'Offer', price: '0', priceCurrency: 'USD'},
      softwareHelp: {'@type': 'CreativeWork', url: `${siteUrl}/`},
      downloadUrl: 'https://pypi.org/project/pairmux/',
      codeRepository: repoUrl,
      sameAs: [repoUrl, 'https://pypi.org/project/pairmux/'],
    },
  ],
};

const config: Config = {
  title: 'pairmux',
  tagline: 'Blocking, observable terminals for AI agents — with live human handoff',
  favicon: 'img/favicon.ico',

  headTags: [
    {tagName: 'link', attributes: {rel: 'icon', type: 'image/svg+xml', href: '/img/favicon.svg'}},
    {tagName: 'link', attributes: {rel: 'apple-touch-icon', href: '/img/apple-touch-icon.png'}},
    {tagName: 'meta', attributes: {name: 'theme-color', media: '(prefers-color-scheme: light)', content: '#ffffff'}},
    {tagName: 'meta', attributes: {name: 'theme-color', media: '(prefers-color-scheme: dark)', content: '#1b1b1d'}},
    {
      tagName: 'script',
      attributes: {type: 'application/ld+json'},
      innerHTML: JSON.stringify(structuredData),
    },
  ],

  future: {
    v4: true, // Improve compatibility with the upcoming Docusaurus v4
  },

  url: siteUrl,
  baseUrl: '/',

  organizationName: 'treeleaves30760',
  projectName: 'pairmux',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  markdown: {
    // .md renders as CommonMark, .mdx as MDX — so the copied ChangeLog and
    // command-line snippets never trip MDX's JSX parser.
    format: 'detect',
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  themes: ['@docusaurus/theme-mermaid'],

  presets: [
    [
      'classic',
      {
        docs: {
          routeBasePath: '/', // docs-only mode: serve the docs at the site root
          sidebarPath: './sidebars.ts',
          editUrl: 'https://github.com/treeleaves30760/pairmux/tree/main/website/',
        },
        // Google ignores <changefreq>/<priority>, and a uniform value says
        // nothing anyway, so omit both. <lastmod> is left off too: it would
        // come from git history, and CI's shallow checkout dates every page
        // to the latest commit. Enable `lastmod: 'date'` (with docs
        // `showLastUpdateTime: true`) only once docs.yml checks out with
        // `fetch-depth: 0`.
        sitemap: {
          changefreq: null,
          priority: null,
          lastmod: null,
          filename: 'sitemap.xml',
        },
        blog: false, // no blog
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    // Default social card (og:image / twitter:image), 1200x630. Shared with
    // the homepage repository's og.png.
    image: 'img/og.png',
    metadata: [
      {name: 'description', content: siteDescription},
      {property: 'og:site_name', content: 'pairmux docs'},
      {property: 'og:type', content: 'website'},
      {property: 'og:image:width', content: '1200'},
      {property: 'og:image:height', content: '630'},
      {property: 'og:image:alt', content: 'pairmux social card: "Your agent drives the terminal. You take over when it matters." Two terminal panels show an agent waiting for a human and a deploy session at an ssh password prompt.'},
      {name: 'twitter:image:alt', content: 'pairmux social card: "Your agent drives the terminal. You take over when it matters." Two terminal panels show an agent waiting for a human and a deploy session at an ssh password prompt.'},
    ],
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'pairmux',
      items: [
        {href: 'https://pairmux.treeleaves30760.com', label: 'Home', position: 'left'},
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Docs',
        },
        {to: '/cli-reference', label: 'CLI Reference', position: 'left'},
        {to: '/migrating-from-apt', label: 'APT Migration', position: 'left'},
        {to: '/changelog', label: 'Changelog', position: 'left'},
        {href: 'https://pypi.org/project/pairmux/', label: 'PyPI', position: 'right'},
        {
          href: 'https://github.com/treeleaves30760/pairmux',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {label: 'Getting Started', to: '/'},
            {label: 'APT Migration', to: '/migrating-from-apt'},
            {label: 'CLI Reference', to: '/cli-reference'},
            {label: 'Concepts', to: '/concepts'},
            {label: 'Changelog', to: '/changelog'},
          ],
        },
        {
          title: 'Project',
          items: [
            {label: 'Home', href: 'https://pairmux.treeleaves30760.com'},
            {label: 'PyPI', href: 'https://pypi.org/project/pairmux/'},
            {label: 'pairmux (GitHub)', href: 'https://github.com/treeleaves30760/pairmux'},
            {label: 'pairmux-skills', href: 'https://github.com/treeleaves30760/pairmux-skills'},
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} pairmux contributors. MIT Licensed. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
