'use strict';
const $ = (id) => document.getElementById(id);
let state = null, models = null, tasks = [], projects = [], settings = null, taskDetail = null;
let tasksSeq = 0; // bumped by local task edits so an older poll result is dropped
let ctxTaskId = null, ctxReturn = null, renamingId = null; // sidebar task menu and inline rename
let stateTimer = null, jobTimer = null, taskTimer = null, openJobId = null, loadNodeId = null, runNodeId = null;

// ---------- helpers ----------
async function api(path, opts = {}) {
  const r = await fetch(path, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, ...opts });
  let body = null;
  try { body = await r.json(); } catch (e) {}
  if (r.status === 401 && path !== '/api/login') { showLogin(); throw new Error('login required'); }
  if (!r.ok) throw new Error((body && (body.error || body.message)) || r.statusText);
  return body;
}
const post = (path, body) => api(path, { method: 'POST', body: JSON.stringify(body || {}) });
function toast(msg, ms = 2600) {
  const t = $('toast'); t.textContent = msg; t.classList.remove('hidden');
  clearTimeout(t._h); t._h = setTimeout(() => t.classList.add('hidden'), ms);
}
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
function bytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1)) + ' ' + u[i];
}
function usd(v) { v = v || 0; return '$' + (v >= 0.1 || v === 0 ? v.toFixed(2) : v.toFixed(3)); }
function ago(t) {
  if (!t) return '';
  const s = Math.max(0, (Date.now() - new Date(t)) / 1000);
  if (s < 60) return Math.floor(s) + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}
function clock(t) { return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }); }
function bar(pct, cls = '') {
  const p = Math.max(0, Math.min(100, pct || 0));
  return `<div class="bar"><i class="${cls || (p > 85 ? 'hot' : '')} w${Math.round(p)}"></i></div>`;
}
const label = (s) => String(s || '').replace(/_/g, ' ');
const pill = (s, text) => `<span class="pill st-${esc(s)}">${esc(text || label(s))}</span>`;
// setHTML only touches the DOM when content changed, so typing and focus survive polling.
function setHTML(el, html) { if (typeof el === 'string') el = $(el); if (el._h !== html) { el.innerHTML = html; el._h = html; } }
const nodeName = (n) => n.name || (n.hello && n.hello.hostname) || n.id;
const nodeById = (id) => state && state.nodes.find((n) => n.id === id);

// ---------- auth + routing ----------
function showLogin() {
  clearInterval(stateTimer); stateTimer = null;
  $('app').classList.add('hidden');
  // Before any password exists the same page offers first-run setup instead of sign-in.
  fetch('/api/setup', { credentials: 'same-origin' }).then((r) => r.json()).catch(() => ({ needed: false })).then((st) => {
    $('setup').classList.toggle('hidden', !st.needed); $('login').classList.toggle('hidden', !!st.needed);
    $('setup-blocked').classList.toggle('hidden', st.allowed !== false);
    $(st.needed ? 'su-new' : 'pw').focus();
  });
}
function showApp() {
  $('login').classList.add('hidden'); $('setup').classList.add('hidden'); $('app').classList.remove('hidden'); route();
  refresh(); if (!stateTimer) stateTimer = setInterval(refresh, 2000);
}
$('login-form').onsubmit = async (e) => {
  e.preventDefault(); $('login-err').classList.add('hidden');
  try { await post('/api/login', { password: $('pw').value }); $('pw').value = ''; showApp(); }
  catch (err) { $('login-err').textContent = err.message; $('login-err').classList.remove('hidden'); }
};
$('setup-form').onsubmit = async (e) => {
  e.preventDefault(); const err = $('setup-err'); err.classList.add('hidden');
  if ($('su-new').value !== $('su-new2').value) { err.textContent = 'The passwords do not match.'; err.classList.remove('hidden'); return; }
  try { await post('/api/setup', { password: $('su-new').value }); $('su-new').value = $('su-new2').value = ''; showApp(); }
  catch (x) { err.textContent = x.message; err.classList.remove('hidden'); }
};
$('btn-logout').onclick = async () => { await post('/api/logout').catch(() => {}); showLogin(); };

const PAGES = ['new', 'task', 'projects', 'nodes', 'models', 'updates', 'settings'];
let page = 'new', pageArg = null;
function route() {
  ntDraft.flush(); tmDraft.flush();
  const m = location.hash.match(/^#\/(\w+)(?:\/([\w-]+))?/) || [];
  page = m[1] === 'tasks' && m[2] ? 'task' : PAGES.includes(m[1]) ? m[1] : 'new';
  pageArg = page === 'task' ? m[2] : null;
  document.querySelectorAll('[data-page]').forEach((el) => el.classList.toggle('hidden', el.dataset.page !== page));
  document.querySelectorAll('.side-nav a, .side-projects').forEach((a) => a.classList.toggle('active', a.dataset.tab === page));
  document.querySelector('.page-plain').classList.toggle('hidden', !['projects', 'nodes', 'models', 'updates', 'settings'].includes(page));
  document.title = page === 'task' ? 'Outpost' : 'Outpost · ' + ({ new: 'New task' }[page] || page[0].toUpperCase() + page.slice(1));
  document.body.classList.toggle('on-task', page === 'task');
  closeSide();
  clearTimeout(taskTimer);
  liveClose();
  if (page === 'task') { taskDetail = null; threadReset(); tmDraft.restore('task:' + pageArg); autoGrow($('tm-text')); liveOpen(pageArg); pollTask(); }
  if (page === 'new') { ntDraft.restore('new'); setTimeout(() => $('nt-prompt').focus(), 0); }
  if (page === 'settings') loadSettings();
  renderSideTasks();
  if (state) refresh();
}
Guard.stamp();
window.addEventListener('hashchange', Guard.onHashChange);

// Sidebar on small screens
function openSide() { $('side').classList.add('open'); $('scrim').classList.remove('hidden'); }
function closeSide() { $('side').classList.remove('open'); $('scrim').classList.add('hidden'); }
$('side-open').onclick = openSide; $('task-menu').onclick = openSide; $('side-close').onclick = closeSide; $('scrim').onclick = closeSide;

// ---------- polling ----------
let failures = 0;
async function refresh() {
  try { state = await api('/api/state'); failures = 0; $('offline-banner').classList.add('hidden'); }
  catch (e) {
    if (e.message === 'login required') return;
    if (++failures >= 2) $('offline-banner').classList.remove('hidden');
    return;
  }
  const needModels = ['nodes', 'models', 'new'].includes(page) || !models;
  await Promise.all([
    needModels ? api('/api/models').then((m) => { models = m; }).catch(() => {}) : null,
    (() => { const seq = tasksSeq; return api('/api/tasks').then((t) => { if (seq === tasksSeq) tasks = t; }).catch(() => {}); })(), // a list fetched before a rename or pin must not undo it
    api('/api/projects').then((p) => { projects = p; }).catch(() => {}),
  ]);
  $('hdr-meta').textContent = `v${state.version}`;
  $('hdr-meta').title = state.advertise;
  $('src-link').href = `https://github.com/64fedoras/outpost/tree/v${state.version}`; // AGPL section 13: source of the running version
  $('ctl-info').textContent = `Controller ${state.advertise} · ${state.platform} · certificate SHA-256 ${state.fingerprint}`;
  pollNotifications();
  renderBadges();
  renderSideTasks();
  renderSideCluster();
  if (page === 'new') renderNew();
  if (page === 'projects') renderProjects();
  if (page === 'nodes') { renderNodes(); renderJobs(); }
  if (page === 'models') renderModels();
  if (page === 'updates') renderUpdates();
}

const ACTIVE = ['queued', 'planning', 'awaiting_answers', 'awaiting_approval', 'running', 'testing', 'building', 'fixing', 'paused_cap'];
const WORKING = ['planning', 'running', 'testing', 'building', 'fixing'];
function workerCount() { return state.nodes.filter((n) => n.online && n.heartbeat && n.heartbeat.inference_ready && !n.heartbeat.paused).length; }
function execCount() { return state.nodes.filter((n) => n.online && n.exec_enabled && !(n.heartbeat && n.heartbeat.paused)).length; }
function updatesAvailable() { return state.nodes.filter((n) => n.update_to).length + (state.controller_update_to ? 1 : 0); }

function renderBadges() {
  const offline = state.nodes.filter((n) => !n.online).length;
  const bn = $('badge-nodes'); bn.textContent = offline; bn.className = 'badge warn' + (offline ? '' : ' hidden'); bn.title = `${offline} offline`;
  const up = updatesAvailable();
  const bu = $('badge-updates'); bu.textContent = up; bu.className = 'badge' + (up ? '' : ' hidden');
}

// What a task's status means to the person, in a few words.
const STATUS_WORDS = {
  queued: 'Waiting for project', planning: 'Planning', awaiting_answers: 'Has questions', awaiting_approval: 'Plan ready', running: 'Working',
  testing: 'Testing', building: 'Building', fixing: 'Fixing tests', paused_cap: 'Paused at cap', interrupted: 'Interrupted',
  done: 'Done', failed: 'Failed', cancelled: 'Stopped',
};
function statusWord(t) {
  if (t.status === 'done' && t.test_status === 'failing') return 'Done, tests fail';
  return STATUS_WORDS[t.status] || label(t.status);
}
function taskTone(t) {
  if (t.needs_you) return 'needs';
  if (WORKING.includes(t.status)) return 'work';
  if (t.status === 'failed' || (t.status === 'done' && t.test_status === 'failing')) return 'bad';
  return 'idle';
}

function sideTaskHTML(t, projectName) {
  const cls = [page === 'task' && pageArg === t.id ? 'active' : '', ctxTaskId === t.id ? 'menu-open' : ''].join(' ');
  const title = renamingId === t.id
    ? `<input class="side-rename" id="side-rename" value="${esc(t.title)}" maxlength="120" aria-label="New name for this task">`
    : `<span class="side-task-title">${t.pinned ? '<span class="pin-mark" aria-label="Pinned">📌</span>' : ''}${esc(t.title)}</span>`;
  return `<div class="side-task ${cls}" data-task="${esc(t.id)}">
    <a href="#/tasks/${esc(t.id)}" class="side-task-link" aria-keyshortcuts="Shift+F10 F2 Delete">
      <span class="tdot ${taskTone(t)}" aria-hidden="true"></span>
      <span class="side-task-text">${title}
      <span class="side-task-meta ${t.needs_you ? 'needs' : ''}">${projectName ? esc(projectName) + ' · ' : ''}${esc(statusWord(t))}${t.round > 1 ? ` · request ${t.round}` : ''} · ${ago(t.updated)}</span></span></a>
    <button class="side-task-more" data-task-menu="${esc(t.id)}" aria-haspopup="menu" aria-expanded="${ctxTaskId === t.id}" aria-label="Actions for ${esc(t.title)}" title="Actions">⋯</button></div>`;
}
// Tasks are grouped by what the person needs to do: answer, watch, or nothing.
// Pinned tasks sit above all groups.
function renderSideTasks(force) {
  if (!tasks || (renamingId && !force)) return; // a rename in progress must survive polling
  if (!tasks.length) { setHTML('side-tasks', `<p class="side-empty">Tasks you start show up here.</p>`); return; }
  const names = {};
  (projects || []).forEach((p) => { names[p.id] = p.name; });
  const groups = [['Pinned', []], ['Needs you', []], ['Running', []], ['Earlier', []]];
  tasks.forEach((t) => {
    const tone = taskTone(t);
    groups[t.pinned ? 0 : tone === 'needs' ? 1 : ACTIVE.includes(t.status) ? 2 : 3][1].push(t);
  });
  setHTML('side-tasks', groups.filter(([, list]) => list.length).map(([name, list]) =>
    `<div class="side-group">${name}</div>` + list.map((t) => sideTaskHTML(t, names[t.project_id])).join('')).join(''));
}

// ---------- task context menu (sidebar) ----------
// Right-click a task, press its ⋯ button, or press Shift+F10 / the menu key on a
// focused task. F2 renames and Delete deletes the focused task directly.
const taskById = (id) => (tasks || []).find((t) => t.id === id);
const canStop = (t) => ACTIVE.includes(t.status) || t.status === 'interrupted';
const canRetry = (t) => t.status === 'failed' || t.status === 'interrupted';

function openTaskMenu(id, x, y, from) {
  const t = taskById(id);
  if (!t) return;
  closeTaskMenu(false);
  ctxTaskId = id; ctxReturn = from || null;
  const item = (act, text, extra = '') => `<button role="menuitem" tabindex="-1" data-ctx="${act}" ${extra}>${text}</button>`;
  const html = [
    item('open-tab', 'Open in new tab'),
    item('rename', 'Rename…<span class="kbd">F2</span>'),
    item('pin', t.pinned ? 'Unpin' : 'Pin to top'),
    '<hr>',
    item('copy-prompt', 'Copy prompt'),
    item('copy-link', 'Copy link'),
    item('duplicate', 'Duplicate as new task'),
    '<hr>',
    canStop(t) ? item('stop', 'Stop') : '',
    canRetry(t) ? item('retry', 'Try again') : '',
    item('delete', 'Delete…<span class="kbd">Del</span>', 'class="danger"'),
  ].join('');
  const m = $('task-ctx');
  m.innerHTML = html; m.setAttribute('aria-label', `Actions for ${t.title}`);
  m.classList.remove('hidden');
  const r = m.getBoundingClientRect(), pad = 6;
  m.style.left = Math.max(pad, Math.min(x, innerWidth - r.width - pad)) + 'px';
  m.style.top = Math.max(pad, Math.min(y, innerHeight - r.height - pad)) + 'px';
  renderSideTasks();
  m.querySelector('button').focus();
}
function closeTaskMenu(restoreFocus = true) {
  const m = $('task-ctx');
  if (!m || m.classList.contains('hidden')) return;
  m.classList.add('hidden'); m.innerHTML = '';
  ctxTaskId = null;
  renderSideTasks();
  if (restoreFocus && ctxReturn && document.body.contains(ctxReturn)) ctxReturn.focus();
  ctxReturn = null;
}

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); return; } catch (e) {}
  // Plain http or an older browser: fall back to a hidden textarea.
  const ta = document.createElement('textarea');
  ta.value = text; ta.setAttribute('readonly', ''); ta.className = 'offscreen';
  document.body.appendChild(ta); ta.select();
  const ok = document.execCommand('copy');
  ta.remove();
  if (!ok) throw new Error('the browser blocked copying');
}
const taskLink = (id) => location.origin + location.pathname + '#/tasks/' + id;

function startRename(id) {
  const t = taskById(id);
  if (!t) return;
  renamingId = id; renderSideTasks(true);
  if (!$('side-rename')) { renamingId = null; return; }
  const inp = $('side-rename');
  inp.focus(); inp.select();
  let done = false;
  const finish = async (save) => {
    if (done) return; done = true;
    const name = inp.value.trim();
    renamingId = null;
    if (save && name && name !== t.title) {
      try { const s = await post(`/api/tasks/${id}/meta`, { title: name }); tasksSeq++; Object.assign(t, s); if (taskDetail && taskDetail.id === id) { taskDetail.title = s.title; setHTML('tv-title', esc(s.title)); } }
      catch (e) { toast('Rename failed: ' + e.message); }
    }
    renderSideTasks();
    const link = document.querySelector(`.side-task[data-task="${CSS.escape(id)}"] .side-task-link`);
    if (link) link.focus();
  };
  inp.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); finish(false); }
  });
  inp.addEventListener('blur', () => finish(true));
  inp.addEventListener('click', (e) => { e.preventDefault(); e.stopPropagation(); });
}

async function deleteTask(id) {
  if (!confirm('Delete this task, its conversation and its files?')) return;
  try {
    await api(`/api/tasks/${id}`, { method: 'DELETE' }); Drafts.clear('task:' + id); Drafts.clearPrefix('ans:' + id + ':');
    tasksSeq++; tasks = tasks.filter((t) => t.id !== id); renderSideTasks();
    if (page === 'task' && pageArg === id) location.hash = '#/new';
    toast('Task deleted'); refresh();
  } catch (e) { toast('Delete failed: ' + e.message); }
}

async function runTaskMenu(act, id) {
  const t = taskById(id);
  closeTaskMenu(act !== 'rename');
  if (!t) return;
  try {
    switch (act) {
      case 'open-tab': window.open('#/tasks/' + id, '_blank', 'noopener'); break;
      case 'rename': startRename(id); break;
      case 'pin': Object.assign(t, await post(`/api/tasks/${id}/meta`, { pinned: !t.pinned })); tasksSeq++; renderSideTasks(); toast(t.pinned ? 'Pinned' : 'Unpinned'); break;
      case 'copy-prompt': { const d = await api(`/api/tasks/${id}`); await copyText(d.prompt || ''); toast('Prompt copied'); break; }
      case 'copy-link': await copyText(taskLink(id)); toast('Link copied'); break;
      case 'duplicate': {
        const d = await api(`/api/tasks/${id}`);
        const fill = () => { const box = $('nt-prompt'); box.value = d.prompt || ''; autoGrow(box); box.focus(); };
        if (page === 'new') fill();
        else { window.addEventListener('hashchange', fill, { once: true }); location.hash = '#/new'; }
        toast('Prompt copied into a new task; check the options and press Start');
        break;
      }
      case 'stop':
        if (!confirm('Stop this task? Files written so far are kept, and you can send a message later to continue.')) return;
        await post(`/api/tasks/${id}/cancel`); toast('Stopped'); refresh(); break;
      case 'retry': await post(`/api/tasks/${id}/resume`); toast('Trying again'); refresh(); break;
      case 'delete': await deleteTask(id); break;
    }
  } catch (e) { toast(e.message); }
}

