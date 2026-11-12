'use strict';
// Add node, run and job dialogs, command palette, modals and delegated click handlers. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- add node ----------
let addToken = null;
function renderAddCmds() {
  if (!addToken) return;
  const target = $('ssh-target').value.trim();
  const install = `sudo chmod +x ~/outpost-linux-amd64 && sudo ~/outpost-linux-amd64 install node --join ${addToken.token}`;
  $('cmd-linux').textContent = target ? `ssh -t ${target} '${install}'` : install;
}
$('ssh-target').addEventListener('input', () => {
  try { localStorage.setItem('outpost.sshTarget', $('ssh-target').value.trim()); } catch (e) {}
  renderAddCmds();
});
// The code stays the same until a node uses it (or it nearly expires); then a
// fresh one replaces it, even while the dialog is open.
async function freshAddToken() {
  if (addToken && addToken.id) {
    try {
      const st = await api(`/api/tokens/${addToken.id}`);
      const left = st.expires ? new Date(st.expires) - Date.now() : 0;
      if (st.status === 'valid' && left > 5 * 60 * 1000) return null;
      if (st.status === 'used') {
        addToken = await post('/api/tokens');
        return `${st.node || 'A node'} joined with the last code. This is a new one.`;
      }
    } catch (e) { if (e.message === 'login required') throw e; }
  }
  addToken = await post('/api/tokens');
  return null;
}
function showAddToken(note) {
  renderAddCmds();
  $('cmd-win').textContent = '.\\' + addToken.windows.replace(/^outpost\.exe/, 'outpost-windows-amd64.exe');
  $('cmd-mac').textContent = 'chmod +x ./outpost-darwin-arm64 && ./outpost-darwin-arm64 ' + addToken.macos.replace(/^outpost /, '');
  $('cmd-curl').textContent = addToken.curl_unix || '';
  $('cmd-curlwin').textContent = addToken.curl_win || '';
  const have = addToken.platforms || [];
  const missing = ['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64', 'windows-amd64'].filter((p) => !have.includes(p));
  const np = $('add-noplat');
  np.textContent = missing.length ? `The one-paste commands only work for platforms whose binary is uploaded in the Updates tab. Missing: ${missing.join(', ')}.` : '';
  np.classList.toggle('hidden', !missing.length);
  const n = $('add-note');
  n.textContent = note || ''; n.classList.toggle('hidden', !note);
}
let addTimer = null;
async function pollAddToken() {
  clearTimeout(addTimer);
  if ($('modal-add').classList.contains('hidden')) return;
  try {
    const before = addToken && addToken.id;
    const note = await freshAddToken();
    if (addToken.id !== before) showAddToken(note);
  } catch (e) {}
  addTimer = setTimeout(pollAddToken, 3000);
}
$('add-regen').onclick = async () => {
  try {
    addToken = await post('/api/tokens', { replace: addToken && addToken.id });
    showAddToken('New code made. The previous code no longer works.');
  } catch (e) { toast(e.message); }
};
$('btn-add').onclick = async () => {
  try {
    await freshAddToken();
    try { $('ssh-target').value = localStorage.getItem('outpost.sshTarget') || ''; } catch (e) {}
    showAddToken(null);
    $('add-url').textContent = state ? state.advertise : '';
    open('modal-add');
    addTimer = setTimeout(pollAddToken, 3000);
  } catch (e) { toast(e.message); }
};

