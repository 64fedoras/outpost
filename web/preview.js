'use strict';
// Live preview: HTML pages, SVG images, Mermaid diagrams and long Markdown
// documents in answers show as a card; Open shows them in a side panel with
// Preview and Code, versions, Download and Save to project. The preview runs in
// a sandboxed iframe (/preview/frame, preview.go): scripts run, but with no
// network, no cookies and no access to Outpost.

const PV_LABEL = { html: 'HTML', svg: 'SVG', mermaid: 'Mermaid', md: 'Markdown' };
const PV_EXT = { html: 'html', svg: 'svg', mermaid: 'mmd', md: 'md' };
let pvItems = [];      // previewable things on the page now; data-pv indexes this
let pvCur = null;      // { list: [items of the same name], i }
let pvTab = 'preview';
let pvSeq = 0;

function pvSlug(s, fallback) {
  const v = String(s || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);
  return v || fallback;
}

// pvFind returns the previewable code blocks in one answer.
function pvFind(text) {
  const out = [];
  const re = /```([\w+-]*)[^\n]*\n([\s\S]*?)```/g;
  let m;
  while ((m = re.exec(text || ''))) {
    const lang = m[1].toLowerCase(), code = m[2], t = code.trim();
    let kind = '';
    if (lang === 'mermaid') kind = 'mermaid';
    else if (lang === 'svg' || (['', 'xml', 'html'].includes(lang) && /^(<\?xml[^>]*>\s*)?<svg[\s>]/i.test(t))) kind = 'svg';
    else if ((lang === 'html' || lang === 'htm' || lang === '') && /<!doctype html|<html[\s>]|<body[\s>]/i.test(t)) kind = 'html';
    else if ((lang === 'md' || lang === 'markdown') && t.length > 400) kind = 'md';
    if (!kind) continue;
    let name;
    if (kind === 'html') { const ti = /<title[^>]*>([^<]*)<\/title>/i.exec(t); name = pvSlug(ti && ti[1], 'page') + '.html'; }
    else if (kind === 'md') { const h = /^#\s+(.+)$/m.exec(t); name = pvSlug(h && h[1], 'document') + '.md'; }
    else if (kind === 'svg') name = 'image.svg';
    else name = 'diagram.mmd';
    out.push({ kind, name, text: code });
  }
  return out;
}

// pvReset starts a new page render; pvCollect adds an answer's items with their
// version numbers (items with the same name in one conversation are versions).
function pvReset() { pvItems = []; }
function pvCollect(text) {
  return pvFind(text).map((it) => {
    const prev = pvItems.filter((x) => x.name === it.name);
    it.ver = prev.length + 1;
    it.idx = pvItems.length;
    pvItems.push(it);
    return it;
  });
}
function pvCards(items) {
  if (!items.length) return '';
  return `<div class="pv-cards">${items.map((it) => {
    const total = pvItems.filter((x) => x.name === it.name).length;
    const size = new Blob([it.text]).size;
    return `<div class="pv-card"><span class="pv-ic" aria-hidden="true">${it.kind === 'mermaid' ? '◇' : it.kind === 'md' ? '¶' : '▣'}</span><div class="pv-card-t"><b class="mono">${esc(it.name)}</b><span class="muted">${PV_LABEL[it.kind]} · ${bytes(size)}${total > 1 || it.ver > 1 ? ` · version ${it.ver}` : ''}</span></div><button class="small primary" data-pv="${it.idx}">Open</button></div>`;
  }).join('')}</div>`;
}

// pvFile opens a task or project file (.html, .svg, .md, .mmd) in the panel.
function pvKindOf(path) {
  const ext = String(path).split('.').pop().toLowerCase();
  return { html: 'html', htm: 'html', svg: 'svg', md: 'md', markdown: 'md', mmd: 'mermaid', mermaid: 'mermaid' }[ext] || '';
}
function pvFile(path, text) {
  const kind = pvKindOf(path);
  if (!kind) return;
  pvShow([{ kind, name: path.split('/').pop(), text, ver: 1 }], 0);
}

function pvShow(list, i) {
  pvCur = { list, i };
  pvTab = 'preview';
  $('pv-panel').classList.remove('hidden');
  document.body.classList.add('pv-open');
  pvDraw();
}
function pvClose() {
  pvCur = null;
  $('pv-panel').classList.add('hidden');
  document.body.classList.remove('pv-open');
  $('pv-frame').src = 'about:blank';
}

function pvDraw() {
  const it = pvCur.list[pvCur.i];
  $('pv-name').textContent = it.name;
  const sel = $('pv-ver');
  sel.classList.toggle('hidden', pvCur.list.length < 2);
  setHTML('pv-ver', pvCur.list.map((x, j) => `<option value="${j}" ${j === pvCur.i ? 'selected' : ''}>Version ${j + 1} of ${pvCur.list.length}</option>`).join(''));
  $('pv-tab-preview').classList.toggle('on', pvTab === 'preview');
  $('pv-tab-code').classList.toggle('on', pvTab === 'code');
  $('pv-tab-preview').setAttribute('aria-pressed', String(pvTab === 'preview'));
  $('pv-tab-code').setAttribute('aria-pressed', String(pvTab === 'code'));
  $('pv-code').classList.toggle('hidden', pvTab !== 'code');
  $('pv-frame').classList.toggle('hidden', pvTab !== 'preview');
  $('pv-save').classList.toggle('hidden', !(projects || []).length);
  if (pvTab === 'code') { setHTML('pv-code', numbered(it.text, it.name)); return; }
  pvLoad(it);
}

function pvDark() { return document.documentElement.dataset.theme === 'dark' || (!document.documentElement.dataset.theme && matchMedia('(prefers-color-scheme: dark)').matches); }

// pvMsg is what the frame gets: Mermaid source, a whole HTML page, or (SVG and
// Markdown) a body plus a small stylesheet that the frame wraps into a page.
function pvMsg(it) {
  const dark = pvDark();
  if (it.kind === 'mermaid') return { kind: 'mermaid', code: it.text, dark };
  if (it.kind === 'html') return { kind: 'html', html: it.text };
  let css = `html{color-scheme:${dark ? 'dark' : 'light'}}body{margin:0;font:15px/1.6 system-ui,sans-serif;background:${dark ? '#1b1c20' : '#fff'};color:${dark ? '#e6e6e6' : '#1d1d1f'}}`;
  if (it.kind === 'svg') return { kind: 'page', css: css + 'body{display:grid;place-items:center;min-height:100vh}svg{max-width:100%;height:auto}', body: it.text };
  css += `main{max-width:760px;margin:0 auto;padding:20px 24px}pre{background:${dark ? '#26272c' : '#f4f4f5'};padding:10px;border-radius:6px;overflow:auto}code{font-family:ui-monospace,monospace;font-size:.92em}table{border-collapse:collapse}td,th{border:1px solid #8884;padding:4px 8px}img{max-width:100%}a{color:#3b82f6}`;
  return { kind: 'page', css, body: `<main>${md(it.text)}</main>` };
}

function pvLoad(it) {
  const f = $('pv-frame');
  const seq = ++pvSeq;
  const msg = pvMsg(it);
  const onMsg = (e) => {
    if (e.source !== f.contentWindow || !e.data || e.data.outpostPreview !== 'ready') return;
    window.removeEventListener('message', onMsg);
    if (seq === pvSeq) f.contentWindow.postMessage(msg, '*'); // the frame's origin is opaque, so '*'
  };
  window.addEventListener('message', onMsg);
  f.src = '/preview/frame?n=' + seq; // a fresh document for every show
}

function pvDownload() {
  const it = pvCur.list[pvCur.i];
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([it.text], { type: 'text/plain' }));
  a.download = it.name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 2000);
}