(function wireTaskMenu() {
  const side = $('side-tasks'), m = $('task-ctx');
  side.addEventListener('contextmenu', (e) => {
    const row = e.target.closest('.side-task');
    if (!row || renamingId) return;
    e.preventDefault();
    const link = row.querySelector('.side-task-link');
    // A keyboard-opened menu (menu key, Shift+F10) has no pointer position.
    if (e.clientX || e.clientY) openTaskMenu(row.dataset.task, e.clientX, e.clientY, link);
    else { const r = row.getBoundingClientRect(); openTaskMenu(row.dataset.task, r.right - 24, r.top + 28, link); }
  });
  side.addEventListener('click', (e) => {
    const b = e.target.closest('[data-task-menu]');
    if (!b) return;
    e.preventDefault(); e.stopPropagation();
    if (ctxTaskId === b.dataset.taskMenu) { closeTaskMenu(); return; }
    const r = b.getBoundingClientRect();
    openTaskMenu(b.dataset.taskMenu, r.left, r.bottom + 4, b);
  });
  side.addEventListener('keydown', (e) => {
    if (renamingId || e.target.tagName === 'INPUT') return;
    const row = e.target.closest('.side-task');
    if (!row) return;
    if (e.key === 'F2') { e.preventDefault(); startRename(row.dataset.task); }
    else if (e.key === 'Delete' && e.target.classList.contains('side-task-link')) { e.preventDefault(); deleteTask(row.dataset.task); }
  });
  m.addEventListener('click', (e) => {
    const b = e.target.closest('[data-ctx]');
    if (b) runTaskMenu(b.dataset.ctx, ctxTaskId);
  });
  m.addEventListener('keydown', (e) => {
    const items = [...m.querySelectorAll('button')];
    const i = items.indexOf(document.activeElement);
    const go = (n) => { e.preventDefault(); items[(n + items.length) % items.length].focus(); };
    if (e.key === 'ArrowDown') go(i + 1);
    else if (e.key === 'ArrowUp') go(i - 1);
    else if (e.key === 'Home') go(0);
    else if (e.key === 'End') go(items.length - 1);
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeTaskMenu(); }
    else if (e.key === 'Tab') closeTaskMenu(false);
  });
  document.addEventListener('pointerdown', (e) => { if (ctxTaskId && !m.contains(e.target) && !e.target.closest('[data-task-menu]')) closeTaskMenu(false); });
  side.addEventListener('scroll', () => closeTaskMenu(false));
  window.addEventListener('resize', () => closeTaskMenu(false));
  window.addEventListener('hashchange', () => closeTaskMenu(false));
})();

function renderSideCluster() {
  const n = state.nodes, on = n.filter((x) => x.online).length, w = workerCount();
  setHTML('side-cluster', `<a href="#/nodes" class="cluster-link"><span class="tdot ${w ? 'work' : on ? 'needs' : 'idle'}"></span>${on} of ${n.length} node${n.length === 1 ? '' : 's'} online · ${w} model${w === 1 ? '' : 's'} ready</a>`);
}

function renderJobs() {
  const jobs = state.jobs.slice(0, 15);
  setHTML('jobs', !jobs.length ? `<div class="empty">No jobs yet.</div>` :
    `<div class="table-wrap"><table><thead><tr><th>Status</th><th>Node</th><th>What</th><th>Started</th></tr></thead><tbody>${
      jobs.map((j) => `<tr class="click" data-act="open-job" data-id="${j.id}"><td>${pill(j.status)}</td><td>${esc(j.node_name)}</td><td class="cmd">${esc(j.command)}</td><td>${ago(j.created)}</td></tr>`).join('')
    }</tbody></table></div>`);
}

// ---------- new task ----------
function plannerOptions(selected) {
  const opts = [];
  opts.push(`<option value="auto" ${selected === 'auto' ? 'selected' : ''}>Planner: Auto (best ready node)</option>`);
  opts.push(`<option value="claude" ${state.has_claude_key ? '' : 'disabled'} ${selected === 'claude' ? 'selected' : ''}>Planner: Claude${state.has_claude_key ? '' : ' (add an API key in Settings)'}</option>`);
  const orOk = state.has_openrouter_key && state.openrouter_model;
  opts.push(`<option value="openrouter" ${orOk ? '' : 'disabled'} ${selected === 'openrouter' ? 'selected' : ''}>Planner: ${state.has_openrouter_key ? (state.openrouter_model ? esc(state.openrouter_label) : 'OpenRouter (pick a model in Settings)') : 'OpenRouter (add an API key in Settings)'}</option>`);
  for (const n of state.nodes) {
    const hb = n.online && n.heartbeat, model = hb && hb.loaded_model;
    const v = 'node:' + n.id;
    opts.push(`<option value="${v}" ${selected === v ? 'selected' : ''}>Planner: ${esc(nodeName(n))}${model ? ' (' + esc(model) + ')' : ' (no model loaded)'}</option>`);
  }
  return opts.join('');
}
function claudeModelOptions(selected, short) {
  const list = (settings && settings.claude_models) || [{ id: 'claude-sonnet-5-5', label: 'Claude Sonnet 5.5', in: 2, out: 10 }, { id: 'claude-opus-5-5', label: 'Claude Opus 5.5', in: 4, out: 20 }, { id: 'claude-haiku-5-5', label: 'Claude Haiku 5.5', in: 0.1, out: 0.5 }];
  return list.map((m) => `<option value="${m.id}" ${m.id === selected ? 'selected' : ''} title="$${m.in} in / $${m.out} out per million tokens">${esc(short ? m.label.replace(/^Claude /, '') : m.label)}${short ? '' : ` ($${m.in} / $${m.out} per M tokens)`}</option>`).join('');
}
const STARTERS = [
  ['A command-line tool', 'A Python command-line tool that renames photos in a folder by the date they were taken (EXIF), with a dry-run flag and unit tests.'],
  ['A small web API', 'A small REST API in Go for a to-do list, stored in SQLite, with handler tests.'],
  ['A log summariser', 'A script that reads an nginx access log and prints the top 10 paths, status codes and client IPs, with tests on a sample log.'],
];
let ntInit = false;
let ntFiles = [];
function renderNew() {
  const sel = $('nt-planner');
  const cur = sel.value || state.default_planner || 'claude';
  setHTML(sel, plannerOptions(state.has_claude_key || cur !== 'claude' ? cur : (state.nodes[0] ? 'node:' + state.nodes[0].id : 'claude')));
  if (!ntInit) {
    ntInit = true;
    $('nt-cap').value = state.default_cap_usd || 2;
    if (!settings) api('/api/settings').then((s) => { settings = s; setHTML('nt-model', claudeModelOptions(state.claude_model, true)); renderOptLine(); }).catch(() => {});
    setHTML('nt-model', claudeModelOptions(state.claude_model, true));
    setHTML('nt-starters', STARTERS.map(([t, p], i) => `<button class="starter" data-act="starter" data-i="${i}"><b>${esc(t)}</b><span>${esc(p)}</span></button>`).join(''));
  }
  const psel = $('nt-project'), pcur = psel.value || localStorage.getItem('nt-project') || '';
  setHTML(psel, `<option value="">No project</option>` + projects.map((p) => `<option value="${p.id}" ${p.id === pcur ? 'selected' : ''}>Project: ${esc(p.name)}</option>`).join(''));
  $('nt-model').classList.toggle('hidden', sel.value !== 'claude');
  $('nt-cap-wrap').classList.toggle('hidden', sel.value !== 'claude' && sel.value !== 'openrouter');
  renderOptLine();
  const w = workerCount(), ex = execCount(), on = state.nodes.filter((n) => n.online).length;
  setHTML('new-cluster', state.nodes.length
    ? `${on} of ${state.nodes.length} nodes online, ${w} with a model ready${ex ? `, ${ex} can run tests` : '. No node can run tests yet (tick exec on the Nodes page)'}.`
    : 'No nodes yet. Add one on the Nodes page, then come back here.');
  const steps = [];
  if (!state.nodes.length) steps.push(`Add a node on the <a href="#/nodes">Nodes</a> page.`);
  if (models && !models.models.some((m) => m.status === 'ready')) steps.push(`Add a model on the <a href="#/models">Models</a> page.`);
  if (state.nodes.length && !w) steps.push(`Load a model on a node (<a href="#/nodes">Nodes</a> → Load model).`);
  if (!state.has_claude_key && !state.has_openrouter_key) steps.push(`Optional: add a Claude or OpenRouter API key in <a href="#/settings">Settings</a> to plan with a cloud model.`);
  if (!state.nodes.some((n) => n.exec_enabled)) steps.push(`Optional: tick <b>exec</b> on a node so tasks can run their tests.`);
  setHTML('home-setup', steps.length ? `<div class="setup"><b>Before your first task</b><ol>${steps.map((s) => `<li>${s}</li>`).join('')}</ol></div>` : '');
}
// One line that says what "Plan it" will use; the controls sit behind "Change options".
function renderOptLine() {
  const sel = $('nt-planner'), opt = sel.selectedOptions[0];
  const claude = sel.value === 'claude', paid = claude || sel.value === 'openrouter';
  const model = $('nt-model').selectedOptions[0];
  const parts = [claude ? 'Claude ' + (model ? model.textContent : '') : opt ? opt.textContent.replace(/^Planner: /, '') : 'no planner'];
  if (paid) parts.push('cap $' + ($('nt-cap').value || 0));
  parts.push($('nt-review').checked ? 'peer review on' : 'peer review off');
  if ($('nt-mockup').checked) parts.push('mockups first');
  setHTML('nt-optline', esc(parts.join(' · ')));
}
$('nt-opt-toggle').addEventListener('click', () => {
  const box = $('nt-opts'), show = box.classList.contains('hidden');
  box.classList.toggle('hidden', !show);
  $('nt-opt-toggle').setAttribute('aria-expanded', show);
  $('nt-opt-toggle').textContent = show ? 'Hide options' : 'Change options';
});
['nt-model', 'nt-cap', 'nt-review', 'nt-mockup'].forEach((id) => $(id).addEventListener('change', renderOptLine));
$('nt-cap').addEventListener('input', renderOptLine);
$('nt-planner').addEventListener('change', () => { renderNew(); ntImg.refresh(); });
$('nt-project').addEventListener('change', () => { try { localStorage.setItem('nt-project', $('nt-project').value); } catch (e) {} });
$('nt-files').addEventListener('change', () => {
  ntFiles = Array.from($('nt-files').files);
  $('nt-attach-n').textContent = ntFiles.length ? `${ntFiles.length} file${ntFiles.length === 1 ? '' : 's'}` : 'Attach';
});
async function startTask() {
  $('nt-err').classList.add('hidden');
  const body = { prompt: $('nt-prompt').value, planner: $('nt-planner').value, claude_model: $('nt-model').value, cap_usd: parseFloat($('nt-cap').value) || 0, review: $('nt-review').checked, mockup: $('nt-mockup').checked, project: $('nt-project').value, images: ntImg.payload() };
  if (!body.prompt.trim()) { $('nt-prompt').focus(); return; }
  $('nt-go').disabled = true;
  try {
    let r;
    if (ntFiles.length) {
      const fd = new FormData();
      fd.append('payload', JSON.stringify(body));
      ntFiles.forEach((f) => fd.append('files', f, f.name));
      const resp = await fetch('/api/tasks', { method: 'POST', credentials: 'same-origin', body: fd });
      r = await resp.json().catch(() => ({}));
      if (!resp.ok) throw new Error(r.error || resp.statusText);
    } else r = await post('/api/tasks', body);
    ntFiles = []; $('nt-files').value = ''; $('nt-attach-n').textContent = 'Attach';
    $('nt-prompt').value = ''; ntDraft.done(); ntImg.clear();
    location.hash = '#/tasks/' + r.id;
    refresh();
  } catch (e) { $('nt-err').textContent = e.message; $('nt-err').classList.remove('hidden'); }
  $('nt-go').disabled = false;
}
$('nt-go').onclick = startTask;
$('nt-prompt').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) startTask(); });

// ---------- projects ----------
function renderProjects() {
  setHTML('projects-list', !projects.length ? `<div class="empty">No projects yet. Tasks outside a project work as before.</div>` :
    projects.map((p) => `<div class="card u12">
      <div class="section-head u13"><b>${esc(p.name)}</b><span class="spacer"></span>
        <a href="/api/projects/${p.id}/download"><button>Download .zip</button></a>
        <button class="danger" data-act="project-delete" data-id="${p.id}">Delete</button></div>
      <p class="hint">${p.files} file${p.files === 1 ? '' : 's'} · ${p.tasks} task${p.tasks === 1 ? '' : 's'}${p.git_url ? ` · ${esc(p.git_url)} → branch <code>${esc(p.git_branch)}</code>${p.git_push ? ' (pushed after each round)' : ' (committed, not pushed)'}${p.last_push ? ' · last push ' + ago(p.last_push) : ''}` : ''}${p.last_pr ? ` · <a href="${esc(p.last_pr)}" target="_blank" rel="noopener">last pull request</a>` : ''}</p>
      <div class="btns u14">
        <label class="chip-input">tests <input data-pf="test_command" data-id="${p.id}" value="${esc(p.test_command)}" placeholder="none"></label>
        <label class="toggle"><input type="checkbox" data-pf="review" data-id="${p.id}" ${p.review === false ? '' : 'checked'}> peer review</label>
        ${p.git_url ? `<label class="toggle"><input type="checkbox" data-pf="git_push" data-id="${p.id}" ${p.git_push ? 'checked' : ''}> push</label>
        <label class="toggle"><input type="checkbox" data-pf="git_pr" data-id="${p.id}" ${p.git_pr ? 'checked' : ''}> open PR</label>` : ''}
        <button data-act="project-save" data-id="${p.id}">Save</button>
        <a href="#/new" data-act="project-use" data-id="${p.id}"><button class="primary">New task here</button></a></div></div>`).join(''));
}
$('np-go').onclick = async () => {
  const err = $('np-err'); err.classList.add('hidden');
  $('np-go').disabled = true; $('np-go').textContent = $('np-url').value.trim() ? 'Cloning…' : 'Creating…';
  try {
    await post('/api/projects', { name: $('np-name').value, git_url: $('np-url').value.trim(), git_branch: $('np-branch').value.trim(), git_token: $('np-token').value, git_ssh_key: $('np-key').value,
      test_command: $('np-test').value, git_push: $('np-push').checked, git_pr: $('np-pr').checked });
    ['np-name', 'np-url', 'np-branch', 'np-token', 'np-key', 'np-test'].forEach((i) => { $(i).value = ''; });
    $('np-push').checked = $('np-pr').checked = false;
    toast('Project created'); refresh();
  } catch (e) { err.textContent = e.message; err.classList.remove('hidden'); }
  $('np-go').disabled = false; $('np-go').textContent = 'Create project';
};
async function projectAction(act, el) {
  const id = el.dataset.id;
  if (act === 'project-delete') {
    if (!confirm('Delete this project and its files? Its git remote is not touched.')) return;
    await api(`/api/projects/${id}`, { method: 'DELETE' }); toast('Project deleted');
  } else if (act === 'project-save') {
    const f = (k) => document.querySelector(`[data-pf="${k}"][data-id="${id}"]`);
    const body = { test_command: f('test_command').value, review: f('review').checked };
    if (f('git_push')) { body.git_push = f('git_push').checked; body.git_pr = f('git_pr').checked; }
    await post(`/api/projects/${id}`, body); toast('Saved');
  } else if (act === 'project-use') {
    try { localStorage.setItem('nt-project', id); } catch (e) {}
    $('nt-project').value = id;
  }
}

// ---------- task thread ----------
async function pollTask() {
  clearTimeout(taskTimer);
  if (page !== 'task') return;
  const id = pageArg;
  try { taskDetail = await api('/api/tasks/' + id); }
  catch (e) { setHTML('tv-title', esc(e.message)); return; }
  if (id !== pageArg) return;
  await loadRounds();
  if (id !== pageArg) return;
  renderTask(taskDetail);
  // With the live stream open, "changed" events trigger refreshes; polling is only the fallback.
  const live = liveES && liveES.readyState === 1;
  taskTimer = setTimeout(pollTask, live ? 15000 : WORKING.includes(taskDetail.status) ? 1500 : 4000);
}

// ---------- live output (server-sent events) ----------
let liveES = null, liveCalls = {}, liveKick = null;
const liveExpanded = new Set();   // subtask ids whose live pane the user expanded (panes start collapsed)
function liveClose() {
  if (liveES) liveES.close();
  liveES = null; liveCalls = {}; clearTimeout(liveKick);
}
function liveOpen(id) {
  if (!window.EventSource) return;
  liveES = new EventSource(`/api/tasks/${id}/events`);
  liveES.onmessage = (e) => {
    let ev; try { ev = JSON.parse(e.data); } catch (_) { return; }
    if (ev.t === 'start') liveCalls[ev.call] = { sub: ev.sub || '', label: ev.label, node: ev.node, text: ev.text || '', tok: ev.tok || 0, tps: ev.tps || 0, est: !!ev.est };
    else if (ev.t === 'reset' && liveCalls[ev.call]) Object.assign(liveCalls[ev.call], { text: '', tok: 0, tps: 0 });
    else if (ev.t === 'end') delete liveCalls[ev.call];
    else if (ev.t === 'delta' && liveCalls[ev.call]) {
      const c = liveCalls[ev.call];
      c.text = (c.text + ev.text).slice(-LIVE_MAX); c.tok = ev.tok; c.tps = ev.tps;
      const pre = document.querySelector(`[data-live="${ev.call}"]`);
      if (pre) { setLiveText(pre, c); const m = pre.parentNode.querySelector('.live-rate'); if (m) m.textContent = liveRate(c); return; }
    }
    // Anything but a delta for an already visible pane changes the layout: re-render soon.
    clearTimeout(liveKick);
    liveKick = setTimeout(() => { if (page === 'task') pollTask(); }, ev.t === 'changed' ? 250 : 50);
  };
}
const LIVE_MAX = 8000;   // characters shown per call; the controller keeps 64 KB
function liveRate(c) { return c.tps ? `${c.est ? '~' : ''}${c.tps} tok/s` : ''; }
function setLiveText(pre, c) {
  const stick = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
  pre.textContent = c.text;
  if (stick) pre.scrollTop = pre.scrollHeight;
}
function livePane(c, id) {
  const open = liveExpanded.has(c.sub || id);
  return `<details class="live" data-sub="${esc(c.sub || id)}" ${open ? 'open' : ''}><summary>live output${c.node ? ` · ${esc(c.node)}${/ review /.test(c.label || '') ? ' checking' : ''}` : ''} <span class="live-rate">${esc(liveRate(c))}</span></summary><pre class="live-text" data-live="${esc(id)}">${esc(c.text)}</pre></details>`;
}
document.addEventListener('toggle', (e) => {
  const d = e.target;
  if (d.classList && d.classList.contains('live')) d.open ? liveExpanded.add(d.dataset.sub) : liveExpanded.delete(d.dataset.sub);
}, true);

