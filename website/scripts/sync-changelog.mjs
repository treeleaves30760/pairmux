// Generates docs/changelog.md from the repository ChangeLog.md so the site has a
// single source of truth. Runs automatically before `start` and `build` (see the
// prestart/prebuild scripts in package.json). Plugin-less on purpose.
import {readFileSync, writeFileSync} from 'node:fs';

const src = new URL('../../ChangeLog.md', import.meta.url);
const dest = new URL('../docs/changelog.md', import.meta.url);

const raw = readFileSync(src, 'utf8');

// Drop the leading "# Changelog" H1: the front-matter title below renders it, and
// two H1s would be a duplicate heading.
const lines = raw.split('\n');
let start = 0;
while (start < lines.length && lines[start].trim() === '') start++;
if (start < lines.length && /^#\s+/.test(lines[start])) start++;
const body = lines.slice(start).join('\n').replace(/^\n+/, '');

const frontMatter = [
  '---',
  'title: Changelog',
  'slug: /changelog',
  'description: Release history for pairmux, in Keep a Changelog format.',
  '---',
  '',
  '<!-- This page is generated from the repository ChangeLog.md at build time. -->',
  '<!-- Do not edit it directly — edit ChangeLog.md in the repo root. -->',
  '',
].join('\n');

writeFileSync(dest, frontMatter + body);
console.log(`sync-changelog: wrote ${dest.pathname} from ${src.pathname}`);
