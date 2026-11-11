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
    return `<div class="pj-card-wrap ${ctxProjId === p.id ? 'menu-open' : ''}" data-proj="${esc(p.id)}"><a class="pj-card pj-card-link" href="#/projects/${esc(p.id)}" aria-keyshortcuts="Shift+F10 F2 Delete"><span class="pj-card-name">${p.pinned ? '<span class="pin-mark" aria-label="Pinned">📌</span>' : ''}▤ ${esc(p.name)}</span>
      <span class="pj-card-sub">${p.files} file${p.files === 1 ? '' : 's'} · ${p.git_url ? 'git → ' + esc(p.git_branch) : 'plain folder'} · ${p.tasks} task${p.tasks === 1 ? '' : 's'}</span>
      <span class="pj-card-pills">${pills || '<span class="muted">No tasks yet</span>'}</span></a>
      <button class="side-task-more pj-card-more" data-proj-menu="${esc(p.id)}" aria-haspopup="menu" aria-expanded="${ctxProjId === p.id}" aria-label="Actions for ${esc(p.name)}" title="Actions">⋯</button></div>`;
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
      html = `<div class="pj-answer">${m.text ? `<div class="pj-answer-text">${esc(m.text)}</div>` : ''}${m.error ? `<div class="error-msg">${m.error === 'stopped' ? 'Stopped.' : esc(m.error)}${/planner|API key/i.test(m.error) && m.error !== 'stopped' ? `<div class="card-actions"><button class="small" data-act="proj-planner-pick" data-id="${esc(p.id)}">Pick this project's planner</button> <a href="#/settings" data-act="proj-planner-settings">Open Settings → Tasks</a></div>` : ''}</div>` : ''}${cards(m.tasks)}<div class="pj-when">${meta}</div></div>`;
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
  const sug = (p.suggestions || []).length;
  setHTML('pj-files-body', `<p class="hint">Project instructions are under <b>Edit</b>: right-click the project, or press <b>⋯</b> at the top of its page.${sug ? ` <button class="link-btn" data-act="proj-edit" data-id="${esc(p.id)}">${sug} suggested rule${sug === 1 ? '' : 's'} waiting</button>` : ''}</p>
    <h4 class="pj-h4">Files <span class="muted">${total}</span></h4>
    ${files.length ? `<ul class="pj-files">${files.map((f) => `<li><span class="mono">${esc(f.path)}</span><span class="muted">${bytes(f.bytes)}</span></li>`).join('')}</ul>${total > files.length ? `<p class="hint">Showing the first ${files.length}. The .zip has them all.</p>` : ''}` : '<p class="hint">No files yet. The first task writes them.</p>'}`);
}
function openPjFiles() { const p = projectById(pageArg); if (!p) return; $('pj-files-zip').href = `/api/projects/${encodeURIComponent(p.id)}/download`; renderPjFiles(p); open('modal-pj-files'); }
$('pj-files-btn').onclick = openPjFiles;

