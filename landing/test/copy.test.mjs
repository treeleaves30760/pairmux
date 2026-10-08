import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const script = await readFile(new URL('../src/assets/copy.js', import.meta.url), 'utf8');

function createDocument({ clipboard, selectionAvailable = true } = {}) {
  const listeners = new Map();
  const selected = [];
  const focusCalls = [];
  const range = { selectNodeContents: (element) => selected.push(element) };
  const selection = {
    removeAllRanges: () => selected.push('clear'),
    addRange: (value) => selected.push(value),
  };
  const command = {
    textContent: '  uvx --default-index https://pypi.org/simple pairmux@latest version\n',
    closest: (selector) => {
      assert.equal(selector, 'pre');
      return { focus: (options) => focusCalls.push(options) };
    },
  };
  const status = { textContent: '' };
  const button = {
    dataset: { copyTarget: 'command', copyStatus: 'status' },
    textContent: 'Copy',
    hidden: true,
    disabled: false,
    addEventListener: (event, handler) => listeners.set(event, handler),
  };
  const elements = new Map([['command', command], ['status', status]]);
  const document = {
    querySelectorAll: () => [button],
    getElementById: (id) => elements.get(id),
    createRange: () => range,
  };
  const context = vm.createContext({
    document,
    navigator: { clipboard },
    window: { getSelection: () => selectionAvailable ? selection : null },
  });
  vm.runInContext(script, context);
  return { button, status, command, elements, listeners, selected, focusCalls };
}

test('copy controls become available only after enhancement and copy exactly the command', async () => {
  const copied = [];
  const page = createDocument({ clipboard: { writeText: async (text) => copied.push(text) } });
  assert.equal(page.button.hidden, false);
  await page.listeners.get('click')();
  assert.deepEqual(copied, ['uvx --default-index https://pypi.org/simple pairmux@latest version']);
  assert.equal(page.button.textContent, 'Copied');
  assert.equal(page.status.textContent, 'Command copied.');
  assert.equal(page.button.disabled, false);
  assert.equal(page.focusCalls.length, 0);
});

test('clipboard denial gives an actionable message and focuses selectable command text', async () => {
  const page = createDocument({ clipboard: { writeText: async () => { throw new Error('Permission denied'); } } });
  await page.listeners.get('click')();
  assert.equal(page.button.textContent, 'Copy');
  assert.equal(page.button.disabled, false);
  assert.equal(page.status.textContent, 'Copy unavailable. Select the command above and copy it manually.');
  assert.equal(page.focusCalls.length, 1);
  assert.equal(page.focusCalls[0].preventScroll, true);
  assert.equal(page.selected[0], page.command);
  assert.equal(page.selected[1], 'clear');
  assert.equal(page.selected.length, 3);
});

test('missing clipboard support uses the same manual-copy fallback', async () => {
  const page = createDocument();
  await page.listeners.get('click')();
  assert.match(page.status.textContent, /copy it manually/);
  assert.equal(page.button.disabled, false);
  assert.equal(page.selected[0], page.command);
});

test('manual-copy instructions still work without a Selection API', async () => {
  const page = createDocument({ selectionAvailable: false });
  await page.listeners.get('click')();
  assert.match(page.status.textContent, /Select the command above/);
  assert.equal(page.button.disabled, false);
  assert.equal(page.focusCalls.length, 1);
});

test('copy disables the control while in flight and reenables it after completion', async () => {
  let complete;
  const page = createDocument({ clipboard: { writeText: () => new Promise((resolve) => { complete = resolve; }) } });
  const result = page.listeners.get('click')();
  assert.equal(page.button.disabled, true);
  assert.equal(page.button.textContent, 'Copying');
  complete();
  await result;
  assert.equal(page.button.disabled, false);
  assert.equal(page.button.textContent, 'Copied');
});

test('enhancement skips a malformed control without failing the rest of the page', () => {
  const button = { dataset: { copyTarget: 'missing', copyStatus: 'missing' }, hidden: true };
  const context = vm.createContext({
    document: { querySelectorAll: () => [button], getElementById: () => null },
  });
  assert.doesNotThrow(() => vm.runInContext(script, context));
  assert.equal(button.hidden, true);
});
