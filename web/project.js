'use strict';
// ---------- projects ----------
// #/projects lists the projects as cards. #/projects/<id> is one project: its
// requests and their tasks in the middle, a box that starts a task in the project,
// and the Threads panel on the right (artifacts, then the project's tasks grouped
// as Waiting on you, Working, Idle and Resolved). Files and settings open as dialogs.

let pjTimer = 0, pjData = null, pjDataFor = '', pjFetchedAt = 0, pjFetching = false, pjResolvedOpen = false, pjNewDraft = '';
const pjWide = () => matchMedia('(min-width: 1100px)').matches;
function pjPanelPref() { try { const v = localStorage.getItem('pj-panel'); return v === null ? null : v === '1'; } catch (e) { return null; } }
let pjPanelOpen = pjPanelPref() ?? pjWide();

const projectById = (id) => (projects || []).find((p) => p.id === id);
const projectTasks = (id) => (tasks || []).filter((t) => t.project_id === id);
const RESTING = ['done', 'failed', 'cancelled'];
const canResolve = (t) => RESTING.includes(t.status) && !t.needs_you;

// projectGroup puts a task in one of the four Threads sections.
function projectGroup(t) {
  if (t.needs_you) return 'waiting';
  if (ACTIVE.includes(t.status) || resuming(t)) return 'working';
  return t.resolved ? 'resolved' : 'idle';
}
function projectCounts(id) {
  const c = { waiting: 0, working: 0, idle: 0, resolved: 0 };
  projectTasks(id).forEach((t) => { c[projectGroup(t)]++; });
  return c;
}

function renderProjects() {
  if (page === 'project') { renderProjectPage(); return; }
  const cards = (projects || []).map((p) => {
    const c = projectCounts(p.id);
    const pills = [c.waiting ? `<span class="pill pj-needs">${c.waiting} waiting on you</span>` : '', c.working ? `<span class="pill pj-work">${c.working} working</span>` : '',
      c.idle ? `<span class="pill">${c.idle} idle</span>` : '', c.resolved ? `<span class="pill">${c.resolved} resolved</span>` : ''].join('');
    return `<a class="pj-card" href="#/projects/${esc(p.id)}"><span class="pj-card-name">▤ ${esc(p.name)}</span>
      <span class="pj-card-sub">${p.files} file${p.files === 1 ? '' : 's'} · ${p.git_url ? 'git → ' + esc(p.git_branch) : 'plain folder'} · ${p.tasks} task${p.tasks === 1 ? '' : 's'}</span>
      <span class="pj-card-pills">${pills || '<span class="muted">No tasks yet</span>'}</span></a>`;
  }).join('');
  setHTML('projects-list', `<div class="pj-cards">${cards}<button class="pj-card pj-card-new" data-act="proj-new">＋ New project<span class="pj-card-sub">An empty folder, or a clone of a git repository</span></button></div>`);
}

// ---------- one project ----------
async function pjFetch(force) {
  const id = pageArg;
  if (!id || pjFetching || (!force && pjDataFor === id && Date.now() - pjFetchedAt < 3000)) return;
  pjFetching = true;
  try {
    const d = await api(`/api/projects/${encodeURIComponent(id)}/overview`);
    if (pageArg === id) {
      pjData = d; pjDataFor = id; pjFetchedAt = Date.now(); renderProjectPage();
      clearTimeout(pjTimer);
      if (d.chat && d.chat.busy) pjTimer = setTimeout(() => { if (page === 'project' && pageArg === id) pjFetch(true); }, 1200);
    }
  } catch (e) { if (pageArg === id) { pjData = null; pjDataFor = id; pjFetchedAt = Date.now(); renderProjectPage(); } }
  pjFetching = false;
}
function projectRoute() {
  if (pjDataFor !== pageArg) { pjData = null; pjResolvedOpen = false; }
  $('pj-text').value = pjNewDraft = '';
  autoGrow($('pj-text'));
  pjPanelOpen = pjPanelPref() ?? pjWide();
  if (!pjWide()) pjPanelOpen = false; // the phone opens the panel by hand, as a sheet
  renderProjectPage();
  pjFetch(true);
}

