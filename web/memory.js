'use strict';
// Memory (memory.go): Settings → Memory lists the facts every chat and project
// conversation starts with; a suggestion under an answer ("Remember …?") can be
// remembered, edited first or dismissed. Setting offers (setting_offers.go) are
// the other card under an answer: a setting that blocked it, with one button to
// turn it on and answer again. Plain script, shares globals with app.js.

let memData = { memories: [], tokens: 0 };
let memEditing = null; // id of the fact being edited in Settings

function memScopeName(project) {
  if (!project) return 'Everywhere';
  const p = projectById(project);
  return `Project ${p ? p.name : '(deleted)'} only`;
}
function memScopeOptions(sel) {
  return `<option value="">Everywhere</option>${(projects || []).map((p) => `<option value="${esc(p.id)}" ${sel === p.id ? 'selected' : ''}>Project ${esc(p.name)} only</option>`).join('')}`;
}

async function loadMemory() {
  try { memData = await api('/api/memory'); } catch (e) { return; }
  if (settings) {
    $('set-mem-on').checked = settings.memory_on !== false;
    $('set-mem-suggest').checked = settings.memory_suggest !== false;
    $('set-mem-suggest').disabled = settings.memory_on === false;
  }
  if (document.activeElement !== $('mem-new-scope')) setHTML('mem-new-scope', memScopeOptions($('mem-new-scope').value));
  renderMemory();
}

function renderMemory() {
  const list = memData.memories || [];
  setHTML('mem-list', list.length ? list.map((m) => {
    if (memEditing === m.id) {
      return `<div class="mem-row editing"><div class="mem-edit"><input id="mem-edit-text" maxlength="300" value="${esc(m.text)}" aria-label="Fact"><select id="mem-edit-scope" aria-label="Where it applies">${memScopeOptions(m.project || '')}</select></div>
        <div class="mem-acts"><button class="small primary" data-mem-save="${esc(m.id)}">Save</button><button class="small" data-mem-cancel>Cancel</button></div></div>`;
    }
    return `<div class="mem-row"><div class="mem-t"><div>${esc(m.text)}</div><div class="muted">${esc(memScopeName(m.project))}${m.source ? ' · ' + esc(m.source) : ''} · ${ago(m.created)}</div></div>
      <div class="mem-acts"><button class="small" data-mem-edit="${esc(m.id)}">Edit</button><button class="small danger" data-mem-forget="${esc(m.id)}">Forget</button></div></div>`;
  }).join('') : '<p class="hint mem-empty">Nothing remembered yet.</p>');
  $('mem-count').textContent = list.length ? `${list.length} ${list.length === 1 ? 'memory' : 'memories'} · about ${memData.tokens} tokens per message` : '';
}

async function memAdd() {
  const text = $('mem-new').value.trim();
  if (!text) { $('mem-new').focus(); return; }
  try {
    await post('/api/memory', { text, project: $('mem-new-scope').value });
    $('mem-new').value = ''; toast('Remembered'); loadMemory();
  } catch (e) { toast(e.message); }
}
$('mem-add').onclick = memAdd;
$('mem-new').addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); memAdd(); } });
$('set-mem-on').onchange = (e) => saveSettings({ memory_on: e.target.checked }, e.target.checked ? 'Memory on' : 'Memory off: chats start without it');
$('set-mem-suggest').onchange = (e) => saveSettings({ memory_suggest: e.target.checked }, e.target.checked ? 'Suggestions on' : 'Suggestions off');

// A suggestion card under an answer. where is {chat: id} or {project: id}.
function memSuggestCard(m, where) {
  if (!m.suggest) return '';
  const key = where.chat ? `chat:${where.chat}` : `project:${where.project}`;
  const label = where.project ? 'Remember for this project' : 'Remember';
  return `<div class="mem-sug" data-mem-where="${esc(key)}" data-mem-msg="${esc(m.id)}"><div class="mem-sug-t">💡 ${label}: <b>“<span class="mem-sug-text">${esc(m.suggest)}</span>”</b></div>
    <div class="mem-sug-acts"><button class="small primary" data-mem-keep>Remember</button><button class="small" data-mem-editfirst>Edit first</button><button class="small" data-mem-dismiss>Not this</button></div></div>`;
}

async function memAnswer(card, text) {
  const [kind, id] = card.dataset.memWhere.split(':');
  const body = { msg: card.dataset.memMsg, text: text || '' };
  body[kind] = id;
  try {
    await post('/api/memory/suggestion', body);
    card.remove();
    toast(text ? 'Remembered. Settings → Memory lists everything.' : 'Not remembered');
    if (page === 'settings') loadMemory();
  } catch (e) { toast(e.message); }
}

