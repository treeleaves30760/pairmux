import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const sidebars: SidebarsConfig = {
  docsSidebar: [
    'getting-started',
    'cli-reference',
    'concepts',
    'skills',
    {
      type: 'category',
      label: 'Guides',
      collapsed: false,
      items: [
        'guides/long-running-commands',
        'guides/interactive-programs',
        'guides/human-collaboration',
        'guides/multi-agent-sharing',
      ],
    },
    'changelog',
  ],
};

export default sidebars;