function pjStrip(subs, cls = 'strip') {
  return subs && subs.length ? `<span class="${cls}" aria-hidden="true">${subs.map((s) => `<i class="seg s-${esc(s)}"></i>`).join('')}</span>` : '';
}
function pjTaskMeta(t) {
  const bits = [statusWord(t)];
  if (t.total && ACTIVE.includes(t.status)) bits.push(`${t.done} of ${t.total} subtasks`);
  if (t.round > 1) bits.push('request ' + t.round);
  return bits.join(' · ');
}
function pjTaskCard(t, f) {
  const tone = taskTone(t);
  const act = t.needs_you ? `<a class="pj-open" href="#/tasks/${esc(t.id)}"><button class="small primary">${t.status === 'awaiting_approval' ? 'Review plan' : t.status === 'awaiting_answers' ? 'Answer' : 'Open'}</button></a>`
    : canResolve(t) ? `<button class="small" data-act="proj-resolve" data-id="${esc(t.id)}" data-on="${t.resolved ? '' : '1'}">${t.resolved ? 'Reopen' : '✓ Resolve'}</button>` : '';
  const result = f && f.result && RESTING.includes(t.status) ? `<p class="pj-result">${esc(f.result)}</p>` : '';
  return `<div class="pj-tcard ${t.resolved ? 'resolved' : ''}"><span class="tdot ${tone}" aria-hidden="true"></span>
    <div class="pj-tbody"><a class="pj-ttitle" href="#/tasks/${esc(t.id)}">${esc(t.title)}</a>
      <div class="pj-tmeta ${tone === 'needs' ? 'needs' : tone === 'bad' ? 'bad' : ''}">${esc(pjTaskMeta(t))} · ${ago(t.updated)}</div>
      ${WORKING.includes(t.status) && f ? pjStrip(f.subtasks) : ''}${result}</div>
    <div class="pj-tact">${act}</div></div>`;
}

// The middle of the page: the project conversation, with each task it started
// as a live card under the message, plus tasks started elsewhere (the New task
// page, a schedule) as their request and card, all in time order.
function renderPjFeed(p) {
  const list = projectTasks(p.id);
  const byId = {}; list.forEach((t) => { byId[t.id] = t; });
  const ov = pjData && pjDataFor === p.id ? pjData : null;
  if (!ov) return `<div class="pj-empty">${pjDataFor === p.id ? 'Could not load this project.' : 'Loading…'}</div>`;
  const feedById = {}; ov.feed.forEach((f) => { feedById[f.id] = f; });
  const msgs = (ov.chat && ov.chat.messages) || [];
  const inChat = new Set(); msgs.forEach((m) => (m.tasks || []).forEach((id) => inChat.add(id)));
  const showTask = (id) => byId[id] && (pjResolvedOpen || !byId[id].resolved);
  const cards = (ids) => (ids || []).filter(showTask).map((id) => pjTaskCard(byId[id], feedById[id])).join('');
  const items = [];
  msgs.forEach((m) => {
    let html;
    if (m.role === 'user') html = `<div class="pj-ask"><div class="bubble">${esc(m.text)}</div><span class="pj-when">${(m.tasks || []).length ? 'started as a task · ' : ''}${ago(m.time)}</span></div>${cards(m.tasks)}`;
    else {
      const meta = [m.who && esc(m.who), m.cost_usd ? usd(m.cost_usd) : '', ago(m.time)].filter(Boolean).join(' · ');
      html = `<div class="pj-answer">${m.text ? `<div class="pj-answer-text">${esc(m.text)}</div>` : ''}${m.error ? `<div class="error-msg">${m.error === 'stopped' ? 'Stopped.' : esc(m.error)}</div>` : ''}${cards(m.tasks)}<div class="pj-when">${meta}</div></div>`;
    }
    items.push({ time: new Date(m.time).getTime(), html });
  });
  let hidden = 0;
  ov.feed.forEach((f) => {
    if (inChat.has(f.id) || !byId[f.id]) return;
    if (!showTask(f.id)) { hidden++; return; }
    items.push({ time: new Date(f.created).getTime(), html: f.requests.map((r) => `<div class="pj-ask"><div class="bubble">${esc(r.text)}</div><span class="pj-when">${ago(r.time)}</span></div>`).join('') + pjTaskCard(byId[f.id], f) });
  });
  inChat.forEach((id) => { if (byId[id] && !showTask(id)) hidden++; });
  items.sort((x, y) => x.time - y.time);
  if (ov.chat && ov.chat.busy) items.push({ html: `<div class="pj-answer pj-thinking"><span class="dots" aria-label="Thinking"><i></i><i></i><i></i></span> <span class="muted">Thinking…</span> <button class="small" data-act="proj-chat-stop">Stop</button></div>` });
  const more = hidden > 0 ? `<button class="link-btn pj-showres" data-act="proj-show-resolved">${pjResolvedOpen ? 'Hide resolved tasks' : `${hidden} resolved task${hidden === 1 ? '' : 's'} hidden. Show them`}</button>` : '';
  if (!items.length) return `${more}<div class="pj-empty"><h3>What should ${esc(p.name)} do next?</h3><p class="hint">Ask about the project, or describe work to do: the planner answers here and starts tasks for it. <b>Start as task</b> skips the conversation and starts your message as a task. Every task starts from the project's current files, and one task works in the project at a time.</p></div>`;
  return more + items.map((i) => `<div class="pj-item">${i.html}</div>`).join('');
}

