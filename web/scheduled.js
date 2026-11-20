'use strict';
// Scheduled page (#/scheduled): every saved task with its schedule, next run, last runs, pause and
// resume, Pause all, and editing the saved request. Plain script, no modules: shares globals with
// app.js and new-task.js (schedText, openSched, templateAction) and loads after them (see index.html).

const scOpen = new Set(); // rows the user opened
let scMenuFor = null, scMenuBack = null;

// Times are shown by the controller's clock, the same clock the schedules use.
function ctlTime(iso, withDate) {
  const off = (state && state.controller_utc_offset) || 0;
  const d = new Date(new Date(iso).getTime() + off * 60000);
  const o = withDate ? { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: 'UTC' }
    : { weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: 'UTC' };
  return d.toLocaleString([], o);
}
const farFromNow = (iso) => Math.abs(new Date(iso) - Date.now()) > 6 * 86400e3;
function utcLabel(min) {
  if (!min) return 'UTC';
  const h = Math.floor(Math.abs(min) / 60), m = Math.abs(min) % 60;
  return `UTC${min < 0 ? '−' : '+'}${h}${m ? ':' + String(m).padStart(2, '0') : ''}`;
}

const RUN_WORKING = ['queued', 'planning', 'running', 'testing', 'building', 'fixing'];
const RUN_BAD = ['failed', 'cancelled', 'interrupted'];
// runClass: one square in the last-runs strip.
function runClass(r) {
  if (r.kind === 'skipped' || r.kind === 'missed') return 's';
  if (r.kind === 'not_started' || RUN_BAD.includes(r.status)) return 'f';
  if (RUN_WORKING.includes(r.status) || ['awaiting_answers', 'awaiting_approval', 'paused_cap'].includes(r.status)) return 'r';
  if (r.kind === 'late') return 'l';
  return r.status ? '' : 's';
}
function runLabel(r) {
  if (r.kind === 'skipped') return pill('skipped');
  if (r.kind === 'missed') return pill('skipped', 'missed');
  if (r.kind === 'not_started') return pill('failed', 'did not start');
  return r.status ? pill(r.status) : pill('cancelled', 'deleted');
}
function runTook(r) {
  if (!r.seconds) return '–';
  const m = Math.round(r.seconds / 60);
  return m < 1 ? '<1 min' : m < 120 ? m + ' min' : (m / 60).toFixed(1) + ' h';
}
function runNote(r) {
  const parts = [];
  if (r.kind === 'manual') parts.push('Run now.');
  if (r.note) parts.push(esc(r.note));
  if (r.task && r.status) parts.push(`<a href="#/tasks/${esc(r.task)}">Open task</a>`);
  return parts.join(' ');
}
const tplRunning = (t) => (t.runs || []).some((r) => r.task && RUN_WORKING.includes(r.status)) || RUN_WORKING.includes(t.last_status);
function tplWarn(t) {
  const r = (t.runs || [])[0];
  return !!(r && (runClass(r) === 'f' || r.kind === 'missed'));
}