function threadReset() {
  editing = null;
  const th = $('thread'); th.innerHTML = ''; th._items = {};
  $('tm-text').value = '';
  ['tv-title', 'tv-status', 'tv-spend'].forEach((id) => { $(id).innerHTML = ''; $(id)._h = null; });
}

// Keyed rendering: each thread item is only rebuilt when its HTML changes,
// so choices, typed text and focus inside a card survive polling.
function renderThreadItems(items) {
  const th = $('thread');
  th._items = th._items || {};
  const nearBottom = th.scrollHeight - th.scrollTop - th.clientHeight < 140;
  const keep = new Set(items.map((i) => i.key));
  for (const k of Object.keys(th._items)) if (!keep.has(k)) { th._items[k].remove(); delete th._items[k]; }
  let prev = null, grew = false;
  for (const it of items) {
    let el = th._items[it.key];
    if (!el) { el = document.createElement('div'); th._items[it.key] = el; grew = true; }
    if (el._h !== it.html) { el.className = 'item ' + it.cls; el.innerHTML = it.html; el._h = it.html; grew = true; }
    const want = prev ? prev.nextSibling : th.firstChild;
    if (want !== el) th.insertBefore(el, want);
    prev = el;
  }
  if (grew && (nearBottom || !th._scrolled)) { th.scrollTop = th.scrollHeight; th._scrolled = true; }
  restoreAnswerDrafts();
}

// Answer boxes ("Something else…" and an agent's question) keep what was typed, per task and question.
function answerKey(el) {
  const card = el.closest('.card-msg'), btn = card && card.querySelector('button[data-msg]');
  if (!btn || !pageArg) return '';
  const q = el.closest('fieldset.q');
  return `ans:${pageArg}:${btn.dataset.msg}:${q ? q.dataset.q : 'aq'}`;
}
function restoreAnswerDrafts() {
  document.querySelectorAll('#thread .opt-other, #thread input[id^="aq-"]').forEach((el) => {
    if (el.value || el.disabled) return;
    const k = answerKey(el), d = k && Drafts.get(k);
    if (d) el.value = d;
  });
}
$('thread').addEventListener('input', (e) => {
  if (!e.target.matches('.opt-other, input[id^="aq-"]')) return;
  const k = answerKey(e.target); if (k) Drafts.set(k, e.target.value);
});

function renderTask(t) {
  setHTML('tv-title', esc(t.title || firstWords(t.prompt)));
  setHTML('tv-status', `<span class="tdot ${taskTone({ ...t, needs_you: ['awaiting_answers', 'awaiting_approval', 'paused_cap', 'interrupted'].includes(t.status) || (t.messages || []).some((m) => m.pending) })}"></span>${esc(statusWord(t))}`);
  $('tv-status').className = 'status-chip';
  setHTML('tv-spend', t.planner === 'claude' || t.planner === 'openrouter' || t.cost_usd > 0 ? spendMeter(t) : '');
  const files = t.files || [];
  $('tv-files-btn').classList.toggle('hidden', !files.length);
  $('tv-download').classList.toggle('hidden', !files.length || !['done', 'cancelled', 'failed'].includes(t.status));
  $('tv-download').href = `/api/tasks/${t.id}/download`;
  $('tv-stop').classList.toggle('hidden', !ACTIVE.includes(t.status));
  $('tv-delete').classList.toggle('hidden', ACTIVE.includes(t.status));
  renderThreadItems(threadItems(t));
  renderComposer(t);
  if (!$('modal-activity').classList.contains('hidden')) renderActivity(t);
  if (!$('modal-files').classList.contains('hidden')) renderFiles(t);
}
const firstWords = (s) => { s = String(s || '').trim().split('\n')[0]; return s.length > 70 ? s.slice(0, 70) + '…' : s; };

function spendMeter(t) {
  const pct = t.cap_usd ? Math.min(100, 100 * t.cost_usd / t.cap_usd) : 0;
  return `<span class="spend" title="Cloud model spend on this task: ${usd(t.cost_usd)} of a ${usd(t.cap_usd)} cap">${usd(t.cost_usd)}<span class="spend-bar"><i class="${pct >= 100 ? 'bad' : pct > 80 ? 'hot' : ''} w${Math.round(pct)}"></i></span>${usd(t.cap_usd)}</span>`;
}

// The conversation, plus live cards for the current round.
function threadItems(t) {
  const items = [];
  const msgs = t.messages || [];
  const round = t.round || 1;
  const lastIdx = (kind) => { for (let i = msgs.length - 1; i >= 0; i--) if (msgs[i].kind === kind && !msgs[i].superseded) return i; return -1; };
  const lastQ = lastIdx('questions'), lastPlan = lastIdx('plan');
  const started = t.approved && ['running', 'testing', 'building', 'fixing', 'paused_cap', 'done', 'failed', 'cancelled', 'interrupted'].includes(t.status) && t.subtasks && t.subtasks.length;
  let progressPlaced = false;
  msgs.forEach((m, i) => {
    // The live progress card sits right after this round's approval.
    const approvedHere = started && !progressPlaced && m.round === round && m.kind === 'event' && /approved the (edited )?plan/.test(m.text || '');
    items.push(msgItem(t, m, i === lastQ && t.status === 'awaiting_answers', i === lastPlan && t.status === 'awaiting_approval'));
    if (approvedHere) { items.push({ key: 'progress-' + round, cls: 'card-row', html: progressCard(t) }); progressPlaced = true; }
  });
  if (started && !progressPlaced) items.push({ key: 'progress-' + round, cls: 'card-row', html: progressCard(t) });
  // Planner and other calls that belong to no subtask.
  const loose = Object.entries(liveCalls).filter(([, c]) => !c.sub);
  if (loose.length) items.push({ key: 'live-loose', cls: 'card-row', html: `<div class="card-msg quiet"><div class="card-head">${loose.map(([, c]) => esc(c.node ? `Writing on ${c.node}` : 'Working')).join(' · ')}</div>${loose.map(([id, c]) => livePane(c, id)).join('')}</div>` });
  const tail = statusCard(t);
  if (tail) items.push(tail);
  return items;
}

// Editing a sent message (see tasks_edit.go for the rules the controller enforces).
let editing = null;   // { id, text } while a message is open in the editor
const PENCIL = '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M11.5 2.5l2 2L5 13l-3 1 1-3z"/></svg>';
function canEditMsg(t, m) {
  if (m.role !== 'user' || m.superseded || (m.kind !== 'text' && m.kind !== 'answers')) return false;
  const live = (t.messages || []).filter((x) => x.round === m.round && x.role === 'user' && !x.superseded && (x.kind === 'text' || x.kind === 'answers'));
  if (t.status === 'awaiting_answers' || t.status === 'awaiting_approval') return m.round === (t.round || 1) && !t.approved;
  if (['done', 'failed', 'cancelled', 'interrupted'].includes(t.status)) return !!live[0] && live[0].id === m.id;
  return false;
}

function msgItem(t, m, liveQuestions, livePlan) {
  const key = m.id;
  if (m.superseded) return supersededItem(t, m);
  if (m.role === 'user') {
    const tag = m.kind === 'agent_answer' ? '<span class="bubble-tag">answer to the agent</span>' : m.kind === 'steer' ? '<span class="bubble-tag">note while working</span>' : m.kind === 'answers' ? '<span class="bubble-tag">answers</span>' : '';
    const pics = (m.images || []).length ? `<div class="bubble-imgs">${m.images.map((n) => `<a href="/api/tasks/${t.id}/image?name=${encodeURIComponent(n)}" target="_blank" rel="noopener"><img src="/api/tasks/${t.id}/image?name=${encodeURIComponent(n)}" alt="attached image" loading="lazy"></a>`).join('')}</div>` : '';
    if (editing && editing.id === m.id && canEditMsg(t, m)) {
      const rerun = ['done', 'failed', 'cancelled', 'interrupted'].includes(t.status);
      return { key, cls: 'from-user', html: `<div class="bubble-edit"><textarea id="msg-edit-text" rows="4">${esc(editing.text)}</textarea>
        <div class="bubble-edit-actions"><span class="hint">${rerun ? 'Saving puts back the files from this request on and plans again. The old answer stays in the thread, dimmed.' : 'Saving plans again from here. Later messages are kept, dimmed.'}</span>
        <button data-act="task-msg-cancel">Cancel</button><button class="primary" data-act="task-msg-save" data-msg="${esc(m.id)}">Save and plan</button></div></div>` };
    }
    const edited = m.edited ? '<span class="edited-mark">edited</span>' : '';
    const btn = canEditMsg(t, m) ? `<button class="msg-tool" data-act="task-msg-edit" data-msg="${esc(m.id)}" title="Edit message" aria-label="Edit message">${PENCIL}</button>` : '';
    return { key, cls: 'from-user', html: `<div class="bubble-row">${btn}<div class="bubble">${tag}${esc(m.text)}${pics}${edited}</div></div>` };
  }
  if (m.role === 'event' || m.kind === 'event') return { key, cls: 'event-row', html: `<span>${esc(m.text)}</span>` };
  switch (m.kind) {
    case 'questions': return { key, cls: 'card-row', html: questionsCard(m, liveQuestions) };
    case 'agent_question': return { key, cls: 'card-row', html: agentQuestionCard(m) };
    case 'plan': return { key, cls: 'card-row', html: planCard(t, m, livePlan) };
    case 'result': return { key, cls: 'card-row', html: resultCard(t, m) };
    case 'shots': case 'mockup': return { key, cls: 'card-row', html: shotsCard(t, m, msgs_lastMockup(t) === m.id) };
    case 'error': return { key, cls: 'card-row', html: `<div class="card-msg bad"><div class="card-head">Something went wrong</div><p>${esc(m.text)}</p>${t.status === 'failed' && m.round === (t.round || 1) ? '<div class="card-actions"><button class="primary" data-act="task-resume">Try again</button><span class="hint">or send a message to change course</span></div>' : ''}</div>` };
    default: return { key, cls: 'from-planner', html: `<div class="said">${esc(m.text)}</div>` };
  }
}

// The old branch after an edit: kept in the thread, dimmed and short.
function supersededItem(t, m) {
  let what;
  if (m.role === 'user') what = `You: ${esc(firstWords(m.text))}`;
  else if (m.kind === 'plan') what = `Plan: ${esc((m.plan && m.plan.title) || '')}`;
  else if (m.kind === 'questions') what = 'Questions from the planner';
  else if (m.kind === 'result') what = `Result: ${esc(firstWords(m.text))}`;
  else what = esc(firstWords(m.text) || m.kind);
  return { key: m.id, cls: 'event-row superseded', html: `<span>${what}</span>${m.role === 'user' || m.kind === 'plan' ? ' <span class="muted">· replaced by an edit</span>' : ''}` };
}

function msgs_lastMockup(t) {
  const ms = (t.messages || []).filter((m) => m.kind === 'mockup');
  return ms.length ? ms[ms.length - 1].id : '';
}

// Screenshots of what a round built; for a mockup, one column per variant with a Pick button.
function shotsCard(t, m, pickable) {
  const mock = m.kind === 'mockup';
  const img = (n, cls) => `<a href="/api/tasks/${t.id}/shot?name=${encodeURIComponent(n)}" target="_blank" rel="noopener"><img class="${cls}" src="/api/tasks/${t.id}/shot?name=${encodeURIComponent(n)}" alt="screenshot" loading="lazy"></a>`;
  const cols = (m.shots || []).map((s) => `<figure class="shot"><figcaption><b>${esc(mock ? 'Variant ' + s.label.toUpperCase() : s.page)}</b>${mock ? ` <span class="muted mono">${esc(s.page)}</span>` : ''}</figcaption>
    <div class="shot-pair">${img(s.desktop, 'shot-desktop')}${img(s.phone, 'shot-phone')}</div>
    ${mock && pickable && t.mockup && t.status === 'done' ? `<button class="primary" data-act="task-pick" data-page="${esc(s.page)}">Build this one</button>` : ''}</figure>`).join('');
  return `<div class="card-msg shots"><div class="card-head">${mock ? 'Mockup variants' : 'Screenshots'}</div><p>${esc(m.text)}</p><div class="shot-grid">${cols}</div></div>`;
}

function questionsCard(m, live) {
  if (!live) {
    return `<div class="card-msg quiet"><div class="card-head">The planner asked</div><ul class="asked">${(m.questions || []).map((q) => `<li>${esc(q.question)}</li>`).join('')}</ul></div>`;
  }
  const qs = (m.questions || []).map((q) => {
    const type = q.multi ? 'checkbox' : 'radio';
    return `<fieldset class="q" data-q="${esc(q.id)}" ${live ? '' : 'disabled'}><legend>${esc(q.question)}</legend>
      <div class="opts">${(q.options || []).map((o) => `<label class="opt"><input type="${type}" name="${esc(m.id + q.id)}" value="${esc(o)}"><span>${esc(o)}</span></label>`).join('')}
      ${live ? `<input class="opt-other" placeholder="Something else…" aria-label="Your own answer to: ${esc(q.question)}">` : ''}</div></fieldset>`;
  }).join('');
  return `<div class="card-msg"><div class="card-head">A few questions before planning</div>${m.text ? `<p>${esc(m.text)}</p>` : ''}${qs}
    ${live ? `<div class="card-actions"><button class="primary" data-act="task-answer" data-msg="${esc(m.id)}">Send answers</button><span class="hint">Skip any you don't mind; the planner picks a sensible default.</span></div>` : ''}</div>`;
}

// A question an agent asked while working; the answer goes straight back to it.
function agentQuestionCard(m) {
  const q = (m.questions || [])[0] || { question: m.text, options: [] };
  if (!m.pending) return `<div class="card-msg quiet"><div class="card-head">A worker asked</div><p>${esc(q.question)}</p></div>`;
  const opts = (q.options || []).map((o) => `<button class="small" data-act="task-agent-answer" data-msg="${esc(m.id)}" data-answer="${esc(o)}">${esc(o)}</button>`).join('');
  return `<div class="card-msg"><div class="card-head">A worker needs a decision</div><p>${esc(q.question)}</p>
    ${opts ? `<div class="card-actions">${opts}</div>` : ''}
    <div class="card-actions"><input class="opt-other" id="aq-${esc(m.id)}" placeholder="Your answer…" aria-label="Your answer"><button class="primary" data-act="task-agent-answer" data-msg="${esc(m.id)}">Send answer</button><span class="hint">Only that worker waits; the rest keeps going.</span></div></div>`;
}

// Editing the plan (server rules: tasks_rerun.go). planEdit holds the draft while the editor is open.
let planEdit = null;   // { id, plan } while the plan card is open in the editor
const canEditPlan = (t, m) => {
  const ms = t.messages || [], at = ms.indexOf(m);
  if (!m.plan || m.plan.mockup || m.superseded || ms.slice(at + 1).some((x) => x.kind === 'plan' && !x.superseded)) return false;
  return t.status === 'awaiting_approval' || (t.approved && ['done', 'failed', 'cancelled'].includes(t.status));
};
function planEditor(t, m) {
  const p = planEdit.plan, wait = t.status === 'awaiting_approval';
  const inp = (cls, v, label, ph) => `<label class="pe-f"><span>${label}</span><input class="${cls}" value="${esc(v || '')}" placeholder="${esc(ph || '')}"></label>`;
  const subs = (p.subtasks || []).map((s, i) => `<div class="pe-sub" data-i="${i}"><div class="pe-sub-head"><b>${i + 1}.</b>
      <input class="pe-s-title" value="${esc(s.title)}" aria-label="Subtask title" placeholder="Title">
      <select class="small pe-s-diff" aria-label="Difficulty">${['easy', 'normal', 'hard'].map((d) => `<option value="${d}" ${(s.difficulty || 'normal') === d ? 'selected' : ''}>${d}</option>`).join('')}</select>
      <button class="msg-tool shown" data-act="task-pe-up" data-i="${i}" title="Move up" aria-label="Move up" ${i ? '' : 'disabled'}>↑</button>
      <button class="msg-tool shown" data-act="task-pe-down" data-i="${i}" title="Move down" aria-label="Move down" ${i < p.subtasks.length - 1 ? '' : 'disabled'}>↓</button>
      <button class="msg-tool shown" data-act="task-pe-del" data-i="${i}" title="Delete subtask" aria-label="Delete subtask">✕</button></div>
      <label class="pe-f"><span>Files (comma separated)</span><input class="pe-s-files" value="${esc((s.files || []).join(', '))}" ${s.kind === 'investigate' ? 'placeholder="none: this subtask investigates"' : ''}></label>
      <label class="pe-f"><span>Starts after (subtask ids: ${esc(s.id)} is this one)</span><input class="pe-s-deps" value="${esc((s.depends_on || []).join(', '))}" placeholder="e.g. s1, s2"></label>
      <label class="pe-f"><span>Instructions</span><textarea class="pe-s-ins" rows="3">${esc(s.instructions)}</textarea></label></div>`).join('');
  return `<div class="card-msg plan live" id="plan-edit"><div class="card-head">Edit plan</div>
    ${inp('pe-title', p.title, 'Title')}<label class="pe-f"><span>Summary</span><textarea class="pe-summary" rows="2">${esc(p.summary)}</textarea></label>
    <div class="pe-row">${inp('pe-lang', p.language, 'Language')}${inp('pe-setup', p.setup_command, 'Setup command')}${inp('pe-test', p.test_command, 'Test command')}${inp('pe-build', p.build_command, 'Build command')}</div>
    ${inp('pe-arts', (p.artifacts || []).join(', '), 'Build outputs (comma separated)')}
    <div class="pe-subs">${subs}</div>
    <div class="card-actions"><button class="small" data-act="task-pe-add">+ Add subtask</button></div>
    <div class="card-actions"><button data-act="task-pe-cancel">Cancel</button><button data-act="task-pe-save">Save</button>
      <button class="primary" data-act="task-pe-run">${wait ? 'Save and approve' : 'Save and re-run'}</button>
      <span class="hint">${wait ? 'The old plan stays above, dimmed.' : 'Re-running puts back the files from the last run and starts clean. The earlier run stays above.'}</span></div></div>`;
}
// Read the editor fields back into planEdit.plan.
function readPlanEditor() {
  const root = $('plan-edit'); if (!root || !planEdit) return;
  const v = (c) => (root.querySelector('.' + c) || {}).value || '';
  const list = (x) => x.split(',').map((a) => a.trim()).filter(Boolean);
  const p = planEdit.plan;
  p.title = v('pe-title'); p.summary = v('pe-summary'); p.language = v('pe-lang'); p.setup_command = v('pe-setup'); p.test_command = v('pe-test'); p.build_command = v('pe-build');
  p.artifacts = list(v('pe-arts'));
  p.subtasks = [...root.querySelectorAll('.pe-sub')].map((el, i) => {
    const old = p.subtasks[i] || {}, q = (c) => el.querySelector('.' + c).value;
    return { ...old, title: q('pe-s-title'), difficulty: q('pe-s-diff'), files: list(q('pe-s-files')), depends_on: list(q('pe-s-deps')), instructions: q('pe-s-ins') };
  });
}

