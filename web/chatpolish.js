'use strict';
// Chat polish: export a chat (Markdown, text, print to PDF), personas (named instructions to start a chat from),
// and Compare (one prompt to two nodes, answers side by side). Loaded after chat.js and chatskills.js.
// No inline styles or scripts (the CSP forbids them); the print view is a hidden #chat-print block that CSS shows only when printing.

// ---------- export ----------
function chatSlug() { return pvSlug(chatCur && chatCur.title, 'chat'); }

function chatRunLine(r) {
  const lang = { python: 'Python', go: 'Go', sh: 'Shell' }[r.lang] || r.lang;
  return `Ran ${lang}${r.node ? ' on ' + r.node : ''}: ${r.error ? r.error : 'exit ' + r.exit}${(r.files || []).length ? ', wrote ' + r.files.map((f) => f.name).join(', ') : ''}`;
}

// chatExportParts turns the chat into [{who, meta, text, notes[]}] in reading order.
function chatExportParts() {
  return (chatCur.messages || []).filter((m) => m.text || (m.images || []).length || (m.docs || []).length).map((m) => {
    const notes = [];
    if ((m.images || []).length) notes.push(`Attached ${m.images.length} image${m.images.length === 1 ? '' : 's'}`);
    for (const d of m.docs || []) notes.push(`Attached ${d.name}`);
    for (const r of m.runs || []) notes.push(chatRunLine(r));
    if (m.research && (m.research.lanes || []).length) notes.push('Researched: ' + m.research.lanes.map((l) => l.q).join(' · '));
    if (m.role === 'user') return { who: 'You', meta: '', text: m.text || '', notes };
    return { who: 'Assistant', meta: [m.model, m.node && 'on ' + m.node].filter(Boolean).join(' '), text: m.text || '', notes };
  });
}

function chatExportMarkdown() {
  const when = new Date().toLocaleString();
  let out = `# ${chatCur.title || 'Chat'}\n\n_Exported from Outpost on ${when}._\n`;
  for (const p of chatExportParts()) {
    out += `\n## ${p.who}${p.meta ? ` (${p.meta})` : ''}\n\n`;
    if (p.text) out += p.text.trim() + '\n';
    if (p.notes.length) out += '\n' + p.notes.map((n) => `> ${n}`).join('\n') + '\n';
  }
  return out;
}

function chatExportText() {
  const line = '-'.repeat(60);
  let out = `${chatCur.title || 'Chat'}\nExported from Outpost on ${new Date().toLocaleString()}\n`;
  for (const p of chatExportParts()) {
    out += `\n${line}\n${p.who}${p.meta ? ` (${p.meta})` : ''}\n\n`;
    if (p.text) out += p.text.trim() + '\n';
    for (const n of p.notes) out += `[${n}]\n`;
  }
  return out;
}

function chatDownload(name, text, type) {
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([text], { type }));
  a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 2000);
}

function chatPrint() {
  const box = $('chat-print');
  setHTML('chat-print', `<h1>${esc(chatCur.title || 'Chat')}</h1><p class="muted">Exported from Outpost on ${esc(new Date().toLocaleString())}</p>` +
    chatExportParts().map((p) => `<section class="pr-msg pr-${p.who === 'You' ? 'user' : 'bot'}"><h2>${esc(p.who)}${p.meta ? ` <span class="muted">${esc(p.meta)}</span>` : ''}</h2><div class="md">${md(p.text)}</div>${p.notes.map((n) => `<p class="pr-note">${esc(n)}</p>`).join('')}</section>`).join(''));
  document.body.classList.add('printing');
  const done = () => { document.body.classList.remove('printing'); box.textContent = ''; window.removeEventListener('afterprint', done); };
  window.addEventListener('afterprint', done);
  setTimeout(() => window.print(), 50);
}

$('chat-export-btn').onclick = () => open('modal-chat-export');
$('exp-md').onclick = () => { chatDownload(chatSlug() + '.md', chatExportMarkdown(), 'text/markdown'); close('modal-chat-export'); toast('Saved ' + chatSlug() + '.md'); };
$('exp-txt').onclick = () => { chatDownload(chatSlug() + '.txt', chatExportText(), 'text/plain'); close('modal-chat-export'); toast('Saved ' + chatSlug() + '.txt'); };
$('exp-pdf').onclick = () => { close('modal-chat-export'); chatPrint(); };