document.addEventListener('click', async (e) => {
  const t = e.target.closest('button');
  if (!t) return;
  const card = t.closest('.mem-sug');
  if (card) {
    if (t.hasAttribute('data-mem-keep')) { memAnswer(card, card.querySelector('.mem-sug-text').textContent); return; }
    if (t.hasAttribute('data-mem-dismiss')) { memAnswer(card, ''); return; }
    if (t.hasAttribute('data-mem-editfirst')) {
      const cur = card.querySelector('.mem-sug-text').textContent;
      setHTML(card, `<div class="mem-sug-t">💡 Remember:</div><div class="mem-edit"><input class="mem-sug-input" maxlength="300" value="${esc(cur)}" aria-label="Fact to remember"></div><div class="mem-sug-acts"><button class="small primary" data-mem-keepedit>Remember</button><button class="small" data-mem-dismiss>Not this</button></div>`);
      card.querySelector('input').focus();
      return;
    }
    if (t.hasAttribute('data-mem-keepedit')) { const v = card.querySelector('input').value.trim(); if (v) memAnswer(card, v); return; }
  }
  if (t.dataset.memEdit) { memEditing = t.dataset.memEdit; renderMemory(); $('mem-edit-text').focus(); return; }
  if (t.hasAttribute('data-mem-cancel')) { memEditing = null; renderMemory(); return; }
  if (t.dataset.memSave) {
    try {
      await post('/api/memory/' + encodeURIComponent(t.dataset.memSave), { text: $('mem-edit-text').value, project: $('mem-edit-scope').value });
      memEditing = null; toast('Saved'); loadMemory();
    } catch (err) { toast(err.message); }
    return;
  }
  if (t.dataset.memForget) {
    const m = (memData.memories || []).find((x) => x.id === t.dataset.memForget);
    if (!m || !await askConfirm(`Forget “${m.text}”?`)) return;
    try { await api('/api/memory/' + encodeURIComponent(m.id), { method: 'DELETE' }); toast('Forgotten'); loadMemory(); } catch (err) { toast(err.message); }
  }
});

// ---------- setting offers ----------
// offerStrip hides an [[offer: …]] line while an answer is still streaming in.
function offerStrip(text) { return String(text || '').replace(/\[\[\s*offer\s*:\s*[a-z_]*\s*\]\]/gi, '').replace(/\[\[\s*o[a-z]*\s*:?[a-z_\s]*$/i, ''); }

function offerCard(m, where) {
  const o = m.offer;
  if (!o) return '';
  const key = where.chat ? `chat:${where.chat}` : `project:${where.project}`;
  const scope = o.admin ? 'Changes Settings for the whole cluster' : 'Changes this chat only';
  const hint = [o.hint, o.where ? `${scope}; change it back in ${o.where}.` : scope + '.'].filter(Boolean).join(' ');
  return `<div class="offer-card" data-offer-where="${esc(key)}" data-offer-msg="${esc(m.id)}" data-offer-key="${esc(o.key)}">
    <div class="offer-t">⚙️ ${esc(o.why)}</div><div class="offer-h">${esc(hint)}</div>
    <div class="mem-sug-acts"><button class="small primary" data-offer-apply>${esc(o.label)} and try again</button><button class="small" data-offer-dismiss>Not now</button></div></div>`;
}

async function offerAnswer(card, act) {
  const [kind, id] = card.dataset.offerWhere.split(':');
  const body = { msg: card.dataset.offerMsg, key: card.dataset.offerKey, act };
  body[kind] = id;
  card.querySelectorAll('button').forEach((b) => { b.disabled = true; });
  try {
    const r = await post('/api/setting-offer', body);
    card.remove();
    if (act === 'apply') toast(r.retried ? 'Turned on. Answering again…' : 'Turned on');
    if (kind === 'chat' && typeof chatLoad === 'function' && page === 'chat') { await chatLoad(id); }
    if (kind === 'project' && typeof pjFetch === 'function' && page === 'project') pjFetch(true);
  } catch (e) { toast(e.message); card.querySelectorAll('button').forEach((b) => { b.disabled = false; }); }
}

document.addEventListener('click', (e) => {
  const t = e.target.closest('button');
  const card = t && t.closest('.offer-card');
  if (!card) return;
  if (t.hasAttribute('data-offer-apply')) offerAnswer(card, 'apply');
  else if (t.hasAttribute('data-offer-dismiss')) offerAnswer(card, 'dismiss');
});
