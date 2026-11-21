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
// What the suggestion looks for: a make-verb with a picture noun, or a 3D noun.
const SKILL_PICTURE = /^\s*(please\s+)?(can you\s+|could you\s+)?(draw|paint|sketch|render|illustrate|generate|create|make|produce)\b[^.\n]{0,24}\b(image|picture|photo|photograph|illustration|drawing|painting|logo|wallpaper|portrait|poster|icon)s?\b/i;
const SKILL_CAD = /\b(3d[ -]?(model|print(able)?|part)|openscad|stl file|cad (model|part))\b/i;
const skillGuess = (t) => (SKILL_CAD.test(t) && /\b(make|design|create|generate|model|build|print)\b/i.test(t) ? 'cad' : SKILL_PICTURE.test(t) ? 'image' : null);

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
  else skillSet(k);
});
document.addEventListener('click', (e) => { if (!e.target.closest('.plus-wrap')) skillMenuClose(); });
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') skillMenuClose(); });

// Chips next to ＋: the chosen skill and Run code when it is on. Also the suggestion under the text.
function skillRender() {
  const run = $('chat-run').classList.contains('on');
  setHTML('chat-chips', (chatSkill ? `<button type="button" class="chat-pill on" data-sk-chip="skill" title="Remove" aria-label="${SKILLS[chatSkill].name} skill on. Remove">${SKILLS[chatSkill].icon} ${SKILLS[chatSkill].name} ✕</button>` : '')
    + (run ? '<button type="button" class="chat-pill on" data-sk-chip="run" title="Turn off" aria-label="Run code on. Turn off">▶ Run code ✕</button>' : ''));
  const g = !chatSkill && !skillCardState ? skillGuess($('chat-text').value) : null;
  const show = g && skillDismissed !== $('chat-text').value;
  $('chat-suggest').classList.toggle('hidden', !show);
  if (show) setHTML('chat-suggest', `<span>${SKILLS[g].icon} Sounds like a ${g === 'cad' ? '3D model' : 'picture'} request.</span> <button type="button" class="small" data-sk-sug="${g}">${SKILLS[g].title}</button> <button type="button" class="link-btn" data-sk-sug="no" aria-label="Dismiss">Not now</button>`);
  // The card.
  const c = skillCardState;
  if (!c) { setHTML('chat-skill', ''); return; }
  const k = SKILLS[c.kind];
  if (c.done) { setHTML('chat-skill', `<div class="skill-card"><div class="rowline"><b>${k.icon} ${c.done}</b><span class="spacer"></span><button type="button" class="link-btn" data-sk="cancel" aria-label="Close">✕</button></div><div class="sub">${c.doneNote}</div></div>`); return; }
  let opts = '', note = '', can = true;
  if (c.kind === 'image') {
    const ms = (imgModels || []).filter((m) => m.kind === 'image');
    const ready = (m) => m.accepted && m.nodes.some((n) => n.online && n.can_run);
    if (!c.model) { const m = ms.find(ready); c.model = m ? m.id : ''; }
    can = !!c.model;
    opts = `<select id="sk-model" class="chip-select" aria-label="Image model">${ms.map((m) => `<option value="${esc(m.id)}" ${m.id === c.model ? 'selected' : ''}>${esc(m.label)}${ready(m) ? '' : ' · not ready'}</option>`).join('')}</select>
      <select id="sk-size" class="chip-select" aria-label="Size">${imgSizes.image.map((s) => `<option value="${s[0]}" ${s[0] === (c.size || '768x768') ? 'selected' : ''}>${s[1]}</option>`).join('')}</select>
      <select id="sk-count" class="chip-select" aria-label="How many">${imgCounts.image.map((s) => `<option value="${s[0]}" ${+s[0] === (c.count || 1) ? 'selected' : ''}>${s[1]}</option>`).join('')}</select>`;
    note = can ? '' : 'No node can draw yet. Open More settings to download a model.';
  } else {
    const nodes = (cadInfo && cadInfo.model_nodes) || [];
    const norender = cadInfo && !(cadInfo.render_nodes || []).length;
    can = !cadInfo || (nodes.length > 0 && !norender);
    opts = '<span class="sub">Written by the strongest free node, rendered with OpenSCAD.</span>';
    note = !cadInfo || can ? '' : norender ? 'No node can render yet: a node that allows commands needs OpenSCAD installed. More settings explains how.' : 'No node has a model loaded.';
  }
  setHTML('chat-skill', `<div class="skill-card" role="group" aria-label="${k.title}"><div class="rowline"><b>${k.icon} ${k.title}</b><span class="spacer"></span><button type="button" class="link-btn" data-sk="cancel" aria-label="Cancel">✕</button></div>
    <textarea id="sk-prompt" rows="2" aria-label="Prompt">${esc(c.prompt)}</textarea>
    <div class="sk-opts">${opts}</div>${note ? `<div class="sub warn-text">${esc(note)}</div>` : ''}
    <div class="card-actions"><button type="button" class="primary" data-sk="go" ${can ? '' : 'disabled'}>Make it</button><button type="button" data-sk="more">More settings…</button></div></div>`);
}
async function skillOpenCard(kind, prompt) {
  skillCardState = { kind, prompt };
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
  if (!chatSkill) return;
  e.preventDefault(); e.stopImmediatePropagation();
  const text = skillTake();
  if (!text) { $('chat-text').focus(); return; }
  skillOpenCard(chatSkill, text);
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
  if (chip) { if (chip.dataset.skChip === 'run') $('chat-run').click(); else skillSet(null); return; }
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
      const r = await post('/api/cad', { prompt, node: 'auto', rounds: 4, check: true });
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
