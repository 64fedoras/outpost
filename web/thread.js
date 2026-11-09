'use strict';
// Task thread, live output and the project file browser. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- task thread ----------
async function pollTask() {
  clearTimeout(taskTimer);
  if (page !== 'task') return;
  const id = pageArg;
  let t;
  try { t = await api('/api/tasks/' + id); }
  catch (e) {
    if (id !== pageArg || e.message === 'login required') return;
    // The controller is restarting (or the network blipped): keep showing the
    // task as it was and try again shortly, so it comes back on its own.
    if (!taskDetail || /no such task/.test(e.message)) setHTML('tv-title', esc(e.message));
    taskTimer = setTimeout(pollTask, 1500);
    return;
  }
  taskDetail = t;
  if (id !== pageArg) return;
  await loadRounds();
  if (id !== pageArg) return;
  if (!ACTIVE.includes(taskDetail.status)) liveLast = {};   // nothing more to show once the task has ended
  renderTask(taskDetail);
  // With the live stream open, "changed" events trigger refreshes; polling is only the fallback.
  const live = liveES && liveES.readyState === 1;
  taskTimer = setTimeout(pollTask, live ? 15000 : WORKING.includes(taskDetail.status) ? 1500 : 4000);
}

// ---------- live output (server-sent events) ----------
let liveES = null, liveCalls = {}, liveLast = {}, liveKick = null;   // liveLast: slot -> last finished call, shown until the next one starts
const liveExpanded = new Set();   // subtask ids whose live pane the user expanded (panes start collapsed)
function liveClose() {
  if (liveES) liveES.close();
  if (chatES) { chatES.close(); chatES = null; }
  liveES = null; liveCalls = {}; liveLast = {}; clearTimeout(liveKick);
}
function liveOpen(id) {
  if (!window.EventSource) return;
  const es = liveES = new EventSource(`/api/tasks/${id}/events`);
  // After the controller restarts the stream reconnects: reload the task at
  // once instead of waiting for the slow fallback poll.
  es.onopen = () => { if (page === 'task' && pageArg === id) { liveCalls = {}; liveLast = {}; clearTimeout(liveKick); liveKick = setTimeout(pollTask, 50); } };
  // A stream the browser gave up on (an error status while the controller
  // was starting) is opened again.
  es.onerror = () => {
    if (es.readyState !== 2) return;
    setTimeout(() => { if (liveES === es && page === 'task' && pageArg === id) { liveCalls = {}; liveLast = {}; liveOpen(id); } }, 2000);
  };
  liveES.onmessage = (e) => {
    let ev; try { ev = JSON.parse(e.data); } catch (_) { return; }
    if (ev.t === 'start') { delete liveLast[ev.sub || '']; liveCalls[ev.call] = { sub: ev.sub || '', label: ev.label, node: ev.node, text: ev.text || '', tok: ev.tok || 0, tps: ev.tps || 0, est: !!ev.est, read: ev.read || 0, total: ev.total || 0, left: ev.left || 0 }; }
    else if (ev.t === 'progress' && liveCalls[ev.call]) {
      const c = liveCalls[ev.call];
      Object.assign(c, { read: ev.read || 0, total: ev.total || 0, left: ev.left || 0 });
      const pre = document.querySelector(`[data-live="${ev.call}"]`);
      const m = pre && pre.parentNode.querySelector('.live-rate');
      if (m) { m.textContent = liveRate(c); return; }
    }
    else if (ev.t === 'reset' && liveCalls[ev.call]) Object.assign(liveCalls[ev.call], { text: '', tok: 0, tps: 0 });
    else if (ev.t === 'end') { const c = liveCalls[ev.call]; if (c && c.text) liveLast[c.sub] = Object.assign({ done: true }, c); delete liveCalls[ev.call]; }
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
function liveRate(c) {
  if (c.tps) return `${c.est ? '~' : ''}${c.tps} tok/s`;
  // Before the first token: how far the node has read the prompt (llama.cpp's prompt progress).
  if (c.total && !c.text && c.read < c.total) {
    const left = c.left >= 90 ? `about ${Math.round(c.left / 60)} min left` : c.left > 0 ? `about ${c.left} s left` : '';
    return `reading the prompt: ${c.read.toLocaleString()} of ${c.total.toLocaleString()} tokens${left ? ', ' + left : ''}`;
  }
  return '';
}
function setLiveText(pre, c) {
  const stick = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
  pre.textContent = c.text;
  if (stick) pre.scrollTop = pre.scrollHeight;
}
// Running calls plus the last finished call of any slot that has none running.
function liveFor(match) {
  const out = Object.entries(liveCalls).filter(([, c]) => match(c));
  for (const [slot, c] of Object.entries(liveLast)) if (match(c) && !out.some(([, r]) => r.sub === slot)) out.push(['last:' + slot, c]);
  return out;
}
function livePane(c, id) {
  const open = liveExpanded.has(c.sub || '~');
  return `<details class="live" data-sub="${esc(c.sub || '~')}" ${open ? 'open' : ''}><summary>live output${c.node ? ` · ${esc(c.node)}${/ review /.test(c.label || '') ? ' checking' : ''}` : ''} <span class="live-rate">${c.done ? 'finished' : esc(liveRate(c))}</span></summary><pre class="live-text" data-live="${esc(id)}">${esc(c.text)}</pre></details>`;
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
    if (el._h !== it.html) {
      // keep each live pane's scroll position across the rebuild (bottom stays pinned)
      const pos = {};
      el.querySelectorAll('.live-text').forEach((p) => { const k = p.closest('.live')?.dataset.sub; if (k) pos[k] = { top: p.scrollTop, stick: p.scrollHeight - p.scrollTop - p.clientHeight < 24 }; });
      el.className = 'item ' + it.cls; el.innerHTML = it.html; el._h = it.html; grew = true;
      el.querySelectorAll('.live-text').forEach((p) => { const o = pos[p.closest('.live')?.dataset.sub]; if (o) p.scrollTop = o.stick ? p.scrollHeight : o.top; });
    }
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
  setHTML('tv-status', `<span class="tdot ${taskTone({ ...t, needs_you: (['awaiting_answers', 'awaiting_approval', 'paused_cap', 'interrupted'].includes(t.status) && !resuming(t)) || (t.messages || []).some((m) => m.pending) })}"></span>${esc(statusWord(t))}`);
  $('tv-status').className = 'status-chip';
  setHTML('tv-spend', t.planner === 'claude' || t.planner === 'openrouter' || t.cost_usd > 0 ? spendMeter(t) : '');
  const files = t.files || [];
  $('tv-files-btn').classList.toggle('hidden', !files.length);
  $('tv-download').classList.toggle('hidden', !files.length || !['done', 'cancelled', 'failed'].includes(t.status));
  $('tv-download').href = `/api/tasks/${t.id}/download`;
  $('tv-stop').classList.toggle('hidden', !canStop(t));
  // Stop next to Send while the task works (or waits to resume), so it is where the eye is.
  $('tm-stop').classList.toggle('hidden', !(WORKING.includes(t.status) || resuming(t)));
  $('tv-delete').classList.toggle('hidden', ACTIVE.includes(t.status));
  renderThreadItems(threadItems(t));
  tickElapsed();
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
  // Planner and other calls that belong to no subtask come after the tests and fixes card.
  if (t.activity && ['testing', 'fixing', 'building'].includes(t.status)) items.push({ key: 'activity', cls: 'card-row', html: activityCard(t) });
  const loose = liveFor((c) => !c.sub);
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
  if (m.kind === 'hand_edit') {
    const files = (m.files || []).map((f) => `<a href="#" data-act="view-file" data-path="${esc(f)}">${esc(f)}</a>`).join('');
    const hasRound = (taskRounds || []).some((r) => r.round === m.round);
    return { key, cls: 'card-row', html: `<div class="card-msg"><div class="card-head">Your edits</div><p>${esc(m.text)}</p>${files ? `<div class="changed">${files}</div>` : ''}${hasRound ? `<div class="card-actions"><button class="small" data-act="show-changes" data-round="${m.round}">Changes</button></div>` : ''}</div>` };
  }
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
    case 'error': return { key, cls: 'card-row', html: `<div class="card-msg bad"><div class="card-head">Something went wrong</div>${errorBody(m.text)}${t.status === 'failed' && m.round === (t.round || 1) ? '<div class="card-actions"><button class="primary" data-act="task-resume">Try again</button><span class="hint">or send a message to change course</span></div>' : ''}</div>` };
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

// Minutes for the plan card's time estimate (estimate.go); always "about".
function estMin(sec) { return sec < 90 ? '1 min' : Math.round(sec / 60) + ' min'; }

function planCard(t, m, live) {
  if (planEdit && planEdit.id === m.id && canEditPlan(t, m)) return planEditor(t, m);
  const p = m.plan || {};
  const est = live && t.estimate ? t.estimate : null;
  const subEst = (s) => {
    const e = est && est.subtasks && est.subtasks[s.id];
    return e ? `<span class="st-est" title="Estimated time for this subtask on its likely node">~${estMin(e.seconds)} on ${esc(e.node_name)}</span>` : '';
  };
  const diffSel = (s) => live ? `<select class="small" data-diff="${esc(s.id)}" aria-label="Difficulty of ${esc(s.title)}" title="Hard subtasks go to the strongest model; easy ones to the fastest node">${['easy', 'normal', 'hard'].map((d) => `<option value="${d}" ${(s.difficulty || 'normal') === d ? 'selected' : ''}>${d}</option>`).join('')}</select>`
    : (s.difficulty && s.difficulty !== 'normal' ? `<span class="tag">${esc(s.difficulty)}</span>` : '');
  const subs = (p.subtasks || []).map((s) => `<li><details><summary><span class="st-title">${esc(s.title)}</span>${diffSel(s)}<span class="st-files">${s.kind === 'investigate' ? 'investigates, writes no files' : s.files.map(esc).join(', ')}</span>${subEst(s)}</summary>
      <p class="st-ins">${esc(s.instructions)}</p>${s.depends_on && s.depends_on.length ? `<p class="st-dep">Starts after ${s.depends_on.map(esc).join(', ')}</p>` : ''}</details></li>`).join('');
  const facts = [p.language ? esc(p.language) : '', p.test_command ? `tests: <code>${esc(p.test_command)}</code>` : 'no automated tests', p.build_command ? `build: <code>${esc(p.build_command)}</code>` : '', `${(p.subtasks || []).length} subtasks`,
    est ? `<span class="plan-est" title="${esc(`From each node's measured speed and the expected size of the files${est.rough ? '; some nodes have no measured speed yet, so this is rough' : ''}. Fix rounds after failing tests are not included.`)}">about ${estMin(est.seconds)} on ${est.nodes} node${est.nodes === 1 ? '' : 's'}${est.seconds - est.write_seconds > 1 ? ` (writing ${estMin(est.write_seconds)}, tests ${estMin(est.seconds - est.write_seconds)})` : ''}${est.rough ? ', rough' : ''}</span>` : ''].filter(Boolean).join(' · ');
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
    <ol class="plan-steps">${subs}</ol>${fileOpsNote(p)}${actions}</div>`;
}
// Files the plan removes or moves (fileops.go), shown before approval.
function fileOpsNote(p) {
  const del = p.delete || [], ren = p.rename || [];
  if (!del.length && !ren.length) return '';
  const parts = [];
  if (ren.length) parts.push(`Moves ${ren.map((r) => `<code>${esc(r.from)}</code> → <code>${esc(r.to)}</code>`).join(', ')}`);
  if (del.length) parts.push(`Deletes ${del.map((f) => `<code>${esc(f)}</code>`).join(', ')}`);
  return `<p class="card-note fileops">${parts.join('. ')}. Undo on this request puts them back.</p>`;
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
    const lives = liveFor((c) => c.sub === s.id).map(([id, c]) => livePane(c, id)).join('');
    const idle = ['done', 'failed', 'cancelled'].includes(t.status) && s.kind !== 'investigate' && (x.status === 'done' || x.status === 'failed');
    const redo = idle ? `<button class="small p-redo" data-act="task-redo" data-sub="${esc(s.id)}" title="Write this subtask again on a node you pick, then re-run the tests">Redo…</button>` : '';
    return `<li class="p-${x.status}"><span class="p-ico" aria-hidden="true">${ST_ICON[x.status] || ''}</span><span class="p-title">${esc(s.title)}</span><span class="p-who">${who}</span>${redo}${lives}</li>`;
  }).join('');
  let phase = '';
  if (t.activity) phase = '';   // the "Tests and fixes" card below says it in more detail
  else if (t.status === 'testing') phase = 'Running the tests…';
  else if (t.status === 'building') phase = 'Building…';
  else if (t.status === 'fixing') phase = `Tests failed; fixing (round ${t.fix_rounds + 1} of ${t.max_fix_rounds || 3}). The fixer reads, edits and re-runs on its own.`;
  else if (t.status === 'running' && !t.workers_online) phase = 'Waiting for a node with a model ready. Load one on the Nodes page.';
  const head = t.status === 'running' || t.status === 'testing' || t.status === 'building' || t.status === 'fixing'
    ? `Working · ${done} of ${specs.length} subtasks done` : `${done} of ${specs.length} subtasks done`;
  return `<div class="card-msg progress"><div class="card-head">${head}</div><div class="strip" role="img" aria-label="${done} of ${specs.length} subtasks done">${strip}</div>
    <ul class="p-list">${rows}</ul>${phase ? `<p class="card-note">${esc(phase)}</p>` : ''}</div>`;
}

// "Tests and fixes": what the test-and-fix phase is doing right now and what it did, newest last.
// Elapsed times tick every second from the server's clock (see tickElapsed), so they stay right between polls.
const fmtDur = (sec) => { sec = Math.max(0, Math.round(sec)); return sec < 60 ? `${sec}s` : sec < 3600 ? `${Math.floor(sec / 60)}m ${String(sec % 60).padStart(2, '0')}s` : `${Math.floor(sec / 3600)}h ${String(Math.floor(sec / 60) % 60).padStart(2, '0')}m`; };
const ACT_CLS = { running: 'p-running', ok: 'p-done', bad: 'p-failed', warn: 'p-warn' };
const ACT_ICO = { ok: '✓', bad: '✕', warn: '!' };
function activityCard(t) {
  const a = t.activity, skew = Date.now() - new Date(a.server_time).getTime();
  const rows = (a.steps || []).map((x) => {
    const start = new Date(x.start).getTime() + skew;
    const dur = x.status === 'running' ? `<span data-since="${start}"></span>` : fmtDur((new Date(x.end).getTime() - new Date(x.start).getTime()) / 1000);
    return `<li class="${ACT_CLS[x.status] || 'p-done'}"><span class="p-ico" aria-hidden="true">${ACT_ICO[x.status] || ''}</span><span class="p-title">${esc(x.text)}</span><span class="p-who">${dur}</span></li>`;
  }).join('');
  const round = t.status === 'fixing' || t.fix_rounds ? ` · fix round ${Math.min(a.round, a.max_rounds)} of ${a.max_rounds}` : '';
  return `<div class="card-msg progress activity"><div class="card-head">Tests and fixes${round}</div>
    <ul class="p-list">${rows}</ul>${a.stall ? `<p class="card-note stall">⚠ ${esc(a.stall)}</p>` : ''}</div>`;
}
function tickElapsed() {
  document.querySelectorAll('[data-since]').forEach((el) => { el.textContent = fmtDur((Date.now() - Number(el.dataset.since)) / 1000); });
}
setInterval(tickElapsed, 1000);

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
  return `<div class="card-msg result ${tone}"><div class="card-head">${tone === 'good' ? 'Finished' : 'Finished with failing tests'}</div><p>${esc(m.text)}</p>${statsLine(m.stats)}
    ${files ? `<div class="changed"><span class="hint">${m.round > 1 ? 'Changed in this request' : 'Files'}</span>${files}</div>` : ''}
    ${latest ? artifactsBlock(t) : ''}
    ${latest && t.test_output && t.test_status !== 'passed' ? `<details class="testout"><summary>Last test output</summary><pre class="out">${esc(t.test_output)}</pre></details>` : ''}
    ${(taskRounds || []).some((r) => r.round === m.round) ? `<div class="card-actions"><button class="small" data-act="show-changes" data-round="${m.round}">Changes</button><a href="/api/tasks/${t.id}/patch?round=${m.round}" title="This request's changes as a git diff (git apply)"><button class="small">.patch</button></a></div>` : ''}
    ${latest ? `<div class="card-actions"><a href="/api/tasks/${t.id}/download"><button class="primary">Download .zip</button></a><a href="/api/tasks/${t.id}/handoff" title="The files plus HANDOFF.md and instruction files for Claude Code, Codex, Gemini CLI, GitHub Copilot and Cursor"><button class="small">Hand off to a coding assistant</button></a>${(taskRounds || []).length ? `<a href="/api/tasks/${t.id}/patch" title="Every change Outpost made, as one git diff"><button class="small">All changes .patch</button></a>` : ''}<span class="hint">Want changes? Send a follow-up below; it builds on these files.</span></div>` : ''}</div>`;
}

// Redo one subtask (tasks_redo.go).
let redoSub = null;
function openRedo(t, sid) {
  const s = ((t.plan || {}).subtasks || []).find((x) => x.id === sid);
  if (!s) return;
  redoSub = { task: t.id, sub: sid };
  $('redo-title').textContent = s.title;
  const nodes = (state ? state.nodes : []).filter((n) => n.online && n.heartbeat && n.heartbeat.loaded_model);
  setHTML('redo-node', `<option value="strongest">The strongest free node</option>${nodes.map((n) => `<option value="${esc(n.id)}">${esc(nodeName(n))} (${esc(n.heartbeat.loaded_model)})</option>`).join('')}<option value="planner">The planner (${esc(t.planner_label || 'planner')})</option>`);
  $('redo-note').value = '';
  open('modal-redo'); $('redo-note').focus();
}
$('redo-go').onclick = async () => {
  if (!redoSub) return;
  try { await post(`/api/tasks/${redoSub.task}/subtasks/${encodeURIComponent(redoSub.sub)}/redo`, { node: $('redo-node').value, note: $('redo-note').value }); close('modal-redo'); toast('Redoing'); pollTask(); }
  catch (e) { toast(e.message); }
};

// The one-line summary under a Finished card (stats.go).
function statsLine(s) {
  if (!s) return '';
  const dur = (sec) => sec < 90 ? `${sec} s` : sec < 5400 ? `${Math.round(sec / 60)} min` : `${(sec / 3600).toFixed(1)} h`;
  const parts = [];
  if (s.seconds) parts.push(dur(s.seconds));
  if ((s.nodes || []).length) parts.push(`written by ${s.nodes.map(esc).join(', ')}`);
  if (s.retried) parts.push(`${s.retried} subtask${s.retried === 1 ? '' : 's'} retried`);
  if (s.fix_rounds) parts.push(`${s.fix_rounds} fix round${s.fix_rounds === 1 ? '' : 's'}`);
  if (s.files) parts.push(`${s.files} file${s.files === 1 ? '' : 's'}, <span class="st-done">+${s.lines_added || 0}</span> <span class="st-failed">−${s.lines_removed || 0}</span> lines`);
  if (s.cost_usd) parts.push(`${usd(s.cost_usd)} cloud`);
  return parts.length ? `<p class="stats-line">${parts.join(' · ')}</p>` : '';
}

// A card for states that need the person, at the end of the thread.
function statusCard(t) {
  const locals = state ? state.nodes.filter((n) => n.online && n.heartbeat && n.heartbeat.loaded_model)
    .map((n) => `<option value="node:${n.id}">${esc(nodeName(n))} (${esc(n.heartbeat.loaded_model)})</option>`).join('') : '';
  switch (t.status) {
    case 'planning':
      return { key: 'tail-planning', cls: 'from-planner', html: `<div class="said thinking">Thinking it through with ${esc(t.planner_label)}<span class="dots" aria-hidden="true"><i></i><i></i><i></i></span></div>` };
    case 'paused_cap':
      if (t.cap_kind === 'monthly') {
        return { key: 'tail-mcap', cls: 'card-row', html: `<div class="card-msg warn"><div class="card-head">Paused at the monthly cap</div>
        <p>This month's cloud spend for all tasks has reached the monthly cap. Raise it on the <a href="#/spending">Spending page</a> to keep going with the cloud model, finish with a local planner, or leave it: it continues on its own when the next month starts.</p>
        ${locals ? `<div class="card-actions"><select id="ta-local" class="chip-select">${locals}</select><button data-act="task-local">Finish with this local planner</button></div>` : ''}</div>` };
      }
      return { key: 'tail-cap', cls: 'card-row', html: `<div class="card-msg warn"><div class="card-head">Paused at the spending cap</div>
        <p>Spent ${usd(t.cost_usd)} of ${usd(t.cap_usd)}. Raise the cap to keep going with the cloud model, or finish with a local planner.</p>
        <div class="card-actions"><label class="chip-input">new cap $<input id="ta-newcap" type="number" min="0.05" step="0.25" value="${(Math.ceil((t.cap_usd * 2) * 4) / 4).toFixed(2)}"></label><button class="primary" data-act="task-raise">Raise cap and continue</button></div>
        ${locals ? `<div class="card-actions"><select id="ta-local" class="chip-select">${locals}</select><button data-act="task-local">Finish with this local planner</button></div>` : ''}</div>` };
    case 'interrupted':
      if (resuming(t)) {
        const node = state && state.nodes.find((n) => 'node:' + n.id === t.planner);
        return { key: 'tail-resuming', cls: 'from-planner', html: `<div class="said thinking">The controller restarted. Picking up where it stopped as soon as ${node ? esc(nodeName(node)) : 'its node'} is back with its model loaded; finished subtasks are kept<span class="dots" aria-hidden="true"><i></i><i></i><i></i></span></div>` };
      }
      return { key: 'tail-int', cls: 'card-row', html: `<div class="card-msg warn"><div class="card-head">Interrupted</div><p>The controller restarted while this task was active. Tasks on a local planner resume on their own when a node is ready (unless turned off in Settings); otherwise Resume picks up where it stopped; finished subtasks are kept.</p>
        <div class="card-actions"><button class="primary" data-act="task-resume">Resume</button></div></div>` };
  }
  return null;
}

function renderComposer(t) {
  const box = $('tm-text');
  const modes = {
    awaiting_answers: ['Or answer in your own words…', 'Your reply goes to the planner with the questions above.'],
    planning: ['Steer the planner, e.g. "use SQLite, keep it to one file"…', 'Sent after the planner\'s current answer. Use Send now to interrupt it instead.'],
    awaiting_approval: ['Ask for changes to the plan…', 'The planner revises the plan; nothing runs until you approve.'],
    running: ['Steer the work, e.g. "use SQLite, not JSON files"…', 'Notes apply to subtasks that start from now on, reviews and test fixes.'],
    paused_cap: ['Steer the work…', 'Notes apply to the rest of this request.'],
    interrupted: ['Ask for something new…', 'Resume first to finish the current request; a message starts a new one on these files.'],
  };
  if (resuming(t)) modes.interrupted = ['Ask for something new…', 'It resumes on its own; a message starts a new request on these files instead.'];
  modes.testing = modes.fixing = modes.running;
  const [ph, hint] = modes[t.status] || ['Ask for a follow-up change…', 'Builds on this project\'s files: the planner plans only what changes.'];
  if (box.placeholder !== ph) box.placeholder = ph;
  setHTML('tm-hint', esc(hint));
  // Notes sent while the planner is answering wait here until it takes them.
  const q = t.queued || [];
  $('tm-queue').classList.toggle('hidden', !q.length);
  setHTML('tm-queue', q.length ? `<div class="queue-head"><span>${q.length === 1 ? 'Queued · goes in after the current answer' : q.length + ' queued · go in after the current answer'}</span><button type="button" class="link-btn" data-q="now" title="Stop the planner's current answer, drop it, and plan again with ${q.length === 1 ? 'this note' : 'these notes'}">Send now</button></div>` +
    q.map((n) => `<div class="queue-item"><span class="queue-text">${esc(n.text)}${(n.images || []).length ? ` <i>🖼 ${n.images.length}</i>` : ''}</span><button type="button" class="icon-btn" data-q="rm" data-id="${esc(n.id)}" aria-label="Remove this queued note" title="Remove">✕</button></div>`).join('') : '');
}
$('tm-queue').addEventListener('click', async (e) => {
  const b = e.target.closest('button[data-q]');
  if (!b || !pageArg) return;
  try {
    if (b.dataset.q === 'now') await post(`/api/tasks/${pageArg}/send-now`);
    else await api(`/api/tasks/${pageArg}/queued/${encodeURIComponent(b.dataset.id)}`, { method: 'DELETE' });
  } catch (err) { toast(err.message, 4000); }
  pollTask(); refresh();
});

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
$('tm-stop').onclick = async () => {
  if (!pageArg || !confirm('Stop this task? Files written so far are kept, and you can send a message later to continue.')) return;
  try { await post(`/api/tasks/${pageArg}/cancel`); toast('Stopped'); } catch (e) { toast(e.message, 4000); }
  pollTask(); refresh();
};
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
    setHTML('ch-body', r.files.map((f) => `<details open><summary class="mono">${esc(f.path)} <span class="muted">${f.status === 'added' ? 'new file' : f.status === 'deleted' ? 'deleted' : 'changed'}${f.gone && f.status !== 'deleted' ? ', since deleted' : ''}</span></summary><pre class="out code diff">${unified(f.before, f.after)}</pre></details>`).join(''));
    $('ch-undo').classList.toggle('hidden', !r.undoable);
  } catch (e) { setHTML('ch-body', `<span class="error-msg">${esc(e.message)}</span>`); }
}

async function taskAction(act, el) {
  const id = pageArg;
  try {
    switch (act) {
      case 'task-redo': openRedo(taskDetail, el.dataset.sub); return;
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

let fbPath = null, fbText = '';
async function viewFile(path) {
  if (taskDetail) renderFiles(taskDetail);
  open('modal-files');
  fbPath = path; fbText = '';
  fbEditing(false);
  $('fb-title').textContent = path; $('fb-body').textContent = 'Loading…';
  $('fb-dl').classList.remove('hidden');
  $('fb-dl').href = `/api/tasks/${pageArg}/file?path=${encodeURIComponent(path)}&download=1`;
  $('fb-edit').classList.add('hidden');
  try {
    const r = await fetch(`/api/tasks/${pageArg}/file?path=${encodeURIComponent(path)}`, { credentials: 'same-origin' });
    fbText = await r.text();
    setHTML('fb-body', numbered(fbText, path));
    const idle = taskDetail && ['done', 'failed', 'cancelled'].includes(taskDetail.status);
    // Text only: a NUL byte means a binary file.
    $('fb-edit').classList.toggle('hidden', !(r.ok && idle && !fbText.includes('\u0000') && fbText.length < 1e6));
  } catch (e) { $('fb-body').textContent = e.message; }
}
// Hand edits (tasks_handedit.go): the viewer turns into a text box.
function fbEditing(on) {
  $('fb-body').classList.toggle('hidden', on); $('fb-editor').classList.toggle('hidden', !on);
  $('fb-save').classList.toggle('hidden', !on); $('fb-cancel').classList.toggle('hidden', !on);
  $('fb-edit').classList.toggle('hidden', on); $('fb-dl').classList.toggle('hidden', on);
  if (on) { $('fb-editor').value = fbText; $('fb-editor').focus(); }
}
$('fb-edit').onclick = () => fbEditing(true);
$('fb-cancel').onclick = () => { fbEditing(false); $('fb-edit').classList.remove('hidden'); };
$('fb-save').onclick = async () => {
  const content = $('fb-editor').value;
  if (content === fbText) { $('fb-cancel').click(); return; }
  try {
    await post(`/api/tasks/${pageArg}/file`, { path: fbPath, content });
    toast('Saved as your edit; Changes and Undo work on it');
    await loadRounds(); viewFile(fbPath); pollTask();
  } catch (e) { toast(e.message); }
};
$('fb-editor').addEventListener('keydown', (e) => {
  if (e.key === 'Tab' && !e.shiftKey) { e.preventDefault(); document.execCommand('insertText', false, '\t'); }
  if (e.key === 's' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); $('fb-save').click(); }
});