function planCard(t, m, live) {
  if (planEdit && planEdit.id === m.id && canEditPlan(t, m)) return planEditor(t, m);
  const p = m.plan || {};
  const diffSel = (s) => live ? `<select class="small" data-diff="${esc(s.id)}" aria-label="Difficulty of ${esc(s.title)}" title="Hard subtasks go to the strongest model; easy ones to the fastest node">${['easy', 'normal', 'hard'].map((d) => `<option value="${d}" ${(s.difficulty || 'normal') === d ? 'selected' : ''}>${d}</option>`).join('')}</select>`
    : (s.difficulty && s.difficulty !== 'normal' ? `<span class="tag">${esc(s.difficulty)}</span>` : '');
  const subs = (p.subtasks || []).map((s) => `<li><details><summary><span class="st-title">${esc(s.title)}</span>${diffSel(s)}<span class="st-files">${s.kind === 'investigate' ? 'investigates, writes no files' : s.files.map(esc).join(', ')}</span></summary>
      <p class="st-ins">${esc(s.instructions)}</p>${s.depends_on && s.depends_on.length ? `<p class="st-dep">Starts after ${s.depends_on.map(esc).join(', ')}</p>` : ''}</details></li>`).join('');
  const facts = [p.language ? esc(p.language) : '', p.test_command ? `tests: <code>${esc(p.test_command)}</code>` : 'no automated tests', p.build_command ? `build: <code>${esc(p.build_command)}</code>` : '', `${(p.subtasks || []).length} subtasks`].filter(Boolean).join(' · ');
  let actions = '';
  if (live) {
    const warn = [];
    if (p.test_command && !t.exec_nodes) warn.push('No node has exec on, so the tests will be skipped.');
    if (!t.workers_online) warn.push('No node has a model ready; work waits until one does.');
    const est = (t.planner === 'claude' || t.planner === 'openrouter') && t.estimate_usd ? `Planning cost ${usd(t.cost_usd)}; the rest is estimated at about ${usd(Math.max(0, t.estimate_usd - t.cost_usd))}.` : '';
    actions = `${warn.length || est ? `<p class="card-note">${esc([est, ...warn].filter(Boolean).join(' '))}</p>` : ''}
      <div class="card-actions">${t.planner === 'claude' || t.planner === 'openrouter' ? `<label class="chip-input">cap $<input id="ta-cap" type="number" min="0.05" step="0.25" value="${t.cap_usd}" aria-label="Spending cap in US dollars"></label>` : ''}
      <button class="primary" data-act="task-approve">Approve and start</button><span class="hint">or reply below to change the plan</span></div>`;
  }
  const editable = canEditPlan(t, m);
  if (editable && !live) {
    const failedSub = (t.subtasks || []).some((x) => x.status === 'failed');
    const needRetry = failedSub || t.test_status === 'failing' || t.status === 'failed';
    actions = `<div class="card-actions"><button class="primary" data-act="task-rerun" data-mode="all">Re-run plan</button>
      ${needRetry ? `<button data-act="task-rerun" data-mode="failed" title="Keep finished subtasks and their files; run the failed ones and the tests again">${failedSub ? 'Retry failed subtasks' : 'Retry tests and fixes'}</button>` : ''}
      <span class="hint">same plan, no new planning</span></div>`;
  }
  const pencil = editable ? `<button class="msg-tool shown" data-act="task-pe-open" data-msg="${esc(m.id)}" title="Edit plan" aria-label="Edit plan">${PENCIL}</button>` : '';
  return `<div class="card-msg plan ${live ? 'live' : ''}"><div class="card-head">Plan · ${esc(p.title || '')}${m.edited ? ' <span class="edited-mark inline">edited</span>' : ''}${pencil}</div>
    ${m.text ? `<p>${esc(m.text)}</p>` : ''}<p class="plan-summary">${esc(p.summary || '')}</p><p class="plan-facts">${facts}</p>
    <ol class="plan-steps">${subs}</ol>${actions}</div>`;
}

const ST_ICON = { pending: '', running: '', reviewing: '', written: '', done: '✓', failed: '✕' };
function progressCard(t) {
  const plan = t.plan || { subtasks: [] };
  const byId = {}; (t.subtasks || []).forEach((s) => { byId[s.id] = s; });
  const specs = plan.subtasks || [];
  const done = specs.filter((s) => (byId[s.id] || {}).status === 'done').length;
  const strip = specs.map((s) => `<i class="seg s-${(byId[s.id] || {}).status || 'pending'}" title="${esc(s.title)}"></i>`).join('');
  const rows = specs.map((s) => {
    const x = byId[s.id] || { status: 'pending' };
    let who = '';
    if (x.waiting) who = `${esc(x.node_name || 'a worker')} asked you a question`;
    else if (x.status === 'running' && s.kind === 'investigate') who = `investigating on ${esc(x.node_name || '…')}${x.agent_calls ? ` · ${x.agent_calls} tool calls` : ''}`;
    else if (x.status === 'running') who = `writing on ${esc(x.node_name || '…')}${x.attempts ? ` · try ${x.attempts + 1}` : ''}`;
    else if (s.kind === 'investigate' && (x.status === 'written' || x.status === 'done')) who = `investigated on ${esc(x.node_name || 'the planner')}${x.agent_calls ? ` · ${x.agent_calls} tool calls` : ''}`;
    else if (x.status === 'reviewing' || x.status === 'written') who = `${esc(x.node_name || '')} wrote it · ${esc(x.reviewer || 'another node')} is checking`;
    else if (x.status === 'done') who = `${x.by_planner ? 'written by the planner' : esc(x.node_name || '')}${x.reviewer ? ` · checked by ${esc(x.reviewer)}${x.review_fixed ? ', who fixed it' : ''}` : ''}${x.best_of ? ` · best of several: ${esc(x.best_of)}` : ''}`;
    else if (x.status === 'failed') who = esc(x.error || 'failed');
    else who = s.depends_on && s.depends_on.length ? `waits for ${s.depends_on.map(esc).join(', ')}` : 'waiting for a free node';
    const lives = Object.entries(liveCalls).filter(([, c]) => c.sub === s.id).map(([id, c]) => livePane(c, id)).join('');
    return `<li class="p-${x.status}"><span class="p-ico" aria-hidden="true">${ST_ICON[x.status] || ''}</span><span class="p-title">${esc(s.title)}</span><span class="p-who">${who}</span>${lives}</li>`;
  }).join('');
  let phase = '';
  if (t.status === 'testing') phase = 'Running the tests…';
  else if (t.status === 'building') phase = 'Building…';
  else if (t.status === 'fixing') phase = `Tests failed; fixing (round ${t.fix_rounds + 1} of 3). The fixer reads, edits and re-runs on its own.`;
  else if (t.status === 'running' && !t.workers_online) phase = 'Waiting for a node with a model ready. Load one on the Nodes page.';
  const head = t.status === 'running' || t.status === 'testing' || t.status === 'building' || t.status === 'fixing'
    ? `Working · ${done} of ${specs.length} subtasks done` : `${done} of ${specs.length} subtasks done`;
  return `<div class="card-msg progress"><div class="card-head">${head}</div><div class="strip" role="img" aria-label="${done} of ${specs.length} subtasks done">${strip}</div>
    <ul class="p-list">${rows}</ul>${phase ? `<p class="card-note">${esc(phase)}</p>` : ''}</div>`;
}

// Build outputs: one link each, plus zips. Sizes use the same bytes() as the file browser.
function artifactsBlock(t) {
  const arts = t.artifacts || [];
  if (t.build_status === 'failed') return `<details class="testout"><summary>The build failed</summary><pre class="out">${esc(t.build_output || '')}</pre></details>`;
  if (!arts.length) return '';
  const links = arts.map((a) => `<a href="/api/tasks/${t.id}/artifact?path=${encodeURIComponent(a.path)}">${esc(a.path)} <span class="muted">(${bytes(a.bytes)})</span></a>`).join('');
  const publish = arts.some((a) => /^(.*\/)?outpost-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$/.test(a.path))
    ? '<button class="small" data-act="task-publish" title="Upload the outpost-&lt;platform&gt; binaries as releases. Updating the controller and nodes stays a click on the Updates tab.">Publish to Updates</button>' : '';
  return `<div class="changed"><span class="hint">Build outputs</span>${links}</div>
    <div class="card-actions">${publish}<a href="/api/tasks/${t.id}/artifacts.zip"><button class="small">artifacts.zip</button></a><a href="/api/tasks/${t.id}/download?artifacts=1"><button class="small">Source and artifacts .zip</button></a></div>`;
}

function resultCard(t, m) {
  const latest = m.round === (t.round || 1);
  const files = (m.files || []).map((f) => `<a href="#" data-act="view-file" data-path="${esc(f)}">${esc(f)}</a>`).join('');
  const tone = /tests still fail/.test(m.text) ? 'warn' : 'good';
  return `<div class="card-msg result ${tone}"><div class="card-head">${tone === 'good' ? 'Finished' : 'Finished with failing tests'}</div><p>${esc(m.text)}</p>
    ${files ? `<div class="changed"><span class="hint">${m.round > 1 ? 'Changed in this request' : 'Files'}</span>${files}</div>` : ''}
    ${latest ? artifactsBlock(t) : ''}
    ${latest && t.test_output && t.test_status !== 'passed' ? `<details class="testout"><summary>Last test output</summary><pre class="out">${esc(t.test_output)}</pre></details>` : ''}
    ${(taskRounds || []).some((r) => r.round === m.round) ? `<div class="card-actions"><button class="small" data-act="show-changes" data-round="${m.round}">Changes</button></div>` : ''}
    ${latest ? `<div class="card-actions"><a href="/api/tasks/${t.id}/download"><button class="primary">Download .zip</button></a><span class="hint">Want changes? Send a follow-up below; it builds on these files.</span></div>` : ''}</div>`;
}

// A card for states that need the person, at the end of the thread.
function statusCard(t) {
  const locals = state ? state.nodes.filter((n) => n.online && n.heartbeat && n.heartbeat.loaded_model)
    .map((n) => `<option value="node:${n.id}">${esc(nodeName(n))} (${esc(n.heartbeat.loaded_model)})</option>`).join('') : '';
  switch (t.status) {
    case 'planning':
      return { key: 'tail-planning', cls: 'from-planner', html: `<div class="said thinking">Thinking it through with ${esc(t.planner_label)}<span class="dots" aria-hidden="true"><i></i><i></i><i></i></span></div>` };
    case 'paused_cap':
      return { key: 'tail-cap', cls: 'card-row', html: `<div class="card-msg warn"><div class="card-head">Paused at the spending cap</div>
        <p>Spent ${usd(t.cost_usd)} of ${usd(t.cap_usd)}. Raise the cap to keep going with the cloud model, or finish with a local planner.</p>
        <div class="card-actions"><label class="chip-input">new cap $<input id="ta-newcap" type="number" min="0.05" step="0.25" value="${(Math.ceil((t.cap_usd * 2) * 4) / 4).toFixed(2)}"></label><button class="primary" data-act="task-raise">Raise cap and continue</button></div>
        ${locals ? `<div class="card-actions"><select id="ta-local" class="chip-select">${locals}</select><button data-act="task-local">Finish with this local planner</button></div>` : ''}</div>` };
    case 'interrupted':
      return { key: 'tail-int', cls: 'card-row', html: `<div class="card-msg warn"><div class="card-head">Interrupted</div><p>The controller restarted while this task was active. Tasks on a local planner resume on their own when a node is ready (unless turned off in Settings); otherwise Resume picks up where it stopped; finished subtasks are kept.</p>
        <div class="card-actions"><button class="primary" data-act="task-resume">Resume</button></div></div>` };
  }
  return null;
}

function renderComposer(t) {
  const box = $('tm-text');
  const modes = {
    awaiting_answers: ['Or answer in your own words…', 'Your reply goes to the planner with the questions above.'],
    planning: ['Steer the planner, e.g. "use SQLite, keep it to one file"…', 'The planner gets your note when its current answer is in, and revises it.'],
    awaiting_approval: ['Ask for changes to the plan…', 'The planner revises the plan; nothing runs until you approve.'],
    running: ['Steer the work, e.g. "use SQLite, not JSON files"…', 'Notes apply to subtasks that start from now on, reviews and test fixes.'],
    paused_cap: ['Steer the work…', 'Notes apply to the rest of this request.'],
    interrupted: ['Ask for something new…', 'Resume first to finish the current request; a message starts a new one on these files.'],
  };
  modes.testing = modes.fixing = modes.running;
  const [ph, hint] = modes[t.status] || ['Ask for a follow-up change…', 'Builds on this project\'s files: the planner plans only what changes.'];
  if (box.placeholder !== ph) box.placeholder = ph;
  setHTML('tm-hint', esc(hint));
}

