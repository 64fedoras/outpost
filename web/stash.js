'use strict';
// Stash page (#/stash and #/stash/<id>): collections of resources (documents) that chats and tasks can search.
// The controller keeps the files; nodes only embed text in memory. Plain script, shares globals with app.js.

let shList = null, shDetail = null, shAt = 0, shBusyLoad = false, shBuilt = null, shHits = null, shNote = '', shAll = false, shUploading = '';

const SH_STATUS = { empty: ['cancelled', 'Empty'], indexing: ['running', 'Indexing'], needs_reindex: ['skipped', 'Needs re-index'], ready: ['ready', 'Ready'] };
function shPill(v) {
  const [cls, text] = SH_STATUS[v.status] || ['cancelled', v.status];
  return pill(cls, v.status === 'indexing' && v.progress ? `${text} ${v.progress}%` : text);
}
const shModelLabel = (m) => String(m || '').replace(/-q\d.*$/, '').replace(/-/g, ' ');

async function shLoad(force) {
  if (shBusyLoad) return;
  shBusyLoad = true;
  try {
    if (pageArg) {
      shDetail = await api('/api/stash/' + encodeURIComponent(pageArg));
    } else {
      shList = await api('/api/stash');
    }
    shAt = Date.now();
  } catch (e) {
    if (pageArg && /no such stash/.test(e.message)) { location.hash = '#/stash'; }
    else if (!shList && !shDetail) setHTML('sh-root', `<div class="error-msg">${esc(e.message)}</div>`);
    shBusyLoad = false;
    return;
  }
  shBusyLoad = false;
  shRender();
}

function stashRoute() {
  shDetail = null; shBuilt = null; shHits = null; shNote = ''; shAll = false;
  $('sh-root')._h = null;
  shAt = 0;
  shLoad(true);
}

// Called with every state poll: refresh quickly while something is indexing, slowly otherwise.
function stashTick() {
  const working = pageArg ? shDetail && shDetail.stash.status === 'indexing' : shList && shList.stashes.some((s) => s.status === 'indexing');
  if (Date.now() - shAt > (working ? 1500 : 10000)) shLoad();
}

function shRender() {
  if (pageArg) { if (shDetail) shRenderDetail(); return; }
  shBuilt = null;
  if (shList) shRenderList();
}

function shBanner(ready) {
  if (ready) return '';
  return `<div class="sh-banner"><span><b>No embedding model yet.</b> The Stash needs a small one (Nomic Embed Text, about 150 MB). It runs on a node's CPU and stays out of your chat model's way.</span><span class="spacer"></span><button class="small primary" data-sh="add-embed">Add Nomic Embed Text</button></div>`;
}

function shRenderList() {
  const d = shList;
  const rows = d.stashes.map((s) => {
    const stale = s.status === 'needs_reindex';
    const sub = s.status === 'indexing' ? `Indexing · ${s.resources} resource${s.resources === 1 ? '' : 's'}`
      : stale ? 'Indexed with another embedding model than the one in use now. Search works again after re-indexing.'
        : [s.description, s.model ? shModelLabel(s.model) : ''].filter(Boolean).join(' · ');
    const prog = s.status === 'indexing' ? `<div class="sh-prog">${bar(s.progress)}</div>` : '';
    return `<div class="sh-row"><div><div class="sh-name">${esc(s.name)} ${shPill(s)}${s.failed ? ` <span class="pill st-failed">${s.failed} failed</span>` : ''}</div>
      <div class="sh-sub${stale ? ' warn' : ''}">${esc(sub)}</div>${prog}</div>
      <div class="sh-nums"><b>${s.resources} resource${s.resources === 1 ? '' : 's'}</b>${s.passages} passage${s.passages === 1 ? '' : 's'} · ${bytes(s.bytes)}</div>
      <div class="btns">${stale ? `<button class="small primary" data-sh="reindex" data-id="${esc(s.id)}">Re-index</button>` : ''}<button class="small${stale ? '' : ' primary'}" data-sh="open" data-id="${esc(s.id)}">Open</button></div></div>`;
  }).join('');
  const body = rows ? `<div class="card sh-list">${rows}</div>`
    : `<div class="card"><p><b>Nothing in the Stash yet.</b></p><p class="sub wrap">Make a stash, drop in documents (PDF, Word, Excel, text, Markdown, CSV, code), and tick it in a chat to have the answer drawn from them. Projects get their own stash for their tasks.</p></div>`;
  setHTML('sh-root', `<div class="sh-top"><h1>Stash</h1><span class="spacer"></span><button class="small primary" data-sh="new">＋ New stash</button></div>
    <p class="sh-lead">Resources your chats and tasks can search before they answer. They are kept on the controller, split into passages and indexed with a small embedding model on one of your nodes; the nodes keep none of it and nothing is sent to the cloud. Tick a stash in a chat to use it.</p>
    ${shBanner(d.embed_ready)}${body}`);
}

