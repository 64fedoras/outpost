'use strict';
// Settings page. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- settings ----------
// Settings shows one section at a time; the list on the left switches between them (remembered in this browser).
// Clicking a link never touches the #/settings route, so the palette and other pages can open a section by clicking its link.
(function settingsNav() {
  const links = [...document.querySelectorAll('#set-nav a')];
  if (!links.length) return;
  // Section ids differ from data-sec for 'model': #set-model is the Claude model <select>, so never build ids from data-sec alone.
  const secs = links.map((a) => document.querySelector('section.set-sec' + a.getAttribute('href')));
  const show = (i) => {
    links.forEach((a, j) => { a.classList.toggle('active', i === j); a.setAttribute('aria-current', i === j ? 'true' : 'false'); });
    secs.forEach((x, j) => x.classList.toggle('hidden', i !== j));
    try { localStorage.setItem('outpost.settingsSec', links[i].dataset.sec); } catch (e) { /* storage blocked */ }
    if (typeof clampHints === 'function' && typeof page !== 'undefined' && page === 'settings') clampHints();
  };
  links.forEach((a, i) => a.addEventListener('click', (e) => {
    e.preventDefault();
    show(i);
    if (typeof page !== 'undefined' && page === 'settings') scrollTo({ top: 0 });
  }));
  let start = 0;
  try { const k = localStorage.getItem('outpost.settingsSec'); const i = links.findIndex((a) => a.dataset.sec === k); if (i >= 0) start = i; } catch (e) { /* storage blocked */ }
  show(start);
})();
async function loadSettings() {
  try { settings = await api('/api/settings'); } catch (e) { return; }
  $('set-key-state').innerHTML = settings.has_anthropic_key ? `<span class="st-done">· saved (${esc(settings.anthropic_key_hint)})</span>` : '<span class="muted">· not set</span>';
  $('set-or-key-state').innerHTML = settings.has_openrouter_key ? `<span class="st-done">· saved (${esc(settings.openrouter_key_hint)})</span>` : '<span class="muted">· not set</span>';
  $('set-brave-key-state').innerHTML = settings.has_brave_key ? `<span class="st-done">· saved (${esc(settings.brave_key_hint)})</span>` : '<span class="muted">· not set</span>';
  $('set-or-model').value = settings.openrouter_model || '';
  if (settings.has_openrouter_key) loadOpenRouterModels();
  $('set-hf-state').innerHTML = settings.has_hf_token ? '<span class="st-done">· saved</span>' : '<span class="muted">· not set</span>';
  $('set-model').innerHTML = claudeModelOptions(settings.claude_model);
  $('set-cap').value = settings.default_cap_usd;
  $('set-autoresume').checked = !!settings.auto_resume;
  $('set-bestofn').value = settings.best_of_n || 'hard';
  $('set-instr').value = settings.instructions || '';
  $('set-ghrepo').value = settings.github_repo || '';
  $('set-ghrepo').placeholder = settings.github_repo_default || 'https://github.com/64fedoras/outpost';
  $('set-ghkind').value = settings.update_host_kind || '';
  ghHttpNote();
  $('set-ghtoken').value = ''; $('set-ghtest-out').textContent = '';
  $('set-ghtoken-state').innerHTML = settings.github_token_set ? '<span class="st-done">· token set</span>' : '<span class="muted">· not set</span>';
  $('set-ghupd').checked = settings.github_updates !== false; $('set-ghauto').checked = !!settings.github_auto_install;
  $('set-idle').value = settings.idle_unload || '';
  if (document.activeElement !== $('set-health-amber')) $('set-health-amber').value = settings.health_amber || 5;
  if (document.activeElement !== $('set-health-red')) $('set-health-red').value = settings.health_red || 15;
  $('set-wake').checked = !!settings.wake_for_work;
  $('set-sandbox').value = settings.sandbox || 'basic';
  $('set-idle-now').textContent = settings.idle_unload_effective ? 'Nodes unload after: ' + settings.idle_unload_effective + (settings.idle_unload ? '' : ' (default)') : '';
  $('set-pooling').checked = !!settings.pooling_enabled;
  $('set-autofree').checked = settings.auto_free !== false;
  $('set-pooling-notice').classList.toggle('hidden', !settings.pooling_notice);
  $('set-agent').checked = settings.agent_mode !== false; $('set-websearch').checked = !!settings.web_search; $('set-chatweb').checked = !!settings.chat_web_search; $('set-chatsearchwith').value = settings.chat_search_with || ''; $('set-webprivate').checked = !!settings.web_allow_private;
  if (!state) state = await api('/api/state').catch(() => null);
  if (state) $('set-planner').innerHTML = plannerOptions(settings.default_planner);
  renderAutoOrder(settings.auto_order || ['local', 'claude', 'openrouter']);
  loadNotifySettings();
  loadMemory();
  loadSecurity();
  loadStorage();
  loadScorecard();
  Guard.settingsReset();
}
// Order of providers for the Auto (local + API) planner: ticked ones are tried top to bottom.
const AUTO_NAMES = { local: 'A ready node (local model)', claude: 'Claude', openrouter: 'OpenRouter' };
function renderAutoOrder(enabled) {
  const on = new Set(enabled), box = $('set-autoorder');
  const all = [...enabled, ...['local', 'claude', 'openrouter'].filter((k) => !on.has(k))];
  box.innerHTML = all.map((k, i) => `<div class="ao-row" data-ao-row="${k}"><label class="check"><input type="checkbox" data-ao="${k}" ${on.has(k) ? 'checked' : ''}> ${AUTO_NAMES[k]}</label>
    <button class="msg-tool shown" data-ao-move="-1" ${i ? '' : 'disabled'} aria-label="Move up" title="Move up">↑</button>
    <button class="msg-tool shown" data-ao-move="1" ${i < all.length - 1 ? '' : 'disabled'} aria-label="Move down" title="Move down">↓</button></div>`).join('');
}
// Full order as shown (ticked or not), and the ticked part of it.
const autoOrderRows = () => [...$('set-autoorder').querySelectorAll('[data-ao-row]')];
function autoOrderValue() { return autoOrderRows().filter((r) => r.querySelector('[data-ao]').checked).map((r) => r.dataset.aoRow); }
$('set-autoorder').addEventListener('click', (e) => {
  const b = e.target.closest('[data-ao-move]'); if (!b) return;
  const rows = autoOrderRows(), row = b.closest('[data-ao-row]'), i = rows.indexOf(row), j = i + +b.dataset.aoMove;
  if (j < 0 || j >= rows.length) return;
  const keys = rows.map((r) => r.dataset.aoRow), on = new Set(autoOrderValue());
  [keys[i], keys[j]] = [keys[j], keys[i]];
  // Unticked providers keep their place in the list but are not sent.
  renderAutoOrderKeys(keys, on);
});
function renderAutoOrderKeys(keys, on) {
  $('set-autoorder').innerHTML = keys.map((k, i) => `<div class="ao-row" data-ao-row="${k}"><label class="check"><input type="checkbox" data-ao="${k}" ${on.has(k) ? 'checked' : ''}> ${AUTO_NAMES[k]}</label>
    <button class="msg-tool shown" data-ao-move="-1" ${i ? '' : 'disabled'} aria-label="Move up" title="Move up">↑</button>
    <button class="msg-tool shown" data-ao-move="1" ${i < keys.length - 1 ? '' : 'disabled'} aria-label="Move down" title="Move down">↓</button></div>`).join('');
}
let orLoaded = false;
async function loadOpenRouterModels() {
  if (orLoaded) return;
  try {
    const r = await api('/api/openrouter/models');
    orLoaded = true;
    setHTML('or-models', r.models.map((m) => `<option value="${esc(m.id)}">${esc(m.name)} ($${m.in.toFixed(2)} in / $${m.out.toFixed(2)} out per M tokens)</option>`).join(''));
    $('set-or-model-state').textContent = `· ${r.models.length} models`;
  } catch (e) { $('set-or-model-state').textContent = '· ' + e.message; }
}
function loadNotifySettings() {
  const hour = settings.notify_digest_hour ?? 8;
  const hours = Array.from({ length: 24 }, (_, h) => `<option value="${h}" ${h === hour ? 'selected' : ''}>${String(h).padStart(2, '0')}:00</option>`).join('');
  setHTML('nf-events', (settings.notify_event_list || []).map(({ id, label }) => `<label class="check"><input type="checkbox" data-nf="${id}" ${settings.notify_events[id] ? 'checked' : ''}> ${esc(label)}${id === 'digest' ? ` at <select class="small" id="nf-digest-hour" aria-label="Summary time">${hours}</select> <span class="muted">(controller's clock)</span>` : ''}</label>`).join(''));
  $('nf-browser').checked = !!settings.notify_browser;
  $('nf-ntfy').value = settings.notify_ntfy_url || ''; $('nf-webhook').value = settings.notify_webhook_url || ''; $('nf-public').value = settings.notify_public_url || '';
  $('nf-ntfy-token-state').textContent = settings.notify_has_ntfy_token ? '· saved' : '· not set';
  $('nf-browser-state').textContent = 'Notification' in window ? (Notification.permission === 'denied' ? '· blocked in this browser' : '') : '· not supported here';
}
function notifyBody() {
  const events = {}; document.querySelectorAll('[data-nf]').forEach((b) => { events[b.dataset.nf] = b.checked; });
  const n = { browser: $('nf-browser').checked, ntfy_url: $('nf-ntfy').value, webhook_url: $('nf-webhook').value, public_url: $('nf-public').value, events };
  if ($('nf-digest-hour')) n.digest_hour = parseInt($('nf-digest-hour').value, 10);
  if ($('nf-ntfy-token').value.trim()) n.ntfy_token = $('nf-ntfy-token').value.trim();
  return n;
}
$('nf-save').onclick = async () => {
  if ($('nf-browser').checked && 'Notification' in window && Notification.permission === 'default') await Notification.requestPermission();
  const body = notifyBody(); $('nf-ntfy-token').value = '';
  saveSettings({ notify: body }, 'Notification settings saved');
};
$('nf-test').onclick = async () => {
  try { await post('/api/settings', { notify: notifyBody() }); $('nf-ntfy-token').value = ''; await post('/api/notifications/test'); toast('Test sent'); loadSettings(); } catch (e) { toast(e.message); }
};
// Desktop notifications: the controller keeps recent events; each tab asks for the ones after its last position.
// The pushed state carries the newest id (notify_last), so a tab on the stream asks only when it moves.
let notifyLast = null, notifyPushed;
async function pollNotifications() {
  if (notifyLast !== null && notifyPushed === notifyLast) return;
  try {
    const r = await api('/api/notifications' + (notifyLast === null ? '' : '?since=' + notifyLast));
    notifyLast = r.last;
    if (!r.notifications.length || !('Notification' in window) || Notification.permission !== 'granted') return;
    const s = settings || await api('/api/settings').catch(() => null);
    if (!s || !s.notify_browser) return;
    for (const n of r.notifications) {
      if (!s.notify_events[n.event]) continue;
      const x = new Notification(n.title, { body: n.message, tag: 'outpost-' + n.id });
      x.onclick = () => { window.focus(); if (n.task_id) location.hash = '#/tasks/' + n.task_id; else location.hash = '#/nodes'; x.close(); };
    }
  } catch (e) {}
}
async function loadSecurity() {
  try {
    const [ss, tk, tl] = await Promise.all([api('/api/sessions'), api('/api/apitokens'), api('/api/tls')]);
    const lk = settings ? settings.locked_ips || [] : [];
    $('sec-lock').textContent = settings ? `${settings.login_failures} failed sign-in${settings.login_failures === 1 ? '' : 's'} in the last 15 minutes.` + (lk.length ? ` Locked out: ${lk.join(', ')}.` : '') + ' After 10 failures an address is blocked for 15 minutes.' : '';
    setHTML('sessions-list', ss.map((s) => `<div class="rowline"><span>${esc(s.ip || 'unknown')} · ${esc((s.ua || '').slice(0, 60))} <span class="muted">· signed in ${ago(s.created)}</span>${s.current ? ' ' + pill('done', 'this browser') : ''}</span>${s.current ? '' : `<button class="danger" data-sess="${esc(s.id)}">Sign out</button>`}</div>`).join('') || '<p class="muted">None</p>');
    setHTML('tokens-list', tk.map((t) => `<div class="rowline"><span>${esc(t.name)}${t.scope === 'chat' ? ' ' + pill('ready', 'chat only') : t.scope === 'images' ? ' ' + pill('ready', 'chat and pictures') : t.scope === 'mcp' ? ' ' + pill('ready', 'MCP only') : ''} <span class="muted">· created ${ago(t.created)}${t.last_used && !t.last_used.startsWith('0001') ? ' · last used ' + ago(t.last_used) : ' · never used'}</span></span><button class="danger" data-tok="${esc(t.id)}">Revoke</button></div>`).join('') || '<p class="muted">No tokens yet.</p>');
    setHTML('agent-hint', 'To connect an editor, agent or Claude Code, create a token (tick Chat only) and see <b>Connect a tool</b> below.');
    connectRender();
    let info = `Fingerprint <code>${esc(tl.fingerprint)}</code>`;
    if (tl.subject) info += `<br>${tl.self_signed ? 'Self-signed' : 'Issued by ' + esc(tl.issuer)} for ${esc(tl.subject)}; expires ${new Date(tl.expires).toLocaleDateString()}.`;
    if ((tl.needs_repin || []).length) info += `<br><b>Offline during the change, run <code>outpost repin --fingerprint ${esc(tl.fingerprint)}</code> on: ${tl.needs_repin.map(esc).join(', ')}</b>`;
    setHTML('tls-info', info);
    if (settings) {
      $('bk-on').checked = !!settings.backup_enabled; $('bk-dir').value = settings.backup_dir || '';
      $('bk-pass-state').textContent = settings.has_backup_pass ? '· saved' : '· not set';
      const lb = settings.last_backup, el = $('bk-last');
      el.classList.toggle('hidden', !lb);
      if (lb) setHTML('bk-last', lb.verified
        ? `Last nightly copy <code>${esc(lb.file)}</code>, ${ago(lb.at)}: read back and verified (${lb.files} files).`
        : `<b class="st-failed">${lb.file ? `Last nightly copy <code>${esc(lb.file)}</code>` : 'The nightly backup'} failed ${ago(lb.at)}:</b> ${esc(lb.error)}. Check the folder and passphrase.`);
    }
  } catch (e) {}
}
$('sessions-list').onclick = async (e) => { const id = e.target.dataset.sess; if (id) { await api('/api/sessions/' + id, { method: 'DELETE' }).catch((x) => toast(x.message)); loadSecurity(); } };
$('tokens-list').onclick = async (e) => { const id = e.target.dataset.tok; if (id && await askConfirm('Revoke this token? Scripts using it stop working.')) { await api('/api/apitokens/' + id, { method: 'DELETE' }).catch((x) => toast(x.message)); loadSecurity(); } };
$('sess-all').onclick = async () => { if (await askConfirm('Sign out every browser, including this one?')) { await post('/api/sessions/logout-all'); location.reload(); } };
$('tok-create').onclick = async () => {
  const name = $('tok-name').value.trim(); if (!name) return;
  try {
    const img = $('tok-img').checked, chat = $('tok-chat').checked || img, mcp = $('tok-mcp').checked && !chat;
    const t = await post('/api/apitokens', { name, scope: img ? 'images' : chat ? 'chat' : mcp ? 'mcp' : '' }); $('tok-name').value = ''; $('tok-chat').checked = false; $('tok-img').checked = false; $('tok-mcp').checked = false;
    const n = $('tok-new'); n.classList.remove('hidden');
    setHTML('tok-new', `Copy this now, it is shown only once:<div class="codebox"><span id="tok-val">${esc(t.token)}</span><button id="tok-copy">Copy</button></div>${chat ? `<p class="hint u6">In your editor or chat app use the OpenAI provider with base URL <code>${esc(location.origin)}/v1</code>, this token as the API key, and model <code>auto</code> (or a model name from <code>/v1/models</code>). The controller's certificate is self-signed unless you replaced it, so the app may need to trust it.</p>` : ''}`);
    $('tok-copy').onclick = async () => { try { await navigator.clipboard.writeText(t.token); toast('Copied'); } catch (e) { toast('Select the token and press Ctrl+C'); } };
    loadSecurity();
  } catch (e) { toast(e.message); }
};
$('tls-save').onclick = async () => {
  if (!await askConfirm('Replace the HTTPS certificate? Connected nodes are re-pinned automatically; the page will reload.')) return;
  try { const r = await post('/api/tls', { cert: $('tls-cert').value, key: $('tls-key').value }); $('tls-cert').value = ''; $('tls-key').value = ''; toast((r.needs_repin || []).length ? 'Certificate changed; some nodes need a re-pin' : 'Certificate changed'); loadSecurity(); } catch (e) { toast(e.message); }
};
$('bk-download').onclick = async () => {
  const passphrase = $('bk-pass').value;
  if (passphrase.length < 8) return toast('The passphrase needs 8 or more characters');
  $('bk-download').disabled = true; toast('Building the backup…', 8000);
  try {
    const r = await fetch('/api/backup', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ passphrase, include_large: $('bk-large').checked }) });
    if (!r.ok) throw new Error((await r.json()).error || r.statusText);
    const a = document.createElement('a'); a.href = URL.createObjectURL(await r.blob());
    a.download = (r.headers.get('Content-Disposition') || '').split('filename=')[1]?.replace(/"/g, '') || 'outpost-backup.opb'; a.click(); $('bk-pass').value = ''; toast('Backup downloaded');
  } catch (e) { toast(e.message); } finally { $('bk-download').disabled = false; }
};
$('bk-sched-save').onclick = async () => {
  try { settings = await post('/api/backup/schedule', { enabled: $('bk-on').checked, dir: $('bk-dir').value, passphrase: $('bk-sched-pass').value }); $('bk-sched-pass').value = ''; toast('Schedule saved'); loadSecurity(); } catch (e) { toast(e.message); }
};
async function saveSettings(body, msg) {
  try { settings = await post('/api/settings', body); Guard.settingsSaved(); toast(msg); loadSettings(); refresh(); } catch (e) { toast(e.message); }
}
$('set-key-save').onclick = () => { const v = $('set-key').value.trim(); if (!v) return; $('set-key').value = ''; saveSettings({ anthropic_key: v }, 'API key saved'); };
$('set-key-clear').onclick = async () => { if (await askConfirm('Remove the Claude API key?')) saveSettings({ anthropic_key: '' }, 'API key removed'); };
$('set-or-key-save').onclick = () => { const v = $('set-or-key').value.trim(); if (!v) return; $('set-or-key').value = ''; orLoaded = false; saveSettings({ openrouter_key: v }, 'OpenRouter key saved'); };
$('set-or-key-clear').onclick = async () => { if (await askConfirm('Remove the OpenRouter API key?')) saveSettings({ openrouter_key: '' }, 'OpenRouter key removed'); };
$('set-brave-key-save').onclick = () => { const v = $('set-brave-key').value.trim(); if (!v) return; $('set-brave-key').value = ''; saveSettings({ brave_key: v }, 'Brave key saved'); };
$('set-brave-key-clear').onclick = async () => { if (await askConfirm('Remove the Brave Search API key?')) saveSettings({ brave_key: '' }, 'Brave key removed'); };
$('set-or-model-save').onclick = () => saveSettings({ openrouter_model: $('set-or-model').value.trim() }, 'OpenRouter model saved');
$('set-hf-save').onclick = () => { const v = $('set-hf').value.trim(); if (!v) return; $('set-hf').value = ''; saveSettings({ hf_token: v }, 'Token saved'); };
$('set-hf-clear').onclick = () => saveSettings({ hf_token: '' }, 'Token removed');
$('set-instr-save').onclick = () => saveSettings({ instructions: $('set-instr').value }, 'Instructions saved');
$('set-ghupd-save').onclick = async () => {
  const body = { github_updates: $('set-ghupd').checked, github_auto_install: $('set-ghauto').checked, github_repo: $('set-ghrepo').value.trim(), update_host_kind: $('set-ghkind').value };
  const norm = (u) => u.toLowerCase().replace(/^[a-z]+:\/\//, '').replace(/^www\./, '').replace(/(\.git)?\/*$/, '');
  const cur = settings.github_repo || settings.github_repo_default || '';
  if (body.github_repo && norm(body.github_repo) !== norm(cur) && !await askConfirm('Trust ' + body.github_repo + ' to supply updates for this controller and all nodes?' + (settings.github_token_set && !$('set-ghtoken').value.trim() ? ' If it is on another server, the saved access token is removed.' : ''))) return;
  const tok = $('set-ghtoken').value.trim();
  if (tok) body.github_token = tok;
  $('set-ghtoken').value = '';
  saveSettings(body, 'Update settings saved');
};
$('set-ghtoken-clear').onclick = () => saveSettings({ github_token: '' }, 'Access token removed');
function ghHttpNote() { $('set-ghrepo-http').classList.toggle('hidden', !/^\s*http:\/\//i.test($('set-ghrepo').value)); }
$('set-ghrepo').addEventListener('input', ghHttpNote);
$('set-ghtest').onclick = async () => {
  const out = $('set-ghtest-out'); out.className = 'hint'; out.textContent = 'Testing…';
  try {
    const r = await post('/api/github-updates/test', { repo: $('set-ghrepo').value.trim(), kind: $('set-ghkind').value, token: $('set-ghtoken').value.trim() });
    out.className = 'st-done'; out.textContent = r.message;
  } catch (e) { out.className = 'error-msg'; out.textContent = e.message; }
};
$('set-sandbox-save').onclick = () => saveSettings({ sandbox: $('set-sandbox').value }, 'Sandbox saved');
$('set-autofree-save').onclick = () => saveSettings({ auto_free: $('set-autofree').checked }, $('set-autofree').checked ? 'Automatic disk cleanup on' : 'Automatic disk cleanup off');
try { $('set-theme').value = localStorage.getItem('outpost.theme') || ''; } catch (e) {}
$('set-theme').addEventListener('change', () => {
  const v = $('set-theme').value;
  try { if (v) localStorage.setItem('outpost.theme', v); else localStorage.removeItem('outpost.theme'); } catch (e) {}
  if (v) document.documentElement.setAttribute('data-theme', v); else document.documentElement.removeAttribute('data-theme');
  toast(v ? `Theme: ${v}` : 'Theme follows the system');
});
try { $('set-chatkey').checked = chatCtrlSend(); } catch (e) {}
$('set-chatkey').addEventListener('change', () => { chatSendKeySet($('set-chatkey').checked); toast($('set-chatkey').checked ? 'Chat: Ctrl+Enter sends' : 'Chat: Enter sends'); });
try { $('set-detailed').checked = showDetailed; } catch (e) {}
$('set-detailed').addEventListener('change', () => {
  showDetailed = $('set-detailed').checked;
  try { localStorage.setItem('outpost.detailedList', showDetailed ? '1' : '0'); } catch (e) {}
  if (!showDetailed && nodeView === 'detail') setNodeView('list'); else if (state) renderNodes();
  toast(showDetailed ? 'Detailed list view shown on the Nodes page' : 'Detailed list view hidden');
});
$('set-wake').addEventListener('change', () => saveSettings({ wake_for_work: $('set-wake').checked }, $('set-wake').checked ? 'Nodes are woken for waiting tasks' : 'Automatic wake off'));
$('set-idle-save').onclick = () => saveSettings({ idle_unload: $('set-idle').value.trim() }, 'Idle unload saved');
$('set-pooling-save').onclick = () => saveSettings({ pooling_enabled: $('set-pooling').checked }, $('set-pooling').checked ? 'Pooling enabled' : 'Pooling off; pools dissolved');
$('set-claude-save').onclick = () => saveSettings({ claude_model: $('set-model').value, default_cap_usd: parseFloat($('set-cap').value) || 0 }, 'Claude model and cap saved');
$('set-save').onclick = () => saveSettings({ default_planner: $('set-planner').value, auto_order: autoOrderValue(), web_search: $('set-websearch').checked, chat_web_search: $('set-chatweb').checked, chat_search_with: $('set-chatsearchwith').value, web_allow_private: $('set-webprivate').checked }, 'Defaults saved');
$('set-run-save').onclick = () => saveSettings({ agent_mode: $('set-agent').checked, auto_resume: $('set-autoresume').checked, best_of_n: $('set-bestofn').value }, 'Task settings saved');
$('pw-form').onsubmit = async (e) => {
  e.preventDefault();
  const err = $('pw-err'); err.classList.add('hidden');
  if ($('pw-new').value !== $('pw-new2').value) { err.textContent = 'The new passwords do not match.'; err.classList.remove('hidden'); return; }
  try {
    await post('/api/password', { current: $('pw-cur').value, new: $('pw-new').value });
    ['pw-cur', 'pw-new', 'pw-new2'].forEach((id) => { $(id).value = ''; });
    toast('Password changed. Other browsers were signed out.', 4000);
  } catch (x) { err.textContent = x.message; err.classList.remove('hidden'); }
};

$('cfg-export').onclick = () => { location.href = '/api/config/export'; };
$('cfg-import').onclick = () => $('cfg-file').click();
$('cfg-file').onchange = async () => {
  const f = $('cfg-file').files[0]; $('cfg-file').value = '';
  if (!f) return;
  try {
    const body = JSON.parse(await f.text());
    if (!await askConfirm('Replace the settings, saved tasks and node labels in this file on this controller? Keys and passwords stay as they are.')) return;
    const r = await post('/api/config/import', body);
    toast(`Imported ${r.settings} settings, ${r.templates} saved tasks, ${r.labels} node labels`);
  } catch (e) { toast(e.message); }
};

$('set-health-save').onclick = () => {
  const a = parseInt($('set-health-amber').value, 10), r = parseInt($('set-health-red').value, 10);
  if (!(a >= 1) || !(r > a)) return toast('Red must be more points than amber, and amber at least 1');
  saveSettings({ health_amber: a, health_red: r }, 'Health colours saved');
};
$('set-health-default').onclick = () => saveSettings({ health_amber: 5, health_red: 15 }, 'Health colours reset to 5 and 15');

async function loadScorecard() {
  const r = await api('/api/scorecard').catch(() => null);
  const rows = (r && r.rows) || [];
  if (!rows.length) { setHTML('scorecard', '<span class="muted">No finished tasks counted yet.</span>'); return; }
  setHTML('scorecard', `<div class="table-wrap"><table><thead><tr><th>Model</th><th>Language</th><th>Subtasks</th><th>Clean</th><th>Retries</th><th>Reviewer changed</th></tr></thead><tbody>${rows.map((x) =>
    `<tr><td>${esc(x.model)}</td><td>${esc(x.language || '—')}</td><td>${x.subtasks}</td><td>${Math.round(100 * x.clean / x.subtasks)}%</td><td>${x.retries}</td><td>${x.review_fixed}</td></tr>`).join('')}</tbody></table></div>`);
}

// ---------- Settings → Connect a tool: snippets for the OpenAI, Anthropic and Ollama APIs ----------
let connectTab = 'openai';
function connectRender() {
  const o = esc(location.origin), box = (t) => `<div class="codebox"><span>${t}</span><button data-copy-text="${t.replace(/"/g, '&quot;')}">Copy</button></div>`;
  const tabs = {
    openai: `<p class="hint">For Continue, aider, Open WebUI, Hermes and most editors and agents.</p>
      <label>Base URL</label>${box(`${o}/v1`)}<label>API key</label><p class="hint">A token from above. Model: <code>auto</code>, or a name from <code>/v1/models</code>.</p>`,
    anthropic: `<p class="hint">For tools that only speak Claude's API, including Claude Code. Any <code>claude-…</code> model name means <code>auto</code>. Text, images, tool use and streaming are passed to the local model; thinking text is left out.</p>
      <label>Base URL</label>${box(`${o}/anthropic`)}
      <label>Claude Code (one line in a terminal)</label>${box(`ANTHROPIC_BASE_URL=${o}/anthropic ANTHROPIC_AUTH_TOKEN=your-token ANTHROPIC_MODEL=auto claude`)}
      <p class="hint">The certificate is self-signed unless you replaced it, so Node-based tools need <code>NODE_EXTRA_CA_CERTS=/path/to/outpost.crt</code> (download it from the browser's certificate viewer) or your own certificate. Claude Code sends long prompts and many tools: use the biggest model you have.</p>`,
    ollama: `<p class="hint">For apps that only know Ollama. They must be able to send an <code>Authorization: Bearer</code> header with the token. Pulling and deleting models is refused: use the Models page.</p>
      <label>Ollama URL</label>${box(`${o}/ollama`)}<p class="hint">Answers <code>/api/chat</code>, <code>/api/generate</code>, <code>/api/tags</code>, <code>/api/show</code> and <code>/api/version</code>.</p>`,
    mcp: `<p class="hint">Outpost as an MCP server: Claude Code, Claude Desktop and other MCP clients can start tasks on the cluster, answer their questions, check progress, read the files and ask a local model. Use a token with <b>MCP only</b> ticked.</p>
      <label>Claude Code (one line in a terminal)</label>${box(`claude mcp add --transport http outpost ${o}/mcp --header "Authorization: Bearer your-token"`)}
      <label>Claude Desktop and others (MCP config file)</label>${box(`{"mcpServers":{"outpost":{"type":"http","url":"${o}/mcp","headers":{"Authorization":"Bearer your-token"}}}}`)}
      <p class="hint">Tools: <code>start_task</code>, <code>task_status</code>, <code>answer_task</code>, <code>approve_plan</code>, <code>get_files</code>, <code>list_tasks</code>, <code>ask_local_model</code>, <code>cluster_status</code>. The certificate must be trusted (<code>NODE_EXTRA_CA_CERTS</code>, or your own certificate).</p>
      <label class="toggle"><input type="checkbox" id="ct-mcp-paid" ${settings && settings.mcp_allow_paid ? 'checked' : ''}> Let MCP start tasks with Claude or OpenRouter planners (off: local planners only)</label>
      <label class="toggle"><input type="checkbox" id="ct-mcp-approve" ${settings && settings.mcp_allow_approve ? 'checked' : ''}> Let MCP approve plans (off: you approve each plan here)</label>`,
  };
  document.querySelectorAll('#ct-tabs [data-ct]').forEach((b) => { b.classList.toggle('on', b.dataset.ct === connectTab); b.setAttribute('aria-selected', String(b.dataset.ct === connectTab)); });
  setHTML('ct-body', tabs[connectTab] || '');
  if (settings && document.activeElement !== $('ct-cors')) $('ct-cors').value = (settings.api_cors_origins || []).join('\n');
}
$('ct-tabs').addEventListener('click', (e) => { const b = e.target.closest('[data-ct]'); if (b) { connectTab = b.dataset.ct; connectRender(); } });
$('ct-body').addEventListener('change', (e) => {
  if (e.target.id === 'ct-mcp-paid') saveSettings({ mcp_allow_paid: e.target.checked }, e.target.checked ? 'MCP may use paid planners' : 'MCP uses local planners only');
  if (e.target.id === 'ct-mcp-approve') saveSettings({ mcp_allow_approve: e.target.checked }, e.target.checked ? 'MCP may approve plans' : 'You approve MCP plans');
});
$('ct-body').addEventListener('click', async (e) => { const b = e.target.closest('[data-copy-text]'); if (b) { await copyText(b.dataset.copyText); toast('Copied'); } });
$('ct-cors-save').onclick = () => saveSettings({ api_cors_origins: $('ct-cors').value.split(/\s+/).filter(Boolean) }, 'Origins saved');