async function sendTaskMessage() {
  const box = $('tm-text'), text = box.value.trim();
  if (!text || !pageArg) return;
  $('tm-send').disabled = true;
  try { await post(`/api/tasks/${pageArg}/messages`, { text, images: tmImg.payload() }); box.value = ''; tmDraft.done(); tmImg.clear(); autoGrow(box); $('thread')._scrolled = false; }
  catch (e) { toast(e.message, 4000); }
  $('tm-send').disabled = false;
  pollTask(); refresh();
}
$('tm-send').onclick = sendTaskMessage;
$('tm-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); sendTaskMessage(); } });
function autoGrow(el) { el.style.height = 'auto'; el.style.height = Math.min(el.scrollHeight, 220) + 'px'; }
$('tm-text').addEventListener('input', (e) => autoGrow(e.target));

// Answers from the live questions card (the only one whose fieldsets are enabled).
function collectAnswers() {
  const answers = {};
  document.querySelectorAll('.q:not([disabled])').forEach((f) => {
    const id = f.dataset.q, picked = [...f.querySelectorAll('input:checked')].map((i) => i.value);
    const other = (f.querySelector('.opt-other') || {}).value;
    if (other && other.trim()) picked.push(other.trim());
    if (picked.length) answers[id] = picked;
  });
  return answers;
}

function renderActivity(t) {
  const ev = $('tv-events');
  setHTML(ev, (t.events || []).map((e) => `<div class="${e.level}"><span class="time">${clock(e.time)}</span><span class="msg">${esc(e.text)}</span></div>`).join(''));
  ev.scrollTop = ev.scrollHeight;
}
function renderFiles(t) {
  const files = (t.files || []).slice().sort((x, y) => x.path.localeCompare(y.path));
  setHTML('files-hint', `${files.length} file${files.length === 1 ? '' : 's'} in the project so far. Click one to read it.`);
  const seen = new Set();
  setHTML('tv-files', files.map((f) => {
    const parts = f.path.split('/'), out = [];
    parts.slice(0, -1).forEach((d, i) => { const k = parts.slice(0, i + 1).join('/'); if (!seen.has(k)) { seen.add(k); out.push(`<div class="fb-dir pl${Math.min(i, 20)}">${esc(d)}/</div>`); } });
    out.push(`<div class="fb-row pl${Math.min(parts.length - 1, 20)}"><input type="checkbox" class="fb-tick" value="${esc(f.path)}" aria-label="Tick ${esc(f.path)}"><a href="#" data-act="view-file" data-path="${esc(f.path)}">${esc(parts[parts.length - 1])} <span class="muted">(${bytes(f.bytes)})</span></a></div>`);
    return out.join('');
  }).join(''));
  $('files-dl').href = `/api/tasks/${t.id}/download`;
}

let taskRounds = [];
async function loadRounds() {
  if (!pageArg) return;
  try { taskRounds = (await api(`/api/tasks/${pageArg}/rounds`)).rounds || []; } catch (e) { taskRounds = []; }
}

// A small keyword highlighter; it only colours, never changes the text.
function highlight(code, path) {
  const e = esc(code);
  if (!/\.(go|js|ts|py|rs|c|h|cpp|java|sh|json|html|css|rb|php|cs|kt|swift)$/i.test(path)) return e;
  return e.replace(/(&quot;(?:[^&\n]|&(?!quot;))*?&quot;|&#39;[^\n]*?&#39;|\/\/[^\n]*|#[^\n]*)|\b(func|function|def|class|return|if|else|elif|for|while|import|from|package|const|let|var|struct|type|fn|pub|use|true|false|null|nil|None|True|False|self|this|new|try|catch|except|throw|async|await)\b|\b(\d+(?:\.\d+)?)\b/g,
    (m, str, kw, num) => str ? `<span class="hl-s">${m}</span>` : kw ? `<span class="hl-k">${m}</span>` : `<span class="hl-n">${m}</span>`);
}
function numbered(code, path) {
  const lines = highlight(code, path).split('\n');
  return lines.map((l, i) => `<span class="ln">${i + 1}</span>${l}`).join('\n');
}

// Line diff: trims the shared start and end, then a longest-common-subsequence table.
function diffLines(a, b) {
  const x = a === '' ? [] : a.split('\n'), y = b === '' ? [] : b.split('\n');
  let s = 0;
  while (s < x.length && s < y.length && x[s] === y[s]) s++;
  let ex = x.length, ey = y.length;
  while (ex > s && ey > s && x[ex - 1] === y[ey - 1]) { ex--; ey--; }
  const mid = [], n = ex - s, m = ey - s;
  if (n * m > 4e6) {
    for (let i = s; i < ex; i++) mid.push(['-', x[i]]);
    for (let j = s; j < ey; j++) mid.push(['+', y[j]]);
  } else {
    const L = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--)
      L[i][j] = x[s + i] === y[s + j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
    let i = 0, j = 0;
    while (i < n && j < m) {
      if (x[s + i] === y[s + j]) { mid.push([' ', x[s + i]]); i++; j++; }
      else if (L[i + 1][j] >= L[i][j + 1]) mid.push(['-', x[s + i++]]);
      else mid.push(['+', y[s + j++]]);
    }
    while (i < n) mid.push(['-', x[s + i++]]);
    while (j < m) mid.push(['+', y[s + j++]]);
  }
  const all = [];
  for (let i = 0; i < s; i++) all.push([' ', x[i]]);
  all.push(...mid);
  for (let i = ex; i < x.length; i++) all.push([' ', x[i]]);
  return all;
}
// Unified view: changed lines with 3 lines of context.
function unified(a, b) {
  const d = diffLines(a, b), keep = d.map(() => false);
  d.forEach((r, i) => { if (r[0] !== ' ') for (let k = Math.max(0, i - 3); k <= Math.min(d.length - 1, i + 3); k++) keep[k] = true; });
  let out = '', gap = false;
  d.forEach((r, i) => {
    if (!keep[i]) { gap = true; return; }
    if (gap && out) out += '<span class="d-gap">…</span>\n';
    gap = false;
    out += `<span class="d-${r[0] === '+' ? 'add' : r[0] === '-' ? 'del' : 'ctx'}">${r[0]} ${esc(r[1])}</span>\n`;
  });
  return out || '<span class="d-ctx">(no differences)</span>';
}

let changesRound = 0;
async function showChanges(round) {
  changesRound = round;
  setHTML('ch-body', '<span class="muted">Loading…</span>'); $('ch-undo').classList.add('hidden');
  $('ch-title').textContent = `Changes in request ${round}`; open('modal-changes');
  try {
    const r = await api(`/api/tasks/${pageArg}/changes?round=${round}`);
    setHTML('ch-hint', `${r.files.length} file${r.files.length === 1 ? '' : 's'} changed. Shown against the files as they are now.`);
    setHTML('ch-body', r.files.map((f) => `<details open><summary class="mono">${esc(f.path)} <span class="muted">${f.status === 'added' ? 'new file' : 'changed'}${f.gone ? ', since deleted' : ''}</span></summary><pre class="out code diff">${unified(f.before, f.after)}</pre></details>`).join(''));
    $('ch-undo').classList.toggle('hidden', !r.undoable);
  } catch (e) { setHTML('ch-body', `<span class="error-msg">${esc(e.message)}</span>`); }
}

async function taskAction(act, el) {
  const id = pageArg;
  try {
    switch (act) {
      case 'task-approve': await post(`/api/tasks/${id}/approve`, { cap_usd: parseFloat(($('ta-cap') || {}).value) || 0 }); break;
      case 'task-pick': await post(`/api/tasks/${id}/pick`, { page: el.dataset.page }); toast('Building from that variant'); break;
      case 'task-msg-edit': {
        const m = ((taskDetail && taskDetail.messages) || []).find((x) => x.id === el.dataset.msg);
        if (!m) return;
        editing = { id: m.id, text: m.text };
        renderTask(taskDetail);
        const ta = $('msg-edit-text'); if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); }
        return;
      }
      case 'task-pe-open': {
        const m = ((taskDetail && taskDetail.messages) || []).find((x) => x.id === el.dataset.msg);
        if (!m) return;
        planEdit = { id: m.id, plan: JSON.parse(JSON.stringify(m.plan)) };
        renderTask(taskDetail);
        readPlanEditor(); planEdit.orig = JSON.stringify(planEdit.plan);   // baseline for the unsaved-changes check
        return;
      }
      case 'task-pe-cancel': planEdit = null; renderTask(taskDetail); return;
      case 'task-pe-add': case 'task-pe-up': case 'task-pe-down': case 'task-pe-del': {
        readPlanEditor();
        const subs = planEdit.plan.subtasks, i = parseInt(el.dataset.i, 10);
        if (act === 'task-pe-add') {
          let n = subs.length + 1; while (subs.some((x) => x.id === 's' + n)) n++;
          subs.push({ id: 's' + n, title: '', files: [], depends_on: [], instructions: '', difficulty: 'normal' });
        } else if (act === 'task-pe-del') {
          const gone = subs.splice(i, 1)[0];
          subs.forEach((x) => { x.depends_on = (x.depends_on || []).filter((d) => d !== gone.id); });
        } else {
          const j = act === 'task-pe-up' ? i - 1 : i + 1;
          if (j >= 0 && j < subs.length) [subs[i], subs[j]] = [subs[j], subs[i]];
        }
        renderTask(taskDetail);
        return;
      }
      case 'task-pe-save': case 'task-pe-run': {
        readPlanEditor();
        if (act === 'task-pe-run' && taskDetail.status !== 'awaiting_approval' && !confirm('Put the files back as they were before the last run and run this plan from the start?')) return;
        await post(`/api/tasks/${id}/plan/edit`, { plan: planEdit.plan, run: act === 'task-pe-run', cap_usd: parseFloat(($('ta-cap') || {}).value) || 0 });
        planEdit = null;
        toast(act === 'task-pe-run' ? 'Running the edited plan' : 'Plan saved');
        break;
      }
      case 'task-rerun': {
        const whole = el.dataset.mode !== 'failed';
        if (whole && !confirm('Put the files back as they were before the last run and run the same plan from the start?')) return;
        await post(`/api/tasks/${id}/rerun`, { mode: whole ? 'all' : 'failed' });
        toast(whole ? 'Re-running the plan' : 'Retrying');
        break;
      }
      case 'task-msg-cancel': editing = null; renderTask(taskDetail); return;
      case 'task-msg-save': {
        const text = (($('msg-edit-text') || {}).value || '').trim();
        if (!text) { toast('Write a message first'); return; }
        await post(`/api/tasks/${id}/messages/${encodeURIComponent(el.dataset.msg)}/edit`, { text });
        editing = null;
        break;
      }
      case 'task-answer': {
        const answers = collectAnswers();
        await post(`/api/tasks/${id}/answers`, { answers });
        Drafts.clearPrefix('ans:' + id + ':' + el.dataset.msg + ':');
        break;
      }
      case 'task-agent-answer': {
        const msg = el.dataset.msg, answer = el.dataset.answer || ($('aq-' + msg) || {}).value || '';
        if (!answer.trim()) { toast('Write an answer first'); return; }
        await post(`/api/tasks/${id}/agent-answer`, { id: msg, answer });
        Drafts.clear('ans:' + id + ':' + msg + ':aq');
        break;
      }
      case 'task-publish': {
        const r = await post(`/api/tasks/${id}/publish`);
        const bad = Object.entries(r.refused || {}).map(([k, v]) => `${k}: ${v}`);
        toast(`${(r.published || []).length} published to Updates` + (bad.length ? `. Refused: ${bad.join('; ')}` : '. Update the controller and nodes from the Updates tab.'), bad.length ? 9000 : 4000);
        return;
      }
      case 'task-cancel': if (!confirm('Stop this task? Files written so far are kept, and you can send a message later to continue.')) return; await post(`/api/tasks/${id}/cancel`); break;
      case 'task-raise': await post(`/api/tasks/${id}/resume`, { cap_usd: parseFloat($('ta-newcap').value) || 0 }); toast('Continuing'); break;
      case 'task-local': await post(`/api/tasks/${id}/resume`, { planner: $('ta-local').value }); toast('Continuing with the local planner'); break;
      case 'task-resume': await post(`/api/tasks/${id}/resume`); toast('Resumed'); break;
      case 'task-delete': if (!confirm('Delete this task, its conversation and its files?')) return; await api(`/api/tasks/${id}`, { method: 'DELETE' }); Drafts.clear('task:' + id); Drafts.clearPrefix('ans:' + id + ':'); location.hash = '#/new'; refresh(); return;
      case 'task-activity': if (taskDetail) { renderActivity(taskDetail); open('modal-activity'); } return;
      case 'show-changes': showChanges(parseInt(el.dataset.round, 10)); return;
      case 'undo-round': if (!confirm('Put the files back as they were before this request?')) return; await post(`/api/tasks/${id}/undo?round=${changesRound}`); close('modal-changes'); toast('Round undone'); break;
      case 'task-files': if (taskDetail) { renderFiles(taskDetail); open('modal-files'); } return;
    }
  } catch (e) { toast(e.message, 4000); }
  $('thread')._scrolled = false;
  pollTask(); refresh();
}

// Zip the ticked files: a form post, so the browser streams the download.
$('files-zip').onclick = () => {
  const paths = [...document.querySelectorAll('.fb-tick:checked')].map((c) => c.value);
  if (!paths.length) { toast('Tick the files to zip first'); return; }
  const f = document.createElement('form');
  f.method = 'POST'; f.action = `/api/tasks/${pageArg}/zip`;
  const i = document.createElement('input');
  i.type = 'hidden'; i.name = 'paths'; i.value = paths.join('\n');
  f.appendChild(i); document.body.appendChild(f); f.submit(); f.remove();
};

async function viewFile(path) {
  if (taskDetail) renderFiles(taskDetail);
  open('modal-files');
  $('fb-title').textContent = path; $('fb-body').textContent = 'Loading…';
  $('fb-dl').classList.remove('hidden');
  $('fb-dl').href = `/api/tasks/${pageArg}/file?path=${encodeURIComponent(path)}&download=1`;
  try {
    const r = await fetch(`/api/tasks/${pageArg}/file?path=${encodeURIComponent(path)}`, { credentials: 'same-origin' });
    setHTML('fb-body', numbered(await r.text(), path));
  } catch (e) { $('fb-body').textContent = e.message; }
}

// ---------- nodes ----------
// benchLine shows a node's measured speed, a running or failed benchmark, and the re-run link.
function benchLine(n, loaded, ready) {
  if (!loaded || !n.online) return '';
  const b = n.bench && n.bench.model === loaded ? n.bench : null;
  const f = n.bench_fail && n.bench_fail.model === loaded ? n.bench_fail : null;
  const label = b || f ? 'Re-run' : 'Run';
  const rerun = n.benching ? '' : ready ? ` · <a href="#" data-act="benchmark" data-id="${n.id}" title="Measure this node's speed again">${label} benchmark</a>`
    : ` · <span class="muted" title="The benchmark can run once the model has finished loading">${label} benchmark (model loading…)</span>`;
  let txt;
  if (n.benching) txt = 'Benchmarking…';
  else if (b) txt = `Speed ${b.tok_per_s.toFixed(1)} tok/s${b.prompt_tps ? ' · reads ' + Math.round(b.prompt_tps) + ' tok/s' : ''}${b.samples ? ' · ' + b.samples + ' subtask' + (b.samples === 1 ? '' : 's') : b.light ? ' · short benchmark' : ' · benchmark'}`;
  else if (f) txt = `<span class="muted" title="${esc(f.error)}">Benchmark failed (${esc(f.error.length > 60 ? f.error.slice(0, 60) + '…' : f.error)}); retries in a few minutes with a shorter test. Real subtasks fill in the speed meanwhile.</span>`;
  else txt = '<span class="muted">Not benchmarked yet</span>';
  return `<div class="sub wrap u15" title="Benchmark when the model loaded, then a rolling average of real subtasks">${txt}${rerun}</div>`;
}

function renderNodes() {
  const nodes = state.nodes;
  const online = nodes.filter((n) => n.online).length;
  setHTML('node-count', nodes.length ? `${online} of ${nodes.length} online` : '');
  setHTML('nodes', !nodes.length ? `<div class="empty u16">No nodes yet. Click <b>Add node</b> to get the install command.</div>` : nodes.map(nodeCard).join(''));
}

function opView(n) {
  const o = state.ops && state.ops[n.id];
  if (!o || !o.op) return '';
  const age = (Date.now() - new Date(o.updated)) / 1000;
  const what = { runtime: 'llama.cpp', toolchain: 'Installing Go', 'model.push': 'Copying model from controller', 'model.fetch': 'Downloading from Hugging Face', rpc: 'Lending memory', 'model.load': 'Loading model', 'model.unload': 'Unloading', 'model.remove': 'Removing model', 'model.cleanup': 'Cleaning up models' }[o.op] || o.op;
  if (o.op === 'model.cleanup' && o.state === 'done') return age < 600 && o.detail ? `<div class="sub u17">Cleanup: ${esc(o.detail)}</div>` : '';
  if (o.state === 'failed') return age < 3600 ? `<div class="note bad u17">${esc(what)} failed: ${esc(o.error)}</div>` : '';
  if (['done', 'ready'].includes(o.state)) return '';
  const pct = o.bytes_total ? 100 * o.bytes_done / o.bytes_total : (o.state === 'loading' ? 100 : 0);
  return `<div class="prog u6"><span>${esc(what)}${o.target ? ' · ' + esc(o.target) : ''}: ${esc(o.state)}${o.bytes_total ? ` ${bytes(o.bytes_done)} of ${bytes(o.bytes_total)}` : '…'}</span>${bar(pct, 'good')}</div>`;
}

// "Recommended for this node" row: the best coding model that fits, one click to load.
function opActive(n) {
  const o = state.ops && state.ops[n.id];
  return !!(o && o.op && !['done', 'ready', 'failed'].includes(o.state) && (Date.now() - new Date(o.updated)) < 600000);
}
function recRow(n, loaded) {
  const rec = n.online && models && models.recommended && models.recommended[n.id];
  if (!rec || (loaded && rec.name === loaded) || n.lending_to || opActive(n)) return '';
  const where = rec.in_library ? '' : ' · adds to library';
  return `<div class="rec-row"><span class="pill st-ready">Recommended</span> <b>${esc(rec.label)}</b>
    <button class="small primary" data-act="load-rec" data-id="${n.id}" data-name="${esc(rec.name)}" data-lib="${rec.in_library ? 1 : 0}">${loaded ? 'Switch to this' : 'Load'}</button>
    <div class="sub wrap u18">${esc(rec.reason)} · ${bytes(rec.bytes)}${where}</div></div>`;
}
function nodeCard(n) {
  const h = n.hello || {}, hb = n.online ? n.heartbeat : null, cpu = h.cpu || {};
  const os = h.os ? `${h.os}/${h.arch}` : 'unknown';
  const ramPct = hb && hb.ram_total_bytes ? (100 * hb.ram_used_bytes / hb.ram_total_bytes) : 0;
  const gpus = (h.gpus || []).map((g, i) => {
    const used = hb && hb.vram_used_bytes ? hb.vram_used_bytes[i] : 0;
    const util = hb && hb.gpu_util_pct ? hb.gpu_util_pct[i] : 0;
    return `<div class="metric"><span class="k">GPU</span>${bar(util)}<span class="v">${util || 0}% · ${bytes(used)}/${bytes(g.vram_bytes)}</span></div>
      <div class="sub u19">${esc(g.name)}</div>`;
  }).join('');
  const status = n.online ? `online · ${n.running_jobs}/${n.max_jobs} jobs` : (n.last_seen ? `offline · seen ${ago(n.last_seen)}` : 'never connected');
  const paused = n.online && hb && hb.paused;
  const loaded = hb && hb.loaded_model, ready = hb && hb.inference_ready;
  const stored = (h.models || []);
  const inUse = new Set([loaded, n.load_prefs && n.load_prefs.name].filter(Boolean));
  const needsVulkan = h.os === 'linux' && (h.gpus || []).some((g) => g.vendor !== 'apple') && !h.vulkan_available;
  const modelBox = n.lending_to ? `<div class="model-box">
      <div class="row"><span class="dot ${hb && hb.rpc_endpoint ? 'on' : 'busy'}"></span><span class="grow">Lending memory to <b>${esc(n.lending_to)}</b>${hb && hb.rpc_endpoint ? '' : ' · starting'}</span></div>
      <div class="sub wrap u15">Its RAM${(h.gpus || []).length ? ' and VRAM' : ''} hold part of ${esc(n.lending_to)}'s model. Unload that model to get this node back.</div>
      ${opView(n)}
    </div>` : `<div class="model-box">
      <div class="row"><span class="dot ${ready ? 'on' : loaded ? 'busy' : ''}"></span><span class="grow">${loaded ? `<b>${esc(loaded)}</b> · ${hb.idle_unloaded ? '<span title="Stopped after sitting idle to free memory. It loads again on the next request.">idle, unloaded</span>' : ready ? (hb.inference_busy ? 'working' : 'ready') : 'starting'}` : '<span class="muted">No model loaded</span>'}</span>
        ${n.online ? `<button class="small" data-act="load" data-id="${n.id}">${loaded ? 'Change' : 'Load model'}…</button>${loaded ? `<button class="small" data-act="unload" data-id="${n.id}">Unload</button>` : ''}` : ''}</div>
      ${recRow(n, loaded)}
      ${n.online ? `<div class="sub u15">Unloads when idle: ${esc(n.idle_unload_effective || '1h')}${n.idle_unload ? '' : ' (default)'} · <a href="#" data-act="idle" data-id="${n.id}">change</a></div>` : ''}
      ${benchLine(n, loaded, ready)}
      ${n.pool_names && n.pool_names.length ? `<div class="sub wrap u15">Pooled with <b>${n.pool_names.map(esc).join(', ')}</b></div>` : ''}
      <div class="sub u15">${h.runtime ? `llama.cpp ${esc(h.runtime.build)} · ${esc(h.runtime.variant)}` : 'llama.cpp installs on first load'}${stored.length ? ' · stored: ' + stored.map((m) => `${esc(m.name)}${inUse.has(m.name) ? '' : ` <a href="#" data-act="rm-node-model" data-id="${n.id}" data-name="${esc(m.name)}" title="Delete from this node">×</a>`}`).join(', ') + (n.online ? ` · <a href="#" data-act="clean" data-id="${n.id}">clean up…</a>` : '') : ''}</div>
      ${hb && hb.llama_error && !ready ? `<div class="note bad u15">${esc(hb.llama_error)}</div>` : ''}
      ${needsVulkan ? `<div class="note warn u15">GPU found, but no Vulkan driver, so models run on CPU. On the node: <code>sudo apt install libvulkan1 mesa-vulkan-drivers</code>, then load the model again.</div>` : ''}
      ${opView(n)}
    </div>`;
  return `<div class="card">
    <div class="node-top"><span class="dot ${n.online ? 'on' : ''}"></span><span class="node-name" title="${esc(n.id)}">${esc(nodeName(n))}</span><span class="tag">${h.agent_version ? 'v' + esc(h.agent_version) + ' · ' : ''}${esc(os)}</span></div>
    <div class="sub">${esc(cpu.model || 'CPU')} · ${(cpu.sockets || 1) > 1 ? cpu.sockets + ' sockets · ' : ''}${cpu.cores || '?'}C/${cpu.threads || '?'}T</div>
    <div class="metric"><span class="k">CPU</span>${bar(hb ? hb.cpu_pct : 0)}<span class="v">${hb ? hb.cpu_pct.toFixed(1) + '%' : '—'}</span></div>
    <div class="metric"><span class="k">RAM</span>${bar(ramPct)}<span class="v">${hb ? bytes(hb.ram_used_bytes) + '/' + bytes(hb.ram_total_bytes) : '—'}</span></div>
    ${gpus}
    ${paused ? `<div class="note warn u17"><b>Paused on the node.</b> It takes no new work until someone chooses <b>Accept new work</b> in its tray icon.</div>` : ''}
    <div class="sub u17">${status}${hb && hb.swap_used_bytes ? ` · swap ${bytes(hb.swap_used_bytes)}` : ''}${n.addr ? ` · ${esc(n.addr.split(':')[0])}` : ''}</div>
    ${modelBox}
    ${toolsBox(n)}
    <div class="node-foot">
      <label class="toggle" title="Allow this node to run shell commands (needed for tests)"><input type="checkbox" data-exec="${n.id}" ${n.exec_enabled ? 'checked' : ''}> exec</label>
      ${sandboxPicker(n)}
      <span class="u20"></span>
      <button class="small" data-act="restart" data-id="${n.id}" ${n.online && !n.restarting ? '' : 'disabled'} title="Restart the Outpost service on this node">${n.restarting ? 'Restarting…' : 'Restart'}</button>
      <button class="small" data-act="rename" data-id="${n.id}">Rename</button>
      <button class="small danger" data-act="remove" data-id="${n.id}">Remove</button>
      <button class="small" data-act="run" data-id="${n.id}" ${n.online && n.exec_enabled ? '' : 'disabled'} title="${n.exec_enabled ? '' : 'Enable exec first'}">Run…</button>
    </div>
  </div>`;
}

