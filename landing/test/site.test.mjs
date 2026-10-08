import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cp, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { buildSite } from '../build.mjs';

const landingDirectory = dirname(dirname(fileURLToPath(import.meta.url)));
const repositoryDirectory = dirname(landingDirectory);
const sourceDirectory = join(landingDirectory, 'src');
const origin = 'https://pairmux.treeleaves30760.com';
const sources = [
  'index.html',
  '404.html',
  '_headers',
  'robots.txt',
  'sitemap.xml',
  'assets/styles.css',
  'assets/copy.js',
  'assets/favicon.svg',
];
const installers = ['install.sh', 'install.ps1'];
let scratchDirectory;
let outputDirectory;
let html;

before(async () => {
  // Resolve macOS /var's symlink so output safety tests use real directories.
  scratchDirectory = await mkdtemp(join(await realpath(tmpdir()), 'pairmux-landing-test-'));
  outputDirectory = join(scratchDirectory, 'built');
  await buildSite({ outputDir: outputDirectory });
  html = await readFile(join(outputDirectory, 'index.html'), 'utf8');
});

after(async () => {
  if (scratchDirectory) await rm(scratchDirectory, { recursive: true, force: true });
});

async function listFiles(directory, prefix = '') {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = `${prefix}${entry.name}`;
    if (entry.isDirectory()) files.push(...await listFiles(join(directory, entry.name), `${path}/`));
    else files.push(path);
  }
  return files.sort();
}

async function snapshot(directory) {
  const contents = new Map();
  for (const file of await listFiles(directory)) contents.set(file, await readFile(join(directory, file)));
  return contents;
}

function decodeHtml(text) {
  return text.replace(/&quot;/g, '"').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
}

function parseHeaders(text) {
  const rules = new Map();
  let headers;
  for (const line of text.split('\n')) {
    if (!line.trim() || line.trimStart().startsWith('#')) continue;
    if (!/^\s/.test(line)) {
      headers = new Map();
      rules.set(line.trim(), headers);
    } else {
      assert.ok(headers, 'Headers need a preceding path rule');
      const separator = line.indexOf(':');
      assert.ok(separator !== -1, `Invalid header: ${line}`);
      headers.set(line.slice(0, separator).trim().toLowerCase(), line.slice(separator + 1).trim());
    }
  }
  return rules;
}

test('build copies only the allowlisted site files and canonical installers byte for byte', async () => {
  assert.deepEqual(await listFiles(outputDirectory), [...sources, ...installers, '.pairmux-landing-output'].sort());
  for (const file of sources) {
    assert.deepEqual(await readFile(join(outputDirectory, file)), await readFile(join(sourceDirectory, file)), file);
  }
  for (const file of installers) {
    assert.deepEqual(await readFile(join(outputDirectory, file)), await readFile(join(repositoryDirectory, file)), file);
    await assert.rejects(lstat(join(sourceDirectory, file)), { code: 'ENOENT' });
  }
  assert.equal((await readFile(join(outputDirectory, '.pairmux-landing-output'), 'utf8')), 'pairmux landing generated output v1\n');
});

test('rebuilding a generated tree is deterministic', async () => {
  const directory = join(scratchDirectory, 'rebuild');
  await buildSite({ outputDir: directory });
  const previous = await snapshot(directory);
  await buildSite({ outputDir: directory });
  assert.deepEqual(await snapshot(directory), previous);
});