function scRow(t) {
  const proj = t.request.project ? (projects.find((p) => p.id === t.request.project) || {}).name || 'project' : 'no project';
  const planner = t.request.planner ? (t.request.planner === 'auto' ? 'Auto (local)' : t.request.planner.startsWith('node:') ? (nodeById(t.request.planner.slice(5)) ? nodeName(nodeById(t.request.planner.slice(5))) : 'a node') : t.request.planner) : 'default planner';
  const sub = [esc(proj), 'planner ' + esc(planner)];
  if (t.request.node_label) sub.push('nodes labelled ' + esc(t.request.node_label));
  if (t.paused) sub.push('paused ' + esc(ctlTime(t.paused, true).replace(/,? \d\d:\d\d$/, '')));
  if (t.last_note) sub.push(`<span class="warn-text">${esc(t.last_note)}</span>`);
  const runs = (t.runs || []).slice().reverse();
  const strip = runs.length ? `<div class="sc-runs" title="Last ${runs.length} run${runs.length === 1 ? '' : 's'}, oldest first">${runs.map((r) => `<i class="${runClass(r)}" title="${esc(ctlTime(r.at, true))}: ${esc(r.kind === 'skipped' || r.kind === 'missed' ? r.kind : r.status || r.kind)}"></i>`).join('')}</div>` : '';
  const running = tplRunning(t);
  let when = '';
  if (!t.schedule) when = '';
  else if (running) { const r = (t.runs || []).find((x) => RUN_WORKING.includes(x.status)); when = `<b>running now</b>${r ? 'started ' + esc(ctlTime(r.at).split(' ').pop()) : ''}`; }
  else if (t.paused) when = '<b>–</b>paused';
  else if (state.schedules_paused) when = '<b>–</b>all paused';
  else if (t.next_run) when = `<b>${esc(ctlTime(t.next_run, farFromNow(t.next_run)))}</b>next run`;
  const cls = ['sc-row', scOpen.has(t.id) ? 'open' : '', !t.schedule || t.paused ? 'paused' : '', t.schedule && !t.paused && tplWarn(t) ? 'warnrow' : ''].filter(Boolean).join(' ');
  const pills = (t.paused ? pill('cancelled', 'Paused') + ' ' : '') + (t.schedule ? `<span class="pill st-ready">${esc(schedText(t))}</span>` : '');
  const second = t.schedule ? (t.paused ? `<button class="small primary" data-act="sc-resume" data-id="${esc(t.id)}">Resume</button>` : `<button class="small" data-act="sc-pause" data-id="${esc(t.id)}">Pause</button>`)
    : `<button class="small" data-act="tpl-sched" data-id="${esc(t.id)}">Schedule…</button>`;
  const runCls = t.paused || !t.schedule ? 'small' : 'small primary';
  const row = `<div class="${cls}" data-sc-row="${esc(t.id)}">
    <span class="sc-dot" aria-hidden="true"></span>
    <button class="sc-main" data-act="sc-toggle" data-id="${esc(t.id)}" aria-expanded="${scOpen.has(t.id)}">
      <span class="sc-name">${esc(t.name)} ${pills}</span>
      <span class="sc-sub">${sub.join(' · ')}</span>${strip}
    </button>
    <div class="sc-when">${when}</div>
    <div class="sc-acts"><button class="${t.schedule ? runCls : 'small primary'}" data-act="tpl-run" data-id="${esc(t.id)}"${running ? ' disabled title="A run is still going"' : ''}>Run now</button>${second}<button class="small sc-more" data-act="sc-menu" data-id="${esc(t.id)}" aria-label="More actions for ${esc(t.name)}" aria-haspopup="menu">⋯</button></div>
  </div>`;
  if (!scOpen.has(t.id)) return row;
  const r = t.request;
  const opts = [r.review === false ? 'Peer review off' : 'Peer review on', `${r.fix_rounds || state.default_fix_rounds} fix rounds`, `tests ${r.test_minutes || state.default_test_minutes} min`];
  if (r.test_first) opts.push('tests first');
  if (r.mockup) opts.push('mockups first');
  const hist = (t.runs || []).length ? `<table class="sc-hist">${t.runs.map((x) => `<tr><td class="t">${esc(ctlTime(x.at, farFromNow(x.at)))}</td><td>${runLabel(x)}</td><td class="d">${runTook(x)}</td><td class="n">${runNote(x)}</td></tr>`).join('')}</table>`
    : '<p class="hint">No runs yet.</p>';
  return row + `<div class="sc-detail">
    <div><h3>Request</h3><div class="sc-req">${esc(r.prompt)}</div><div class="sc-opts">${opts.join(' · ')}</div>
      <div class="btns"><button class="small" data-act="sc-edit" data-id="${esc(t.id)}">Edit request…</button><button class="small" data-act="tpl-sched" data-id="${esc(t.id)}">${t.schedule ? 'Edit schedule…' : 'Schedule…'}</button></div></div>
    <div><h3>Last runs</h3>${hist}</div>
  </div>`;
}

function renderScheduled() {
  if (!state) return;
  const list = state.templates || [];
  const all = !!state.schedules_paused;
  const sw = $('sc-pause-all'); sw.classList.toggle('on', all); sw.setAttribute('aria-checked', String(all));
  $('sc-tz').textContent = `Saved tasks that start on their own, planned on your nodes so they never spend money. Times are the controller's clock (${utcLabel(state.controller_utc_offset || 0)}).`;
  setHTML('sc-banner', all ? `<div class="sc-banner" role="status"><span aria-hidden="true">⏸</span><span><b>All schedules are paused.</b> Nothing starts on its own until you turn this off; Run now still works.</span><span class="spacer"></span><button class="small" data-act="sc-pause-all" data-on="0">Resume all</button></div>` : '');
  const sched = list.filter((t) => t.schedule), plain = list.filter((t) => !t.schedule);
  let h = '';
  if (!list.length) h = `<div class="card empty-card"><p>No saved tasks yet. Write a request on the <a href="#/new">New task</a> page and press <b>Save for later</b>, then <b>Schedule…</b> it here.</p></div>`;
  else {
    h += sched.length ? `<div class="card sc-list">${sched.map(scRow).join('')}</div>` : `<p class="hint">Nothing is scheduled yet. Press <b>Schedule…</b> on a saved task below.</p>`;
    if (plain.length) h += `<div class="section-head sc-group"><h2>Saved, not scheduled</h2></div><div class="card sc-list">${plain.map(scRow).join('')}</div>`;
  }
  setHTML('sc-list', h);
}

