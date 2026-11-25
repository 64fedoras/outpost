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
// Without a saved choice the panel opens only once the project has tasks; an empty one is just empty headings.
const pjPanelDefault = (id) => pjWide() && projectTasks(id).length > 0;

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
  setHTML('projects-list', `<div class="pj-cards">${cards || `<button class="pj-card pj-card-new" data-act="proj-new">＋ New project<span class="pj-card-sub">An empty folder, or a clone of a git repository</span></button>`}</div>`); // the header button covers it once there are cards
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
      if (d.chat && (d.chat.busy || d.chat.research)) pjTimer = setTimeout(() => { if (page === 'project' && pageArg === id) pjFetch(true); }, 1200);
    }
  } catch (e) { if (pageArg === id) { pjData = null; pjDataFor = id; pjFetchedAt = Date.now(); renderProjectPage(); } }
  pjFetching = false;
}
let pjRouteFor = ''; // the project the page was last routed to; '' after leaving the page
function projectRoute() {
  if (pjDataFor !== pageArg) { pjData = null; pjResolvedOpen = !pjFolded.has('resolved'); pjCtx.mem = pjCtx.stash = null; }
  if (pjRouteFor !== pageArg) { // a fresh visit; opening or closing a task in the pane keeps the composer and the panel
    $('pj-text').value = pjNewDraft = '';
    autoGrow($('pj-text'));
    pjPanelOpen = pjPanelPref() ?? pjPanelDefault(pageArg);
    if (!pjWide()) pjPanelOpen = false; // the phone opens the panel by hand, as a sheet
  }
  pjRouteFor = pageArg;
  if (paneTask) pjPanelOpen = true;
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
  return `<div class="pj-tcard ${t.resolved ? 'resolved' : ''}">${statusIcon(taskIconKey(t))}
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
  const known = new Set((ov.files || []).map((f) => f.path));
  pvReset();
  msgs.forEach((m) => (m.reads || []).forEach((r) => { const x = /^Read (\S+)/.exec(r); if (x) known.add(x[1]); }));
  msgs.forEach((m) => {
    let html;
    if (m.role === 'user') html = `<div class="pj-ask"><div class="bubble">${esc(m.text)}</div><span class="pj-when">${(m.tasks || []).length ? 'started as a task · ' : ''}${ago(m.time)}</span></div>${cards(m.tasks)}`;
    else {
      const meta = [m.who && esc(m.who), m.cost_usd ? usd(m.cost_usd) : '', ago(m.time)].filter(Boolean).join(' · ');
      html = `<div class="pj-answer">${pjResearch(m)}${pjReads(m.reads)}${m.text ? `<div class="pj-answer-text md">${pjLinkRefs(md(m.text), known)}</div>` : ''}${m.text ? pvCards(pvCollect(m.text)) : ''}${offerCard(m, { project: p.id })}${memSuggestCard(m, { project: p.id })}${m.error ? `<div class="error-msg">${m.error === 'stopped' ? 'Stopped.' : esc(m.error)}${/planner|API key/i.test(m.error) && m.error !== 'stopped' ? `<div class="card-actions"><button class="small" data-act="proj-planner-pick" data-id="${esc(p.id)}">Pick this project's planner</button> <a href="#/settings" data-act="proj-planner-settings">Open Settings → Tasks</a></div>` : ''}</div>` : ''}${cards(m.tasks)}<div class="pj-when">${meta}</div></div>`;
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
  if (ov.chat && ov.chat.busy) items.push({ html: `<div class="pj-answer pj-thinking"><span class="dots" aria-label="Thinking"><i></i><i></i><i></i></span> <span class="muted">${(ov.chat.reading || []).length ? 'Reading the project…' : 'Thinking…'}</span> <button class="small" data-act="proj-chat-stop">Stop</button>${pjReads(ov.chat.reading)}</div>` });
  const more = hidden > 0 ? `<button class="link-btn pj-showres" data-act="proj-show-resolved">${pjResolvedOpen ? 'Hide resolved tasks' : `${hidden} resolved task${hidden === 1 ? '' : 's'} hidden. Show them`}</button>` : '';
  if (!items.length) return `${more}<div class="pj-empty"><h3>What should ${esc(p.name)} do next?</h3><p class="hint"><b>Ask</b> talks it over with the planner, which can start tasks for you. <b>Start as task</b> skips the talk and runs your message as a task. Every task starts from the project's current files; one runs at a time.</p></div>`;
  return more + items.map((i) => `<div class="pj-item">${i.html}</div>`).join('');
}

// pjResearch shows a research answer (projects_research.go): the lanes while
// it works, then a folded record above the report and where it was saved.
const RS_LIVE = ['planning', 'lanes', 'writing'];
function pjResearch(m) {
  const r = m.research;
  if (!r) return '';
  const q = m.question ? `<div class="pj-rs-q">🔎 Research: ${esc(m.question)}</div>` : '';
  if (RS_LIVE.includes(r.stage)) return `${q}${researchLive(r).replace('</div>', ' <button class="small" data-act="proj-chat-stop">Stop</button></div>')}`;
  const saved = m.saved ? `<div class="pj-rs-saved muted">Saved to the project's <a href="#" data-act="proj-open-stash">Stash</a> as ${esc(m.saved)}, so tasks can use it.${m.note ? ' ' + esc(m.note) : ''}</div>` : (m.note ? `<div class="pj-rs-saved muted">${esc(m.note)}</div>` : '');
  return `${q}${researchCard(m)}${saved}`;
}

// pjReads shows what the planner looked at while answering, one chip per tool call.
function pjReads(list) {
  if (!list || !list.length) return '';
  return `<div class="pj-reads">${list.map((r) => {
    const x = /^Read (\S+)(?: (\d+))?/.exec(r);
    return x ? `<a href="#" class="pj-read" data-act="proj-file" data-path="${esc(x[1])}"${x[2] ? ` data-line="${esc(x[2])}"` : ''}>${esc(r)}</a>` : `<span class="pj-read">${esc(r)}</span>`;
  }).join('')}</div>`;
}

// pjLinkRefs turns project file names in an answer (app.go, src/app.go:42)
// into links that open the file viewer at that line. Code blocks and links are left alone.
function pjLinkRefs(html, known) {
  if (!known.size) return html;
  const base = {}; known.forEach((p) => { const b = p.split('/').pop(); base[b] = base[b] === undefined ? p : null; });
  const tpl = document.createElement('template'); tpl.innerHTML = html;
  const walk = document.createTreeWalker(tpl.content, NodeFilter.SHOW_TEXT);
  const nodes = []; while (walk.nextNode()) nodes.push(walk.currentNode);
  const re = /([\w./-]+\.[A-Za-z0-9]{1,8})(?::(\d+)(?:-\d+)?)?/g;
  nodes.forEach((n) => {
    if (n.parentElement && n.parentElement.closest('a, pre')) return;
    const s = n.nodeValue; let last = 0, frag = null, m;
    re.lastIndex = 0;
    while ((m = re.exec(s))) {
      const name = m[1].replace(/^\.\//, '');
      const path = known.has(name) ? name : base[name];
      if (!path) continue;
      frag = frag || document.createDocumentFragment();
      frag.append(s.slice(last, m.index));
      const a = document.createElement('a');
      a.href = '#'; a.className = 'pj-ref'; a.dataset.act = 'proj-file'; a.dataset.path = path;
      if (m[2]) a.dataset.line = m[2];
      a.textContent = m[0]; frag.append(a); last = m.index + m[0].length;
    }
    if (frag) { frag.append(s.slice(last)); n.replaceWith(frag); }
  });
  return tpl.innerHTML;
}

// The read-only file viewer for project files, scrolled to a line when given.
async function openPjFile(path, line) {
  const id = pageArg;
  $('pjf-title').textContent = path + (line ? `:${line}` : '');
  $('pjf-dl').href = `/api/projects/${encodeURIComponent(id)}/file?path=${encodeURIComponent(path)}&download=1`;
  $('pjf-body').textContent = 'Loading…';
  open('modal-pj-file');
  try {
    const r = await fetch(`/api/projects/${encodeURIComponent(id)}/file?path=${encodeURIComponent(path)}`, { credentials: 'same-origin' });
    const text = await r.text();
    if (!r.ok) { $('pjf-body').textContent = 'This file is not in the project now (a later task may have moved or deleted it).'; return; }
    setHTML('pjf-body', numbered(text, path).split('\n').map((l, i) => i + 1 === +line ? `<span class="pj-hl">${l}</span>` : l).join('\n'));
    const hl = $('pjf-body').querySelector('.pj-hl');
    if (hl) hl.scrollIntoView({ block: 'center' });
  } catch (e) { $('pjf-body').textContent = e.message; }
}

// One task in the Threads pane: status icon, title (up to two lines), a one-line
// subtitle (the last result note once it rests, else what it is doing), the time on the
// right, and a third line with the progress strip or the button that answers it.
function pjRow(t, f) {
  const g = projectGroup(t);
  const tone = taskTone(t);
  const note = f && f.result && g !== 'working' && g !== 'waiting' ? f.result.split('\n').find((l) => l.trim()) : '';
  const sub = (note || statusWord(t)) + (t.round > 1 && !note ? ' · request ' + t.round : '');
  let extra = '';
  if (g === 'waiting') extra = `<a href="#/tasks/${esc(t.id)}" tabindex="-1"><button class="small primary">${t.status === 'awaiting_approval' ? 'Review' : t.status === 'awaiting_answers' ? 'Answer' : 'Open'}</button></a>`;
  else if (g === 'working' && f && f.subtasks.length) extra = `${pjStrip(f.subtasks, 'strip pj-mini')}<span class="pj-cnt">${t.done}/${t.total}</span>`;
  const rb = canResolve(t) ? `<button class="pj-rb" data-act="proj-resolve" data-id="${esc(t.id)}" data-on="${t.resolved ? '' : '1'}" title="${t.resolved ? 'Reopen (move back to Idle)' : 'Mark resolved'}" aria-label="${t.resolved ? 'Reopen' : 'Mark resolved'}: ${esc(t.title)}">${t.resolved ? '↺' : '✓'}</button>` : '';
  return `<div class="pj-row g-${g}">${statusIcon(g === 'resolved' ? 'done' : taskIconKey(t))}
    <a class="pj-row-main" href="#/tasks/${esc(t.id)}"><span class="pj-row-t">${esc(t.title)}</span><span class="pj-row-m ${tone === 'needs' ? 'needs' : tone === 'bad' && g !== 'resolved' ? 'bad' : ''}" title="${esc(sub)}">${esc(sub)}</span></a>
    <span class="pj-row-r">${rb}<span>${ago(t.updated).replace(' ago', '')}</span></span>
    ${extra ? `<div class="pj-row-x">${extra}</div>` : ''}</div>`;
}
const ART_ICON = { shot: '▭', build: '⚙', result: '⇩', pr: '⇄', resource: '▤' };
const ART_KIND = { shot: 'Screenshot', build: 'Build', result: 'Result', pr: 'GitHub', resource: 'Stash' };
function pjArtCard(a) {
  const ext = a.kind === 'resource' ? (a.name.split('.').pop() || '').slice(0, 5).toUpperCase() : '';
  const pv = a.preview ? `<img src="${esc(a.preview)}" alt="" loading="lazy">` : `<span>${ART_ICON[a.kind] || '▤'}${ext && ext !== a.name.toUpperCase() ? `<small>${esc(ext)}</small>` : ''}</span>`;
  const ext2 = a.kind === 'shot' || a.kind === 'pr' ? 'target="_blank" rel="noopener"' : '';
  const sub = a.kind === 'resource' ? 'Stash' : a.task_title || ART_KIND[a.kind] || '';
  return `<a class="pj-acard k-${esc(a.kind)}" href="${esc(a.url)}" ${ext2} title="${esc(a.name)}${sub ? ' · ' + esc(sub) : ''}">
    <span class="pj-apv k-${esc(a.kind)}">${pv}</span><span class="pj-acap"><span class="pj-aname">${esc(a.name)}</span>${a.time && +new Date(a.time) > 0 ? `<span class="pj-awhen">${esc(ago(a.time).replace(' ago', ''))}</span>` : ''}</span></a>`;
}
// Which Threads sections are folded, kept per browser (Resolved starts folded).
const pjFolded = new Set((() => { try { const v = JSON.parse(localStorage.getItem('outpost.pjFolded') || 'null'); return Array.isArray(v) ? v : ['resolved']; } catch (e) { return ['resolved']; } })());
function pjFold(key) {
  if (pjFolded.has(key)) pjFolded.delete(key); else pjFolded.add(key);
  try { localStorage.setItem('outpost.pjFolded', JSON.stringify([...pjFolded])); } catch (e) { /* only for this visit */ }
  pjResolvedOpen = !pjFolded.has('resolved');
}
const PJ_ARTS_ROW = 12; // cards in the scrolling row before "View all"
function renderPjPanel(p) {
  const list = projectTasks(p.id);
  const feed = {}; ((pjData && pjDataFor === p.id && pjData.feed) || []).forEach((f) => { feed[f.id] = f; });
  const groups = { waiting: [], working: [], idle: [], resolved: [] };
  list.forEach((t) => groups[projectGroup(t)].push(t));
  Object.values(groups).forEach((g) => g.sort((x, y) => new Date(y.updated) - new Date(x.updated)));
  const arts = (pjData && pjDataFor === p.id && pjData.artifacts) || [];
  const head = (key, name, n, extra = '') => `<div class="pj-sec-h"><button class="pj-toggle" data-act="proj-fold" data-key="${key}" aria-expanded="${!pjFolded.has(key)}"><b>${name}</b><span class="pj-n">· ${n}</span><span class="pj-chev" aria-hidden="true">${pjFolded.has(key) ? '›' : '⌄'}</span></button>${extra}</div>`;
  const sec = (key, name, extra = '') => {
    const g = groups[key];
    if (!g.length && key !== 'resolved') return '';
    return `<section class="pj-sec ${key}">${head(key, name, g.length, extra)}${pjFolded.has(key) ? '' : g.map((t) => pjRow(t, feed[t.id])).join('')}</section>`;
  };
  const shown = arts.slice(0, PJ_ARTS_ROW);
  const artsSec = arts.length ? `<section class="pj-sec arts">${head('arts', 'Artifacts', arts.length)}
      ${pjFolded.has('arts') ? '' : `<div class="pj-arts-wrap"><div class="pj-arts" id="pj-arts-row">${shown.map(pjArtCard).join('')}${arts.length > 4 ? `<button class="pj-acard pj-aall" data-act="proj-arts">View all ${arts.length}</button>` : ''}</div>
        <button class="pj-ascroll prev" data-act="proj-arts-scroll" data-dir="-1" aria-label="Scroll artifacts left" tabindex="-1">‹</button><button class="pj-ascroll next" data-act="proj-arts-scroll" data-dir="1" aria-label="Scroll artifacts right" tabindex="-1">›</button></div>`}</section>`
    : `<section class="pj-sec arts"><div class="pj-sec-h"><b>Artifacts</b><span class="pj-n">· 0</span></div><p class="pj-none">Screenshots, builds, pull requests and the project's <button class="link-btn" data-act="proj-stash">Stash</button> documents show here.</p></section>`;
  const none = list.length ? '' : '<p class="pj-none">No tasks yet. Ask something below, or start it as a task.</p>';
  return `${artsSec}
    ${sec('waiting', 'Waiting on you')}
    ${sec('working', 'Working', groups.working.length > 1 ? '<span class="pj-note" title="One task works at a time; the rest are queued">one at a time</span>' : '')}
    ${sec('idle', 'Idle', groups.idle.some(canResolve) && !pjFolded.has('idle') ? '<span class="spacer"></span><button class="link-btn" data-act="proj-resolve-all">Resolve all</button>' : '')}
    ${none}${list.length ? sec('resolved', 'Resolved') : ''}`;
}
// The scroll arrows on the artifacts row show only while there is more to that side.
function pjArtsArrows() {
  const r = $('pj-arts-row');
  if (!r) return;
  const w = r.parentElement;
  w.classList.toggle('can-prev', r.scrollLeft > 2);
  w.classList.toggle('can-next', r.scrollLeft + r.clientWidth < r.scrollWidth - 2);
  if (!r._arrows) { r._arrows = true; r.addEventListener('scroll', pjArtsArrows, { passive: true }); }
}

function renderProjectPage() {
  if (page !== 'project') return;
  const p = projectById(pageArg);
  $('pj-wrap').classList.toggle('panel-open', pjPanelOpen);
  pjPaneChrome();
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
  if (!paneTask && pjTab === 'threads') { setHTML('pj-threads', renderPjPanel(p)); pjArtsArrows(); }
  if (!paneTask && pjTab === 'context' && pjPanelOpen) { setHTML('pj-context', renderPjContext(p)); pjCtxLoad(p.id); }
  if ($('modal-pj-files') && !$('modal-pj-files').classList.contains('hidden')) renderPjFiles(p);
  if (!pjData || pjDataFor !== p.id || Date.now() - pjFetchedAt > 3000) pjFetch();
}

function setPjPanel(open) {
  if (!open && paneTask) { pjPanelOpen = false; location.hash = '#/projects/' + pageArg; return; } // closing the pane closes the task in it
  pjPanelOpen = open;
  if (pjWide()) { try { localStorage.setItem('pj-panel', open ? '1' : '0'); } catch (e) {} }
  renderProjectPage();
  if (open && !pjWide()) $('pj-panel-close').focus();
}
const PJ_W_LIST = { key: 'outpost.pjPanelW', def: 380, min: 280, max: 760 };
const PJ_W_TASK = { key: 'outpost.pjTaskW', def: 640, min: 380, max: 1200 }; // a task in the pane has its own, wider width
const pjResizer = sideResizer({ wrap: $('pj-wrap'), panel: $('pj-panel'), cssVar: '--pj-panel-w', ...PJ_W_LIST, label: 'Resize the project pane' });

// ---------- the pane's tabs and a task inside it ----------
let pjTab = (() => { try { return localStorage.getItem('outpost.pjTab') === 'context' ? 'context' : 'threads'; } catch (e) { return 'threads'; } })();
function pjSetTab(tab) {
  pjTab = tab;
  try { localStorage.setItem('outpost.pjTab', tab); } catch (e) { /* only for this visit */ }
  renderProjectPage();
}
// The task page's section moves into the pane while a task is open there and back afterwards,
// so the one task view (thread.js) serves both; route() calls this after it shows and hides pages.
const pjTaskSec = document.querySelector('section[data-page="task"]');
const pjTaskHome = { parent: pjTaskSec.parentElement, next: pjTaskSec.nextElementSibling };
function pjPlaceTask() {
  const inPane = pjTaskSec.parentElement === $('pj-panel');
  if (page === 'project' && paneTask) {
    if (!inPane) $('pj-panel').append(pjTaskSec);
    pjTaskSec.classList.remove('hidden');
    pjTaskSec.classList.add('in-pane');
  } else if (inPane) {
    pjTaskHome.parent.insertBefore(pjTaskSec, pjTaskHome.next);
    pjTaskSec.classList.remove('in-pane');
  }
  if (page !== 'project') pjRouteFor = '';
}
function pjPaneChrome() {
  const inTask = !!paneTask;
  $('pj-wrap').classList.toggle('pane-task', inTask);
  $('pj-tabs').classList.toggle('hidden', inTask);
  $('pj-pane-back').classList.toggle('hidden', !inTask);
  $('pj-pane-full').classList.toggle('hidden', !inTask);
  if (inTask) $('pj-pane-full').href = '#/tasks/' + paneTask;
  $('pj-threads').classList.toggle('hidden', inTask || pjTab !== 'threads');
  $('pj-context').classList.toggle('hidden', inTask || pjTab !== 'context');
  ['threads', 'context'].forEach((k) => { const b = $('pj-tab-' + k); b.setAttribute('aria-selected', String(pjTab === k)); b.tabIndex = pjTab === k ? 0 : -1; b.classList.toggle('on', pjTab === k); });
  $('pj-panel').setAttribute('aria-label', inTask ? 'Task' : pjTab === 'context' ? 'Context' : 'Threads');
  pjResizer.use(inTask ? PJ_W_TASK : PJ_W_LIST);
  pjResizer.refresh();
}

// ---------- the Context tab ----------
// What the project's work reads from, in one place: its instructions, its memory (Settings →
// Memory facts for this project; the project conversation and its tasks' planners start with
// them), its files and its Stash. Each section folds; "Add context" adds to any of them.
const pjCtx = { mem: null, memAt: 0, stash: null, stashFor: '', stashAt: 0, loading: false, openDirs: new Set() };
const pjCtxFolded = new Set((() => { try { const v = JSON.parse(localStorage.getItem('outpost.pjCtxFolded') || 'null'); return Array.isArray(v) ? v : []; } catch (e) { return []; } })());
async function pjCtxLoad(id, force) {
  if (pjCtx.loading) return;
  const sid = pjData && pjDataFor === id ? pjData.stash_id || '' : '';
  const now = Date.now();
  const wantMem = force || !pjCtx.mem || now - pjCtx.memAt > 10000;
  const indexing = pjCtx.stash && (pjCtx.stash.resources || []).some((r) => r.status && r.status !== 'ready');
  const wantStash = sid && (force || pjCtx.stashFor !== sid || now - pjCtx.stashAt > (indexing ? 2000 : 10000));
  if (!wantMem && !wantStash) return;
  pjCtx.loading = true;
  try {
    const [mem, st] = await Promise.all([wantMem ? api('/api/memory').catch(() => null) : null, wantStash ? api('/api/stash/' + encodeURIComponent(sid)).catch(() => null) : null]);
    if (mem) { pjCtx.mem = mem.memories || []; pjCtx.memAt = Date.now(); }
    if (st) { pjCtx.stash = st; pjCtx.stashFor = sid; pjCtx.stashAt = Date.now(); }
  } finally { pjCtx.loading = false; }
  if (page === 'project' && pageArg === id && pjTab === 'context' && !paneTask) setHTML('pj-context', renderPjContext(projectById(id)));
  if (indexing || (pjCtx.stash && (pjCtx.stash.resources || []).some((r) => r.status && r.status !== 'ready'))) setTimeout(() => { if (page === 'project' && pageArg === id && pjTab === 'context') pjCtxLoad(id); }, 2000);
}
function pjCtxHead(key, name, n, extra = '') {
  const f = pjCtxFolded.has(key);
  return `<div class="pj-sec-h"><button class="pj-toggle" data-act="proj-ctx-fold" data-key="${key}" aria-expanded="${!f}"><span class="pj-chev" aria-hidden="true">${f ? '›' : '⌄'}</span><b>${name}</b><span class="pj-n">${n}</span></button><span class="spacer"></span>${extra}</div>`;
}
const PJ_FILE_ICON = '<svg class="pj-ci" viewBox="0 0 16 16" aria-hidden="true"><path d="M4 1.5h5l3 3v10H4z" fill="none" stroke="currentColor" stroke-width="1.2"/><path d="M9 1.5v3h3" fill="none" stroke="currentColor" stroke-width="1.2"/></svg>';
const PJ_DIR_ICON = '<svg class="pj-ci" viewBox="0 0 16 16" aria-hidden="true"><path d="M1.5 3.5h4l1.5 1.5h7.5v8h-13z" fill="none" stroke="currentColor" stroke-width="1.2"/></svg>';
// pjTree turns the flat file list into rows: folders (folded unless opened) then files.
function pjTree(files) {
  const root = { dirs: {}, files: [] };
  files.forEach((f) => {
    const parts = f.path.split('/'); let n = root;
    parts.slice(0, -1).forEach((d) => { n = n.dirs[d] = n.dirs[d] || { dirs: {}, files: [] }; });
    n.files.push(f);
  });
  const count = (n) => n.files.length + Object.values(n.dirs).reduce((a, d) => a + count(d), 0);
  const rows = (n, base, depth) => Object.keys(n.dirs).sort().map((d) => {
    const path = base + d + '/', open = pjCtx.openDirs.has(path), kid = n.dirs[d];
    return `<button class="pj-crow dir pj-d${Math.min(depth, 6)}" data-act="proj-ctx-dir" data-path="${esc(path)}" aria-expanded="${open}"><span class="pj-chev" aria-hidden="true">${open ? '⌄' : '›'}</span>${PJ_DIR_ICON}<span class="pj-cn">${esc(d)}</span><span class="pj-cm">${count(kid)}</span></button>${open ? rows(kid, path, depth + 1) : ''}`;
  }).join('') + n.files.slice().sort((a, b) => a.path.localeCompare(b.path)).map((f) => `<button class="pj-crow pj-d${Math.min(depth, 6)}" data-act="proj-file" data-path="${esc(f.path)}" title="${esc(f.path)}"><span class="pj-chev"></span>${PJ_FILE_ICON}<span class="pj-cn">${esc(f.path.split('/').pop())}</span><span class="pj-cm">${bytes(f.bytes)}</span></button>`).join('');
  return rows(root, '', 0);
}
function renderPjContext(p) {
  if (!p) return '';
  const ov = pjData && pjDataFor === p.id ? pjData : null;
  const instr = (p.instructions || '').trim();
  const mem = (pjCtx.mem || []).filter((m) => m.project === p.id);
  const shared = (pjCtx.mem || []).filter((m) => !m.project).length;
  const memOff = typeof settings !== 'undefined' && settings && settings.memory_on === false;
  const files = ov ? ov.files || [] : [], total = ov ? ov.files_total || 0 : 0;
  const st = ov && ov.stash_id && pjCtx.stashFor === ov.stash_id ? pjCtx.stash : null;
  const res = st ? st.resources || [] : [];
  const sec = (key, name, n, body, extra) => `<section class="pj-sec ctx ${key}">${pjCtxHead(key, name, n, extra)}${pjCtxFolded.has(key) ? '' : body}</section>`;
  const add = (act, label) => `<button class="pj-cadd" data-act="${act}" title="${label}" aria-label="${label}">＋</button>`;
  const instrBody = instr
    ? `<button class="pj-crow" data-act="proj-ctx-instr"><span class="pj-chev"></span><svg class="pj-ci" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 2.5h10v11H3zM5.5 5.5h5M5.5 8h5M5.5 10.5h3" fill="none" stroke="currentColor" stroke-width="1.2"/></svg><span class="pj-cn">Project instructions</span><span class="pj-cp">${esc(instr.replace(/\s+/g, ' ').slice(0, 160))}</span></button>`
    : '<p class="pj-cnote">No instructions yet. They are standing rules every task and the project conversation follow, such as the language, test style or what never to touch. <button class="link-btn" data-act="proj-ctx-instr">Add instructions</button></p>';
  const memBody = (pjCtx.mem === null ? '<p class="pj-cnote">Loading…</p>' : mem.map((m) => `<div class="pj-crow mem"><span class="pj-chev"></span><svg class="pj-ci" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.2"/><path d="M8 5v3.2l2 1.3" fill="none" stroke="currentColor" stroke-width="1.2"/></svg><span class="pj-cn wrap">${esc(m.text)}</span><span class="pj-cm">${esc(ago(m.created).replace(' ago', ''))}</span><button class="pj-rb" data-act="proj-ctx-forget" data-id="${esc(m.id)}" title="Forget" aria-label="Forget: ${esc(m.text)}">✕</button></div>`).join('')
    + `<p class="pj-cnote">${memOff ? '<b>Memory is off</b> in Settings, so none of this is used now. ' : ''}${mem.length ? '' : 'Nothing remembered for this project yet. '}The project conversation and the planner of each task here start with these${shared ? `, plus ${shared} fact${shared === 1 ? '' : 's'} kept for everywhere` : ''}. <a href="#/settings">Settings → Memory</a></p>`);
  const filesBody = files.length ? `<div class="pj-tree">${pjTree(files)}</div>${total > files.length ? `<p class="pj-cnote">Showing ${files.length} of ${total} files. Download the .zip for all of them.</p>` : ''}`
    : '<p class="pj-cnote">No files yet. Tasks write the project\'s files here; each task starts from them.</p>';
  const resBody = (ov && ov.stash_id && !st ? '<p class="pj-cnote">Loading…</p>' : res.map((r) => {
    const stTxt = r.status === 'ready' ? `${r.passages} passage${r.passages === 1 ? '' : 's'}` : r.status === 'error' || r.error ? 'could not read' : r.total ? `indexing ${r.done || 0}/${r.total}` : esc(r.status || '');
    return `<div class="pj-crow res"><span class="pj-chev"></span><span class="pj-kind">${esc((r.kind || 'text').toUpperCase().slice(0, 4))}</span><a class="pj-cn" href="#/stash/${esc(ov.stash_id)}" title="${esc(r.name)}${r.error ? ' · ' + esc(r.error) : ''}">${esc(r.name)}</a><span class="pj-cm ${r.error ? 'bad' : ''}">${stTxt}</span><button class="pj-rb" data-act="proj-ctx-unstash" data-id="${esc(r.id)}" title="Remove from the Stash" aria-label="Remove ${esc(r.name)} from the Stash">✕</button></div>`;
  }).join(''))
    + `<p class="pj-cnote">${res.length ? '' : 'No documents yet. '}PDF, Word, Excel, OpenDocument, CSV or text. A task's planner on a node is given the passages that match its request; they are never sent to Claude or OpenRouter, and nodes never keep them.${ov && ov.stash_id ? ` <a href="#/stash/${esc(ov.stash_id)}">Open the Stash</a>` : ''}</p>`;
  return `<div class="pj-ctx-top"><span class="muted">What this project's work reads from</span><span class="spacer"></span>
      <details class="menu pj-addctx"><summary class="small">＋ Add context</summary><div class="menu-list" role="menu">
        <button role="menuitem" data-act="proj-ctx-instr">Instructions…</button>
        <button role="menuitem" data-act="proj-ctx-remember">Memory note…</button>
        <button role="menuitem" data-act="proj-ctx-upload">Stash documents…</button></div></details></div>
    ${sec('instr', 'Instructions', instr ? 1 : 0, instrBody)}
    ${sec('mem', 'Memory', pjCtx.mem === null ? '' : mem.length, memBody, add('proj-ctx-remember', 'Remember something for this project'))}
    ${sec('files', 'Files', total, filesBody)}
    ${sec('stash', 'Stash', res.length, resBody, add('proj-ctx-upload', 'Add documents to the Stash'))}`;
}
// Upload chosen files into the project's Stash (made on first use) one by one.
async function pjStashUpload(fileList) {
  const id = pageArg, files = [...fileList];
  if (!files.length) return;
  const { id: sid } = await post(`/api/projects/${encodeURIComponent(id)}/stash`);
  let ok = 0;
  for (const f of files) {
    try {
      const r = await fetch(`/api/stash/${encodeURIComponent(sid)}/resources?name=${encodeURIComponent(f.name)}`, { method: 'POST', body: f, credentials: 'same-origin' });
      if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
      ok++;
    } catch (e) { toast(`${f.name}: ${e.message}`, 5000); }
  }
  if (ok) toast(`Added ${ok} document${ok === 1 ? '' : 's'} to the Stash`);
  await pjFetch(true); pjCtxLoad(id, true);
}
$('pj-threads-btn').onclick = () => { if (pjPanelOpen && pjTab !== 'threads' && !paneTask) { pjSetTab('threads'); return; } setPjPanel(!pjPanelOpen); };
$('pj-pane-back').onclick = () => { location.hash = '#/projects/' + pageArg; };
$('pj-tabs').onclick = (e) => { const b = e.target.closest('[data-tab]'); if (b) pjSetTab(b.dataset.tab); };
$('pj-tabs').onkeydown = (e) => { // arrow keys move between the two tabs
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
  e.preventDefault(); pjSetTab(pjTab === 'threads' ? 'context' : 'threads'); $('pj-tab-' + pjTab).focus();
};
// On a wide screen a task link anywhere on the project page (the Threads pane, a card in the
// conversation) opens the task inside the pane; a modified click or a phone opens its page.
$('pj-wrap').addEventListener('click', (e) => {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || !pjWide() || page !== 'project') return;
  const a = e.target.closest('a[href^="#/tasks/"]');
  if (!a || a.target === '_blank' || a.id === 'pj-pane-full') return;
  const id = a.getAttribute('href').slice(8).split(/[/?#]/)[0];
  if (!id) return;
  e.preventDefault();
  location.hash = `#/projects/${pageArg}/tasks/${id}`;
});
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
// Enter asks, like Chat; Shift+Enter adds a line. Not while an input method is composing.
$('pj-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); pjSend(false); } });
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
    ${files.length ? `<ul class="pj-files">${files.map((f) => `<li><a href="#" class="mono" data-act="proj-file" data-path="${esc(f.path)}">${esc(f.path)}</a><span class="muted">${bytes(f.bytes)}</span></li>`).join('')}</ul>${total > files.length ? `<p class="hint">Showing the first ${files.length}. The .zip has them all.</p>` : ''}` : '<p class="hint">No files yet. The first task writes them.</p>'}`);
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
    if (!await askConfirm('Delete this project and its files? Its git remote is not touched. (A project that still has tasks cannot be deleted; delete them first.)')) return;
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
    case 'proj-new': npGit(false); open('modal-newproj'); return;
    case 'proj-planner-pick': openPjSettings(id); return;
    case 'proj-planner-settings':
      location.hash = '#/settings';
      setTimeout(() => { const a = document.querySelector('#set-nav a[href="#set-tasks"]'); if (a) a.click(); }, 150);
      return;
    case 'proj-arts': openPjArts(); return;
    case 'proj-show-resolved': pjResolvedOpen = !pjResolvedOpen; if (pjResolvedOpen) pjFolded.delete('resolved'); else pjFolded.add('resolved'); renderProjectPage(); return;
    case 'proj-fold': pjFold(el.dataset.key); renderProjectPage(); return;
    case 'proj-arts-scroll': { const r = $('pj-arts-row'); if (r) r.scrollBy({ left: +el.dataset.dir * Math.max(160, r.clientWidth - 80), behavior: 'smooth' }); return; }
    case 'proj-stash': $('pj-stash-btn').click(); return;
    case 'proj-ctx-fold': { const k = el.dataset.key; if (pjCtxFolded.has(k)) pjCtxFolded.delete(k); else pjCtxFolded.add(k); try { localStorage.setItem('outpost.pjCtxFolded', JSON.stringify([...pjCtxFolded])); } catch (e) { /* only for this visit */ } renderProjectPage(); return; }
    case 'proj-ctx-dir': { const d = el.dataset.path; if (pjCtx.openDirs.has(d)) pjCtx.openDirs.delete(d); else pjCtx.openDirs.add(d); renderProjectPage(); return; }
    case 'proj-ctx-instr': pjCloseAddMenu(); openPjEdit(pageArg); setTimeout(() => $('pj-edit-instr') && $('pj-edit-instr').focus(), 0); return;
    case 'proj-ctx-remember': {
      pjCloseAddMenu();
      const text = await askText('Remember for this project', '', { ok: 'Remember', body: 'One short fact the project conversation and the planner of each task here should know, such as "The dice app must stay Python 3.9 compatible." Up to 300 characters.' });
      if (text === null || !String(text).trim()) return;
      await post('/api/memory', { text: String(text).trim(), project: pageArg, source: 'added by hand' });
      toast('Remembered'); pjCtxLoad(pageArg, true); return;
    }
    case 'proj-ctx-forget':
      if (!await askConfirm('Forget this? The project stops using it from the next message or task.')) return;
      await api('/api/memory/' + encodeURIComponent(el.dataset.id), { method: 'DELETE' }); pjCtxLoad(pageArg, true); return;
    case 'proj-ctx-upload': pjCloseAddMenu(); $('pj-ctx-file').click(); return;
    case 'proj-ctx-unstash': {
      const sid = pjData && pjData.stash_id;
      if (!sid || !await askConfirm('Remove this document from the project\'s Stash? Tasks stop using it.')) return;
      await api(`/api/stash/${encodeURIComponent(sid)}/resources/${encodeURIComponent(el.dataset.id)}`, { method: 'DELETE' });
      await pjFetch(true); pjCtxLoad(pageArg, true); return;
    }
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
    case 'proj-file': await openPjFile(el.dataset.path, el.dataset.line); return;
    case 'proj-open-stash': $('pj-stash-btn').click(); return;
    case 'proj-chat-stop': await post(`/api/projects/${encodeURIComponent(pageArg)}/chat/stop`); pjFetch(true); return;
    case 'proj-chat-clear':
      if (!await askConfirm('Clear this project\'s conversation? The tasks it started are kept.')) return;
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

// The git fields stay out of sight until asked for: most projects start as an empty folder.
function npGit(on) { $('np-git').classList.toggle('hidden', !on); $('np-git-show').classList.toggle('hidden', on); $('np-git-show').setAttribute('aria-expanded', String(on)); if (on) $('np-url').focus(); }
$('np-git-show').onclick = () => npGit(true);
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
        if (!await askConfirm(`Clear ${p.name}'s conversation? The tasks it started are kept.`)) return;
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

function pjCloseAddMenu() { const d = document.querySelector('.pj-addctx'); if (d) d.open = false; }
$('pj-ctx-file').onchange = async (e) => { const f = e.target.files; try { await pjStashUpload(f); } catch (x) { toast(x.message, 5000); } e.target.value = ''; };
