'use strict';
// Styled replacements for the browser's confirm() and prompt().
//   if (!await askConfirm('Delete this task? Its files go too.')) return;
//   const name = await askText('Node name', current);   // null when cancelled
// The first sentence (up to the first "?") becomes the title and the rest the body. The button is
// named after the leading verb, and destructive verbs get the red button with Cancel focused.
// Escape cancels (only this dialog, not the one beneath it), Enter confirms, Tab stays inside.
const ASK_VERBS = [
  [/^(Delete|Remove|Clear|Revoke|Replace|Reset)\b/, 1], [/^Sign out\b/, 1, 'Sign out'], [/^Stop\b/, 1], [/^Restart\b/, 0],
  [/^Roll\b/, 1, 'Roll back'], [/^Pause\b/, 0], [/^Put the files back\b/, 1, 'Put files back'], [/^Trust\b/, 0],
  [/^Copy\b/, 1, 'Copy and delete'], [/^Check GitHub\b/, 0, 'Update now'], [/^Update\b/, 0],
];
let askEl = null, askDone = null;
function askBuild() {
  if (askEl) return askEl;
  askEl = document.createElement('div');
  askEl.id = 'modal-ask'; askEl.className = 'overlay ask-overlay hidden'; askEl.setAttribute('role', 'alertdialog'); askEl.setAttribute('aria-modal', 'true');
  askEl.innerHTML = '<form class="modal ask-modal" autocomplete="off"><h3 id="ask-title"></h3><p class="hint" id="ask-body"></p>' +
    '<input id="ask-input" class="hidden" type="text" aria-labelledby="ask-title"><div class="modal-foot"><button type="button" id="ask-no">Cancel</button><button type="submit" id="ask-yes" class="primary">OK</button></div></form>';
  document.body.appendChild(askEl);
  askEl.setAttribute('aria-labelledby', 'ask-title'); askEl.setAttribute('aria-describedby', 'ask-body');
  askEl.querySelector('form').addEventListener('submit', (e) => { e.preventDefault(); askFinish(true); });
  askEl.querySelector('#ask-no').onclick = () => askFinish(false);
  askEl.addEventListener('mousedown', (e) => { if (e.target === askEl) askFinish(false); });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !askEl.classList.contains('hidden')) { e.preventDefault(); e.stopPropagation(); askFinish(false); }
  }, true);
  return askEl;
}
function askFinish(ok) {
  if (!askDone) return;
  const done = askDone; askDone = null;
  askEl.classList.add('hidden');
  const input = askEl.querySelector('#ask-input'), val = input.value;
  const back = done.back; if (back && document.contains(back) && back.focus) back.focus({ preventScroll: true });
  done.resolve(done.text ? (ok ? val : null) : ok);
}
function askShow({ title, body, yes, danger, text, value }) {
  const el = askBuild();
  if (askDone) askFinish(false);
  const back = document.activeElement;
  el.querySelector('#ask-title').textContent = title;
  const b = el.querySelector('#ask-body'); b.textContent = body || ''; b.classList.toggle('hidden', !body);
  const input = el.querySelector('#ask-input'); input.classList.toggle('hidden', !text); input.value = text ? (value || '') : '';
  const y = el.querySelector('#ask-yes'); y.textContent = yes; y.classList.toggle('danger', !!danger); y.classList.toggle('primary', !danger);
  el.classList.remove('hidden');
  const p = new Promise((resolve) => { askDone = { resolve, back, text }; });
  (text ? input : danger ? el.querySelector('#ask-no') : y).focus({ preventScroll: true });
  if (text) input.select();
  return p;
}
function askConfirm(message, opts = {}) {
  const i = message.indexOf('?');
  const title = i > 0 && i < message.length - 1 ? message.slice(0, i + 1) : message;
  const body = title === message ? '' : message.slice(i + 1).trim();
  let yes = 'OK', danger = false;
  for (const [re, d, label] of ASK_VERBS) { const m = message.match(re); if (m) { yes = label || m[0]; danger = !!d; break; } }
  return askShow({ title, body, yes: opts.ok || yes, danger: opts.danger ?? danger });
}
function askText(message, value = '', opts = {}) {
  return askShow({ title: message, body: opts.body || '', yes: opts.ok || 'Save', text: true, value });
}