function pjRow(t, f) {
  const g = projectGroup(t);
  let right = '';
  if (g === 'waiting') right = `<a href="#/tasks/${esc(t.id)}" tabindex="-1"><button class="small primary">${t.status === 'awaiting_approval' ? 'Review' : t.status === 'awaiting_answers' ? 'Answer' : 'Open'}</button></a>`;
  else if (g === 'working') right = `${f && f.subtasks.length ? `<span class="pj-cnt">${t.done}/${t.total}</span>${pjStrip(f.subtasks, 'strip pj-mini')}` : ''}<span>${ago(t.updated).replace(' ago', '')}</span>`;
  else right = `<span>${ago(t.updated).replace(' ago', '')}</span>${canResolve(t) ? `<button class="pj-rb" data-act="proj-resolve" data-id="${esc(t.id)}" data-on="${t.resolved ? '' : '1'}" title="${t.resolved ? 'Reopen (move back to Idle)' : 'Mark resolved'}" aria-label="${t.resolved ? 'Reopen' : 'Mark resolved'}: ${esc(t.title)}">${t.resolved ? '↺' : '✓'}</button>` : ''}`;
  const tone = taskTone(t);
  return `<div class="pj-row"><span class="tdot ${g === 'resolved' ? 'idle' : tone}" aria-hidden="true"></span>
    <a class="pj-row-main" href="#/tasks/${esc(t.id)}"><span class="pj-row-t">${esc(t.title)}</span><span class="pj-row-m ${tone === 'needs' ? 'needs' : tone === 'bad' && g !== 'resolved' ? 'bad' : ''}">${esc(statusWord(t))}${t.round > 1 ? ' · request ' + t.round : ''}</span></a>
    <span class="pj-row-r">${right}</span></div>`;
}
const ART_ICON = { shot: '▭', build: '⚙', result: '⇩', pr: '⇄' };
function pjArtCard(a) {
  const pv = a.preview ? `<img src="${esc(a.preview)}" alt="" loading="lazy">` : `<span>${ART_ICON[a.kind] || '▤'}</span>`;
  return `<a class="pj-acard" href="${esc(a.url)}" ${a.kind === 'shot' || a.kind === 'pr' ? 'target="_blank" rel="noopener"' : ''} title="${esc(a.name)}${a.task_title ? ' · ' + esc(a.task_title) : ''}">
    <span class="pj-apv k-${esc(a.kind)}">${pv}</span><span class="pj-aname">${esc(a.name)}</span><span class="pj-asub">${esc(a.task_title || (a.kind === 'pr' ? 'GitHub' : ''))}</span></a>`;
}
function renderPjPanel(p) {
  const list = projectTasks(p.id);
  const feed = {}; ((pjData && pjDataFor === p.id && pjData.feed) || []).forEach((f) => { feed[f.id] = f; });
  const groups = { waiting: [], working: [], idle: [], resolved: [] };
  list.forEach((t) => groups[projectGroup(t)].push(t));
  const arts = (pjData && pjDataFor === p.id && pjData.artifacts) || [];
  const sec = (key, name, inner, extra = '') => `<section class="pj-sec ${key}"><div class="pj-sec-h"><b>${name} · ${groups[key].length}</b>${extra}</div>${inner}</section>`;
  const rows = (key, empty) => groups[key].length ? groups[key].map((t) => pjRow(t, feed[t.id])).join('') : `<div class="pj-none">${empty}</div>`;
  return `<section class="pj-sec"><div class="pj-sec-h"><b>Artifacts</b><span class="spacer"></span>${arts.length > 3 ? `<button class="link-btn" data-act="proj-arts">View all ${arts.length}</button>` : ''}</div>
      ${arts.length ? `<div class="pj-arts">${arts.slice(0, 3).map(pjArtCard).join('')}</div>` : '<div class="pj-none">Screenshots, build outputs, result zips and pull requests from this project\'s tasks show up here.</div>'}</section>
    ${sec('waiting', 'Waiting on you', rows('waiting', 'Questions and plans to approve show up here.'))}
    ${sec('working', 'Working', rows('working', 'Nothing running.'), groups.working.length > 1 ? '<span class="spacer"></span><span class="pj-note">one task works at a time; the rest are queued</span>' : '')}
    ${sec('idle', 'Idle', rows('idle', 'Finished tasks wait here until you resolve them.'), groups.idle.some(canResolve) ? '<span class="spacer"></span><button class="link-btn" data-act="proj-resolve-all">Resolve all</button>' : '')}
    <section class="pj-sec resolved"><button class="pj-sec-h pj-toggle" data-act="proj-show-resolved" aria-expanded="${pjResolvedOpen}"><b>Resolved · ${groups.resolved.length}</b> <span aria-hidden="true">${pjResolvedOpen ? '▾' : '›'}</span></button>
      ${pjResolvedOpen ? rows('resolved', 'Nothing resolved yet. Resolve a finished task to tidy it away; a follow-up brings it back.') : ''}</section>`;
}