function pvSaveOpen() {
  const it = pvCur.list[pvCur.i];
  setHTML('pv-save-proj', (projects || []).map((p) => `<option value="${esc(p.id)}" ${page === 'project' && pageArg === p.id ? 'selected' : ''}>${esc(p.name)}</option>`).join(''));
  $('pv-save-path').value = it.name;
  open('modal-pv-save');
  $('pv-save-path').focus();
}
async function pvSave() {
  const it = pvCur.list[pvCur.i], id = $('pv-save-proj').value, path = $('pv-save-path').value.trim();
  if (!id || !path) return;
  try {
    const r = await post(`/api/projects/${encodeURIComponent(id)}/file`, { path, content: it.text });
    close('modal-pv-save');
    toast(`${r.replaced ? 'Replaced' : 'Saved'} ${r.path} in ${(projectById(id) || {}).name || 'the project'}`);
  } catch (e) { toast(e.message); }
}

document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-pv]');
  if (!b) return;
  e.preventDefault();
  const it = pvItems[+b.dataset.pv];
  if (!it) return;
  const list = pvItems.filter((x) => x.name === it.name);
  pvShow(list, list.indexOf(it));
});
$('pv-x').onclick = pvClose;
$('pv-tab-preview').onclick = () => { pvTab = 'preview'; pvDraw(); };
$('pv-tab-code').onclick = () => { pvTab = 'code'; pvDraw(); };
$('pv-ver').onchange = () => { pvCur.i = +$('pv-ver').value; pvDraw(); };
$('pv-dl').onclick = pvDownload;
$('pv-save').onclick = pvSaveOpen;
$('pv-save-go').onclick = pvSave;
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && pvCur && !document.querySelector('.overlay:not(.hidden)')) pvClose(); });