function shRenderDetail() {
  const { stash: s, resources: rs } = shDetail;
  if (shBuilt !== s.id) {
    shBuilt = s.id;
    $('sh-root')._h = null;
    $('sh-root').innerHTML = `<div class="sh-top"><a class="sh-back" href="#/stash">‹ Stash</a></div>
      <div id="sh-d-head"></div><div id="sh-d-banner"></div>
      <div class="sh-grid">
        <div class="card"><h3>Resources</h3>
          <div class="sh-drop" id="sh-drop"><b>Drop files here</b> or <a href="#" data-sh="choose">choose files</a><br><span class="sub wrap">PDF, Word, Excel, text, Markdown, CSV and code files · up to 50 MB each</span>
            <input type="file" id="sh-file" multiple hidden></div>
          <details class="sh-paste" id="sh-paste"><summary>Paste text instead</summary>
            <input id="sh-paste-name" placeholder="Name, for example Meeting notes" aria-label="Name for the pasted text" maxlength="80">
            <textarea id="sh-paste-text" placeholder="Paste or type the text" aria-label="Text to add"></textarea>
            <button class="small primary" data-sh="paste">Add text</button></details>
          <div id="sh-d-docs"></div>
          <details class="sh-adv" id="sh-adv"><summary>Advanced</summary><div id="sh-d-adv"></div></details>
        </div>
        <div class="card"><h3>Try a search</h3>
          <p class="sub wrap">See what a chat would find. Matches by meaning and by exact words together.</p>
          <form class="sh-q" id="sh-q"><input id="sh-q-in" placeholder="Ask something your documents answer" aria-label="Search this stash" autocomplete="off"><button class="small primary" type="submit">Search</button></form>
          <div id="sh-d-hits"></div></div>
      </div>`;
    shWire();
  }
  const resLine = `${s.resources} resource${s.resources === 1 ? '' : 's'} · ${s.passages} passage${s.passages === 1 ? '' : 's'}${s.model ? ' · indexed with ' + shModelLabel(s.model) : ''}`;
  setHTML('sh-d-head', `<div class="sh-top"><h1>${esc(s.name)}</h1>${shPill(s)}<span class="spacer"></span>
      ${s.status === 'needs_reindex' || s.failed ? '<button class="small primary" data-sh="reindex" data-id="' + esc(s.id) + '">Re-index</button>' : ''}
      <button class="small" data-sh="rename">Rename</button><button class="small danger" data-sh="delete">Delete</button></div>
    <p class="sh-lead">${esc(s.description ? s.description + '. ' : '')}${resLine}.</p>`);
  setHTML('sh-d-banner', shBanner(shDetail.embed_ready));
  const shown = shAll ? rs : rs.slice(0, 8);
  const rows = shown.map((r) => {
    let state;
    if (r.status === 'ready') state = `${r.passages} passage${r.passages === 1 ? '' : 's'}`;
    else if (r.status === 'failed') state = `<span title="${esc(r.error)}">${esc(r.error || 'failed')}</span>`;
    else state = r.status === 'indexing' && r.total ? `Indexing ${Math.round(100 * r.done / r.total)}%` : 'Waiting';
    return `<tr><td><a href="/api/stash/${esc(s.id)}/resources/${esc(r.id)}/file" download="${esc(r.name)}" title="Download the original"><b>${esc(r.name)}</b></a>${r.warning ? `<div class="sub wrap">${esc(r.warning)}</div>` : ''}</td>
      <td class="n">${bytes(r.bytes)}</td><td class="${r.status === 'failed' ? 'bad' : 'n'}">${state}</td><td class="n"><a href="#" data-sh="remove" data-id="${esc(r.id)}" data-name="${esc(r.name)}">Remove</a></td></tr>`;
  }).join('');
  const more = rs.length > shown.length ? `<tr><td class="n" colspan="4"><a href="#" data-sh="all">Show ${rs.length - shown.length} more</a></td></tr>` : '';
  setHTML('sh-d-docs', (shUploading ? `<p class="sub wrap" role="status">${esc(shUploading)}</p>` : '') + (rs.length ? `<table class="sh-docs">${rows}${more}</table>` : '<p class="sub">No resources yet.</p>'));
  const sizes = [400, 800, 1200, 1600], overs = [0, 60, 120, 200];
  if (!sizes.includes(s.chunk_chars)) sizes.push(s.chunk_chars);
  if (!overs.includes(s.overlap)) overs.push(s.overlap);
  const opt = (arr, cur, unit) => arr.sort((a, b) => a - b).map((n) => `<option value="${n}"${n === cur ? ' selected' : ''}>${n} characters${n === cur && unit ? ' (current)' : ''}</option>`).join('');
  const adv = $('sh-d-adv');
  if (!adv.contains(document.activeElement)) {
    setHTML(adv, `<div class="opts"><label>Passage size<br><select class="small" id="sh-size">${opt(sizes, s.chunk_chars, true)}</select></label><label>Overlap<br><select class="small" id="sh-over">${opt(overs, s.overlap, true)}</select></label></div>
      <p class="sub wrap">Changing these re-indexes the stash. The defaults (800 and 120) suit most documents.</p>`);
  }
  shRenderHits();
}