function renderProjectPage() {
  if (page !== 'project') return;
  const p = projectById(pageArg);
  $('pj-wrap').classList.toggle('panel-open', pjPanelOpen);
  if (!p) {
    $('pj-name').textContent = projects ? 'Project not found' : 'Loading…';
    setHTML('pj-feed', projects ? `<div class="pj-empty"><h3>This project does not exist any more.</h3><p><a href="#/projects">All projects</a></p></div>` : '');
    setHTML('pj-threads', '');
    return;
  }
  document.title = 'Outpost · ' + p.name;
  $('pj-name').textContent = p.name;
  setHTML('pj-switch', (projects || []).map((q) => `<option value="${esc(q.id)}" ${q.id === p.id ? 'selected' : ''}>${esc(q.name)}</option>`).join('') + '<option value="">All projects…</option>');
  setHTML('pj-sub', `${p.files} file${p.files === 1 ? '' : 's'}${p.test_command ? ` · tests <code>${esc(p.test_command)}</code>` : ''}${p.git_url ? ` · git → <code>${esc(p.git_branch)}</code>${p.last_push ? ' · pushed ' + ago(p.last_push) : ''}` : ''}`);
  const c = projectCounts(p.id);
  $('pj-threads-n').textContent = c.waiting || '';
  $('pj-threads-n').classList.toggle('hidden', !c.waiting);
  $('pj-threads-btn').setAttribute('aria-expanded', String(pjPanelOpen));
  $('pj-threads-btn').classList.toggle('on', pjPanelOpen);
  $('pj-download').href = `/api/projects/${encodeURIComponent(p.id)}/download`;
  const feedEl = $('pj-feed'), atEnd = feedEl.scrollHeight - feedEl.scrollTop - feedEl.clientHeight < 40;
  const before = feedEl._h;
  setHTML(feedEl, renderPjFeed(p));
  if (before !== feedEl._h && (atEnd || !before)) feedEl.scrollTop = feedEl.scrollHeight;
  setHTML('pj-threads', renderPjPanel(p));
  if ($('modal-pj-files') && !$('modal-pj-files').classList.contains('hidden')) renderPjFiles(p);
  if (!pjData || pjDataFor !== p.id || Date.now() - pjFetchedAt > 3000) pjFetch();
}

