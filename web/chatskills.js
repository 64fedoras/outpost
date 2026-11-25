'use strict';
// Chat skills: the ＋ menu (attach, run code, make an image, make a 3D model), the chip for a chosen skill,
// the "looks like a picture request" suggestion, and the confirm card that sits above the composer.
// The card prefills the same requests the Images and 3D pages send, so nothing new is needed on the controller.
// Loaded after chat.js, images.js and cad.js. No inline styles (the CSP forbids them).
let chatSkill = null, skillCardState = null, skillDismissed = '', skillPending = null;
const SKILLS = {
  image: { icon: '✦', name: 'Image', title: 'Make an image', hint: 'Describe the picture…', route: '#/image', page: 'image' },
  cad: { icon: '◇', name: '3D model', title: 'Make a 3D model', hint: 'Describe the part (shape and sizes in mm)…', route: '#/cad', page: 'cad' },
};
// What the suggestion looks for: a make-verb with a picture noun, or a 3D cue.
const SKILL_PICTURE = /^\s*(please\s+)?(can you\s+|could you\s+)?(draw|paint|sketch|render|illustrate|generate|create|make|produce)\b[^.\n]{0,24}\b(image|picture|photo|photograph|illustration|drawing|painting|logo|wallpaper|portrait|poster|icon)s?\b/i;
// 3D requests (cad.go). A cue says the message is about a printable part; a clear request also has a
// make or change verb and is not phrased as a question. Code (```, std::, C++) is never a 3D request,
// so "the STL vector" stays in chat.
const CAD_SITES = /https?:\/\/(www\.)?(makerworld\.com|printables\.com|thingiverse\.com|cults3d\.com|thangs\.com|myminifactory\.com)\/\S*|https?:\/\/\S+\.(stl|3mf|obj)\b/i;
const SKILL_CAD_CUE = /\b(3d[ -]?(models?|print(s|ed|er|able|ing)?|parts?|designs?)|openscad|stl|3mf|\.scad|rack[ -]?mount(ed|s)?)\b/i;
const SKILL_CAD_VERB = /\b(make|design|create|generate|build|model|modify|remix|adapt|resize|rescale|scale|change|alter|convert|turn|draw up|print me|i need|i want)\b/i;
const SKILL_POLITE = /^\s*((please|pls|hey|hi)[\s,]+)?(can|could|would|will) you\b/i;
const SKILL_QWORD = /^\s*(what|how|why|which|where|when|who|whose|should|is|are|was|were|does|do|did|can|could|would|will|am|any|tips?)\b/i;
const SKILL_CODE = /```|std::|c\+\+/i;
// cadIntent: 'clear' (Send opens the 3D card), 'hint' (chat answers, with Make this in 3D), or null.
function cadIntent(t) {
  t = t || '';
  if (SKILL_CODE.test(t) || !(SKILL_CAD_CUE.test(t) || CAD_SITES.test(t))) return null;
  const q = !SKILL_POLITE.test(t) && (SKILL_QWORD.test(t) || /\?\s*$/.test(t));
  return !q && SKILL_CAD_VERB.test(t) ? 'clear' : 'hint';
}
const skillGuess = (t) => (cadIntent(t) ? 'cad' : SKILL_PICTURE.test(t) ? 'image' : null);
// A named device or part number (GL-AR750, RPi4, ESP32), so the card offers a web look-up of its size.
const cadDevice = (t) => (t.replace(/https?:\/\/\S+/g, '').match(/\b[A-Za-z0-9][A-Za-z0-9.-]{2,}\b/g) || [])
  .some((w) => /\d/.test(w) && /[A-Za-z]{2}/.test(w) && !/^\d+(\.\d+)?(mm|cm|in|inch|u|m)$/i.test(w) && !/^3d/i.test(w));
// Chats where "Answer in chat instead" was chosen: Send no longer opens the 3D card there.
const cadChatOnly = new Set((() => { try { return JSON.parse(localStorage.getItem('outpost.cadChatOnly') || '[]'); } catch (e) { return []; } })());
let cadChatOnlyNew = false;
function cadChatOnlyAdd(id) {
  cadChatOnly.add(id);
  try { localStorage.setItem('outpost.cadChatOnly', JSON.stringify([...cadChatOnly].slice(-200))); } catch (e) { /* only for this visit */ }
}

function skillSet(k) {
  chatSkill = k; skillCardState = null; skillRender();
  const t = $('chat-text'); if (k) t.placeholder = SKILLS[k].hint; else if (typeof chatPlaceholder === 'function') chatPlaceholder();
  t.focus();
}
function skillMenuClose() { $('chat-plus-menu').classList.add('hidden'); $('chat-plus').setAttribute('aria-expanded', 'false'); }
function skillMenuRender() {
  const run = $('chat-run').classList.contains('on');
  setHTML('chat-plus-menu', `<button type="button" role="menuitem" data-sk-menu="attach"><span aria-hidden="true">📎</span> Attach images or documents</button>
    <button type="button" role="menuitem" data-sk-menu="run" aria-pressed="${run}"><span aria-hidden="true">▶</span> Run code${run ? ' <span class="muted">· on</span>' : ''}</button>
    <button type="button" role="menuitem" data-sk-menu="stash"><span aria-hidden="true">▦</span> Search a stash${shChatIds().length ? ` <span class="muted">· ${shChatIds().length} on</span>` : ''}</button>
    <hr><div class="menu-h">Skills</div>
    <button type="button" role="menuitem" data-sk-menu="image"><span aria-hidden="true">✦</span> Make an image</button>
    <button type="button" role="menuitem" data-sk-menu="cad"><span aria-hidden="true">◇</span> Make a 3D model</button>`);
}
$('chat-plus').onclick = (e) => {
  e.stopPropagation();
  const open = $('chat-plus-menu').classList.contains('hidden');
  if (open) { skillMenuRender(); $('chat-plus-menu').classList.remove('hidden'); } else skillMenuClose();
  $('chat-plus').setAttribute('aria-expanded', String(open));
};
$('chat-plus-menu').addEventListener('click', (e) => {
  const b = e.target.closest('[data-sk-menu]'); if (!b) return;
  skillMenuClose();
  const k = b.dataset.skMenu;
  if (k === 'attach') $('chat-attach').click();
  else if (k === 'run') $('chat-run').click();
  else if (k === 'stash') shPickOpen();
  else skillSet(k);
});
document.addEventListener('click', (e) => { if (!e.target.closest('.plus-wrap')) skillMenuClose(); });
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') skillMenuClose(); });

// Chips next to ＋: the chosen skill and Run code when it is on. Also the suggestion under the text.
function skillRender() {
  const run = $('chat-run').classList.contains('on');
  setHTML('chat-chips', (chatSkill ? `<button type="button" class="chat-pill on" data-sk-chip="skill" title="Remove" aria-label="${SKILLS[chatSkill].name} skill on. Remove">${SKILLS[chatSkill].icon} ${SKILLS[chatSkill].name} ✕</button>` : '')
    + (run ? '<button type="button" class="chat-pill on" data-sk-chip="run" title="Turn off" aria-label="Run code on. Turn off">▶ Run code ✕</button>' : '')
    + shChatChips());
  const g = !chatSkill && !skillCardState ? skillGuess($('chat-text').value) : null;
  const show = g && skillDismissed !== $('chat-text').value;
  $('chat-suggest').classList.toggle('hidden', !show);
  if (show) setHTML('chat-suggest', `<span>${SKILLS[g].icon} Sounds like a ${g === 'cad' ? '3D model' : 'picture'} request.</span> <button type="button" class="small" data-sk-sug="${g}">${SKILLS[g].title}</button> <button type="button" class="link-btn" data-sk-sug="no" aria-label="Dismiss">Not now</button>`);
  // The card.
  const c = skillCardState;
  if (!c) { setHTML('chat-skill', ''); return; }
  const k = SKILLS[c.kind];
  if (c.done) { setHTML('chat-skill', `<div class="skill-card"><div class="rowline"><b>${k.icon} ${c.done}</b><span class="spacer"></span><button type="button" class="link-btn" data-sk="cancel" aria-label="Close">✕</button></div><div class="sub">${c.doneNote}</div></div>`); return; }
  let opts = '', note = '', can = true, top = '', foot = '';
  if (c.kind === 'image') {
    const ms = (imgModels || []).filter((m) => m.kind === 'image');
    const ready = (m) => m.accepted && m.nodes.some((n) => n.online && n.can_run);
    if (!c.model) { const m = ms.find(ready); c.model = m ? m.id : ''; }
    can = !!c.model;
    opts = `<select id="sk-model" class="chip-select" aria-label="Image model">${ms.map((m) => `<option value="${esc(m.id)}" ${m.id === c.model ? 'selected' : ''}>${esc(m.label)}${ready(m) ? '' : ' · not ready'}</option>`).join('')}</select>
      <select id="sk-size" class="chip-select" aria-label="Size">${imgSizes.image.map((s) => `<option value="${s[0]}" ${s[0] === (c.size || '768x768') ? 'selected' : ''}>${s[1]}</option>`).join('')}</select>
      <select id="sk-count" class="chip-select" aria-label="How many">${imgCounts.image.map((s) => `<option value="${s[0]}" ${+s[0] === (c.count || 1) ? 'selected' : ''}>${s[1]}</option>`).join('')}</select>`;
    note = can ? '' : '<div class="sub warn-text">No node can draw yet. Open More settings to download a model.</div>';
  } else {
    const nodes = (cadInfo && cadInfo.model_nodes) || [];
    const render = (cadInfo && cadInfo.render_nodes) || [];
    const norender = cadInfo && !render.length;
    can = !cadInfo || (nodes.length > 0 && !norender);
    if (c.routed) top = `<div class="sub">This reads like a 3D modeling request, so it came here instead of the chat. <button type="button" class="link-btn" data-sk="chat">Answer in chat instead</button></div>`;
    if (c.mesh) top += `<div class="sk-note"><b>The link is an existing model (STL or 3MF).</b> Outpost can't edit a mesh. It will design a new part in OpenSCAD that you can resize, using the page and the measurements it finds.</div>`;
    opts = (c.links ? `<label class="check"><input type="checkbox" id="sk-links"${c.readLinks ? ' checked' : ''}> Read the linked page to see what the original part is</label>` : '')
      + `<label class="check"><input type="checkbox" id="sk-lookup"${c.lookup ? ' checked' : ''}> Look up the measurements of the devices named here on the web</label>`
      + `<span class="sub">Add your own measurements in mm if you have them; they win over web numbers.</span>`;
    foot = `<span class="sub">Written by the strongest free node, rendered with OpenSCAD${render.length ? ' on ' + esc(render.join(', ')) : ''}.</span>`;
    if (norender) note = cadInstallNote(c);
    else if (cadInfo && !nodes.length) note = `<div class="sk-note">No node has a model loaded, so nothing can write the file yet.</div>`;
  }
  setHTML('chat-skill', `<div class="skill-card" role="group" aria-label="${k.title}"><div class="rowline"><b>${k.icon} ${k.title}</b><span class="spacer"></span><button type="button" class="link-btn" data-sk="cancel" aria-label="Cancel">✕</button></div>
${top}
    <textarea id="sk-prompt" rows="2" aria-label="Prompt">${esc(c.prompt)}</textarea>
    <div class="sk-opts">${opts}</div>${note}
    <div class="card-actions"><button type="button" class="primary" data-sk="go" ${can ? '' : 'disabled'}>Make it</button><button type="button" data-sk="more">More settings…</button>${foot}</div></div>`);
  cadWatch();
}
// No node renders: offer the same one-click OpenSCAD install as the Nodes page (controller_toolinstall.go).
function cadInstallNote(c) {
  const ops = Object.values((state && state.ops) || {});
  const op = ops.find((o) => o && o.op === 'toolchain' && o.target === 'openscad');
  const running = op && !['done', 'ready', 'failed'].includes(op.state);
  if (running || (c.installing && !(op && op.state === 'failed'))) return `<div class="sk-note">Installing OpenSCAD${c.installing ? ' on ' + esc(c.installing) : ''}… <span class="muted">Make it turns on when it's ready.</span></div>`;
  const failed = op && op.state === 'failed' ? `<div class="sub warn-text">The last install failed: ${esc(op.error || 'unknown error')}</div>` : '';
  const rec = ((state && state.tool_recs) || []).find((r) => r.tool === 'openscad');
  if (rec && rec.nodes && rec.nodes.length) {
    return `<div class="sk-note"><b>No node can render 3D yet.</b> OpenSCAD is needed, ${esc(rec.size)}.${failed}
      <div class="sk-opts"><select id="sk-inst-node" class="chip-select" aria-label="Node to install OpenSCAD on">${rec.nodes.map((n, i) => `<option value="${esc(n.id)}">${esc(n.name)}${i ? '' : ' (recommended)'}</option>`).join('')}</select>
      <button type="button" class="small primary" data-sk="install">Install OpenSCAD</button></div></div>`;
  }
  if (rec) return `<div class="sk-note"><b>No node can render 3D yet.</b> ${esc(rec.reason || '')}${failed}</div>`;
  return `<div class="sk-note"><b>No node can render 3D yet.</b> Turn on Allow commands for a node on the <a href="#/nodes">Nodes page</a>, then install OpenSCAD there from this card.${failed}</div>`;
}
// While the 3D card waits for a renderer, check again every few seconds so Make it turns on by itself.
let cadWatchTimer = null;
function cadWatch() {
  clearTimeout(cadWatchTimer);
  const c = skillCardState;
  if (!c || c.done || c.kind !== 'cad' || !cadInfo || (cadInfo.render_nodes || []).length) return;
  cadWatchTimer = setTimeout(async () => {
    if (skillCardState !== c) return;
    try { cadInfo = await api('/api/cad'); } catch (e) { /* try again next time */ }
    if ((cadInfo.render_nodes || []).length) c.installing = '';
    if (skillCardState === c) skillRender();
  }, 4000);
}
async function skillOpenCard(kind, prompt, routed) {
  skillCardState = { kind, prompt };
  if (kind === 'cad') {
    const links = /https?:\/\//i.test(prompt);
    Object.assign(skillCardState, { routed: !!routed, links, readLinks: links, mesh: CAD_SITES.test(prompt), lookup: links || cadDevice(prompt) });
  }
  chatSkill = null; $('chat-text').placeholder = ''; if (typeof chatPlaceholder === 'function') chatPlaceholder();
  skillRender();
  try {
    if (kind === 'image' && !imgModels) imgModels = (await api('/api/images/models')).models;
    if (kind === 'cad' && !cadInfo) cadInfo = await api('/api/cad');
  } catch (e) { /* the card still opens; More settings has the details */ }
  skillRender();
  const t = $('sk-prompt'); if (t) { t.focus(); t.setSelectionRange(t.value.length, t.value.length); }
}
function skillTake() { // the text goes to the card; the composer is left empty
  const text = $('chat-text').value.trim();
  $('chat-text').value = ''; $('chat-text').style.height = '';
  return text;
}
// Send with a skill chosen opens the card instead of sending a chat message.
function skillIntercept(e) {
  if (!chatSkill) { cadSendRoute(e); return; }
  e.preventDefault(); e.stopImmediatePropagation();
  const text = skillTake();
  if (!text) { $('chat-text').focus(); return; }
  skillOpenCard(chatSkill, text);
}
// A clear 3D request opens the 3D card instead of going to the chat model. Nothing runs until Make it.
// Not with images or documents attached (the 3D tool can't use them), and not in a chat where
// "Answer in chat instead" was chosen.
function cadSendRoute(e) {
  const text = $('chat-text').value.trim();
  if (!text || cadIntent(text) !== 'clear') return;
  if (chatCur ? cadChatOnly.has(chatCur.id) : cadChatOnlyNew) return;
  if (chatImg.payload().length || chatImg.docs().length) return;
  e.preventDefault(); e.stopImmediatePropagation();
  skillOpenCard('cad', skillTake(), true);
}
$('chat-send').addEventListener('click', skillIntercept, true);
$('chat-text').addEventListener('keydown', (e) => {
  if (e.key !== 'Enter' || e.isComposing || e.shiftKey || (typeof chatTouch === 'function' && chatTouch())) return;
  if (typeof chatCtrlSend === 'function' && chatCtrlSend() && !(e.ctrlKey || e.metaKey)) return;
  skillIntercept(e);
}, true);
$('chat-text').addEventListener('input', skillRender);
document.querySelector('.chat-compose').addEventListener('click', (e) => {
  const chip = e.target.closest('[data-sk-chip]');
  if (chip) { if (chip.dataset.skChip === 'run') $('chat-run').click(); else if (chip.dataset.skChip === 'stash') shChatSet(shChatIds().filter((x) => x !== chip.dataset.id)); else skillSet(null); return; }
  const sug = e.target.closest('[data-sk-sug]');
  if (sug) {
    if (sug.dataset.skSug === 'no') { skillDismissed = $('chat-text').value; skillRender(); return; }
    skillOpenCard(sug.dataset.skSug, skillTake());
  }
});
$('chat-skill').addEventListener('click', async (e) => {
  const b = e.target.closest('[data-sk]'); if (!b) return;
  const c = skillCardState; if (!c) return;
  const a = b.dataset.sk;
  if (a === 'cancel') { skillCardState = null; skillRender(); return; }
  if (a === 'chat') { // put the text back and send it to the chat as normal
    const text = (($('sk-prompt') || {}).value || c.prompt).trim();
    if (chatCur) cadChatOnlyAdd(chatCur.id); else cadChatOnlyNew = true;
    skillCardState = null; skillRender();
    $('chat-text').value = text;
    chatSend();
    return;
  }
  if (a === 'install') {
    const sel = $('sk-inst-node'); if (!sel || !sel.value) return;
    b.disabled = true;
    try {
      await post(`/api/nodes/${sel.value}/toolchain`, { tool: 'openscad' });
      c.installing = sel.options[sel.selectedIndex].text.replace(' (recommended)', '');
    } catch (x) { toast(x.message); }
    skillRender();
    return;
  }
  const prompt = ($('sk-prompt') || {}).value ? $('sk-prompt').value.trim() : c.prompt;
  if (a === 'more') { skillPending = { kind: c.kind, prompt }; skillCardState = null; skillRender(); if (location.hash === SKILLS[c.kind].route) skillFill(); else location.hash = SKILLS[c.kind].route; return; }
  if (a !== 'go' || !prompt) return;
  b.disabled = true;
  try {
    if (c.kind === 'image') {
      const [w, h] = $('sk-size').value.split('x').map(Number);
      await post('/api/images', { model: $('sk-model').value, prompt, width: w, height: h, count: +$('sk-count').value });
      skillCardState = { kind: 'image', done: 'Queued', doneNote: 'It draws in the background. <a href="#/image">Open the pictures</a> to watch it and download the results.' };
    } else {
      const r = await post('/api/cad', { prompt, node: 'auto', rounds: 4, check: true, read_links: !!(c.links && c.readLinks), lookup: !!c.lookup });
      cadSel = r.model.id; cadDetail = null;
      skillCardState = { kind: 'cad', done: 'Started', doneNote: 'The model writes the file and a node renders it. <a href="#/cad">Open the 3D page</a> to watch and download it.' };
    }
  } catch (x) { toast(x.message); skillCardState = Object.assign(c, { prompt }); }
  skillRender();
});
$('chat-skill').addEventListener('change', (e) => {
  const c = skillCardState; if (!c) return;
  if (e.target.id === 'sk-model') c.model = e.target.value;
  if (e.target.id === 'sk-size') c.size = e.target.value;
  if (e.target.id === 'sk-count') c.count = +e.target.value;
  if (e.target.id === 'sk-links') c.readLinks = e.target.checked;
  if (e.target.id === 'sk-lookup') c.lookup = e.target.checked;
});
// Keep the typed prompt when the card draws again (the install check redraws it).
$('chat-skill').addEventListener('input', (e) => { if (e.target.id === 'sk-prompt' && skillCardState) skillCardState.prompt = e.target.value; });

