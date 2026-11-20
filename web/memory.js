'use strict';
// Memory (memory.go): Settings → Memory lists the facts every chat and project
// conversation starts with; a suggestion under an answer ("Remember …?") can be
// remembered, edited first or dismissed. Plain script, shares globals with app.js.

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
