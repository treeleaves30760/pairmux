const buttons = document.querySelectorAll('[data-copy-target]');

function offerManualCopy(command) {
  const container = command.closest('pre');
  container?.focus({ preventScroll: true });

  const selection = window.getSelection();
  if (!selection) return;

  const range = document.createRange();
  range.selectNodeContents(command);
  selection.removeAllRanges();
  selection.addRange(range);
}

for (const button of buttons) {
  const command = document.getElementById(button.dataset.copyTarget);
  const status = document.getElementById(button.dataset.copyStatus);
  if (!command || !status) continue;

  button.hidden = false;
  button.addEventListener('click', async () => {
    button.disabled = true;
    button.textContent = 'Copying';
    status.textContent = '';

    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error('Clipboard API unavailable');
      }
      await navigator.clipboard.writeText(command.textContent.trim());
      button.textContent = 'Copied';
      status.textContent = 'Command copied.';
    } catch {
      button.textContent = 'Copy';
      status.textContent = 'Copy unavailable. Select the command above and copy it manually.';
      offerManualCopy(command);
    } finally {
      button.disabled = false;
    }
  });
}