// ---------- personas ----------
let personas = [];
async function loadPersonas() {
  try { personas = (await api('/api/settings')).chat_personas || []; } catch (e) { personas = []; }
  drawPersonas();
}
function drawPersonas(sel) {
  const el = $('chat-persona');
  setHTML('chat-persona', `<option value="">${personas.length ? 'Start from a persona…' : 'No personas saved yet'}</option>` +
    personas.map((p) => `<option value="${esc(p.id)}" ${sel === p.id ? 'selected' : ''}>${esc(p.name)}</option>`).join(''));
  el.disabled = !personas.length;
  $('chat-persona-del').classList.toggle('hidden', !sel);
}
async function savePersonas(list, sel, msg) {
  try {
    const r = await post('/api/settings', { chat_personas: list });
    personas = r && r.chat_personas ? r.chat_personas : (await api('/api/settings')).chat_personas || [];
    drawPersonas(sel && personas.find((p) => p.id === sel) ? sel : '');
    toast(msg);
  } catch (e) { toast(e.message); }
}
$('chat-instr-btn').addEventListener('click', () => { $('chat-persona-new').classList.add('hidden'); loadPersonas(); });
$('chat-persona').onchange = () => {
  const p = personas.find((x) => x.id === $('chat-persona').value);
  $('chat-persona-del').classList.toggle('hidden', !p);
  if (p) $('chat-instr-text').value = p.text;
};
$('chat-persona-save').onclick = () => {
  if (!$('chat-instr-text').value.trim()) { toast('Write the instructions first, then save them as a persona'); $('chat-instr-text').focus(); return; }
  const cur = personas.find((x) => x.id === $('chat-persona').value);
  $('chat-persona-name').value = cur ? cur.name : '';
  $('chat-persona-new').classList.remove('hidden');
  $('chat-persona-name').focus();
};
$('chat-persona-cancel').onclick = () => $('chat-persona-new').classList.add('hidden');
$('chat-persona-ok').onclick = async () => {
  const name = $('chat-persona-name').value.trim(), text = $('chat-instr-text').value.trim();
  if (!name) { $('chat-persona-name').focus(); return; }
  const same = personas.find((p) => p.name.toLowerCase() === name.toLowerCase());
  const list = same ? personas.map((p) => (p === same ? { ...p, text } : p)) : personas.concat([{ id: '', name, text }]);
  $('chat-persona-new').classList.add('hidden');
  await savePersonas(list, same && same.id, same ? `Updated “${name}”` : `Saved “${name}”`);
  const now = personas.find((p) => p.name.toLowerCase() === name.toLowerCase());
  if (now) drawPersonas(now.id);
};
$('chat-persona-name').addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); $('chat-persona-ok').click(); } });
$('chat-persona-del').onclick = async () => {
  const p = personas.find((x) => x.id === $('chat-persona').value);
  if (!p || !await askConfirm(`Delete the persona “${p.name}”? Chats that already use its text keep it.`)) return;
  await savePersonas(personas.filter((x) => x !== p), '', 'Persona deleted');
};

