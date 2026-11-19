// Fails the build on any npm advisory at moderate severity or above, except the
// ones listed below — and only for as long as those stay genuinely unfixable.
//
// `npm audit --audit-level=moderate` was the gate here before. It went red when
// a transitive dependency picked up an advisory with no published fix, and
// because the audit runs before the build in the Docs workflow, every
// documentation deploy stopped with it. That is the failure mode this script
// exists to prevent: a gate nobody can turn green is a gate that quietly blocks
// everything downstream of it, and after a while nobody reads it either.
//
// So each exception carries the advisory it covers and the reason, and is
// re-validated on every run. If npm starts reporting a fix for an accepted
// package, or the advisory disappears, this fails and tells you to pin it and
// delete the entry. An exception that cannot expire is how the drift starts.
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';

// Accepted advisories. Add an entry ONLY when npm reports no fix available:
// anything else has a version to pin in package.json's "overrides".
const ACCEPTED = [];

const MIN_SEVERITY = 'moderate';
const RANK = {info: 0, low: 1, moderate: 2, high: 3, critical: 4};

function runAudit() {
  // npm audit exits non-zero when it finds anything, which is the normal case
  // here, so the exit code is not the signal — the JSON on stdout is.
  try {
    return JSON.parse(execFileSync('npm', ['audit', '--json'], {encoding: 'utf8'}));
  } catch (error) {
    if (error.stdout) return JSON.parse(error.stdout);
    throw error;
  }
}

// Whether npm's `fixAvailable` for an accepted package is a real fix. `true`
// is. An object names a package to install instead, and counts only when that
// is not a downgrade of what is installed. npm sometimes proposes a downgrade
// that resolves nothing: for GHSA-fc5q-39xc-vjpf it suggested
// @docusaurus/preset-classic 3.6.3 or 3.8.0 in place of the installed 3.10.2,
// on some runs only (most runs said `false`), even though those trees still
// resolve picomatch 2.x inside the affected `<= 4.0.7`. Treating that as a fix
// would make the exception fail at random. Anything we cannot parse or compare
// counts as a fix, so the gate fails closed.
const SEMVER = /^(\d+)\.(\d+)\.(\d+)$/;
function installedVersion(name) {
  try {
    return JSON.parse(readFileSync(new URL(`../node_modules/${name}/package.json`, import.meta.url), 'utf8')).version;
  } catch {
    return undefined;
  }
}
function isRealFix(fixAvailable, installed = installedVersion) {
  if (!fixAvailable) return false;
  if (typeof fixAvailable !== 'object') return true;
  const proposed = SEMVER.exec(fixAvailable.version ?? '');
  const current = SEMVER.exec(installed(fixAvailable.name) ?? '');
  if (!proposed || !current) return true;
  for (let i = 1; i <= 3; i++) {
    const diff = Number(proposed[i]) - Number(current[i]);
    if (diff !== 0) return diff > 0;
  }
  return true;
}

const report = runAudit();
const vulnerabilities = report.vulnerabilities ?? {};

// npm reports one entry per affected package, most of them only affected
// because something they depend on is. Collapse to the root advisories, which
// are the things a human can actually act on.
const roots = new Map();
for (const entry of Object.values(vulnerabilities)) {
  for (const via of entry.via ?? []) {
    if (typeof via !== 'object') continue;
    if (RANK[via.severity] < RANK[MIN_SEVERITY]) continue;
    const id = via.url?.split('/').pop() ?? via.title;
    roots.set(id, {
      id,
      package: via.name,
      title: via.title,
      severity: via.severity,
      url: via.url,
      fixAvailable: vulnerabilities[via.name]?.fixAvailable ?? false,
    });
  }
}

const accepted = new Map();
for (const entry of ACCEPTED) {
  for (const id of entry.advisories) accepted.set(id, entry);
}

const blocking = [];
const allowed = [];
for (const root of roots.values()) {
  const exception = accepted.get(root.id);
  if (!exception) {
    blocking.push(root);
  } else if (isRealFix(root.fixAvailable)) {
    blocking.push({...root, note: 'a fix is now available — pin it and delete the exception'});
  } else {
    allowed.push({root, exception});
  }
}

// An exception for an advisory npm no longer reports is dead weight that will
// outlive everyone's memory of why it was added.
const seen = new Set(roots.keys());
const stale = ACCEPTED.flatMap((entry) =>
  entry.advisories.filter((id) => !seen.has(id)).map((id) => ({entry, id})),
);

for (const {root, exception} of allowed) {
  console.log(`accepted  ${root.severity.padEnd(8)} ${root.package}  ${root.id}`);
  console.log(`          ${exception.reason}`);
}
for (const {entry, id} of stale) {
  console.error(`stale exception: ${entry.package} ${id} is no longer reported — delete it from scripts/audit.mjs`);
}
for (const root of blocking) {
  console.error(`BLOCKING  ${root.severity.padEnd(8)} ${root.package}  ${root.title}`);
  console.error(`          ${root.url}`);
  if (root.note) console.error(`          ${root.note}`);
}

if (blocking.length || stale.length) {
  console.error(
    `\n${blocking.length} blocking advisory/advisories, ${stale.length} stale exception(s).\n` +
      'Pin a fixed version in package.json "overrides", or add a justified exception to scripts/audit.mjs.',
  );
  process.exit(1);
}
console.log(`no blocking advisories at ${MIN_SEVERITY} or above (${allowed.length} accepted)`);