test('canonical, Open Graph, robots, and sitemap all use the landing hostname', async () => {
  assert.match(html, /<html lang="en">/);
  assert.match(html, /<meta name="viewport" content="width=device-width, initial-scale=1">/);
  assert.match(html, /<title>pairmux — A shared terminal for agents and humans<\/title>/);
  assert.ok(html.includes(`<link rel="canonical" href="${origin}/">`));
  assert.ok(html.includes(`<meta property="og:url" content="${origin}/">`));
  for (const property of ['og:type', 'og:site_name', 'og:title', 'og:description']) {
    assert.ok(html.includes(`property="${property}"`), property);
  }
  assert.match(html, /<meta name="description" content="[^"]+">/);
  assert.match(html, /<meta name="twitter:card" content="summary">/);
  const sitemap = await readFile(join(outputDirectory, 'sitemap.xml'), 'utf8');
  assert.match(sitemap, /xmlns="http:\/\/www\.sitemaps\.org\/schemas\/sitemap\/0\.9"/);
  assert.deepEqual([...sitemap.matchAll(/<loc>(.*?)<\/loc>/g)].map((match) => match[1]), [`${origin}/`]);
  const robots = await readFile(join(outputDirectory, 'robots.txt'), 'utf8');
  assert.ok(robots.includes(`Sitemap: ${origin}/sitemap.xml`));
  assert.match(robots, /User-agent: \*\nAllow: \//);
});

test('all local links resolve and external links stay on the approved project destinations', async () => {
  const allowedHosts = new Set(['pairmux-docs.treeleaves30760.com', 'github.com', 'pypi.org']);
  const destinations = [];
  for (const page of ['index.html', '404.html']) {
    const contents = await readFile(join(outputDirectory, page), 'utf8');
    const ids = [...contents.matchAll(/\bid="([^"]+)"/g)].map((match) => match[1]);
    assert.equal(new Set(ids).size, ids.length, `${page} contains duplicate IDs`);
    const references = [...contents.matchAll(/\b(?:href|src)="([^"]+)"/g)].map((match) => match[1]);
    references.push(...[...contents.matchAll(/\bdata-copy-target="([^"]+)"/g)].map((match) => `#${match[1]}`));
    references.push(...[...contents.matchAll(/\bdata-copy-status="([^"]+)"/g)].map((match) => `#${match[1]}`));
    for (const value of references) {
      destinations.push(value);
      if (value.startsWith('#')) {
        assert.ok(ids.includes(value.slice(1)), `Missing anchor ${value} in ${page}`);
      } else if (value.startsWith('/')) {
        const path = value === '/' ? 'index.html' : value.slice(1);
        assert.ok((await lstat(join(outputDirectory, path))).isFile(), `${value} must resolve`);
      } else {
        const url = new URL(value);
        assert.equal(url.protocol, 'https:');
        // The canonical URL is metadata, not an off-site navigation link.
        assert.ok(allowedHosts.has(url.hostname) || value === `${origin}/`, value);
        if (url.hostname === 'github.com') assert.equal(url.pathname, '/treeleaves30760/pairmux');
        if (url.hostname === 'pypi.org') assert.equal(url.pathname, '/project/pairmux/');
        if (url.hostname === 'pairmux-docs.treeleaves30760.com') assert.equal(url.pathname, '/');
      }
    }
  }
  for (const required of [
    'https://pairmux-docs.treeleaves30760.com/',
    'https://github.com/treeleaves30760/pairmux',
    'https://pypi.org/project/pairmux/',
    '/install.sh',
    '/install.ps1',
  ]) assert.ok(destinations.includes(required), required);
  assert.doesNotMatch(html, /treeleaves30760\.github\.io|aimux/i);
});

test('the handoff example uses actual README commands and does not simulate a live session', async () => {
  const readme = await readFile(join(repositoryDirectory, 'README.md'), 'utf8');
  const commands = [...html.matchAll(/<code data-readme-command>([\s\S]*?)<\/code>/g)].map((match) => decodeHtml(match[1]));
  assert.equal(commands.length, 6);
  for (const command of commands) assert.ok(readme.split('\n').includes(command), `Not in README: ${command}`);
  assert.ok(commands.includes('pairmux --json new --name handoff'));
  assert.ok(commands.includes('pairmux attach handoff'));
  assert.match(html, /Static CLI example/);
  assert.match(html, /outside tmux/);
  assert.match(html, /note explains what you did; it is not required to resume/);
  assert.match(html, /not a real secret/);
  assert.match(html, /Not another exec API/);
});

test('install commands distinguish a uvx quick run from a persistent uv tool install', () => {
  const expected = new Map([
    ['installer-command', `curl -fsSL ${origin}/install.sh | bash`],
    ['quick-command', 'uvx --default-index https://pypi.org/simple pairmux@latest version'],
    ['tool-command', 'uv tool install --default-index https://pypi.org/simple pairmux'],
  ]);
  for (const [id, command] of expected) assert.ok(html.includes(`<code id="${id}">${command}</code>`), id);
  assert.match(html, /Quick run, without a persistent install/);
  assert.match(html, /Install for everyday use/);
  assert.match(html, /Python 3\.9 or newer for the uv tool environment/);
  assert.match(html, /tmux 3\.2 or newer to run terminals/);
  assert.match(html, /official Astral sources, not PyPI/);
  assert.match(html, /manual commands set PyPI as the default index; they can still honor extra indexes and other source overrides/);
  assert.match(html, /Read install\.sh before running it/);
});

test('installer headers use readable plain text, nosniff, and revalidation without conflicting cache rules', async () => {
  const rules = parseHeaders(await readFile(join(outputDirectory, '_headers'), 'utf8'));
  const common = rules.get('/*');
  assert.equal(common.get('x-content-type-options'), 'nosniff');
  assert.equal(common.get('x-frame-options'), 'DENY');
  assert.equal(common.get('referrer-policy'), 'strict-origin-when-cross-origin');
  assert.equal(common.has('cache-control'), false, 'Pages combines matching header rules; keep cache policy route-specific');
  const csp = common.get('content-security-policy');
  for (const directive of [
    "default-src 'none'",
    "script-src 'self'",
    "style-src 'self'",
    "img-src 'self'",
    "connect-src 'none'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
    "frame-ancestors 'none'",
  ]) assert.ok(csp.includes(directive), directive);
  assert.doesNotMatch(csp, /unsafe-inline|unsafe-eval|https?:/);
  for (const file of installers) {
    const effective = new Map([...common, ...rules.get(`/${file}`)]);
    assert.equal(effective.get('content-type'), 'text/plain; charset=utf-8');
    assert.equal(effective.get('x-content-type-options'), 'nosniff');
    assert.equal(effective.get('cache-control'), 'no-cache, max-age=0, must-revalidate');
    assert.equal(effective.get('x-robots-tag'), 'noindex');
    assert.equal(effective.has('content-disposition'), false, 'Installer links should be readable, not forced downloads');
  }
});

test('a top-level static 404 disables the Pages SPA fallback and offers real recovery links', async () => {
  const notFound = await readFile(join(outputDirectory, '404.html'), 'utf8');
  assert.notEqual(notFound, html);
  assert.match(notFound, /<title>Page not found — pairmux<\/title>/);
  assert.match(notFound, /<meta name="robots" content="noindex">/);
  assert.match(notFound, /href="\/">Back to pairmux/);
  assert.match(notFound, /href="https:\/\/pairmux-docs\.treeleaves30760\.com\/">Open the docs/);
  assert.doesNotMatch(notFound, /http-equiv="refresh"|<script/i);
  assert.equal((await listFiles(outputDirectory)).includes('_redirects'), false);
});

test('the UI is dependency-free, progressively enhanced, and compatible with the strict CSP', async () => {
  const css = await readFile(join(outputDirectory, 'assets/styles.css'), 'utf8');
  assert.deepEqual([...html.matchAll(/<script\s+[^>]*src="([^"]+)"[^>]*>/g)].map((match) => match[1]), ['/assets/copy.js']);
  assert.equal([...html.matchAll(/<script\b/g)].length, 1);
  assert.doesNotMatch(html, /<style\b|\bstyle=|\bon(?:click|load|error)=|javascript:/i);
  assert.doesNotMatch(css, /@import|@font-face|url\(|animation\s*:|transition\s*:/i);
  assert.match(css, /"Avenir Next", "Trebuchet MS", sans-serif/);
  for (const color of ['#f5f9f8', '#142e35', '#0f766e', '#506973', '#d2e2df', '#173d47']) assert.ok(css.includes(color), color);
  assert.match(css, /:focus-visible/);
  assert.match(css, /prefers-reduced-motion/);
  assert.match(css, /grid-template-columns: minmax\(0, 1fr\)/);
  assert.equal([...html.matchAll(/data-copy-target="[^"]+"/g)].length, 3);
  assert.equal([...html.matchAll(/<button\b[^>]*\bhidden>/g)].length, 3);
  assert.equal([...html.matchAll(/role="status" aria-live="polite" aria-atomic="true"/g)].length, 3);
  for (const [, target, status] of html.matchAll(/data-copy-target="([^"]+)" data-copy-status="([^"]+)"/g)) {
    assert.ok(html.includes(`id="${target}"`));
    assert.ok(html.includes(`id="${status}"`));
  }
  assert.match(html, /class="skip-link" href="#main"/);
  assert.match(html, /class="selectable-code" tabindex="0"/);
});

test('Pages documentation matches the repository-root build and keeps docs deployment separate', async () => {
  const readme = await readFile(join(landingDirectory, 'README.md'), 'utf8');
  for (const text of [
    'Dashboard Git integration',
    '`treeleaves30760/pairmux`',
    '| Production branch | `main` |',
    '| Framework preset | `None` |',
    '| Root directory | Repository root; leave blank |',
    '| Build command | `node landing/build.mjs` |',
    '| Build output directory | `landing/dist` |',
    'Complete this association **before**',
    'bare CNAME',
    '522',
    'documentation deployment, not this',
    '404',
  ]) assert.ok(readme.includes(text), text);
});

test('palette text and control colors meet normal-text contrast requirements', async () => {
  const css = await readFile(join(outputDirectory, 'assets/styles.css'), 'utf8');
  const colors = new Map([...css.matchAll(/--([a-z-]+): (#[0-9a-f]{6});/g)].map((match) => [match[1], match[2]]));
  function luminance(hex) {
    const channels = hex.slice(1).match(/.{2}/g).map((channel) => {
      const value = parseInt(channel, 16) / 255;
      return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
    });
    return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
  }
  for (const [foreground, background] of [
    ['ink', 'paper'], ['muted', 'paper'], ['teal', 'paper'],
    ['terminal-text', 'terminal'], ['terminal-muted', 'terminal'], ['terminal-accent', 'terminal'],
  ]) {
    const values = [luminance(colors.get(foreground)), luminance(colors.get(background))].sort((a, b) => b - a);
    const ratio = (values[0] + 0.05) / (values[1] + 0.05);
    assert.ok(ratio >= 4.5, `${foreground} on ${background} has contrast ${ratio.toFixed(2)}`);
  }
});

test('missing installer input fails before touching a previously built output tree', async () => {
  const directory = join(scratchDirectory, 'missing-installer-output');
  await buildSite({ outputDir: directory });
  const previous = await snapshot(directory);
  const installerRoot = join(scratchDirectory, 'missing-installer');
  await mkdir(installerRoot);
  await cp(join(repositoryDirectory, 'install.sh'), join(installerRoot, 'install.sh'));
  await assert.rejects(buildSite({ outputDir: directory, installerRoot }), /Missing or unreadable build input: .*install\.ps1/);
  assert.deepEqual(await snapshot(directory), previous);
});

test('missing static input fails before generating any output', async () => {
  const sourceDir = join(scratchDirectory, 'missing-source');
  const outputDir = join(scratchDirectory, 'missing-source-output');
  await cp(sourceDirectory, sourceDir, { recursive: true });
  await rm(join(sourceDir, '404.html'));
  await assert.rejects(buildSite({ sourceDir, outputDir }), /Missing or unreadable build input: .*404\.html/);
  await assert.rejects(lstat(outputDir), { code: 'ENOENT' });
});

test('cleanup refuses foreign directories, unexpected files, and non-allowlisted assets', async () => {
  const foreign = join(scratchDirectory, 'foreign');
  await mkdir(foreign);
  await writeFile(join(foreign, 'keep.txt'), 'do not remove\n');
  await assert.rejects(buildSite({ outputDir: foreign }), /not generated by this build/);
  assert.equal(await readFile(join(foreign, 'keep.txt'), 'utf8'), 'do not remove\n');

  const extraFile = join(scratchDirectory, 'extra-file');
  await buildSite({ outputDir: extraFile });
  await writeFile(join(extraFile, 'keep.txt'), 'do not remove\n');
  const previous = await snapshot(extraFile);
  await assert.rejects(buildSite({ outputDir: extraFile }), /unexpected output entry: keep\.txt/);
  assert.deepEqual(await snapshot(extraFile), previous);

  const extraAsset = join(scratchDirectory, 'extra-asset');
  await buildSite({ outputDir: extraAsset });
  await writeFile(join(extraAsset, 'assets/keep.txt'), 'do not remove\n');
  await assert.rejects(buildSite({ outputDir: extraAsset }), /unexpected output asset: keep\.txt/);
  assert.equal(await readFile(join(extraAsset, 'assets/keep.txt'), 'utf8'), 'do not remove\n');
});

test('cleanup refuses repository destinations, symlinked output, and symlinked generated entries', async () => {
  await assert.rejects(buildSite({ outputDir: repositoryDirectory }), /restricted to landing\/dist/);
  await assert.rejects(buildSite({ outputDir: join(repositoryDirectory, 'website') }), /restricted to landing\/dist/);
  const target = join(scratchDirectory, 'symlink-target');
  await mkdir(target);
  const outputLink = join(scratchDirectory, 'output-link');
  await symlink(target, outputLink);
  await assert.rejects(buildSite({ outputDir: outputLink }), /symlink in the output path/);

  const linkedAsset = join(scratchDirectory, 'linked-asset');
  await buildSite({ outputDir: linkedAsset });
  await rm(join(linkedAsset, 'assets/copy.js'));
  await writeFile(join(target, 'copy.js'), 'keep this file\n');
  await symlink(join(target, 'copy.js'), join(linkedAsset, 'assets/copy.js'));
  await assert.rejects(buildSite({ outputDir: linkedAsset }), /unexpected output asset: copy\.js/);
  assert.equal(await readFile(join(target, 'copy.js'), 'utf8'), 'keep this file\n');
});

test('empty or symlinked canonical inputs are not copied', async () => {
  const installerRoot = join(scratchDirectory, 'bad-inputs');
  await mkdir(installerRoot);
  await writeFile(join(installerRoot, 'install.sh'), '');
  await cp(join(repositoryDirectory, 'install.ps1'), join(installerRoot, 'install.ps1'));
  await assert.rejects(buildSite({ installerRoot, outputDir: join(scratchDirectory, 'bad-output') }), /non-empty regular file: .*install\.sh/);
  await rm(join(installerRoot, 'install.sh'));
  await symlink(join(repositoryDirectory, 'install.sh'), join(installerRoot, 'install.sh'));
  await assert.rejects(buildSite({ installerRoot, outputDir: join(scratchDirectory, 'bad-output') }), /non-empty regular file: .*install\.sh/);
});

test('the CLI fails with a nonzero exit code when a required input is missing', async () => {
  const fixtureRepository = join(scratchDirectory, 'cli-fixture');
  const fixtureLanding = join(fixtureRepository, 'landing');
  await mkdir(fixtureLanding, { recursive: true });
  await cp(join(landingDirectory, 'build.mjs'), join(fixtureLanding, 'build.mjs'));
  await cp(sourceDirectory, join(fixtureLanding, 'src'), { recursive: true });
  await cp(join(repositoryDirectory, 'install.sh'), join(fixtureRepository, 'install.sh'));
  const result = spawnSync(process.execPath, [join(fixtureLanding, 'build.mjs')], { encoding: 'utf8', cwd: repositoryDirectory });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Landing build failed: Missing or unreadable build input: .*install\.ps1/);
  await assert.rejects(lstat(join(fixtureLanding, 'dist')), { code: 'ENOENT' });
});

test('the CLI accepts a safe explicit output and rejects ambiguous arguments', async () => {
  const cliOutput = join(scratchDirectory, 'cli-output');
  const result = spawnSync(process.execPath, [join(landingDirectory, 'build.mjs'), '--out-dir', cliOutput], { encoding: 'utf8', cwd: repositoryDirectory });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Built 10 static files/);
  assert.deepEqual(await listFiles(cliOutput), await listFiles(outputDirectory));
  const invalid = spawnSync(process.execPath, [join(landingDirectory, 'build.mjs'), '--out-dir'], { encoding: 'utf8', cwd: repositoryDirectory });
  assert.equal(invalid.status, 1);
  assert.match(invalid.stderr, /Usage: node landing\/build\.mjs/);
});