// ---------- run + job modals ----------
function openRun(id) {
  runNodeId = id;
  $('run-node').textContent = nodeName(nodeById(id));
  $('run-err').classList.add('hidden');
  open('modal-run'); $('run-cmd').focus();
}
$('run-cmd').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('run-go').click(); });
$('run-go').onclick = async () => {
  $('run-err').classList.add('hidden');
  const body = { command: $('run-cmd').value, input_name: $('run-in-name').value, input_text: $('run-in-text').value,
    collect: $('run-collect').value, timeout_s: parseInt($('run-timeout').value, 10) || 600 };
  try { const r = await post(`/api/nodes/${runNodeId}/jobs`, body); close('modal-run'); openJob(r.job_id); refresh(); }
  catch (e) { $('run-err').textContent = e.message; $('run-err').classList.remove('hidden'); }
};
function openJob(id) { openJobId = id; $('job-out').innerHTML = ''; $('job-out')._h = null; open('modal-job'); pollJob(); }
async function pollJob() {
  if (!openJobId) return;
  const id = openJobId;
  let j;
  try { j = await api(`/api/jobs/${id}`); } catch (e) { return; }
  if (id !== openJobId) return;
  $('job-title').textContent = j.command;
  $('job-status').className = `pill st-${j.status}`; $('job-status').textContent = label(j.status);
  const dur = j.ended ? ((new Date(j.ended) - new Date(j.created)) / 1000).toFixed(1) + ' s' : 'running…';
  const inf = j.inference;
  $('job-kv').innerHTML = `<span>${j.kind === 'inference' ? 'model call' : 'command'} on <b>${esc(j.node_name)}</b></span><span>time <b>${dur}</b></span>${
    j.exec_output ? `<span>exit code <b>${j.exec_output.exit_code}</b></span>` : ''}${
    inf ? `<span>model <b>${esc(inf.model)}</b></span><span>tokens <b>${inf.prompt_tokens} in / ${inf.output_tokens} out</b></span><span>speed <b>${inf.tokens_per_s} tok/s</b></span>` : ''}`;
  const out = $('job-out'), atBottom = out.scrollTop + out.clientHeight >= out.scrollHeight - 20;
  let html = (j.chunks || []).map((c) => c.stream === 'stderr' ? `<span class="err">${esc(c.data)}</span>` : esc(c.data)).join('');
  if (!html && j.exec_output) html = esc(j.exec_output.stdout_tail) + (j.exec_output.stderr_tail ? `<span class="err">${esc(j.exec_output.stderr_tail)}</span>` : '');
  if (!html && inf) html = esc(inf.text);
  if (j.truncated) html += '\n<span class="err">[output truncated in the UI after 1 MB]</span>';
  setHTML(out, html || '<span class="u27">(no output yet)</span>');
  if (atBottom) out.scrollTop = out.scrollHeight;
  const err = j.result && j.result.error;
  $('job-err').classList.toggle('hidden', !err);
  if (err) $('job-err').textContent = `${err.code}: ${err.message}`;
  const files = (j.result && j.result.files) || [];
  $('job-files').innerHTML = files.length ? 'Collected: ' + files.map((f) =>
    `<a download="${esc(f.path.split('/').pop())}" href="data:application/octet-stream;base64,${f.inline_b64}">${esc(f.path)} (${bytes(f.bytes)})</a>`).join('') : '';
  $('job-cleaned').classList.toggle('hidden', !j.cleaned);
  if (j.cleaned) $('job-cleaned').textContent = `✓ Workspace deleted on the node (${bytes(j.cleaned.bytes_freed)} freed).`;
  const live = j.status === 'running' || j.status === 'dispatched';
  $('job-cancel').classList.toggle('hidden', !live);
  $('job-cancel').onclick = () => post(`/api/jobs/${id}/cancel`).then(() => toast('Cancel sent')).catch((e) => toast(e.message));
  const recent = j.ended && (Date.now() - new Date(j.ended)) < 15000;
  if (live || (j.kind === 'exec' && !j.cleaned && recent)) jobTimer = setTimeout(pollJob, live ? 500 : 1500);
}

