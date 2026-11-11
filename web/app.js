'use strict';
const $ = (id) => document.getElementById(id);
let state = null, models = null, tasks = [], projects = [], settings = null, taskDetail = null;
let tasksSeq = 0; // bumped by local task edits so an older poll result is dropped
let ctxTaskId = null, ctxReturn = null, renamingId = null; // sidebar task menu and inline rename
let stateTimer = null, jobTimer = null, taskTimer = null, openJobId = null, loadNodeId = null, runNodeId = null;

// ---------- helpers ----------
// errorBody shows a short, readable summary of an error and keeps the raw
// text (which can be kilobytes of escaped code) behind a Details disclosure.
function errorBody(text) {
  text = String(text || '');
  let summary = '';
  if (/failed to parse tool call|tool calls? (were|was) cut off/i.test(text)) {
    summary = "The model's output was cut off mid-file, so its file write could not be read. Outpost retried automatically; try again, or use a model with more output room.";
  } else if (text.length > 300 || text.includes('\n')) {
    summary = text.split('\n')[0].slice(0, 160).replace(/\s+\S*$/, '') + '…';
  }
  if (!summary) return `<p>${esc(text)}</p>`;
  return `<p>${esc(summary)}</p><details class="err-details"><summary>Details</summary><pre class="err-raw">${esc(text)}</pre></details>`;
}

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
  clearInterval(stateTimer); stateTimer = null; stopStateStream();
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
  refresh(); startStateStream();
}
// The controller pushes state over one stream (state_stream.go); polling every 2 s
// is the fallback when the stream does not work (a buffering proxy, an old controller).
let stateES = null, stateFails = 0, statePushed = 0;
function startStateStream() {
  if (stateES || stateTimer) return;
  if (!window.EventSource) { stateTimer = setInterval(refresh, 2000); return; }
  stateES = new EventSource('/api/state/events');
  stateES.addEventListener('state', (e) => {
    stateFails = 0; statePushed = Date.now();
    let d; try { d = JSON.parse(e.data); } catch (x) { return; }
    applyPushed(d);
  });
  stateES.onerror = () => {
    if (++stateFails < 3) return; // EventSource retries on its own first
    stateES.close(); stateES = null;
    if (!stateTimer) stateTimer = setInterval(refresh, 2000);
  };
  // A stream that opened but stays silent (buffered by a proxy) also falls back.
  setTimeout(() => { if (stateES && !statePushed) { stateES.close(); stateES = null; if (!stateTimer) stateTimer = setInterval(refresh, 2000); } }, 8000);
}
function stopStateStream() { if (stateES) { stateES.close(); stateES = null; } statePushed = 0; stateFails = 0; }
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