function setPjPanel(open) {
  pjPanelOpen = open;
  if (pjWide()) { try { localStorage.setItem('pj-panel', open ? '1' : '0'); } catch (e) {} }
  renderProjectPage();
  if (open && !pjWide()) $('pj-panel-close').focus();
}
$('pj-threads-btn').onclick = () => setPjPanel(!pjPanelOpen);
$('pj-panel-close').onclick = () => { setPjPanel(false); $('pj-threads-btn').focus(); };
$('pj-menu').onclick = openSide;
$('pj-switch').onchange = (e) => { location.hash = e.target.value ? '#/projects/' + e.target.value : '#/projects'; };
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && page === 'project' && pjPanelOpen && !pjWide() && !document.querySelector('.overlay:not(.hidden)')) setPjPanel(false);
});

// Send a message to the project conversation, or (asTask) start it as a task at
// once. The project's own defaults (planner, review, fix rounds, node label) apply.
async function pjSend(asTask) {
  const text = $('pj-text').value;
  if (!text.trim()) { $('pj-text').focus(); return; }
  $('pj-go').disabled = $('pj-task').disabled = true;
  try {
    await post(`/api/projects/${encodeURIComponent(pageArg)}/chat`, { text, mode: asTask ? 'task' : '' });
    $('pj-text').value = ''; autoGrow($('pj-text'));
    if (asTask) { toast('Task started'); tasksSeq++; await refresh(); }
    await pjFetch(true);
    $('pj-feed').scrollTop = $('pj-feed').scrollHeight;
  } catch (e) { toast(e.message, 4000); }
  $('pj-go').disabled = $('pj-task').disabled = false;
}
$('pj-go').onclick = () => pjSend(false);
$('pj-task').onclick = () => pjSend(true);
$('pj-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) pjSend(false); });
$('pj-text').addEventListener('input', () => autoGrow($('pj-text')));
$('pj-more').onclick = (e) => {
  e.preventDefault();
  try { localStorage.setItem('nt-project', pageArg); } catch (x) {}
  const text = $('pj-text').value;
  location.hash = '#/new';
  setTimeout(() => { $('nt-project').value = pageArg; if (text) { $('nt-prompt').value = text; autoGrow($('nt-prompt')); } renderNew(); }, 0);
};

// ---------- Files, settings, artifacts and new-project dialogs ----------
function renderPjFiles(p) {
  const files = (pjData && pjDataFor === p.id && pjData.files) || [];
  const total = (pjData && pjData.files_total) || files.length;
  setHTML('pj-files-body', `<h4 class="pj-h4">Project instructions</h4>
    <p class="hint">Standing rules for every task in this project, added after the ones in Settings (and before an <code>OUTPOST.md</code> in the files).</p>
    <textarea rows="4" data-pf="instructions" data-id="${esc(p.id)}" spellcheck="false" placeholder="e.g. Tests use pytest. Keep Python 3.9 compatible.">${esc(p.instructions || '')}</textarea>
    ${(p.suggestions || []).map((s) => `<div class="note u6 nv-note"><span class="nv-grow"><b>Suggested after a failed task:</b> ${esc(s.text)} <span class="muted">(${esc(s.task_title || '')})</span></span><button class="small primary" data-act="project-suggest" data-id="${esc(p.id)}" data-sid="${esc(s.id)}" data-do="add">Add to instructions</button><button class="small" data-act="project-suggest" data-id="${esc(p.id)}" data-sid="${esc(s.id)}" data-do="dismiss">Dismiss</button></div>`).join('')}
    <div class="btns u6"><button class="small primary" data-act="proj-save-instr" data-id="${esc(p.id)}">Save instructions</button></div>
    <h4 class="pj-h4">Files <span class="muted">${total}</span></h4>
    ${files.length ? `<ul class="pj-files">${files.map((f) => `<li><span class="mono">${esc(f.path)}</span><span class="muted">${bytes(f.bytes)}</span></li>`).join('')}</ul>${total > files.length ? `<p class="hint">Showing the first ${files.length}. The .zip has them all.</p>` : ''}` : '<p class="hint">No files yet. The first task writes them.</p>'}`);
}
function openPjFiles() { const p = projectById(pageArg); if (!p) return; $('pj-files-zip').href = `/api/projects/${encodeURIComponent(p.id)}/download`; renderPjFiles(p); open('modal-pj-files'); }
$('pj-files-btn').onclick = openPjFiles;

function openPjSettings() {
  const p = projectById(pageArg); if (!p) return;
  const sel = (k, opts, cur, word) => `<select data-pf="${k}" data-id="${esc(p.id)}">${opts.map((v) => `<option value="${v}" ${(cur || 0) === v ? 'selected' : ''}>${v ? word(v) : 'default'}</option>`).join('')}</select>`;
  setHTML('pj-set-body', `<p class="hint">Defaults for tasks started in <b>${esc(p.name)}</b>. A task's own options still win.</p>
    <label>Test command</label><input data-pf="test_command" data-id="${esc(p.id)}" value="${esc(p.test_command)}" placeholder="none, e.g. go test ./...">
    <div class="row2"><div><label>Fix rounds</label>${sel('fix_rounds', [0, 1, 2, 3, 5, 8], p.fix_rounds, (v) => v)}</div><div><label>Test time limit</label>${sel('test_minutes', [0, 5, 10, 30, 60], p.test_minutes, (v) => v + ' min')}</div></div>
    <label>Nodes <span class="hint">(tasks run only on nodes with this label; empty = any node)</span></label><input data-pf="node_label" data-id="${esc(p.id)}" value="${esc(p.node_label || '')}" placeholder="any label">
    <label class="toggle"><input type="checkbox" data-pf="review" data-id="${esc(p.id)}" ${p.review === false ? '' : 'checked'}> peer review</label>
    ${p.git_url ? `<h4 class="pj-h4">Git</h4><p class="hint"><code>${esc(p.git_url)}</code> → branch <code>${esc(p.git_branch)}</code>${p.last_push ? ' · last push ' + ago(p.last_push) : ''}${p.last_pr ? ` · <a href="${esc(p.last_pr)}" target="_blank" rel="noopener">last pull request</a>` : ''}</p>
    <label class="toggle"><input type="checkbox" data-pf="git_push" data-id="${esc(p.id)}" ${p.git_push ? 'checked' : ''}> push each finished round to the branch</label>
    <label class="toggle"><input type="checkbox" data-pf="git_pr" data-id="${esc(p.id)}" ${p.git_pr ? 'checked' : ''}> also open a GitHub pull request</label>` : ''}
    <textarea class="hidden" data-pf="instructions" data-id="${esc(p.id)}">${esc(p.instructions || '')}</textarea>
    <h4 class="pj-h4">Conversation</h4><p class="hint">The project conversation is answered by the project's planner (or the default planner in Settings). With Claude or OpenRouter each answer is a small paid call, shown on the Spending page as "${esc(p.name)} · project chat".</p>
    <button class="small" data-act="proj-chat-clear" data-id="${esc(p.id)}">Clear conversation…</button>
    <div class="modal-foot"><button class="danger" data-act="project-delete" data-id="${esc(p.id)}">Delete project…</button><span class="spacer"></span><button data-close>Cancel</button><button class="primary" data-act="project-save" data-id="${esc(p.id)}">Save</button></div>`);
  open('modal-pj-settings');
}
$('pj-set-btn').onclick = openPjSettings;

function openPjArts() {
  const arts = (pjData && pjData.artifacts) || [];
  setHTML('pj-arts-body', arts.length ? `<div class="pj-arts all">${arts.map(pjArtCard).join('')}</div>` : '<p class="hint">Nothing yet.</p>');
  open('modal-pj-arts');
}

async function projectAction(act, el) {
  const id = el.dataset.id;
  const f = (k) => document.querySelector(`#modal-pj-settings [data-pf="${k}"][data-id="${id}"]`) || document.querySelector(`[data-pf="${k}"][data-id="${id}"]`);
  if (act === 'project-delete') {
    if (!confirm('Delete this project and its files? Its git remote is not touched. (A project that still has tasks cannot be deleted; delete them first.)')) return;
    await api(`/api/projects/${id}`, { method: 'DELETE' }); toast('Project deleted');
    close('modal-pj-settings');
    if (page === 'project') location.hash = '#/projects';
  } else if (act === 'project-save') {
    const body = { test_command: f('test_command').value, review: f('review').checked, fix_rounds: +f('fix_rounds').value, test_minutes: +f('test_minutes').value, node_label: f('node_label').value, instructions: f('instructions').value };
    if (f('git_push')) { body.git_push = f('git_push').checked; body.git_pr = f('git_pr').checked; }
    await post(`/api/projects/${id}`, body); toast('Saved'); close('modal-pj-settings');
  } else if (act === 'project-suggest') {
    await post(`/api/projects/${id}/suggestions/${encodeURIComponent(el.dataset.sid)}/${el.dataset.do}`, {});
    toast(el.dataset.do === 'add' ? 'Added to the instructions' : 'Dismissed'); refresh();
  } else if (act === 'project-use') {
    try { localStorage.setItem('nt-project', id); } catch (e) {}
    $('nt-project').value = id;
    // The link's own navigation is cancelled by the shared data-act click handler, so go there here.
    location.hash = '#/new';
  }
}

// proj-* actions from the project pages (dispatched by the shared click handler).
async function projPageAction(act, el) {
  const id = el.dataset.id;
  switch (act) {
    case 'proj-new': open('modal-newproj'); return;
    case 'proj-arts': openPjArts(); return;
    case 'proj-show-resolved': pjResolvedOpen = !pjResolvedOpen; renderProjectPage(); return;
    case 'proj-resolve': {
      const on = !!el.dataset.on;
      const t = taskById(id);
      const s = await post(`/api/tasks/${id}/meta`, { resolved: on });
      if (t) Object.assign(t, s);
      tasksSeq++; renderProjectPage(); renderSideTasks();
      toast(on ? 'Resolved' : 'Moved back to Idle'); return;
    }
    case 'proj-resolve-all': {
      const list = projectTasks(pageArg).filter((t) => projectGroup(t) === 'idle' && canResolve(t));
      await Promise.all(list.map((t) => post(`/api/tasks/${t.id}/meta`, { resolved: true }).then((s) => Object.assign(t, s))));
      tasksSeq++; renderProjectPage(); toast(`Resolved ${list.length} task${list.length === 1 ? '' : 's'}`); return;
    }
    case 'proj-chat-stop': await post(`/api/projects/${encodeURIComponent(pageArg)}/chat/stop`); pjFetch(true); return;
    case 'proj-chat-clear':
      if (!confirm('Clear this project\'s conversation? The tasks it started are kept.')) return;
      await api(`/api/projects/${encodeURIComponent(id)}/chat`, { method: 'DELETE' }); close('modal-pj-settings'); toast('Conversation cleared'); pjFetch(true); return;
    case 'proj-save-instr': {
      const box = document.querySelector(`#modal-pj-files [data-pf="instructions"][data-id="${id}"]`);
      await post(`/api/projects/${id}`, { instructions: box.value }); toast('Instructions saved'); refresh(); return;
    }
  }
}

$('np-go').onclick = async () => {
  const err = $('np-err'); err.classList.add('hidden');
  $('np-go').disabled = true; $('np-go').textContent = $('np-url').value.trim() ? 'Cloning…' : 'Creating…';
  try {
    const r = await post('/api/projects', { name: $('np-name').value, git_url: $('np-url').value.trim(), git_branch: $('np-branch').value.trim(), git_token: $('np-token').value, git_ssh_key: $('np-key').value,
      test_command: $('np-test').value, git_push: $('np-push').checked, git_pr: $('np-pr').checked });
    ['np-name', 'np-url', 'np-branch', 'np-token', 'np-key', 'np-test'].forEach((i) => { $(i).value = ''; });
    $('np-push').checked = $('np-pr').checked = false;
    close('modal-newproj'); toast('Project created');
    await refresh();
    if (r && r.id) location.hash = '#/projects/' + r.id;
  } catch (e) { err.textContent = e.message; err.classList.remove('hidden'); }
  $('np-go').disabled = false; $('np-go').textContent = 'Create project';
};