// ---------- command palette (Ctrl+K) ----------
let palItems = [], palSel = 0;
function palSource() {
  const go = (h) => () => { location.hash = h; };
  const sec = (href) => () => { location.hash = '#/settings'; setTimeout(() => { const a = document.querySelector(`#set-nav a[href="${href}"]`); if (a) a.click(); }, 150); };
  const items = [
    { t: 'New task', k: 'page', run: go('#/new') }, { t: 'New image', k: 'page', run: go('#/image') }, { t: 'Chat', k: 'page', run: go('#/chat') },
    { t: 'New chat', k: 'action', run: () => $('chat-new').click() },
    { t: 'Projects', k: 'page', run: go('#/projects') }, { t: 'Nodes', k: 'page', run: go('#/nodes') },
    { t: 'Models', k: 'page', run: go('#/models') }, { t: 'Updates', k: 'page', run: go('#/updates') }, { t: 'Spending', k: 'page', run: go('#/spending') },
    { t: 'Settings', k: 'page', run: go('#/settings') },
    ...[...document.querySelectorAll('#set-nav a')].map((a) => ({ t: 'Settings: ' + a.textContent, k: 'page', run: sec(a.getAttribute('href')) })),
    { t: 'Check cluster', k: 'action', run: () => { location.hash = '#/nodes'; setTimeout(() => $('btn-check').click(), 150); } },
    { t: 'Add node', k: 'action', run: () => { location.hash = '#/nodes'; setTimeout(() => $('btn-add').click(), 150); } },
    { t: 'Theme: light', k: 'action', run: () => { $('set-theme').value = 'light'; $('set-theme').dispatchEvent(new Event('change')); } },
    { t: 'Theme: dark', k: 'action', run: () => { $('set-theme').value = 'dark'; $('set-theme').dispatchEvent(new Event('change')); } },
    { t: 'Theme: same as the system', k: 'action', run: () => { $('set-theme').value = ''; $('set-theme').dispatchEvent(new Event('change')); } },
  ];
  (state ? state.nodes : []).forEach((n) => items.push({ t: nodeName(n), k: 'node', sub: n.online ? (n.heartbeat && n.heartbeat.loaded_model) || 'online' : 'offline', run: () => { nodeSel = n.id; nodeOpen.add(n.id); nodeView = showDetailed ? 'detail' : 'list'; location.hash = '#/nodes'; renderNodes(); } }));
  (tasks || []).forEach((t) => items.push({ t: t.title || 'Task', k: 'task', sub: statusWord(t), run: go('#/tasks/' + t.id) }));
  (projects || []).forEach((p) => items.push({ t: p.name, k: 'project', run: go('#/projects') }));
  (state && state.templates || []).forEach((tp) => items.push({ t: 'Run saved task: ' + tp.name, k: 'action', run: () => templateAction('tpl-run', tp.id).catch((e) => toast(e.message)) }));
  return items;
}
function palRender() {
  const q = $('pal-q').value.trim().toLowerCase(), words = q.split(/\s+/).filter(Boolean);
  const all = palSource();
  palItems = (words.length ? all.filter((i) => { const h = (i.t + ' ' + i.k + ' ' + (i.sub || '')).toLowerCase(); return words.every((w) => h.includes(w)); })
    : all.filter((i) => i.k === 'page' && !i.t.startsWith('Settings:') || i.k === 'task').slice(0, 14)).slice(0, 40);
  palSel = Math.min(palSel, Math.max(0, palItems.length - 1));
  setHTML('pal-list', palItems.length ? palItems.map((i, n) => `<div class="pal-item${n === palSel ? ' on' : ''}" role="option" id="pal-${n}" aria-selected="${n === palSel}" data-pal="${n}"><span class="pal-k">${esc(i.k)}</span><span class="nv-grow nv-ell">${esc(i.t)}</span>${i.sub ? `<span class="sub">${esc(i.sub)}</span>` : ''}</div>`).join('') : '<div class="pal-empty muted">Nothing matches.</div>');
  $('pal-q').setAttribute('aria-activedescendant', palItems.length ? 'pal-' + palSel : '');
  const on = $('pal-list').querySelector('.on'); if (on) on.scrollIntoView({ block: 'nearest' });
}
function palOpen() { $('pal-q').value = ''; palSel = 0; palRender(); open('modal-palette'); $('pal-q').focus(); }
function palRun(n) { const i = palItems[n]; if (!i) return; close('modal-palette'); i.run(); }
$('pal-open').addEventListener('click', palOpen);
$('pal-q').addEventListener('input', () => { palSel = 0; palRender(); });
$('pal-q').addEventListener('keydown', (e) => {
  if (e.key === 'ArrowDown') { e.preventDefault(); palSel = Math.min(palSel + 1, palItems.length - 1); palRender(); }
  else if (e.key === 'ArrowUp') { e.preventDefault(); palSel = Math.max(palSel - 1, 0); palRender(); }
  else if (e.key === 'Enter') { e.preventDefault(); palRun(palSel); }
});
$('pal-list').addEventListener('click', (e) => { const el = e.target.closest('[data-pal]'); if (el) palRun(+el.dataset.pal); });
document.addEventListener('keydown', (e) => {
  if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'k' && !$('app').classList.contains('hidden')) {
    e.preventDefault();
    if ($('modal-palette').classList.contains('hidden')) palOpen(); else close('modal-palette');
  }
});