const PAGES = ['new', 'image', 'task', 'chat', 'projects', 'project', 'nodes', 'models', 'updates', 'spending', 'settings'];
let page = 'new', pageArg = null;
function route() {
  ntDraft.flush(); tmDraft.flush();
  const m = location.hash.match(/^#\/(\w+)(?:\/([\w-]+))?/) || [];
  page = m[1] === 'tasks' && m[2] ? 'task' : m[1] === 'projects' && m[2] ? 'project' : m[1] === 'project' ? 'projects' : PAGES.includes(m[1]) ? m[1] : 'new';
  pageArg = page === 'task' || page === 'chat' || page === 'project' ? m[2] || null : null;
  document.querySelectorAll('[data-page]').forEach((el) => el.classList.toggle('hidden', el.dataset.page !== page));
  document.querySelectorAll('.side-nav a, .side-projects').forEach((a) => a.classList.toggle('active', a.dataset.tab === (page === 'project' ? 'projects' : page)));
  document.querySelector('.page-plain').classList.toggle('hidden', !['chat', 'projects', 'nodes', 'models', 'updates', 'spending', 'settings'].includes(page));
  document.title = page === 'task' ? 'Outpost' : 'Outpost · ' + ({ new: 'New task', image: 'New image' }[page] || page[0].toUpperCase() + page.slice(1));
  document.body.classList.toggle('on-task', page === 'task' || page === 'project');
  closeSide();
  clearTimeout(taskTimer);
  liveClose();
  if (page === 'task') { taskDetail = null; threadReset(); tmDraft.restore('task:' + pageArg); autoGrow($('tm-text')); liveOpen(pageArg); pollTask(); }
  if (page === 'new') { ntDraft.restore('new'); setTimeout(() => $('nt-prompt').focus(), 0); }
  if (page === 'settings') loadSettings();
  if (page === 'spending') loadSpending();
  if (page === 'chat') chatOpen(pageArg);
  if (page === 'project') projectRoute();
  if (typeof imgRoute === 'function') imgRoute(page);
  renderSideTasks();
  if (state) refresh();
}
Guard.stamp();
window.addEventListener('hashchange', Guard.onHashChange);

// Sidebar on small screens
function openSide() { $('side').classList.add('open'); $('scrim').classList.remove('hidden'); }
function closeSide() { $('side').classList.remove('open'); $('scrim').classList.add('hidden'); }
$('side-open').onclick = openSide; $('task-menu').onclick = openSide; $('side-close').onclick = closeSide; $('scrim').onclick = closeSide;

// Damaged state files found when the controller started (recovered.go).
function renderDiskWarning() {
  const w = state && state.disk_warning;
  $('disk-banner').classList.toggle('hidden', !w);
  if (w) $('disk-banner').textContent = w;
}
function renderRecovered() {
  const notes = (state && state.recovered) || [];
  $('recovered-banner').classList.toggle('hidden', !notes.length);
  setHTML('recovered-banner', notes.length ? `<b>${notes.length === 1 ? 'A saved file was damaged' : notes.length + ' saved files were damaged'} when the controller started.</b>
    <ul class="recovered-list">${notes.map((n) => `<li>${esc(n)}</li>`).join('')}</ul>
    <button class="small" id="recovered-dismiss">Dismiss</button>` : '');
}
$('recovered-banner').addEventListener('click', async (e) => {
  if (e.target.id !== 'recovered-dismiss') return;
  await post('/api/recovered/dismiss').catch(() => {});
  if (state) state.recovered = [];
  renderRecovered(); renderDiskWarning();
});

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
    needModels ? api('/api/models').then((m) => { models = m; modelsAt = Date.now(); }).catch(() => {}) : null,
    (() => { const seq = tasksSeq; return api('/api/tasks').then((t) => { if (seq === tasksSeq) tasks = t; }).catch(() => {}); })(), // a list fetched before a rename or pin must not undo it
    api('/api/projects').then((p) => { projects = p; }).catch(() => {}),
  ]);
  renderAll();
}
let modelsAt = 0, pushSeq = 0;
// applyPushed takes one pushed payload: state, task list and projects together.
// The model list is not pushed; on the pages that show it, it is fetched at most every 5 s.
async function applyPushed(d) {
  const seq = tasksSeq, mine = ++pushSeq;
  state = d.state; if (seq === tasksSeq) tasks = d.tasks; projects = d.projects;
  failures = 0; $('offline-banner').classList.add('hidden');
  if ((['nodes', 'models', 'new'].includes(page) || !models) && Date.now() - modelsAt > 5000) {
    modelsAt = Date.now();
    await api('/api/models').then((m) => { models = m; }).catch(() => {});
    if (mine !== pushSeq) return; // a newer payload already rendered
  }
  renderAll();
}
// Screen readers hear a task's status change (Plan ready, Done, Failed) without visiting it.
let lastStatuses = null;
function announceTaskChanges() {
  const now = {};
  (tasks || []).forEach((t) => { now[t.id] = statusWord(t); });
  if (lastStatuses) {
    const msgs = (tasks || []).filter((t) => lastStatuses[t.id] && lastStatuses[t.id] !== now[t.id]).map((t) => `${t.title || 'Task'}: ${now[t.id]}`);
    if (msgs.length) { $('sr-status').textContent = ''; setTimeout(() => { $('sr-status').textContent = msgs.slice(0, 3).join('. '); }, 50); }
  }
  lastStatuses = now;
}
function renderAll() {
  announceTaskChanges();
  $('hdr-meta').textContent = `v${state.version}`;
  renderRecovered(); renderDiskWarning();
  $('hdr-meta').title = state.advertise;
  $('src-link').href = `https://github.com/64fedoras/outpost/tree/v${state.version}`; // AGPL section 13: source of the running version
  $('ctl-info').textContent = `Controller ${state.advertise} · ${state.platform} · certificate SHA-256 ${state.fingerprint}`;
  pollNotifications();
  renderBadges();
  renderSideTasks();
  renderSideCluster();
  if (page === 'new') renderNew();
  if (page === 'projects' || page === 'project') renderProjects();
  if (page === 'chat' && document.activeElement !== $('chat-node')) fillChatNodes();
  if (page === 'nodes') { renderNodes(); renderJobs(); }
  if (page === 'models') renderModels();
  if (page === 'updates') renderUpdates();
  if (page === 'spending' && Date.now() - spendAt > 10000) loadSpending();
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
// A task interrupted by a controller restart that resumes on its own reads as
// working, not as waiting for the user.
const resuming = (t) => t.status === 'interrupted' && !!t.auto_resume;
function statusWord(t) {
  if (resuming(t)) return 'Resuming';
  if (t.status === 'done' && t.test_status === 'failing') return 'Done, tests fail';
  return STATUS_WORDS[t.status] || label(t.status);
}
function taskTone(t) {
  if (t.needs_you) return 'needs';
  if (WORKING.includes(t.status) || resuming(t)) return 'work';
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
    t.project_id && (canResolve(t) || t.resolved) ? item('resolve', t.resolved ? 'Reopen' : 'Mark resolved') : '',
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
      case 'resolve': Object.assign(t, await post(`/api/tasks/${id}/meta`, { resolved: !t.resolved })); tasksSeq++; renderSideTasks(); if (page === 'project') renderProjectPage(); toast(t.resolved ? 'Resolved' : 'Moved back to Idle'); break;
      case 'pin': Object.assign(t, await post(`/api/tasks/${id}/meta`, { pinned: !t.pinned })); tasksSeq++; renderSideTasks(); toast(t.pinned ? 'Pinned' : 'Unpinned'); break;
      case 'copy-prompt': { const d = await api(`/api/tasks/${id}`); await copyText(d.prompt || ''); toast('Prompt copied'); break; }
      case 'copy-link': await copyText(taskLink(id)); toast('Link copied'); break;
      case 'duplicate': {
        const d = await api(`/api/tasks/${id}`);
        // The same request with the same options; attachments and pasted images are not copied.
        const q = { prompt: d.prompt, planner: d.planner, claude_model: d.claude_model, cap_usd: d.cap_usd, review: d.review, mockup: d.mockup,
          test_first: d.test_first, fix_rounds: d.max_fix_rounds, test_minutes: d.test_minutes, project: d.project_id, node_label: d.node_label };
        const fill = () => {
          const missed = ntFill(q);
          const extra = (d.attachments || []).length ? ' (attachments are not copied)' : '';
          toast(missed.length ? `Copied; the ${missed.join(' and ')} no longer exist${missed.length === 1 ? 's' : ''}, so the current choice is used` : 'Copied with its options' + extra + '; press Plan it to start');
        };
        if (page === 'new') fill();
        else { window.addEventListener('hashchange', () => setTimeout(fill, 0), { once: true }); location.hash = '#/new'; }
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