// ---------- the ⋯ menu ----------
function openScMenu(id, btn) {
  const t = (state.templates || []).find((x) => x.id === id);
  if (!t) return;
  scMenuFor = id; scMenuBack = btn;
  const item = (act, text, extra = '') => `<button role="menuitem" tabindex="-1" data-act="${act}" data-id="${esc(id)}" ${extra}>${text}</button>`;
  const m = $('sc-ctx');
  m.innerHTML = [item('sc-edit', 'Edit request…'), item('tpl-sched', t.schedule ? 'Edit schedule…' : 'Schedule…'), item('sc-use', 'Edit first in New task'), item('sc-dup', 'Duplicate'),
    t.schedule ? item('sc-unsched', 'Remove schedule') : '', '<hr>', item('tpl-del', 'Delete…', 'class="danger"')].join('');
  m.setAttribute('aria-label', `Actions for ${t.name}`);
  const r = btn.getBoundingClientRect();
  placeMenu(m, r.right - 200, r.bottom + 4);
  m.querySelector('button').focus();
}
function closeScMenu(restore = true) {
  const m = $('sc-ctx');
  if (m.classList.contains('hidden')) return;
  m.classList.add('hidden'); m.innerHTML = '';
  if (restore && scMenuBack && document.body.contains(scMenuBack)) scMenuBack.focus();
  scMenuFor = scMenuBack = null;
}
document.addEventListener('click', (e) => { if (!e.target.closest('#sc-ctx') && !e.target.closest('[data-act="sc-menu"]')) closeScMenu(false); }, true);
$('sc-ctx').addEventListener('keydown', (e) => {
  const items = [...$('sc-ctx').querySelectorAll('button')];
  const i = items.indexOf(document.activeElement);
  if (e.key === 'Escape') { e.preventDefault(); closeScMenu(); }
  else if (e.key === 'ArrowDown') { e.preventDefault(); items[(i + 1) % items.length].focus(); }
  else if (e.key === 'ArrowUp') { e.preventDefault(); items[(i - 1 + items.length) % items.length].focus(); }
  else if (e.key === 'Tab') closeScMenu(false);
});

// ---------- edit the saved request ----------
let editTpl = null;
function openTplEdit(id) {
  editTpl = (state.templates || []).find((t) => t.id === id);
  if (!editTpl) return;
  $('tpl-edit-name').value = editTpl.name; $('tpl-edit-prompt').value = editTpl.request.prompt;
  $('tpl-edit-err').classList.add('hidden');
  open('modal-tpl-edit');
  setTimeout(() => $('tpl-edit-prompt').focus(), 0);
}
$('tpl-edit-go').onclick = async () => {
  try {
    await post(`/api/templates/${editTpl.id}`, { name: $('tpl-edit-name').value, request: { ...editTpl.request, prompt: $('tpl-edit-prompt').value } });
    close('modal-tpl-edit'); toast('Saved'); refresh();
  } catch (e) { $('tpl-edit-err').textContent = e.message; $('tpl-edit-err').classList.remove('hidden'); }
};

async function scheduledAction(act, el) {
  const id = el.dataset.id;
  const t = (state.templates || []).find((x) => x.id === id);
  if (act !== 'sc-pause-all' && !t) return;
  closeScMenu(false);
  switch (act) {
    case 'sc-toggle': if (scOpen.has(id)) scOpen.delete(id); else scOpen.add(id); renderScheduled(); return;
    case 'sc-menu': openScMenu(id, el); return;
    case 'sc-pause': await post(`/api/templates/${id}`, { paused: true }); toast('Paused. It keeps its schedule; Resume starts it again.'); break;
    case 'sc-resume': await post(`/api/templates/${id}`, { paused: false }); toast('Resumed'); break;
    case 'sc-edit': openTplEdit(id); return;
    case 'sc-use': location.hash = '#/new'; ntFill(t.request); return;
    case 'sc-dup': await post('/api/templates', { name: t.name + ' (copy)', request: t.request }); toast('Copied, without a schedule'); break;
    case 'sc-unsched': if (await askConfirm(`Remove the schedule from "${t.name}"? The saved task stays; Run now still works.`, { ok: 'Remove schedule' })) { await post(`/api/templates/${id}`, { schedule: { every: '' } }); toast('Schedule removed'); } break;
    case 'sc-pause-all': {
      const on = el.dataset.on === '1';
      await post('/api/schedules/pause', { paused: on }); toast(on ? 'All schedules paused' : 'Schedules running again'); break;
    }
    default: return;
  }
  refresh();
}
$('sc-pause-all').onclick = () => scheduledAction('sc-pause-all', { dataset: { on: state.schedules_paused ? '0' : '1' } }).catch((e) => toast(e.message));
$('sc-new').onclick = () => { location.hash = '#/new'; toast('Write the request, then press Save for later'); };