function sandboxPicker(n) {
  if (!n.exec_enabled) return '';
  const h = n.hello || {};
  const eff = h.sandbox || '';
  const layers = (h.sandbox_layers || []).join(', ');
  const badge = !n.online ? '' : !eff ? 'Off (node is older than the sandbox)' : eff === 'off' ? 'Off' : (layers.includes('landlock') ? 'Sandboxed' : 'Partly') + ' · ' + eff;
  const sel = (v, t) => `<option value="${v}" ${(n.sandbox || '') === v ? 'selected' : ''}>${t}</option>`;
  return `<label class="toggle" title="${esc(layers ? 'Enforced here: ' + layers : 'How commands run on this node. Default follows Settings.')}">sandbox
    <select data-sandbox="${n.id}">${sel('', 'default')}${sel('off', 'off')}${sel('basic', 'basic')}${sel('strict', 'strict')}</select>
    ${badge ? `<span class="muted">${esc(badge)}</span>` : ''}</label>`;
}
async function setSandbox(id, v) {
  try { await post(`/api/nodes/${id}/settings`, { sandbox: v }); toast('Sandbox ' + (v || 'default')); refresh(); } catch (e) { toast(e.message); }
}

function toolsBox(n) {
  const h = n.hello || {};
  if (!n.exec_enabled || !h.shell) return n.exec_enabled ? '<div class="sub u17">Tools: unknown until the node updates</div>' : '';
  const tools = h.tools || [];
  const chips = tools.map((t) => `<span class="tag" title="${esc(t.path || '')}${t.managed ? ' (installed by Outpost)' : ''}">${esc(t.name)}${t.version ? ' ' + esc(t.version) : ''}</span>`).join(' ');
  const hasGo = tools.some((t) => t.name === 'go');
  return `<div class="sub wrap u17">Runs commands in ${esc(h.shell)} · ${chips || 'no tools found'}
    ${n.online ? `<a href="#" data-act="install-tool" data-tool="go" data-id="${n.id}">${hasGo ? 'Reinstall' : 'Install'} Go</a>` : ''}</div>
`;
}

async function setExec(id, on) {
  try { await post(`/api/nodes/${id}/settings`, { exec_enabled: on }); toast(`exec ${on ? 'enabled' : 'disabled'}`); refresh(); }
  catch (e) { toast(e.message); }
}
async function renameNode(id) {
  const n = nodeById(id);
  const name = prompt('Node name', nodeName(n));
  if (name === null) return;
  await post(`/api/nodes/${id}/settings`, { name }).catch((e) => toast(e.message));
  refresh();
}
async function setNodeIdle(id) {
  const n = nodeById(id);
  const v = prompt('Unload this node\'s model after it has been idle for (30s, 5m, 1hr, 2d; 0 = never; leave empty for the default in Settings)', n.idle_unload || '');
  if (v === null) return;
  try { await post(`/api/nodes/${id}/settings`, { idle_unload: v }); toast(v.trim() ? 'Saved' : 'Using the default'); } catch (e) { toast(e.message); }
  refresh();
}
async function removeNode(id) {
  const n = nodeById(id);
  if (!confirm(`Remove ${nodeName(n)}? Its key is revoked; if it is online it wipes its work directory and config and stops. Downloaded models stay on its disk.`)) return;
  await api(`/api/nodes/${id}`, { method: 'DELETE' }).catch((e) => toast(e.message));
  refresh();
}

// Clean-up dialog: pick stored models to delete from a node's disk
let cleanNodeId = null;
function openClean(id) {
  cleanNodeId = id;
  const n = nodeById(id);
  const keep = new Set([n.heartbeat && n.heartbeat.loaded_model, n.load_prefs && n.load_prefs.name].filter(Boolean));
  $('clean-node').textContent = nodeName(n);
  $('clean-err').classList.add('hidden');
  $('clean-partial').checked = false;
  $('clean-list').innerHTML = ((n.hello && n.hello.models) || []).map((m) => `<label class="toggle u21"><input type="checkbox" class="clean-cb" value="${esc(m.name)}" data-bytes="${m.bytes}" ${keep.has(m.name) ? 'disabled' : 'checked'}>
    ${esc(m.name)} <span class="muted">· ${bytes(m.bytes)}${keep.has(m.name) ? ' · in use, kept' : ''}</span></label>`).join('') || '<span class="muted">No models stored.</span>';
  $('clean-list').querySelectorAll('.clean-cb').forEach((cb) => cb.addEventListener('change', updateClean));
  $('clean-partial').onchange = updateClean;
  updateClean();
  open('modal-clean');
}
function updateClean() {
  const sel = [...document.querySelectorAll('.clean-cb:checked')];
  const total = sel.reduce((a, cb) => a + parseInt(cb.dataset.bytes, 10), 0);
  $('clean-go').textContent = sel.length ? `Delete ${sel.length} (${bytes(total)})` : 'Delete';
  $('clean-go').disabled = !sel.length && !$('clean-partial').checked;
}
$('clean-go').onclick = async () => {
  const names = [...document.querySelectorAll('.clean-cb:checked')].map((cb) => cb.value);
  try { await post(`/api/nodes/${cleanNodeId}/models/cleanup`, { names, incomplete: $('clean-partial').checked }); close('modal-clean'); toast('Cleaning up'); refresh(); }
  catch (e) { $('clean-err').textContent = e.message; $('clean-err').classList.remove('hidden'); }
};

// Load-model dialog
function openLoad(id) {
  loadNodeId = id;
  const n = nodeById(id);
  $('load-node').textContent = nodeName(n);
  $('load-err').classList.add('hidden');
  const ready = (models && models.models || []).filter((m) => m.status === 'ready');
  const pref = n.load_prefs || {};
  $('load-model').innerHTML = ready.length ? ready.map((m) => `<option value="${esc(m.name)}" ${pref.name === m.name ? 'selected' : ''}>${esc(m.label || m.name)} · ${bytes(m.bytes_total)} · ${isDirect(m) ? 'node downloads' : 'from controller'}</option>`).join('')
    : '<option value="">No models in the library yet; add one on the Models tab</option>';
  $('load-ngl').value = pref.gpu_layers || '';
  $('load-threads').value = pref.threads || 0;
  $('load-extra').value = (pref.extra_args || []).join(' ');
  const helpers = state.nodes.filter((x) => x.id !== id && !(x.pool_names && x.pool_names.length));
  const inPool = new Set(n.pool || []);
  $('load-pool').innerHTML = helpers.length ? helpers.map((x) => {
    const hx = x.hello || {}, vram = (hx.gpus || []).filter((g) => g.vendor !== 'apple').reduce((a, g) => a + g.vram_bytes, 0);
    const busy = x.lending_to && x.lending_to !== nodeName(n);
    const own = x.heartbeat && x.heartbeat.loaded_model;
    return `<label class="toggle u21"><input type="checkbox" class="pool-cb" value="${x.id}" ${inPool.has(x.id) ? 'checked' : ''} ${x.online && !busy ? '' : 'disabled'}>
      ${esc(nodeName(x))} <span class="muted">· ${bytes(hx.ram_bytes)} RAM${vram ? ' + ' + bytes(vram) + ' VRAM' : ''}${!x.online ? ' · offline' : busy ? ' · lending to ' + esc(x.lending_to) : own ? ' · stops its own model (' + esc(own) + ')' : ''}</span></label>`;
  }).join('') : '<span class="muted">No other nodes.</span>';
  $('load-pool-wrap').classList.toggle('hidden', !state.pooling_enabled);
  $('load-pool-wrap').open = inPool.size > 0;
  $('load-pool').querySelectorAll('.pool-cb').forEach((cb) => cb.addEventListener('change', () => onLoadModelChange(parseInt($('load-ctx').value, 10))));
  onLoadModelChange(pref.ctx);
  open('modal-load');
}
function onLoadModelChange(ctx) {
  const m = (models && models.models || []).find((x) => x.name === $('load-model').value);
  const n = nodeById(loadNodeId);
  $('load-ctx').value = ctx || (m && m.ctx) || 8192;
  const fit = $('load-fit');
  if (!m || !n || !n.hello) { fit.classList.add('hidden'); return; }
  let vram = (n.hello.gpus || []).filter((g) => g.vendor !== 'apple').reduce((a, g) => a + g.vram_bytes, 0);
  let ram = n.hello.ram_bytes || 0;
  const size = m.bytes_total || 0;
  const pool = [...document.querySelectorAll('.pool-cb:checked')].map((cb) => nodeById(cb.value)).filter(Boolean);
  for (const p of pool) {
    const hp = p.hello || {};
    ram += hp.ram_bytes || 0;
    vram += (hp.gpus || []).filter((g) => g.vendor !== 'apple').reduce((a, g) => a + g.vram_bytes, 0);
  }
  let msg = '', cls = 'note';
  if (size > ram + vram) { msg = `This model (${bytes(size)}) is larger than this node's RAM${vram ? ' plus VRAM' : ''} (${bytes(ram + vram)}). It will not load.`; cls = 'note bad'; }
  else if (vram && size <= vram * 0.9) msg = `Fits in this node's ${bytes(vram)} of VRAM: fast.`;
  else if (vram) msg = `Larger than this node's ${bytes(vram)} of VRAM; llama.cpp keeps the rest in system RAM. Slower, but works.`;
  else if (size > ram * 0.8) { msg = `Uses most of this node's ${bytes(ram)} of RAM; expect swapping.`; cls = 'note warn'; }
  else msg = `Runs on CPU with ${bytes(ram)} of RAM.`;
  if (pool.length) msg = `Pooled with ${pool.map(nodeName).join(', ')}: ${bytes(ram)} RAM${vram ? ' + ' + bytes(vram) + ' VRAM' : ''} in total. ` + msg.replace(/this node's/g, 'the pool\'s');
  const onNode = (n.hello.models || []).some((x) => x.name === m.name);
  const free = n.hello.models_free_bytes || 0;
  if (!onNode) {
    msg += isDirect(m) ? ` The node downloads ${bytes(size)} from Hugging Face first (it needs internet access).` : ` The controller copies ${bytes(size)} to the node first.`;
    if (free && size > free) { msg += ` The node has only ${bytes(free)} free, so this will fail.`; cls = 'note bad'; }
  } else msg += ' Already on this node.';
  fit.className = cls; fit.textContent = msg;
}
$('load-model').addEventListener('change', () => onLoadModelChange());
$('load-go').onclick = async () => {
  $('load-err').classList.add('hidden');
  const body = { name: $('load-model').value, ctx: parseInt($('load-ctx').value, 10) || 0, gpu_layers: $('load-ngl').value,
    threads: parseInt($('load-threads').value, 10) || 0, extra_args: $('load-extra').value,
    pool: [...document.querySelectorAll('.pool-cb:checked')].map((cb) => cb.value) };
  if (!body.name) return;
  try { await post(`/api/nodes/${loadNodeId}/load`, body); close('modal-load'); toast('Setting up the model; progress shows on the node card'); refresh(); }
  catch (e) { $('load-err').textContent = e.message; $('load-err').classList.remove('hidden'); }
};