function openPjSettings(id = pageArg) {
  const p = projectById(id); if (!p) return;
  const sel = (k, opts, cur, word, dflt) => `<select data-pf="${k}" data-id="${esc(p.id)}">${opts.map((v) => `<option value="${v}" ${(cur || 0) === v ? 'selected' : ''}>${v ? word(v) : 'Default (' + word(dflt) + ')'}</option>`).join('')}</select>`;
  setHTML('pj-set-body', `<p class="hint">Defaults for tasks started in <b>${esc(p.name)}</b>. A task's own options still win.</p>
    <label for="pj-set-planner">Planner <span class="hint">(plans tasks and answers the project conversation)</span></label>
    <select id="pj-set-planner" data-pf="planner" data-id="${esc(p.id)}"><option value="">Default planner (set in Settings → Tasks)</option>${plannerOptions(p.planner || '__none__')}</select>
    <label>Test command</label><input data-pf="test_command" data-id="${esc(p.id)}" value="${esc(p.test_command)}" placeholder="none, e.g. go test ./...">
    <div class="row2"><div><label>Fix rounds</label>${sel('fix_rounds', [0, 1, 2, 3, 5, 8], p.fix_rounds, (v) => v, (state && state.default_fix_rounds) || 3)}</div><div><label>Test time limit</label>${sel('test_minutes', [0, 5, 10, 30, 60], p.test_minutes, (v) => v + ' min', (state && state.default_test_minutes) || 10)}</div></div>
    <label>Nodes <span class="hint">(tasks run only on nodes with this label; empty = any node)</span></label><input data-pf="node_label" data-id="${esc(p.id)}" value="${esc(p.node_label || '')}" placeholder="any label">
    <label class="toggle"><input type="checkbox" data-pf="review" data-id="${esc(p.id)}" ${p.review === false ? '' : 'checked'}> peer review</label>
    ${p.git_url ? `<h4 class="pj-h4">Git</h4><p class="hint"><code>${esc(p.git_url)}</code> → branch <code>${esc(p.git_branch)}</code>${p.last_push ? ' · last push ' + ago(p.last_push) : ''}${p.last_pr ? ` · <a href="${esc(p.last_pr)}" target="_blank" rel="noopener">last pull request</a>` : ''}</p>
    <label class="toggle"><input type="checkbox" data-pf="git_push" data-id="${esc(p.id)}" ${p.git_push ? 'checked' : ''}> push each finished round to the branch</label>
    <label class="toggle"><input type="checkbox" data-pf="git_pr" data-id="${esc(p.id)}" ${p.git_pr ? 'checked' : ''}> also open a GitHub pull request</label>` : ''}
    <h4 class="pj-h4">Conversation</h4><p class="hint">The project conversation is answered by the planner chosen above (or, if left on default, the default planner in Settings → Tasks). With Claude or OpenRouter each answer is a small paid call, shown on the Spending page as "${esc(p.name)} · project chat".</p>
    <button class="small" data-act="proj-chat-clear" data-id="${esc(p.id)}">Clear conversation…</button>
    <div class="modal-foot"><button class="danger" data-act="project-delete" data-id="${esc(p.id)}">Delete project…</button><span class="spacer"></span><button data-close>Cancel</button><button class="primary" data-act="project-save" data-id="${esc(p.id)}">Save</button></div>`);
  // The dialog body is built here, after the page-load binding of [data-close], so bind Cancel now.
  $('pj-set-body').querySelectorAll('[data-close]').forEach((b) => { b.onclick = () => close('modal-pj-settings'); });
  open('modal-pj-settings');
}
$('pj-set-btn').onclick = () => openPjSettings();

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
    close('modal-pj-settings'); close('modal-pj-edit');
    projects = (projects || []).filter((q) => q.id !== id); renderSidePins();
    if (page === 'project' && pageArg === id) location.hash = '#/projects';
  } else if (act === 'project-save') {
    const body = { planner: f('planner').value, test_command: f('test_command').value, review: f('review').checked, fix_rounds: +f('fix_rounds').value, test_minutes: +f('test_minutes').value, node_label: f('node_label').value };
    if (f('git_push')) { body.git_push = f('git_push').checked; body.git_pr = f('git_pr').checked; }
    await post(`/api/projects/${id}`, body); toast('Saved'); close('modal-pj-settings');
  } else if (act === 'project-suggest') {
    await post(`/api/projects/${id}/suggestions/${encodeURIComponent(el.dataset.sid)}/${el.dataset.do}`, {});
    toast(el.dataset.do === 'add' ? 'Added to the instructions' : 'Dismissed');
    // Show the new instructions in the open Edit dialog; a name typed there is kept.
    if (!$('modal-pj-edit').classList.contains('hidden')) {
      const name = $('pj-edit-name').value;
      projects = await api('/api/projects');
      openPjEdit(id); $('pj-edit-name').value = name;
    }
    refresh();
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
    case 'proj-planner-pick': openPjSettings(id); return;
    case 'proj-planner-settings':
      location.hash = '#/settings';
      setTimeout(() => { const a = document.querySelector('#set-nav a[href="#set-tasks"]'); if (a) a.click(); }, 150);
      return;
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
    case 'proj-edit': close('modal-pj-files'); openPjEdit(id); return;
    case 'proj-edit-save': {
      const name = $('pj-edit-name').value.trim();
      if (!name) { toast('Give the project a name'); $('pj-edit-name').focus(); return; }
      await post(`/api/projects/${id}`, { name, instructions: $('pj-edit-instr').value });
      const p = projectById(id); if (p) { p.name = name; p.instructions = $('pj-edit-instr').value; }
      close('modal-pj-edit'); toast('Saved'); renderSidePins(); renderProjects(); renderSideTasks(); return;
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

// ---------- Edit dialog: name and project instructions ----------
function openPjEdit(id, focusName) {
  const p = projectById(id); if (!p) return;
  setHTML('pj-edit-body', `<label for="pj-edit-name">Name</label><input id="pj-edit-name" maxlength="120" value="${esc(p.name)}">
    <label for="pj-edit-instr">Project instructions</label>
    <p class="hint">Standing rules for every task in this project, added after the ones in Settings (and before an <code>OUTPOST.md</code> in the files).</p>
    <textarea id="pj-edit-instr" rows="6" spellcheck="false" placeholder="e.g. Tests use pytest. Keep Python 3.9 compatible.">${esc(p.instructions || '')}</textarea>
    ${(p.suggestions || []).map((s) => `<div class="note u6 nv-note"><span class="nv-grow"><b>Suggested after a failed task:</b> ${esc(s.text)} <span class="muted">(${esc(s.task_title || '')})</span></span><button class="small primary" data-act="project-suggest" data-id="${esc(p.id)}" data-sid="${esc(s.id)}" data-do="add">Add to instructions</button><button class="small" data-act="project-suggest" data-id="${esc(p.id)}" data-sid="${esc(s.id)}" data-do="dismiss">Dismiss</button></div>`).join('')}
    <div class="modal-foot"><span class="spacer"></span><button data-close-edit>Cancel</button><button class="primary" data-act="proj-edit-save" data-id="${esc(p.id)}">Save</button></div>`);
  $('pj-edit-body').querySelector('[data-close-edit]').onclick = () => close('modal-pj-edit');
  $('pj-edit-name').onkeydown = (e) => { if (e.key === 'Enter') { e.preventDefault(); $('pj-edit-body').querySelector('[data-act=proj-edit-save]').click(); } };
  open('modal-pj-edit');
  const f = focusName ? $('pj-edit-name') : $('pj-edit-instr');
  f.focus(); if (focusName) f.select();
}

// ---------- pinned projects in the sidebar ----------
function renderSidePins() {
  const pins = (projects || []).filter((p) => p.pinned);
  const el = $('side-pins');
  el.classList.toggle('hidden', !pins.length);
  setHTML(el, pins.map((p) => {
    const w = projectCounts(p.id).waiting;
    const cls = [page === 'project' && pageArg === p.id ? 'active' : '', ctxProjId === p.id ? 'menu-open' : ''].join(' ');
    return `<div class="side-pin ${cls}" data-proj="${esc(p.id)}"><a class="side-pin-link" href="#/projects/${esc(p.id)}" aria-keyshortcuts="Shift+F10 F2 Delete">
      <span class="side-pin-name">${esc(p.name)}</span>${w ? `<span class="side-pin-n" title="${w} waiting on you">${w}</span>` : ''}</a>
      <button class="side-task-more" data-proj-menu="${esc(p.id)}" aria-haspopup="menu" aria-expanded="${ctxProjId === p.id}" aria-label="Actions for ${esc(p.name)}" title="Actions">⋯</button></div>`;
  }).join(''));
}

// ---------- project context menu ----------
// Right-click a project (its card, its pinned row in the sidebar, or the title
// on its page), press its ⋯ button, hold a finger on it, or press Shift+F10 /
// the menu key on a focused one. F2 opens Edit and Delete deletes directly.
let ctxProjId = null, ctxProjReturn = null;
function openProjMenu(id, x, y, from) {
  const p = projectById(id);
  if (!p) return;
  closeTaskMenu(false); closeProjMenu(false); closeGroupCtx(false);
  ctxProjId = id; ctxProjReturn = from || null;
  const item = (act, text, extra = '') => `<button role="menuitem" tabindex="-1" data-pctx="${act}" ${extra}>${text}</button>`;
  const m = $('proj-ctx');
  m.innerHTML = [
    item('open-tab', 'Open in new tab'),
    item('edit', 'Edit…<span class="kbd">F2</span>'),
    item('pin', p.pinned ? 'Unpin from sidebar' : 'Pin to sidebar'),
    '<hr>',
    item('new-task', 'New task in this project'),
    item('duplicate', 'Duplicate'),
    item('copy-link', 'Copy link'),
    item('download', 'Download files (.zip)'),
    item('settings', 'Settings…'),
    '<hr>',
    item('clear-chat', 'Clear conversation…'),
    item('delete', 'Delete…<span class="kbd">Del</span>', 'class="danger"'),
  ].join('');
  m.setAttribute('aria-label', `Actions for ${p.name}`);
  m.classList.remove('hidden');
  const r = m.getBoundingClientRect(), pad = 6;
  m.style.left = Math.max(pad, Math.min(x, innerWidth - r.width - pad)) + 'px';
  m.style.top = Math.max(pad, Math.min(y, innerHeight - r.height - pad)) + 'px';
  projMenuMarks();
  m.querySelector('button').focus();
}
function closeProjMenu(restoreFocus = true) {
  const m = $('proj-ctx');
  if (!m || m.classList.contains('hidden')) return;
  m.classList.add('hidden'); m.innerHTML = '';
  ctxProjId = null;
  projMenuMarks();
  if (restoreFocus && ctxProjReturn && document.body.contains(ctxProjReturn)) ctxProjReturn.focus();
  ctxProjReturn = null;
}
// projMenuMarks highlights the item whose menu is open without re-rendering it,
// so focus and the card under the pointer stay put.
function projMenuMarks() {
  document.querySelectorAll('[data-proj]').forEach((el) => el.classList.toggle('menu-open', el.dataset.proj === ctxProjId));
  document.querySelectorAll('[data-proj-menu]').forEach((b) => b.setAttribute('aria-expanded', String(b.dataset.projMenu === ctxProjId)));
  $('pj-ctx-btn').setAttribute('aria-expanded', String(!!ctxProjId && ctxProjId === pageArg && page === 'project'));
}
const projLink = (id) => location.origin + location.pathname + '#/projects/' + id;

async function deleteProject(id) {
  const p = projectById(id); if (!p) return;
  if (p.tasks) { toast(`${p.name} still has ${p.tasks} task${p.tasks === 1 ? '' : 's'}; delete them first`, 4000); return; }
  await projectAction('project-delete', { dataset: { id } });
  refresh();
}

async function runProjMenu(act, id) {
  const p = projectById(id);
  closeProjMenu(!['edit', 'settings', 'duplicate'].includes(act));
  if (!p) return;
  try {
    switch (act) {
      case 'open-tab': window.open('#/projects/' + id, '_blank', 'noopener'); break;
      case 'edit': openPjEdit(id, false); break;
      case 'pin':
        await post(`/api/projects/${id}`, { pinned: !p.pinned }); p.pinned = !p.pinned;
        renderSidePins(); if (page === 'projects') renderProjects();
        toast(p.pinned ? 'Pinned to the sidebar' : 'Unpinned'); break;
      case 'new-task':
        try { localStorage.setItem('nt-project', id); } catch (e) {}
        location.hash = '#/new';
        setTimeout(() => { $('nt-project').value = id; renderNew(); $('nt-prompt').focus(); }, 0); break;
      case 'duplicate': if (p.git_url) openPjDup(p); else await duplicateProject(id, false); break;
      case 'copy-link': await copyText(projLink(id)); toast('Link copied'); break;
      case 'download': location.href = `/api/projects/${encodeURIComponent(id)}/download`; break;
      case 'settings': openPjSettings(id); break;
      case 'clear-chat':
        if (!confirm(`Clear ${p.name}'s conversation? The tasks it started are kept.`)) return;
        await api(`/api/projects/${encodeURIComponent(id)}/chat`, { method: 'DELETE' }); toast('Conversation cleared');
        if (page === 'project' && pageArg === id) pjFetch(true); break;
      case 'delete': await deleteProject(id); break;
    }
  } catch (e) { toast(e.message, 4000); }
}

// duplicateProject copies a project; detach leaves the copy without its git repository.
async function duplicateProject(id, detach) {
  const r = await post(`/api/projects/${encodeURIComponent(id)}/duplicate`, { detach });
  await refresh();
  toast(`Made ${r.name}: the files, settings and instructions are copied; tasks and the conversation are not` + (detach ? '; it is not connected to the repository' : ''), 4000);
  if (page === 'project') location.hash = '#/projects/' + r.id;
}
// A git project asks first: the copy would share the repository and its branch.
function openPjDup(p) {
  setHTML('pj-dup-body', `<div class="note warn">⚠ <b>${esc(p.name)}</b> is connected to <code>${esc(p.git_url)}</code> (branch <code>${esc(p.git_branch)}</code>).</div>
    <p class="hint">A copy that keeps the repository shares that branch: if you later turn pushing on in both, their commits land on the same branch. Pushing and pull requests start off in the copy either way.</p>
    <p class="hint"><b>Disconnect</b> makes the copy a plain folder with the same files, without the git history or the git settings.</p>
    <div class="modal-foot"><button data-dup="cancel">Cancel</button><span class="spacer"></span><button data-dup="keep">Keep the repository attached</button><button class="primary" data-dup="detach">Disconnect the copy</button></div>`);
  $('pj-dup-body').querySelectorAll('[data-dup]').forEach((b) => {
    b.onclick = async () => {
      const how = b.dataset.dup;
      close('modal-pj-dup');
      if (how === 'cancel') return;
      try { await duplicateProject(p.id, how === 'detach'); } catch (e) { toast(e.message, 4000); }
    };
  });
  open('modal-pj-dup');
}

(function wireProjMenu() {
  const m = $('proj-ctx');
  const roots = [$('side-pins'), $('projects-list'), document.querySelector('.pj-bar .pj-title')];
  const linkOf = (el) => el.querySelector('.side-pin-link, .pj-card-link') || el;
  const idOf = (el) => el.dataset.proj || pageArg;
  roots.forEach((root) => {
    const sel = root.classList.contains('pj-title') ? '.pj-title' : '[data-proj]';
    root.addEventListener('contextmenu', (e) => {
      const el = e.target.closest(sel);
      if (!el || e.target.closest('select')) return;
      e.preventDefault();
      if (e.clientX || e.clientY) openProjMenu(idOf(el), e.clientX, e.clientY, linkOf(el));
      else { const r = el.getBoundingClientRect(); openProjMenu(idOf(el), Math.min(r.right - 24, r.left + 200), r.top + 28, linkOf(el)); }
    });
    longPress(root, sel, (el, x, y) => openProjMenu(idOf(el), x, y, linkOf(el)));
    root.addEventListener('keydown', (e) => {
      if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;
      const el = e.target.closest('[data-proj]');
      if (!el || !e.target.matches('.side-pin-link, .pj-card-link')) return;
      if (e.key === 'F2') { e.preventDefault(); openPjEdit(el.dataset.proj, true); }
      else if (e.key === 'Delete') { e.preventDefault(); deleteProject(el.dataset.proj); }
    });
  });
  document.addEventListener('click', (e) => {
    const b = e.target.closest('[data-proj-menu], #pj-ctx-btn');
    if (!b) return;
    e.preventDefault(); e.stopPropagation();
    const id = b.dataset.projMenu || pageArg;
    if (ctxProjId === id) { closeProjMenu(); return; }
    const r = b.getBoundingClientRect();
    openProjMenu(id, b.id === 'pj-ctx-btn' ? r.right - 220 : r.left, r.bottom + 4, b);
  }, true);
  m.addEventListener('click', (e) => {
    const b = e.target.closest('[data-pctx]');
    if (b) runProjMenu(b.dataset.pctx, ctxProjId);
  });
  menuKeys(m, closeProjMenu);
  document.addEventListener('pointerdown', (e) => { if (ctxProjId && !m.contains(e.target) && !e.target.closest('[data-proj-menu], #pj-ctx-btn')) closeProjMenu(false); });
  $('side-tasks').addEventListener('scroll', () => closeProjMenu(false));
  window.addEventListener('resize', () => closeProjMenu(false));
  window.addEventListener('hashchange', () => closeProjMenu(false));
})();