// "Make this in 3D" under an answer to a message about 3D printing that went to the chat.
function skillAnswerAct(msgs, i) {
  for (let j = i - 1; j >= 0; j--) {
    if (msgs[j].role !== 'user') continue;
    return cadIntent(msgs[j].text) ? `<button data-sk-make="${j}" title="Open the 3D model card with your message filled in">◇ Make this in 3D</button>` : '';
  }
  return '';
}
$('chat-msgs').addEventListener('click', (e) => {
  const b = e.target.closest('[data-sk-make]'); if (!b || !chatCur) return;
  const m = chatCur.messages[Number(b.dataset.skMake)];
  if (m) skillOpenCard('cad', m.text);
});
// A new chat whose first message went to chat from the 3D card keeps going to chat.
window.addEventListener('hashchange', () => {
  if (!cadChatOnlyNew) return;
  const m = location.hash.match(/^#\/chat\/(.+)$/);
  if (m) { cadChatOnlyAdd(decodeURIComponent(m[1])); cadChatOnlyNew = false; }
});
// "More settings…": land on the full page with the prompt filled in.
function skillFill() {
  const p = skillPending; if (!p) return;
  const el = $(p.kind === 'image' ? 'img-prompt' : 'cad-prompt');
  if (el && page === SKILLS[p.kind].page) { el.value = p.prompt; skillPending = null; el.focus(); }
}
window.addEventListener('hashchange', () => setTimeout(skillFill, 0));
// Keep the chips in step with chatButtons (Run code can change from elsewhere).
(function () { const orig = chatButtons; chatButtons = function () { orig(); skillRender(); }; })();
skillRender();