// ---------- models ----------
function recFor(name) {
  const nodes = Object.entries((models && models.recommended) || {}).filter(([, r]) => r.name === name).map(([id]) => nodeById(id)).filter(Boolean);
  return nodes.length ? ` <span class="pill st-ready" title="Best coding model that fits: ${esc(nodes.map(nodeName).join(', '))}">Recommended · ${esc(nodes.map(nodeName).join(', '))}</span>` : '';
}
async function waitModelReady(name) {
  for (let i = 0; i < 90; i++) {
    const m = (await api('/api/models')).models.find((x) => x.name === name);
    if (m && m.status === 'ready') return;
    if (m && m.status === 'failed') throw new Error(`${name} could not be added: ${m.error}`);
    await new Promise((r) => setTimeout(r, 1000));
  }
  throw new Error(`Timed out adding ${name} to the library`);
}
// Load a model on a node; adds it to the library first (nodes download it) when it is only in the catalog.
async function loadOnNode(nodeId, name, addFirst) {
  try {
    if (addFirst) { toast('Adding to the library…'); await post('/api/models', { catalog_id: name, storage: 'direct' }).catch((e) => { if (!/already in the library/.test(e.message)) throw e; }); await waitModelReady(name); }
    await post(`/api/nodes/${nodeId}/load`, { name });
    toast('Setting up the model; progress shows on the node card');
  } catch (e) { toast(e.message, 6000); throw e; }
  refresh();
}
function openPick(name, isCatalog) {
  const entry = isCatalog ? models.catalog.find((c) => c.id === name) : models.models.find((m) => m.name === name);
  $('pick-model').textContent = entry ? (entry.label || entry.name) : name;
  $('pick-err').classList.add('hidden');
  const fits = ((isCatalog ? models.catalog_fits : models.fits) || {})[name] || [];
  const rank = { vram: 0, split: 1, ram: 2, no: 3 };
  setHTML('pick-list', fits.length ? [...fits].sort((a, b) => rank[a.fit] - rank[b.fit] || a.name.localeCompare(b.name)).map((f) => {
    const n = state.nodes.find((x) => x.id === f.node_id), cur = n && n.heartbeat && n.heartbeat.loaded_model;
    const rec = (models.recommended || {})[f.node_id];
    const why = !f.online ? 'offline' : !f.can_load ? 'busy' : f.fit === 'no' ? 'too big for this node' : '';
    return `<div class="pick-row"><div class="grow"><b>${esc(f.name)}</b> <span class="pill ${FIT_CLS[f.fit]}">${FIT_TEXT[f.fit]}</span>${rec && rec.name === name ? ' <span class="pill st-ready">Recommended</span>' : ''}
      <div class="sub wrap u22">needs about ${bytes(f.need_bytes)}${cur && cur !== name ? ` · replaces ${esc(cur)}` : ''}${why ? ' · ' + why : ''}</div></div>
      <button class="small primary" data-pick-node="${f.node_id}" ${why ? 'disabled' : ''}>Load</button></div>`;
  }).join('') : '<div class="empty">No nodes yet.</div>');
  $('pick-list').onclick = async (e) => {
    const b = e.target.closest('[data-pick-node]'); if (!b) return;
    try { await loadOnNode(b.dataset.pickNode, name, isCatalog); close('modal-pick'); }
    catch (err) { $('pick-err').textContent = err.message; $('pick-err').classList.remove('hidden'); }
  };
  open('modal-pick');
}
const isDirect = (m) => m.storage === 'direct';
function storageLabel(m) {
  return isDirect(m) ? '<span title="Each node downloads it from Hugging Face when you load it">Nodes download</span>'
    : '<span title="Downloaded once to the controller and copied to nodes over your network">On controller</span>';
}
const FIT_TEXT = { vram: 'fits in VRAM', split: 'split VRAM + RAM (slower)', ram: 'runs on CPU', no: 'does not fit' };
const FIT_CLS = { vram: 'st-done', split: 'st-loading', ram: 'st-loading', no: 'st-failed' };
function fitBadges(fits) {
  if (!fits || !fits.length) return '';
  return `<div class="fit-badges u23">${fits.map((f) => `<span class="pill ${FIT_CLS[f.fit]}" title="${esc(f.name)}: ${FIT_TEXT[f.fit]}; needs about ${bytes(f.need_bytes)}${f.online ? '' : ' (node offline)'}">${esc(f.name)} · ${FIT_TEXT[f.fit]}</span>`).join('')}</div>`;
}
function renderModels() {
  if (!models) return;
  setHTML('models-disk', `${bytes(models.disk_free)} free on the controller`);
  const holders = (name) => state.nodes.filter((n) => n.hello && (n.hello.models || []).some((m) => m.name === name)).map(nodeName);
  const list = models.models;
  setHTML('models-list', !list.length ? `<div class="empty">No models yet. Pick one below.</div>` :
    `<div class="table-wrap"><table><thead><tr><th>Model</th><th>Size</th><th>Tier</th><th>Stored</th><th>Status</th><th>On nodes</th><th></th></tr></thead><tbody>${list.map((m) => {
      const pct = m.bytes_total ? 100 * m.bytes_done / m.bytes_total : 0;
      const status = m.status === 'downloading' ? `<div class="prog"><span>downloading to controller ${bytes(m.bytes_done)} of ${bytes(m.bytes_total)}</span>${bar(pct, 'good')}</div>`
        : m.status === 'resolving' ? pill('loading', 'checking Hugging Face')
        : m.status === 'failed' ? `${pill('failed')} <span class="muted">${esc(m.error)}</span>` : pill('ready');
      const h = holders(m.name), nm = esc(m.name);
      const acts = [];
      if (m.status === 'failed') acts.push(`<button class="small" data-act="model-retry" data-name="${nm}">Retry</button>`);
      if (isDirect(m) && m.status === 'ready') acts.push(`<button class="small" data-act="model-store" data-name="${nm}" title="Download it to the controller too, so nodes copy it over your network instead of from the internet">Store on controller</button>`);
      if (!isDirect(m) && m.status === 'ready') acts.push(`<button class="small" data-act="model-release" data-name="${nm}" title="Copy it to the node with the most free disk, then free ${bytes(m.bytes_total)} on the controller. Other nodes copy it from that node.">Release to a node</button>`);
      if (!isDirect(m) && m.status !== 'failed') acts.push(`<button class="small" data-act="model-unstore" data-name="${nm}" title="Free ${bytes(m.bytes_total)} on the controller; nodes download it from Hugging Face instead">Free controller space</button>`);
      const vf = ((models.fits || {})[m.name] || []).filter((f) => f.fit === 'vram' && f.can_load);
      if (m.status === 'ready' && vf.length) acts.unshift(`<button class="small primary" data-act="model-load-fit" data-name="${nm}" title="Loads it on: ${esc(vf.map((f) => f.name).join(', '))}. Replaces any model those nodes have loaded.">Load on ${vf.length} node${vf.length > 1 ? 's' : ''} it fits</button>`);
      if (m.status === 'ready') acts.unshift(`<button class="small" data-act="pick-model" data-name="${nm}">Load on…</button>`);
      acts.push(`<button class="small danger" data-act="model-delete" data-name="${nm}">${m.status === 'downloading' ? 'Cancel' : 'Remove'}</button>`);
      return `<tr><td><b>${esc(m.label || m.name)}</b>${m.vision ? ' <span class="pill st-ready" title="Reads images: loaded with its vision projector">Vision</span>' : ''}${recFor(m.name)}<div class="sub u22">${esc(m.repo)}</div>${m.status === 'ready' ? fitBadges((models.fits || {})[m.name]) : ''}</td><td>${m.bytes_total ? bytes(m.bytes_total) : '—'}</td><td><select class="small" data-tier="${nm}" aria-label="Model strength" title="Used to route hard subtasks to the strongest model">${[['', 'auto (' + (m.tier_guess || 'medium') + ')'], ['small', 'Small'], ['medium', 'Medium'], ['large', 'Large']].map(([v, l]) => `<option value="${v}" ${(m.tier || '') === v ? 'selected' : ''}>${l}</option>`).join('')}</select></td><td>${storageLabel(m)}</td><td>${status}</td>
        <td>${h.length ? h.map(esc).join(', ') : '<span class="muted">none</span>'}</td><td class="r"><div class="btns u24">${acts.join('')}</div></td></tr>`;
    }).join('')}</tbody></table></div>`);
  const have = {}; list.forEach((m) => { have[m.name] = m; });
  const catCard = (c) => {
    const m = have[c.id];
    const btn = m ? (m.status === 'downloading' ? `<button disabled>Downloading ${m.bytes_total ? Math.floor(100 * m.bytes_done / m.bytes_total) : 0}%</button>`
        : m.status === 'failed' ? `<button data-act="model-retry" data-name="${c.id}">Retry</button>` : `<button disabled>In library · ${isDirect(m) ? 'nodes download' : 'on controller'}</button>`)
      : `<button class="primary" data-act="pick-model" data-name="${c.id}" data-catalog="1" title="Adds it to the library (nodes download it) and loads it on the node you pick">Add and load on…</button>
         <button data-act="catalog-add" data-id="${c.id}" data-storage="direct" title="Each node downloads it from Hugging Face when you load it; uses no controller space">Add · nodes download</button>
         <button data-act="catalog-add" data-id="${c.id}" data-storage="controller" title="Download once to the controller (${bytes(c.bytes)}), then copy to nodes over your network">Store on controller</button>`;
    return `<div class="card"><span class="for">${esc(c.for)}</span><h3>${esc(c.label)}${recFor(c.id)}${c.mmproj ? ' <span class="pill st-ready" title="Reads images">Vision</span>' : ''}</h3><p>${esc(c.notes)}</p><div class="btns">${btn}</div><span class="muted u25">${bytes(c.bytes)} · needs ~${c.min_ram_gb} GB RAM+VRAM</span>${fitBadges((models.catalog_fits || {})[c.id])}</div>`;
  };
  setHTML('catalog', models.catalog.map(catCard).join(''));
  const byId = {}; models.catalog.forEach((c) => { byId[c.id] = c; });
  setHTML('suggested', (models.suggested || []).length ? models.suggested.map((sg) => byId[sg.id] && catCard(byId[sg.id])).join('') : '<div class="empty">No nodes are connected yet, so nothing can be sized.</div>');
  const rts = models.runtimes || [];
  setHTML('runtimes', !rts.length ? `<div class="empty">None downloaded yet.</div>` :
    `<div class="table-wrap"><table><thead><tr><th>Build</th><th>For</th><th>Size</th><th>Downloaded</th></tr></thead><tbody>${rts.map((r) => `<tr><td>${esc(r.build)}</td><td>${esc(r.variant)}</td><td>${bytes(r.bytes)}</td><td>${ago(r.downloaded)}</td></tr>`).join('')}</tbody></table></div>`);
}
let hfRepo = '', hfGroups = [];
$('hf-list').onclick = async () => {
  setHTML('hf-files', '<span class="muted">Loading…</span>');
  try {
    const r = await api('/api/hf/files?repo=' + encodeURIComponent($('hf-repo').value.trim()));
    hfRepo = r.repo; hfGroups = r.groups;
    setHTML('hf-files', !r.groups.length ? '<span class="muted">No .gguf files in that repo.</span>' :
      `<div class="table-wrap"><table><tbody>${r.mmproj && r.mmproj.length ? `<tr><td colspan="3"><label class="muted">Vision projector (optional, makes the model read images) <select id="hf-mmproj"><option value="">none</option>${r.mmproj.map((f) => `<option value="${esc(f)}">${esc(f)}</option>`).join('')}</select></label></td></tr>` : ''}${r.groups.map((g, i) => `<tr><td class="mono">${esc(g.name)}${g.files.length > 1 ? ` <span class="muted">(${g.files.length} parts)</span>` : ''}${fitBadges(g.fits)}</td><td>${bytes(g.bytes)}</td><td class="r"><div class="btns u24"><button class="small primary" data-act="hf-add" data-i="${i}" data-storage="direct">Add · nodes download</button><button class="small" data-act="hf-add" data-i="${i}" data-storage="controller">Store on controller</button></div></td></tr>`).join('')}</tbody></table></div>`);
  } catch (e) { setHTML('hf-files', `<span class="error-msg">${esc(e.message)}</span>`); }
};
$('hf-repo').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('hf-list').click(); });
$('rt-check').onclick = async () => {
  try { const r = await post('/api/runtime/check'); toast(`Newest llama.cpp with binaries: ${r.latest}. Nodes pick it up the next time a model loads.`, 5000); }
  catch (e) { toast(e.message); }
};

// ---------- updates ----------
const UPD_BUSY = ['sent', 'downloading', 'installing', 'restarting'];
let ctlUpdating = false;
function renderUpdates() {
  renderGitHub();
  const rows = [];
  const cu = state.controller_update_to;
  rows.push(`<tr><td><span class="dot on"></span></td><td><b>Controller</b></td><td>${esc(state.platform)}</td><td>v${esc(state.version)}</td>
    <td>${cu ? 'v' + esc(cu) : '<span class="muted">up to date</span>'}</td>
    <td>${ctlUpdating ? `<div class="prog"><span>installing and restarting…</span>${bar(100, 'good')}</div>` : ''}</td>
    <td class="r">${cu && !ctlUpdating ? `<button class="small primary" data-act="ctl-update">Update</button>` : ''}
      ${state.controller_rollback_to && !ctlUpdating ? `<button class="small" data-act="ctl-rollback" title="Restart the controller on the previous version">Roll back to v${esc(state.controller_rollback_to)}</button>` : ''}</td></tr>`);
  for (const n of state.nodes) {
    const h = n.hello || {}, u = n.update;
    let prog = '';
    if (u && UPD_BUSY.includes(u.state) && (Date.now() - new Date(u.updated)) < 600000) {
      const pct = u.state === 'downloading' && u.bytes_total ? 100 * u.bytes_done / u.bytes_total : u.state === 'sent' ? 2 : u.state === 'downloading' ? 5 : u.state === 'installing' ? 85 : 95;
      const what = { sent: 'requested', downloading: u.bytes_total ? `downloading ${bytes(u.bytes_done)} of ${bytes(u.bytes_total)}` : 'downloading', installing: 'verifying and installing', restarting: 'restarting' }[u.state];
      prog = `<div class="prog"><span>${esc(what)}</span>${bar(pct, 'good')}</div>`;
    } else if (u && u.state === 'failed') prog = `<span class="error-msg u2">${esc(u.error)}</span>`;
    else if (u && u.state === 'done' && (Date.now() - new Date(u.updated)) < 600000) prog = `<span class="st-done">✓ updated</span>`;
    const busy = u && UPD_BUSY.includes(u.state);
    const canUpdate = n.update_to && n.online && !busy;
    rows.push(`<tr><td><span class="dot ${n.online ? 'on' : ''}" title="${n.online ? 'service online' : 'offline'}"></span></td><td><b>${esc(nodeName(n))}</b><div class="sub u2">${n.online ? 'online' : n.last_seen ? 'offline · seen ' + ago(n.last_seen) : 'never connected'}</div></td>
      <td>${esc(h.os ? h.os + '-' + h.arch : '?')}</td><td>${h.agent_version ? 'v' + esc(h.agent_version) : '?'}</td>
      <td>${n.update_to ? 'v' + esc(n.update_to) : '<span class="muted">' + (state.releases.some((r) => r.platform === (h.os + '-' + h.arch)) ? 'up to date' : 'no release uploaded') + '</span>'}</td>
      <td>${prog}</td>
      <td class="r">${canUpdate ? `<button class="small primary" data-act="node-update" data-id="${n.id}" ${n.running_jobs ? 'disabled title="Waits until running jobs finish"' : ''}>Update</button>` : ''}
        ${h.rollback_to && n.online && !busy ? `<button class="small" data-act="node-rollback" data-id="${n.id}" title="Restart this node on the previous version">Roll back to v${esc(h.rollback_to)}</button>` : ''}</td></tr>`);
  }
  setHTML('upd-table', `<table><thead><tr><th class="u26"></th><th>Machine</th><th>Platform</th><th>Running</th><th>Available</th><th>Progress</th><th></th></tr></thead><tbody>${rows.join('')}</tbody></table>`);
  const outdated = state.nodes.filter((n) => n.online && n.update_to && !(n.update && UPD_BUSY.includes(n.update.state))).length;
  const b = $('btn-upd-all');
  b.disabled = !outdated; b.className = outdated ? 'primary' : '';
  b.textContent = outdated ? `Update ${outdated} node${outdated === 1 ? '' : 's'}` : 'All nodes up to date';
  const rel = state.releases || [];
  setHTML('releases', rel.length ? `<div class="table-wrap"><table><thead><tr><th>Platform</th><th>Version</th><th>Size</th><th>SHA-256</th><th>Uploaded</th><th></th></tr></thead><tbody>${
    rel.map((r) => `<tr><td>${esc(r.platform)}</td><td>v${esc(r.version)}</td><td>${bytes(r.bytes)}</td><td class="mono" title="${esc(r.sha256)}">${esc(r.sha256.slice(0, 12))}…</td><td>${ago(r.uploaded)}</td>
      <td class="r"><button class="small danger" data-act="del-release" data-platform="${esc(r.platform)}">Delete</button></td></tr>`).join('')}</tbody></table></div>` : '');
}
function renderGitHub() {
  const g = state.github_updates || {};
  let t;
  if (g.busy) t = g.busy.startsWith('checking') ? 'Checking GitHub…' : 'Downloading ' + g.busy.replace('downloading ', '') + '…';
  else {
    const mode = !g.enabled ? 'Automatic checks are off (Settings)' : g.auto_install ? 'Checks every 6 hours and installs automatically' : 'Checks every 6 hours; you click Update';
    t = `${mode}. ` + (g.checked && g.checked > '2000' ? `Last checked ${ago(g.checked)}${g.latest ? ', latest is v' + g.latest : ''}.` : 'Not checked yet.');
    if (g.hold) t += ` Auto-install skips v${g.hold} and older after a rollback.`;
  }
  $('gh-info').textContent = t;
  if (g.error) { const e = document.createElement('span'); e.className = 'error-msg'; e.style.display = 'block'; e.textContent = g.error; $('gh-info').appendChild(e); }
  $('btn-gh-check').disabled = $('btn-gh-install').disabled = !!g.busy;
}
async function ghAction(path, msg) {
  try { await post(path); toast(msg); } catch (e) { toast(e.message, 6000); }
  refresh();
}
$('btn-gh-check').onclick = () => ghAction('/api/github-updates/check', 'Checked GitHub');
$('btn-gh-install').onclick = () => { if (confirm('Check GitHub and update idle nodes and the controller to the latest release now?')) ghAction('/api/github-updates/install', 'Installing from GitHub'); };
$('btn-upd-all').onclick = async () => {
  try { const r = await post('/api/update-all'); toast(`Started ${r.started} update(s)`); refresh(); } catch (e) { toast(e.message); }
};
async function rollbackController() {
  const to = state.controller_rollback_to;
  if (!confirm(`Roll the controller back to v${to}? It restarts on that version and stays there until you install an update.`)) return;
  try { await post('/api/controller/rollback'); } catch (e) { toast(e.message); return; }
  ctlUpdating = true; renderUpdates(); toast('Controller restarting…');
  for (let i = 0; i < 60; i++) {
    await new Promise((r) => setTimeout(r, 1000));
    try {
      const r = await fetch('/api/state', { credentials: 'same-origin' });
      if (r.ok && (await r.json()).version === to) { location.reload(); return; }
    } catch (e) {}
  }
  ctlUpdating = false;
  toast('The controller did not come back on the old version; check journalctl -u outpost-controller', 6000);
}

async function updateController() {
  if (!confirm(`Update the controller to v${state.controller_update_to} and restart it? Tasks pause for a moment; running jobs keep going on their nodes.`)) return;
  const target = state.controller_update_to;
  try { await post('/api/controller/update'); } catch (e) { toast(e.message); return; }
  ctlUpdating = true; renderUpdates(); toast('Controller restarting…');
  for (let i = 0; i < 60; i++) {
    await new Promise((r) => setTimeout(r, 1000));
    try {
      const r = await fetch('/api/state', { credentials: 'same-origin' });
      if (r.ok && (await r.json()).version === target) { location.reload(); return; }
    } catch (e) {}
  }
  ctlUpdating = false;
  toast('The controller did not come back on the new version; check journalctl -u outpost-controller', 6000);
}
function uploadRelease(f, onProgress) {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open('POST', '/api/releases');
    x.withCredentials = true;
    x.setRequestHeader('X-Filename', f.name);
    x.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(Math.round(100 * e.loaded / e.total)); };
    x.onerror = () => reject(new Error('network error'));
    x.onload = () => {
      let body = {}; try { body = JSON.parse(x.responseText); } catch (e) {}
      x.status >= 200 && x.status < 300 ? resolve(body) : reject(new Error(body.error || x.statusText || 'upload failed'));
    };
    x.send(f);
  });
}
async function uploadFiles(files) {
  const n = files.length;
  for (const [i, f] of files.entries()) {
    const tag = (n > 1 ? `[${i + 1}/${n}] ` : '') + f.name;
    toast(`Uploading ${tag}…`, 60000);
    try {
      const body = await uploadRelease(f, (p) => toast(`Uploading ${tag}… ${p}%`, 60000));
      toast(`${tag}: ${body.platform} v${body.version}`);
    } catch (e) { toast(`${tag}: ${e.message}`, 4000); await new Promise((r) => setTimeout(r, 2500)); }
  }
  refresh();
}
(() => {
  const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes('Files');
  const ov = $('drop-overlay');
  let depth = 0;
  const hide = () => { depth = 0; ov.classList.add('hidden'); };
  // Whole page is the drop zone while Updates is showing; text drags are ignored.
  window.addEventListener('dragenter', (e) => { if (page !== 'updates' || !hasFiles(e)) return; e.preventDefault(); depth++; ov.classList.remove('hidden'); });
  window.addEventListener('dragover', (e) => { if (page !== 'updates' || !hasFiles(e)) return; e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; });
  window.addEventListener('dragleave', (e) => { if (page !== 'updates' || !hasFiles(e)) return; if (--depth <= 0) hide(); });
  window.addEventListener('drop', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault(); // never let the browser navigate to a dropped file
    const onUpdates = page === 'updates';
    hide();
    if (onUpdates) uploadFiles([...e.dataTransfer.files]);
  });
  window.addEventListener('blur', hide);
  $('drop-input').onchange = (e) => { uploadFiles([...e.target.files]); e.target.value = ''; };
})();

