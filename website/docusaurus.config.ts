import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const config: Config = {
  title: 'pairmux',
  tagline: 'Reliable terminal primitives for AI agents on tmux — humans can watch, attach, and help in the same live terminal',
  favicon: 'img/favicon.ico',

  future: {
    v4: true, // Improve compatibility with the upcoming Docusaurus v4
  },

  url: 'https://treeleaves30760.github.io',
  baseUrl: '/pairmux/',

  organizationName: 'treeleaves30760',
  projectName: 'pairmux',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  onBrokenMarkdownLinks: 'throw',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  markdown: {
    // .md renders as CommonMark, .mdx as MDX — so the copied ChangeLog and
    // command-line snippets never trip MDX's JSX parser.
    format: 'detect',
    mermaid: true,
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
        blog: false, // no blog
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'pairmux',
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Docs',
        },
        {to: '/cli-reference', label: 'CLI Reference', position: 'left'},
        {to: '/changelog', label: 'Changelog', position: 'left'},
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
            {label: 'CLI Reference', to: '/cli-reference'},
            {label: 'Concepts', to: '/concepts'},
            {label: 'Changelog', to: '/changelog'},
          ],
        },
        {
          title: 'Project',
          items: [
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
</content>