// ---------- compare ----------
let cmpId = null, cmpTimer = null;
function cmpNodes() {
  return (state ? state.nodes : []).filter((n) => n.online && n.heartbeat && n.heartbeat.inference_ready && n.heartbeat.loaded_model && !n.heartbeat.paused);
}
function cmpOpen() {
  const nodes = cmpNodes();
  if (nodes.length < 2) { toast('Compare needs two online nodes that each have a model loaded.', 5000); return; }
  const opts = nodes.map((n) => `<option value="${esc(n.id)}">${esc(n.heartbeat.loaded_model)} on ${esc(nodeName(n))}</option>`).join('');
  const a = $('cmp-a').value, b = $('cmp-b').value;
  setHTML('cmp-a', opts); setHTML('cmp-b', opts);
  $('cmp-a').value = nodes.some((n) => n.id === a) ? a : nodes[0].id;
  $('cmp-b').value = nodes.some((n) => n.id === b) && b !== $('cmp-a').value ? b : nodes.find((n) => n.id !== $('cmp-a').value).id;
  if (!$('cmp-prompt').value) $('cmp-prompt').value = $('chat-text').value;
  $('cmp-err').classList.add('hidden');
  open('modal-compare');
  $('cmp-prompt').focus();
}
function cmpDraw(c) {
  const fastest = c.sides.every((s) => s.status === 'done' && s.tok_s) ? (c.sides[0].tok_s >= c.sides[1].tok_s ? 0 : 1) : -1;
  setHTML('cmp-cols', c.sides.map((s, i) => {
    const stat = { waiting: 'waiting for a free slot…', working: 'answering…', stopped: 'stopped', failed: 'failed' }[s.status] || '';
    const stats = s.status === 'done' ? [s.tok_s && `${s.tok_s.toFixed(1)} tok/s`, s.tokens && `${s.tokens} tokens`, s.secs && `${s.secs.toFixed(1)} s`].filter(Boolean).join(' · ') : '';
    return `<div class="cmp-col ${s.status}"><div class="cmp-h"><b>${esc(s.model || 'model')}</b><span class="muted"> on ${esc(s.node)}</span>${i === fastest ? ' <span class="badge">faster</span>' : ''}</div>
      ${stat ? `<p class="muted cmp-st">${s.status === 'working' || s.status === 'waiting' ? '<span class="cmp-dot" aria-hidden="true"></span>' : ''}${esc(stat)}</p>` : ''}
      ${s.error && s.status !== 'stopped' ? `<p class="error-msg">${esc(s.error)}</p>` : ''}
      ${s.text ? `<div class="chat-text md cmp-text">${md(s.text)}</div>` : ''}
      ${stats ? `<div class="sub cmp-stats">${esc(stats)}${s.text ? ` · <button class="link-btn" data-cmp-copy="${i}">Copy</button>` : ''}</div>` : ''}</div>`;
  }).join(''));
  $('cmp-cols').classList.remove('hidden');
  $('cmp-cols')._sides = c.sides;
}
async function cmpPoll() {
  clearTimeout(cmpTimer);
  if (!cmpId || $('modal-compare').classList.contains('hidden')) return;
  try {
    const c = await api('/api/compare/' + cmpId);
    cmpDraw(c);
    $('cmp-run').disabled = !c.done;
    $('cmp-stop').classList.toggle('hidden', c.done);
    if (!c.done) cmpTimer = setTimeout(cmpPoll, 800);
  } catch (e) { $('cmp-err').textContent = e.message; $('cmp-err').classList.remove('hidden'); $('cmp-run').disabled = false; $('cmp-stop').classList.add('hidden'); }
}
$('chat-cmp-btn').onclick = cmpOpen;
$('cmp-run').onclick = async () => {
  const prompt = $('cmp-prompt').value.trim();
  if (!prompt) { $('cmp-prompt').focus(); return; }
  if ($('cmp-a').value === $('cmp-b').value) { $('cmp-err').textContent = 'Pick two different nodes.'; $('cmp-err').classList.remove('hidden'); return; }
  $('cmp-err').classList.add('hidden');
  $('cmp-run').disabled = true;
  try {
    cmpId = (await post('/api/compare', { prompt, nodes: [$('cmp-a').value, $('cmp-b').value] })).id;
    cmpPoll();
  } catch (e) { $('cmp-err').textContent = e.message; $('cmp-err').classList.remove('hidden'); $('cmp-run').disabled = false; }
};
$('cmp-stop').onclick = async () => { try { await api('/api/compare/' + cmpId, { method: 'DELETE' }); } catch (e) { toast(e.message); } };
$('cmp-prompt').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $('cmp-run').click(); } });
$('cmp-cols').addEventListener('click', async (e) => {
  const b = e.target.closest('[data-cmp-copy]');
  if (b) { await copyText($('cmp-cols')._sides[+b.dataset.cmpCopy].text || ''); toast('Copied'); }
});

// Show or hide the header buttons; chatButtons() in chat.js calls this.
function chatPolishButtons() {
  $('chat-export-btn').classList.toggle('hidden', !(chatCur && (chatCur.messages || []).length));
  $('chat-cmp-btn').classList.toggle('hidden', cmpNodes().length < 2);
}