// ---------- settings ----------
// Section list on the left follows the scroll position; a click scrolls to the section without touching the #/settings route.
(function settingsNav() {
  const links = [...document.querySelectorAll('#set-nav a')];
  if (!links.length) return;
  // Section ids differ from data-sec for 'model': #set-model is the Claude model <select>, so never build ids from data-sec alone.
  const secs = links.map((a) => document.querySelector('section.set-sec' + a.getAttribute('href')));
  let lock = 0;
  const mark = (i) => links.forEach((a, j) => a.classList.toggle('active', i === j));
  const spy = () => {
    if (Date.now() < lock || page !== 'settings') return;
    let cur = 0;
    secs.forEach((s, i) => { if (s.getBoundingClientRect().top <= (matchMedia('(max-width: 860px)').matches ? 140 : 90)) cur = i; });
    if (innerHeight + scrollY >= document.documentElement.scrollHeight - 2) cur = secs.length - 1;
    mark(cur);
  };
  addEventListener('scroll', spy, { passive: true });
  addEventListener('resize', spy);
  links.forEach((a, i) => a.addEventListener('click', (e) => {
    e.preventDefault();
    mark(i); lock = Date.now() + 800;
    secs[i].scrollIntoView({ behavior: 'smooth', block: 'start' });
  }));
  mark(0);
})();
async function loadSettings() {
  try { settings = await api('/api/settings'); } catch (e) { return; }
  $('set-key-state').innerHTML = settings.has_anthropic_key ? `<span class="st-done">· saved (${esc(settings.anthropic_key_hint)})</span>` : '<span class="muted">· not set</span>';
  $('set-or-key-state').innerHTML = settings.has_openrouter_key ? `<span class="st-done">· saved (${esc(settings.openrouter_key_hint)})</span>` : '<span class="muted">· not set</span>';
  $('set-or-model').value = settings.openrouter_model || '';
  if (settings.has_openrouter_key) loadOpenRouterModels();
  $('set-hf-state').innerHTML = settings.has_hf_token ? '<span class="st-done">· saved</span>' : '<span class="muted">· not set</span>';
  $('set-model').innerHTML = claudeModelOptions(settings.claude_model);
  $('set-cap').value = settings.default_cap_usd;
  $('set-autoresume').checked = !!settings.auto_resume;
  $('set-bestofn').value = settings.best_of_n || 'hard';
  $('set-instr').value = settings.instructions || '';
  $('set-ghrepo').value = settings.github_repo ? 'https://github.com/' + settings.github_repo : '';
  $('set-ghrepo').placeholder = 'https://github.com/' + (settings.github_repo_default || '64fedoras/outpost');
  $('set-ghtoken').value = ''; $('set-ghtest-out').textContent = '';
  $('set-ghtoken-state').innerHTML = settings.github_token_set ? '<span class="st-done">· token set</span>' : '<span class="muted">· not set</span>';
  $('set-ghupd').checked = settings.github_updates !== false; $('set-ghauto').checked = !!settings.github_auto_install;
  $('set-idle').value = settings.idle_unload || '';
  $('set-sandbox').value = settings.sandbox || 'basic';
  $('set-idle-now').textContent = settings.idle_unload_effective ? 'Nodes unload after: ' + settings.idle_unload_effective + (settings.idle_unload ? '' : ' (default)') : '';
  $('set-pooling').checked = !!settings.pooling_enabled;
  $('set-pooling-notice').classList.toggle('hidden', !settings.pooling_notice);
  $('set-agent').checked = settings.agent_mode !== false; $('set-websearch').checked = !!settings.web_search; $('set-webprivate').checked = !!settings.web_allow_private;
  if (!state) state = await api('/api/state').catch(() => null);
  if (state) $('set-planner').innerHTML = plannerOptions(settings.default_planner);
  loadNotifySettings();
  loadSecurity();
  Guard.settingsReset();
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
const NOTIFY_EVENTS = [['question', 'The planner asked a question'], ['plan', 'A plan is ready to approve'], ['done', 'A task finished'], ['failed', 'A task failed'], ['cap', 'The spending cap paused a task'], ['node_offline', 'A node has been offline for 5 minutes']];
function loadNotifySettings() {
  setHTML('nf-events', NOTIFY_EVENTS.map(([id, label]) => `<label class="check"><input type="checkbox" data-nf="${id}" ${settings.notify_events[id] ? 'checked' : ''}> ${esc(label)}</label>`).join(''));
  $('nf-browser').checked = !!settings.notify_browser;
  $('nf-ntfy').value = settings.notify_ntfy_url || ''; $('nf-webhook').value = settings.notify_webhook_url || ''; $('nf-public').value = settings.notify_public_url || '';
  $('nf-ntfy-token-state').textContent = settings.notify_has_ntfy_token ? '· saved' : '· not set';
  $('nf-browser-state').textContent = 'Notification' in window ? (Notification.permission === 'denied' ? '· blocked in this browser' : '') : '· not supported here';
}
function notifyBody() {
  const events = {}; document.querySelectorAll('[data-nf]').forEach((b) => { events[b.dataset.nf] = b.checked; });
  const n = { browser: $('nf-browser').checked, ntfy_url: $('nf-ntfy').value, webhook_url: $('nf-webhook').value, public_url: $('nf-public').value, events };
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
let notifyLast = null;
async function pollNotifications() {
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
    setHTML('tokens-list', tk.map((t) => `<div class="rowline"><span>${esc(t.name)} <span class="muted">· created ${ago(t.created)}${t.last_used && !t.last_used.startsWith('0001') ? ' · last used ' + ago(t.last_used) : ' · never used'}</span></span><button class="danger" data-tok="${esc(t.id)}">Revoke</button></div>`).join('') || '<p class="muted">No tokens yet.</p>');
    let info = `Fingerprint <code>${esc(tl.fingerprint)}</code>`;
    if (tl.subject) info += `<br>${tl.self_signed ? 'Self-signed' : 'Issued by ' + esc(tl.issuer)} for ${esc(tl.subject)}; expires ${new Date(tl.expires).toLocaleDateString()}.`;
    if ((tl.needs_repin || []).length) info += `<br><b>Offline during the change, run <code>outpost repin --fingerprint ${esc(tl.fingerprint)}</code> on: ${tl.needs_repin.map(esc).join(', ')}</b>`;
    setHTML('tls-info', info);
    if (settings) {
      $('bk-on').checked = !!settings.backup_enabled; $('bk-dir').value = settings.backup_dir || '';
      $('bk-pass-state').textContent = settings.has_backup_pass ? '· saved' : '· not set';
    }
  } catch (e) {}
}
$('sessions-list').onclick = async (e) => { const id = e.target.dataset.sess; if (id) { await api('/api/sessions/' + id, { method: 'DELETE' }).catch((x) => toast(x.message)); loadSecurity(); } };
$('tokens-list').onclick = async (e) => { const id = e.target.dataset.tok; if (id && confirm('Revoke this token? Scripts using it stop working.')) { await api('/api/apitokens/' + id, { method: 'DELETE' }).catch((x) => toast(x.message)); loadSecurity(); } };
$('sess-all').onclick = async () => { if (confirm('Sign out every browser, including this one?')) { await post('/api/sessions/logout-all'); location.reload(); } };
$('tok-create').onclick = async () => {
  const name = $('tok-name').value.trim(); if (!name) return;
  try {
    const t = await post('/api/apitokens', { name }); $('tok-name').value = '';
    const n = $('tok-new'); n.classList.remove('hidden'); n.innerHTML = `Copy this now, it is shown only once:<div class="codebox"><span id="tok-val">${esc(t.token)}</span><button id="tok-copy">Copy</button></div>`;
    $('tok-copy').onclick = async () => { try { await navigator.clipboard.writeText(t.token); toast('Copied'); } catch (e) { toast('Select the token and press Ctrl+C'); } };
    loadSecurity();
  } catch (e) { toast(e.message); }
};
$('tls-save').onclick = async () => {
  if (!confirm('Replace the HTTPS certificate? Connected nodes are re-pinned automatically; the page will reload.')) return;
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
$('set-key-clear').onclick = () => { if (confirm('Remove the Claude API key?')) saveSettings({ anthropic_key: '' }, 'API key removed'); };
$('set-or-key-save').onclick = () => { const v = $('set-or-key').value.trim(); if (!v) return; $('set-or-key').value = ''; orLoaded = false; saveSettings({ openrouter_key: v }, 'OpenRouter key saved'); };
$('set-or-key-clear').onclick = () => { if (confirm('Remove the OpenRouter API key?')) saveSettings({ openrouter_key: '' }, 'OpenRouter key removed'); };
$('set-or-model-save').onclick = () => saveSettings({ openrouter_model: $('set-or-model').value.trim() }, 'OpenRouter model saved');
$('set-hf-save').onclick = () => { const v = $('set-hf').value.trim(); if (!v) return; $('set-hf').value = ''; saveSettings({ hf_token: v }, 'Token saved'); };
$('set-hf-clear').onclick = () => saveSettings({ hf_token: '' }, 'Token removed');
$('set-instr-save').onclick = () => saveSettings({ instructions: $('set-instr').value }, 'Instructions saved');
$('set-ghupd-save').onclick = () => {
  const body = { github_updates: $('set-ghupd').checked, github_auto_install: $('set-ghauto').checked, github_repo: $('set-ghrepo').value.trim() };
  const cur = settings.github_repo || settings.github_repo_default || '';
  if (body.github_repo && !body.github_repo.toLowerCase().endsWith('/' + cur.toLowerCase()) && !confirm('Trust ' + body.github_repo + ' to supply updates for this controller and all nodes?')) return;
  const tok = $('set-ghtoken').value.trim();
  if (tok) body.github_token = tok;
  $('set-ghtoken').value = '';
  saveSettings(body, 'GitHub update settings saved');
};
$('set-ghtoken-clear').onclick = () => saveSettings({ github_token: '' }, 'GitHub token removed');
$('set-ghtest').onclick = async () => {
  const out = $('set-ghtest-out'); out.className = 'hint'; out.textContent = 'Testing…';
  try {
    const r = await post('/api/github-updates/test', { repo: $('set-ghrepo').value.trim(), token: $('set-ghtoken').value.trim() });
    out.className = 'st-done'; out.textContent = r.message;
  } catch (e) { out.className = 'error-msg'; out.textContent = e.message; }
};
$('set-sandbox-save').onclick = () => saveSettings({ sandbox: $('set-sandbox').value }, 'Sandbox saved');
$('set-idle-save').onclick = () => saveSettings({ idle_unload: $('set-idle').value.trim() }, 'Idle unload saved');
$('set-pooling-save').onclick = () => saveSettings({ pooling_enabled: $('set-pooling').checked }, $('set-pooling').checked ? 'Pooling enabled' : 'Pooling off; pools dissolved');
$('set-save').onclick = () => saveSettings({ claude_model: $('set-model').value, default_cap_usd: parseFloat($('set-cap').value) || 0, default_planner: $('set-planner').value, agent_mode: $('set-agent').checked, web_search: $('set-websearch').checked, web_allow_private: $('set-webprivate').checked, auto_resume: $('set-autoresume').checked, best_of_n: $('set-bestofn').value }, 'Defaults saved');
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

// ---------- modals + delegated events ----------
function open(id) { $(id).classList.remove('hidden'); }
function close(id) { $(id).classList.add('hidden'); if (id === 'modal-job') { clearTimeout(jobTimer); openJobId = null; } }
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
      case 'rm-node-model': if (confirm(`Delete ${el.dataset.name} from this node's disk?`)) { await api(`/api/nodes/${id}/models/${encodeURIComponent(el.dataset.name)}`, { method: 'DELETE' }); toast('Deleting'); } break;
      case 'clean': openClean(id); break;
      case 'rename': renameNode(id); break;
      case 'idle': setNodeIdle(id); break;
      case 'restart': {
        const n = nodeById(id);
        const jobs = n.running_jobs ? ` ${n.running_jobs} running job(s) will be cancelled.` : '';
        if (!confirm(`Restart Outpost on ${nodeName(n)}?${jobs} Its model is loaded again afterwards.`)) return;
        await post(`/api/nodes/${id}/restart`); toast('Restarting'); break;
      }
      case 'remove': removeNode(id); break;
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
      case 'model-release': if (confirm(`Copy ${el.dataset.name} to a node, then delete it from the controller? It stays in the library; other nodes copy it from that node (or download it from Hugging Face if that node is gone).`)) { const r = await post(`/api/models/${encodeURIComponent(el.dataset.name)}/release`); toast(r && r.pushed ? `Copying to ${r.node}; the controller copy is freed when it arrives` : `Freed controller space; ${r && r.node} already has it`); } break;
      case 'model-unstore': if (confirm(`Delete ${el.dataset.name}'s files from the controller? It stays in the library; nodes that need it download it from Hugging Face. Copies already on nodes stay.`)) { await post(`/api/models/${encodeURIComponent(el.dataset.name)}/unstore`); toast('Freed controller space'); } break;
      case 'model-retry': await post(`/api/models/${encodeURIComponent(el.dataset.name)}/retry`); toast('Resuming download'); break;
      case 'model-delete': if (confirm(`Remove ${el.dataset.name} from the library (and its files from the controller, if stored there)? Copies already on nodes stay; delete those on the node card.`)) await api(`/api/models/${encodeURIComponent(el.dataset.name)}`, { method: 'DELETE' }); break;
      case 'install-tool': await post(`/api/nodes/${id}/toolchain`, { tool: el.dataset.tool }); toast('Downloading Go to the controller, then the node'); break;
      case 'node-update': await post(`/api/nodes/${id}/update`); toast('Update sent'); break;
      case 'ctl-update': await updateController(); return;
      case 'ctl-rollback': await rollbackController(); return;
      case 'node-rollback': {
        const n = nodeById(id);
        if (!confirm(`Roll ${nodeName(n)} back to v${n.hello.rollback_to}? It restarts on that version and stays there until you update it again.`)) return;
        await post(`/api/nodes/${id}/rollback`); toast('Rolling back'); break;
      }
      case 'del-release': if (confirm(`Delete the uploaded ${el.dataset.platform} release?`)) await api(`/api/releases/${el.dataset.platform}`, { method: 'DELETE' }); break;
      case 'view-file': viewFile(el.dataset.path); return;
      case 'starter': $('nt-prompt').value = STARTERS[+el.dataset.i][1]; $('nt-prompt').focus(); return;
      case 'project-delete': case 'project-save': case 'project-use': await projectAction(act, el); break;
      default: if (act.startsWith('task-')) { await taskAction(act, el); return; }
    }
  } catch (x) { toast(x.message, 4000); }
  refresh();
});

api('/api/state').then(showApp).catch(() => {});

// ---------- image attachments (row 19) ----------
// Images become data URIs in memory and go with the message; the controller
// keeps them with the task. Big photos are scaled down first (Claude takes 5 MB per image).
const IMG_MAX = 5, IMG_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/gif'];
function readImage(file) {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onerror = () => reject(new Error(file.name + ': could not read'));
    fr.onload = () => {
      if (file.size <= 4 * 1024 * 1024 || file.type === 'image/gif') return resolve({ name: file.name || 'image', data: fr.result });
      const img = new Image();
      img.onerror = () => resolve({ name: file.name, data: fr.result });
      img.onload = () => {
        const k = Math.min(1, 2048 / Math.max(img.width, img.height)), c = document.createElement('canvas');
        c.width = Math.round(img.width * k); c.height = Math.round(img.height * k);
        c.getContext('2d').drawImage(img, 0, 0, c.width, c.height);
        resolve({ name: (file.name || 'image').replace(/\.\w+$/, '') + '.jpg', data: c.toDataURL('image/jpeg', 0.85) });
      };
      img.src = fr.result;
    };
    fr.readAsDataURL(file);
  });
}
// visionProblem says why a planner cannot read images, or '' if it can (or we cannot tell here; the controller checks again).
function visionProblem(planner) {
  if (!planner || !planner.startsWith('node:')) return '';
  const n = state && state.nodes.find((x) => 'node:' + x.id === planner), hb = n && n.heartbeat;
  const name = hb && hb.loaded_model;
  if (!name) return 'The local planner has no model loaded, so it cannot read images.';
  const m = models && models.models.find((x) => x.name === name);
  if (m && m.vision) return '';
  if (!models) return '';
  return `${name} cannot read images. Load a Vision model (Qwen3.6 or Gemma 4 from the Models page) on that node, or plan with Claude or OpenRouter.`;
}
function imageAttach(pre, getPlanner, warnEl) {
  let items = [];
  const strip = $(pre + '-imgs'), file = $(pre + '-file');
  function refresh() {
    setHTML(strip, items.map((it, i) => `<span class="img-chip"><img src="${it.data}" alt=""><button type="button" data-i="${i}" aria-label="Remove image">×</button></span>`).join(''));
    const w = items.length ? visionProblem(getPlanner()) : '';
    warnEl.textContent = w; warnEl.classList.toggle('hidden', !w);
  }
  async function add(files) {
    for (const f of files) {
      if (!IMG_TYPES.includes(f.type)) { toast(`${f.name || 'That file'} is not a PNG, JPEG, WebP or GIF image`, 4000); continue; }
      if (items.length >= IMG_MAX) { toast(`At most ${IMG_MAX} images per message`, 4000); break; }
      try { items.push(await readImage(f)); } catch (e) { toast(e.message, 4000); }
    }
    refresh();
  }
  $(pre + '-attach').onclick = () => file.click();
  file.onchange = () => { add([...file.files]); file.value = ''; };
  strip.onclick = (e) => { const b = e.target.closest('button[data-i]'); if (b) { items.splice(+b.dataset.i, 1); refresh(); } };
  const box = pre === 'nt' ? $('nt-prompt') : $('tm-text');
  box.addEventListener('paste', (e) => { const fs = [...(e.clipboardData ? e.clipboardData.files : [])].filter((f) => f.type.startsWith('image/')); if (fs.length) { e.preventDefault(); add(fs); } });
  const comp = box.closest('.composer');
  comp.addEventListener('dragover', (e) => { if ([...(e.dataTransfer.types || [])].includes('Files')) e.preventDefault(); });
  comp.addEventListener('drop', (e) => { const fs = [...e.dataTransfer.files].filter((f) => f.type.startsWith('image/')); if (fs.length) { e.preventDefault(); add(fs); } });
  return { payload: () => items.map((i) => ({ name: i.name, data: i.data })), clear: () => { items = []; refresh(); }, refresh };
}
const ntImg = imageAttach('nt', () => $('nt-planner').value, $('nt-img-warn'));
const tmImg = imageAttach('tm', () => taskDetail && taskDetail.planner, $('tm-img-warn'));
const ntDraft = Drafts.bind($('nt-prompt'), $('nt-draft')), tmDraft = Drafts.bind($('tm-text'), $('tm-draft'));