// ---------- modals + delegated events ----------
// Dialogs: marked as modal for screen readers, focus moves in (first field) and
// returns to what opened it on close, and Tab stays inside while one is open.
const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type=hidden]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
const modalReturn = {};
function open(id) {
  const ov = $(id), m = ov.querySelector('.modal') || ov;
  if (!m.hasAttribute('role') && !ov.hasAttribute('role')) { m.setAttribute('role', 'dialog'); m.setAttribute('aria-modal', 'true'); }
  const h = m.querySelector('h3, h2');
  if (h && !m.hasAttribute('aria-labelledby') && !ov.hasAttribute('aria-labelledby')) { if (!h.id) h.id = id + '-title'; m.setAttribute('aria-labelledby', h.id); }
  if (ov.classList.contains('hidden')) modalReturn[id] = document.activeElement;
  ov.classList.remove('hidden');
  if (!m.contains(document.activeElement)) {
    const f = [...m.querySelectorAll(FOCUSABLE)].find((x) => x.offsetParent !== null && !x.closest('.modal-foot'));
    if (!f && !m.hasAttribute('tabindex')) m.setAttribute('tabindex', '-1'); // no field: the dialog itself takes focus
    (f || m).focus({ preventScroll: true });
  }
}
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Tab') return;
  const ov = [...document.querySelectorAll('.overlay:not(.hidden)')].pop();
  if (!ov) return;
  const items = [...ov.querySelectorAll(FOCUSABLE)].filter((x) => x.offsetParent !== null);
  if (!items.length) return;
  const first = items[0], last = items[items.length - 1];
  if (!ov.contains(document.activeElement)) { e.preventDefault(); first.focus(); }
  else if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
});
function close(id) {
  const wasOpen = !$(id).classList.contains('hidden');
  $(id).classList.add('hidden');
  const back = modalReturn[id]; delete modalReturn[id];
  if (wasOpen && back && document.contains(back) && back.focus) back.focus({ preventScroll: true });
  if (id === 'modal-job') { clearTimeout(jobTimer); openJobId = null; }
  if (id === 'modal-logs') { clearTimeout(logsTimer); logsNodeId = null; }
  if (id === 'modal-check') clearTimeout(checkTimer);
}
document.querySelectorAll('.overlay').forEach((ov) => {
  ov.addEventListener('mousedown', (e) => { if (e.target === ov) close(ov.id); });
  ov.querySelectorAll('[data-close]').forEach((b) => { b.onclick = () => close(ov.id); });
});
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') document.querySelectorAll('.overlay:not(.hidden)').forEach((o) => close(o.id)); });
document.querySelectorAll('[data-copy]').forEach((b) => { b.onclick = async () => {
  try { await navigator.clipboard.writeText($(b.dataset.copy).textContent); toast('Copied'); }
  catch (e) { const r = document.createRange(); r.selectNodeContents($(b.dataset.copy)); getSelection().removeAllRanges(); getSelection().addRange(r); toast('Selected — press Ctrl+C'); }
}; });