function shTerms(q) {
  return [...new Set(String(q).toLowerCase().split(/[^\p{L}\p{N}]+/u).filter((w) => w.length >= 3))].slice(0, 12);
}
function shMark(text, q) {
  const terms = shTerms(q);
  const safe = esc(text);
  if (!terms.length) return safe;
  const re = new RegExp('(' + terms.map((t) => esc(t).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|') + ')', 'giu');
  return safe.replace(re, '<mark>$1</mark>');
}
function shRenderHits() {
  if (!shHits) { setHTML('sh-d-hits', '<p class="sub">Results show up here.</p>'); return; }
  const list = shHits.hits.length ? shHits.hits.map((h) => `<button class="sh-hit" data-sh="passage" data-stash="${esc(h.stash)}" data-index="${h.index}"><div class="h"><b>${esc(h.resource)}</b> · ${h.p ? 'page ' + h.p : 'passage ' + h.n}<span class="sh-score" title="Relevance"><i class="${'w' + Math.round(100 * Math.min(1, Math.max(0.04, h.score)))}"></i></span></div>${shMark(h.t.length > 360 ? h.t.slice(0, 360) + '…' : h.t, shHits.q)}</button>`).join('')
    : '<p class="sub">Nothing matched. Try other words.</p>';
  setHTML('sh-d-hits', (shNote ? `<p class="sub wrap">${esc(shNote)}</p>` : '') + list);
}

async function shUpload(files) {
  const id = pageArg;
  let i = 0, failed = 0;
  for (const f of files) {
    i++;
    shUploading = `Uploading ${i} of ${files.length}: ${f.name}`;
    shRender();
    try {
      const r = await fetch(`/api/stash/${encodeURIComponent(id)}/resources?name=${encodeURIComponent(f.name)}`, { method: 'POST', credentials: 'same-origin', body: f });
      if (r.status === 401) { showLogin(); break; }
      if (!r.ok) { let b = null; try { b = await r.json(); } catch (e) {} failed++; toast(`${f.name}: ${(b && b.error) || r.statusText}`, 5000); }
    } catch (e) { failed++; toast(`${f.name}: ${e.message}`, 5000); }
  }
  shUploading = '';
  if (files.length - failed > 0) toast(`Added ${files.length - failed} resource${files.length - failed === 1 ? '' : 's'}. Indexing starts now.`);
  shAt = 0;
  await shLoad(true);
}

function shWire() {
  const drop = $('sh-drop'), input = $('sh-file');
  const hasFiles = (e) => [...((e.dataTransfer && e.dataTransfer.types) || [])].includes('Files');
  drop.addEventListener('dragover', (e) => { if (hasFiles(e)) { e.preventDefault(); drop.classList.add('on'); } });
  drop.addEventListener('dragleave', () => drop.classList.remove('on'));
  drop.addEventListener('drop', (e) => { drop.classList.remove('on'); if (hasFiles(e)) { e.preventDefault(); shUpload([...e.dataTransfer.files]); } });
  input.addEventListener('change', () => { const fs = [...input.files]; input.value = ''; if (fs.length) shUpload(fs); });
  $('sh-q').addEventListener('submit', async (e) => {
    e.preventDefault();
    const q = $('sh-q-in').value.trim();
    if (!q) return;
    setHTML('sh-d-hits', '<p class="sub" role="status">Searching…</p>');
    try {
      const r = await post(`/api/stash/${encodeURIComponent(pageArg)}/search`, { q, k: 8 });
      shHits = { q, hits: r.hits || [] }; shNote = r.note || '';
    } catch (err) { shHits = null; shNote = ''; setHTML('sh-d-hits', `<div class="error-msg">${esc(err.message)}</div>`); return; }
    shRenderHits();
  });
  const adv = $('sh-d-adv');
  adv.addEventListener('change', async (e) => {
    if (e.target.id !== 'sh-size' && e.target.id !== 'sh-over') return;
    const size = +$('sh-size').value, over = +$('sh-over').value;
    const s = shDetail.stash;
    if (size === s.chunk_chars && over === s.overlap) return;
    if (s.resources && !(await askConfirm(`Re-index ${s.resources} resource${s.resources === 1 ? '' : 's'}? The passages are cut again at ${size} characters with ${over} overlap, then embedded again. Search works again when it finishes.`, { ok: 'Re-index' }))) { shAt = 0; $('sh-d-adv')._h = null; shRender(); return; }
    try { await post('/api/stash/' + encodeURIComponent(s.id), { chunk_chars: size, overlap: over }); } catch (err) { toast(err.message, 5000); }
    $('sh-d-adv')._h = null; shAt = 0; shLoad(true);
  });
}

async function shOpenPassage(stash, index) {
  try {
    const p = await api(`/api/stash/${encodeURIComponent(stash)}/passages/${index}`);
    $('sh-pass-title').textContent = p.resource;
    $('sh-pass-sub').textContent = `${p.stash}${p.passage.p ? ' · page ' + p.passage.p : ''}`;
    const q = page === 'stash' && shHits ? shHits.q : '';
    $('sh-pass-body').innerHTML = (p.before ? `<span class="ctx">${esc(p.before)}</span>\n\n` : '') + `<b>${q ? shMark(p.passage.t, q) : esc(p.passage.t)}</b>` + (p.after ? `\n\n<span class="ctx">${esc(p.after)}</span>` : '');
    open('modal-passage');
  } catch (e) { toast(e.message, 4000); }
}

document.addEventListener('click', async (e) => {
  const el = e.target.closest('[data-sh]');
  if (!el) return;
  const act = el.dataset.sh;
  e.preventDefault();
  try {
    if (act === 'choose') $('sh-file').click();
    else if (act === 'open') location.hash = '#/stash/' + el.dataset.id;
    else if (act === 'all') { shAll = true; shRender(); }
    else if (act === 'passage') shOpenPassage(el.dataset.stash, el.dataset.index);
    else if (act === 'add-embed') {
      await post('/api/models', { catalog_id: 'nomic-embed-text-v1.5-q8', storage: 'direct' });
      toast('Added to the library. Nodes download it the first time it is used.'); shAt = 0; shLoad(true);
    } else if (act === 'new') {
      const name = await askText('Name for the new stash', '', { ok: 'Create' });
      if (!name || !name.trim()) return;
      const s = await post('/api/stash', { name: name.trim() });
      location.hash = '#/stash/' + s.id;
    } else if (act === 'rename') {
      const s = shDetail.stash;
      const name = await askText('Rename the stash', s.name);
      if (!name || !name.trim() || name.trim() === s.name) return;
      await post('/api/stash/' + encodeURIComponent(s.id), { name: name.trim() });
      shAt = 0; shLoad(true);
    } else if (act === 'delete') {
      const s = shDetail.stash;
      if (!(await askConfirm(`Delete the stash "${s.name}"? Its ${s.resources} resource${s.resources === 1 ? '' : 's'} and the index are removed from the controller. Chats that used it keep their answers.`, { ok: 'Delete', danger: true }))) return;
      await api('/api/stash/' + encodeURIComponent(s.id), { method: 'DELETE' });
      location.hash = '#/stash';
    } else if (act === 'remove') {
      if (!(await askConfirm(`Remove "${el.dataset.name}" from the stash? Its passages are deleted from the index.`, { ok: 'Remove', danger: true }))) return;
      await api(`/api/stash/${encodeURIComponent(pageArg)}/resources/${encodeURIComponent(el.dataset.id)}`, { method: 'DELETE' });
      shAt = 0; shLoad(true);
    } else if (act === 'reindex') {
      await post(`/api/stash/${encodeURIComponent(el.dataset.id)}/reindex`);
      toast('Re-indexing started.'); shAt = 0; shLoad(true);
    } else if (act === 'paste') {
      const name = $('sh-paste-name').value.trim() || 'Pasted text', text = $('sh-paste-text').value;
      if (!text.trim()) { toast('Paste some text first.'); return; }
      const f = new File([text], /\.(txt|md)$/i.test(name) ? name : name + '.txt', { type: 'text/plain' });
      $('sh-paste-text').value = ''; $('sh-paste-name').value = ''; $('sh-paste').open = false;
      await shUpload([f]);
    }
  } catch (err) { toast(err.message, 5000); }
});

// ---- Chats: choosing stashes to search, and showing what an answer drew on ----
let shNames = {}, shPickList = null;
const shChatIds = () => (typeof chatCur !== 'undefined' && chatCur ? chatCur.stashes : chatPendingStash) || [];
async function shChatSet(ids) {
  if (chatCur) {
    try { const r = await post(`/api/chats/${chatCur.id}/stash`, { ids }); chatCur.stashes = r.stashes || []; } catch (e) { toast(e.message); return; }
  } else chatPendingStash = ids.slice();
  chatButtons();
  shPickRender();
}
function shChatChips() {
  const ids = shChatIds();
  if (ids.length && ids.some((id) => !shNames[id]) && !shPickList) shNamesLoad();
  return ids.map((id) => `<button type="button" class="chat-pill on" data-sk-chip="stash" data-id="${esc(id)}" title="Stop searching this stash" aria-label="Searching stash ${esc(shNames[id] || '')}. Remove">▦ ${esc(shNames[id] || 'Stash')} ✕</button>`).join('');
}
async function shNamesLoad() {
  try {
    shPickList = await api('/api/stash?all=1');
    for (const s of shPickList.stashes) shNames[s.id] = s.name;
    shPickList = null;
    skillRender();
  } catch (e) { shPickList = null; }
}
async function shPickOpen() {
  let pop = $('sh-pop');
  if (pop) { pop.remove(); return; }
  pop = document.createElement('div');
  pop.id = 'sh-pop'; pop.className = 'sh-pop'; pop.setAttribute('role', 'dialog'); pop.setAttribute('aria-label', 'Stashes to search');
  document.querySelector('.plus-wrap').appendChild(pop);
  pop.innerHTML = '<p class="sub">Loading…</p>';
  try {
    shPickList = await api('/api/stash?all=1');
    for (const s of shPickList.stashes) shNames[s.id] = s.name;
  } catch (e) { pop.innerHTML = `<div class="error-msg">${esc(e.message)}</div>`; return; }
  shPickRender();
}
function shPickRender() {
  const pop = $('sh-pop');
  if (!pop || !shPickList) return;
  const on = new Set(shChatIds());
  const items = shPickList.stashes.map((s) => `<label class="sh-opt"><input type="checkbox" data-sh-pick="${esc(s.id)}"${on.has(s.id) ? ' checked' : ''}${s.status === 'empty' ? ' disabled' : ''}><span><b>${esc(s.name)}</b><span class="sub">${s.passages} passage${s.passages === 1 ? '' : 's'}${s.status === 'indexing' ? ' · indexing' : s.status === 'needs_reindex' ? ' · words only until re-indexed' : s.status === 'empty' ? ' · empty' : ''}</span></span></label>`).join('');
  const html = `<b>Search a stash before answering</b>${items || '<p class="sub wrap">No stashes yet.</p>'}<p class="sub wrap"><a href="#/stash">Manage the Stash</a></p>`;
  if (pop._h !== html) { pop.innerHTML = html; pop._h = html; }
}
document.addEventListener('change', (e) => {
  const cb = e.target.closest('[data-sh-pick]');
  if (!cb) return;
  const id = cb.dataset.shPick, ids = shChatIds().filter((x) => x !== id);
  if (cb.checked) ids.push(id);
  shChatSet(ids);
});
document.addEventListener('click', (e) => { const p = $('sh-pop'); if (p && !e.target.closest('#sh-pop') && !e.target.closest('[data-sk-menu="stash"]')) p.remove(); });
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') { const p = $('sh-pop'); if (p) p.remove(); } });

// The "Searched …" fold above an answer and the source chips below it.
function shSearched(m) {
  if (!m.searched) return '';
  return `<details class="sh-fold"><summary>${m.sources && m.sources.length ? `Searched the stash · ${m.sources.length} passage${m.sources.length === 1 ? '' : 's'} used` : 'Searched the stash · nothing close enough'}</summary><div>${esc(m.searched)}</div></details>`;
}
function shSources(m) {
  if (!m.sources || !m.sources.length) return '';
  return `<div class="sh-src"><span>Sources</span>${m.sources.map((s) => `<button type="button" data-sh="passage" data-stash="${esc(s.stash)}" data-index="${s.index}" title="${esc(s.snippet)}">[${s.n}] ${esc(s.resource)}${s.page ? ' · p. ' + s.page : ''}</button>`).join('')}</div>`;
}