document.addEventListener('change', async (e) => {
  const diff = e.target.closest('[data-diff]');
  if (diff) {
    try { await post(`/api/tasks/${pageArg}/difficulty`, { subtask: diff.dataset.diff, difficulty: diff.value }); } catch (err) { toast(err.message); }
    return;
  }
  const tier = e.target.closest('[data-tier]');
  if (tier) {
    try { await post(`/api/models/${encodeURIComponent(tier.dataset.tier)}/tier`, { tier: tier.value }); toast('Tier saved'); } catch (err) { toast(err.message); }
    return;
  }
  const pz = e.target.closest('[data-pause]');
  if (pz) { if (pz.value) pauseNode(pz.dataset.pause, pz.value); return; }
  const sb = e.target.closest('[data-sandbox]');
  if (sb) { setSandbox(sb.dataset.sandbox, sb.value); return; }
  const cb = e.target.closest('[data-exec]');
  if (cb) setExec(cb.dataset.exec, cb.checked);
});
// A difficulty picker lives inside a <summary>; clicking it must not toggle the details.
document.addEventListener('click', (e) => { if (e.target.closest('select[data-diff]')) e.preventDefault(); });
document.addEventListener('click', async (e) => {
  const row = e.target.closest('[data-href]');
  if (row && !e.target.closest('button, a')) { location.hash = row.dataset.href; return; }
  const el = e.target.closest('[data-act]');
  if (!el) return;
  const act = el.dataset.act, id = el.dataset.id;
  if (el.tagName === 'A') e.preventDefault();
  try {
    switch (act) {
      case 'open-job': openJob(id); break;
      case 'load': openLoad(id); break;
      case 'load-rec': await loadOnNode(id, el.dataset.name, el.dataset.lib !== '1'); break;
      case 'pick-model': openPick(el.dataset.name, el.dataset.catalog === '1'); break;
      case 'benchmark': await post(`/api/nodes/${id}/benchmark`); toast('Benchmark started'); break;
      case 'unload': await post(`/api/nodes/${id}/unload`); toast('Unloading'); break;
      case 'rm-node-model': if (await askConfirm(`Delete ${el.dataset.name} from this node's disk?`)) { await api(`/api/nodes/${id}/models/${encodeURIComponent(el.dataset.name)}`, { method: 'DELETE' }); toast('Deleting'); } break;
      case 'clean': openClean(id); break;
      case 'rename': renameNode(id); break;
      case 'logs': openLogs(id); break;
      case 'clear-pkg': if (await askConfirm(`Clear the package cache on ${nodeName(nodeById(id))}? The next tasks download their Go modules and npm packages again.`)) { await post(`/api/nodes/${id}/package-store/clear`); toast('Clearing the package cache'); } break;
      case 'node-resume': await pauseNode(id, 'resume'); break;
      case 'idle': setNodeIdle(id); break;
      case 'health-reset': resetHealth(id); break;
      case 'yield': toggleYield(id); break;
      case 'restart': {
        const n = nodeById(id);
        const jobs = n.running_jobs ? ` ${n.running_jobs} running job(s) will be cancelled.` : '';
        if (!await askConfirm(`Restart Outpost on ${nodeName(n)}?${jobs} Its model is loaded again afterwards.`)) return;
        await post(`/api/nodes/${id}/restart`); toast('Restarting'); break;
      }
      case 'remove': removeNode(id); break;
      case 'node-detail':
        // Grid "Details": the detailed list when enabled, else the node opened under its List row.
        if (showDetailed) { nodeSel = id; setNodeView('detail'); } else { nodeOpen.add(id); setNodeView('list'); }
        return;
      case 'node-toggle': if (nodeOpen.has(id)) nodeOpen.delete(id); else nodeOpen.add(id); renderNodes(); return;
      case 'node-select': nodeSel = id; renderNodes(); return;
      case 'node-view': setNodeView(el.dataset.nodeView); return;
      case 'cat-filter': catFilter = el.dataset.catFilter; renderModels(); return;
      case 'run': openRun(id); break;
      case 'catalog-add': await post('/api/models', { catalog_id: id, storage: el.dataset.storage }); toast(el.dataset.storage === 'direct' ? 'Added. Nodes download it when you load it.' : 'Downloading to the controller'); break;
      case 'model-load-fit': {
        const fits = ((models.fits || {})[el.dataset.name] || []).filter((f) => f.fit === 'vram' && f.can_load);
        const res = await Promise.allSettled(fits.map((f) => post(`/api/nodes/${f.node_id}/load`, { name: el.dataset.name })));
        const bad = res.filter((r) => r.status === 'rejected').length;
        toast(bad ? `Started on ${fits.length - bad} node(s); ${bad} failed to start` : `Loading on ${fits.map((f) => f.name).join(', ')}; progress shows on the node cards`); break;
      }
      case 'hf-add': { const g = hfGroups[+el.dataset.i]; await post('/api/models', { repo: hfRepo, files: g.files, mmproj: ($('hf-mmproj') || {}).value || '', storage: el.dataset.storage }); toast(el.dataset.storage === 'direct' ? 'Added. Nodes download it when you load it.' : 'Downloading to the controller'); break; }
      case 'model-store': await post(`/api/models/${encodeURIComponent(el.dataset.name)}/store`); toast('Downloading to the controller'); break;
      case 'model-release': if (await askConfirm(`Copy ${el.dataset.name} to a node, then delete it from the controller? It stays in the library; other nodes copy it from that node (or download it from Hugging Face if that node is gone).`)) { const r = await post(`/api/models/${encodeURIComponent(el.dataset.name)}/release`); toast(r && r.pushed ? `Copying to ${r.node}; the controller copy is freed when it arrives` : `Freed controller space; ${r && r.node} already has it`); } break;
      case 'model-unstore': if (await askConfirm(`Delete ${el.dataset.name}'s files from the controller? It stays in the library; nodes that need it download it from Hugging Face. Copies already on nodes stay.`)) { await post(`/api/models/${encodeURIComponent(el.dataset.name)}/unstore`); toast('Freed controller space'); } break;
      case 'model-retry': await post(`/api/models/${encodeURIComponent(el.dataset.name)}/retry`); toast('Resuming download'); break;
      case 'model-delete': if (await askConfirm(`Remove ${el.dataset.name} from the library (and its files from the controller, if stored there)? Copies already on nodes stay; delete those on the node card.`)) await api(`/api/models/${encodeURIComponent(el.dataset.name)}`, { method: 'DELETE' }); break;
      case 'install-tool': await post(`/api/nodes/${id}/toolchain`, { tool: el.dataset.tool }); toast('Downloading Go to the controller, then the node'); break;
      case 'node-update': await post(`/api/nodes/${id}/update`); toast('Update sent'); break;
      case 'ctl-update': await updateController(); return;
      case 'ctl-rollback': await rollbackController(); return;
      case 'node-rollback': {
        const n = nodeById(id);
        if (!await askConfirm(`Roll ${nodeName(n)} back to v${n.hello.rollback_to}? It restarts on that version and stays there until you update it again.`)) return;
        await post(`/api/nodes/${id}/rollback`); toast('Rolling back'); break;
      }
      case 'del-release': if (await askConfirm(`Delete the uploaded ${el.dataset.platform} release?`)) await api(`/api/releases/${el.dataset.platform}`, { method: 'DELETE' }); break;
      case 'view-file': viewFile(el.dataset.path); return;
      case 'tpl-run': case 'tpl-sched': case 'tpl-del': case 'tpl-use': await templateAction(act, id); return;
      case 'wake': await post(`/api/nodes/${id}/wake`, {}); toast('Wake packet sent. A node that wakes connects within a minute or two.'); return;
      case 'labels': editLabels(id); return;
      case 'hours': openHours(id); return;
      case 'label-filter': nodeLabel = el.dataset.label; renderNodes(); return;
      case 'starter': $('nt-prompt').value = STARTERS[+el.dataset.i][1]; $('nt-prompt').focus(); return;
      case 'project-delete': case 'project-save': case 'project-use': case 'project-suggest': await projectAction(act, el); break;
      default: if (act.startsWith('proj-')) { await projPageAction(act, el); return; }
        if (act.startsWith('task-')) { await taskAction(act, el); return; }
    }
  } catch (x) { toast(x.message, 4000); }
  refresh();
});
