'use strict';
// The file browser's viewer and the project's own Artifacts (files and folders the owner
// uploads; project_artifacts.go keeps them on the controller). Plain script, no modules:
// loads after thread.js, whose viewFile and renderFiles use fbShow and fbTree here.
// Each file opens in a viewer that fits its type: pictures, sound, video, PDF and office
// documents (as text, PDF also in a new tab), CSV as a table, STL as a 3D model, pages,
// diagrams and Markdown as text with Preview, anything else as text, binary files by name.

// Which listing the open browser shows: a task's files and build outputs, or a project's uploads.
let fbCtx = { kind: 'task' };
let fbPath = null, fbText = '', fb3d = null;

const FB_TYPES = [
  ['img', /\.(png|jpe?g|gif|webp|bmp|avif|ico)$/i],
  ['audio', /\.(mp3|wav|ogg|oga|m4a|flac|opus)$/i],
  ['video', /\.(mp4|m4v|webm|mov|ogv)$/i],
  ['pdf', /\.pdf$/i],
  ['doc', /\.(docx|xlsx|xlsm|odt)$/i],
  ['csv', /\.(csv|tsv)$/i],
  ['stl', /\.stl$/i],
];
const fbType = (path) => (FB_TYPES.find(([, re]) => re.test(path)) || ['text'])[0];

// The tree: folders as headings, files as links. opts.src is 'file' (a project file, with
// a tick box for "Zip ticked files"), 'art' (a build output) or 'up' (an upload); build
// outputs and uploads can be deleted, a file or a whole folder at a time.
function fbTree(list, opts) {
  const { src, depth0 = 0, del = false } = opts;
  const seen = new Set(), count = {};
  list.forEach((f) => { const parts = f.path.split('/'); for (let i = 1; i < parts.length; i++) { const k = parts.slice(0, i).join('/'); count[k] = (count[k] || 0) + 1; } });
  const act = src === 'up' ? 'fb-up-view' : 'view-file';
  const delBtn = (p, dir) => del ? `<button class="fb-x" data-act="fb-del" data-path="${esc(p)}" data-src="${src}"${dir ? ` data-dir="1" data-n="${count[p] || 0}"` : ''} title="Delete${dir ? ' this folder and everything in it' : ''}" aria-label="Delete ${esc(p)}">✕</button>` : '';
  return list.map((f) => {
    const parts = f.path.split('/'), out = [];
    parts.slice(0, -1).forEach((d, i) => {
      const k = parts.slice(0, i + 1).join('/');
      if (!seen.has(k)) { seen.add(k); out.push(`<div class="fb-dir fb-row pl${Math.min(i + depth0, 20)}"><span>${esc(d)}/</span>${delBtn(k, true)}</div>`); }
    });
    const tick = src === 'file' ? `<input type="checkbox" class="fb-tick" value="${esc(f.path)}" aria-label="Tick ${esc(f.path)}">` : '';
    out.push(`<div class="fb-row pl${Math.min(parts.length - 1 + depth0, 20)}">${tick}<a href="#" data-act="${act}" data-path="${esc(f.path)}"${src === 'art' ? ' data-art="1"' : ''}>${esc(parts[parts.length - 1])} <span class="muted">(${bytes(f.bytes)})</span></a>${delBtn(f.path, false)}</div>`);
    return out.join('');
  }).join('');
}

// A tiny CSV reader (quotes, doubled quotes, CR LF) for the table view; stops at max rows.
function fbParseCSV(text, sep, max) {
  const rows = []; let row = [], cell = '', q = false;
  for (let i = 0; i < text.length && rows.length < max; i++) {
    const ch = text[i];
    if (q) { if (ch === '"') { if (text[i + 1] === '"') { cell += '"'; i++; } else q = false; } else cell += ch; continue; }
    if (ch === '"' && cell === '') q = true;
    else if (ch === sep) { row.push(cell); cell = ''; }
    else if (ch === '\n') { row.push(cell); rows.push(row); row = []; cell = ''; }
    else if (ch !== '\r') cell += ch;
  }
  if ((cell !== '' || row.length) && rows.length < max) { row.push(cell); rows.push(row); }
  return rows;
}

function fbMedia(html) {
  if (fb3d) { fb3d.dispose(); fb3d = null; }
  const m = $('fb-media');
  setHTML(m, html || '');
  m.classList.toggle('hidden', !html);
  $('fb-body').classList.toggle('hidden', !!html);
}

// fbShow opens one file in the viewer pane. o.base is the file's URL without a mode
// (?view=1, ?text=1 are added here), o.dl its download link, o.edit whether it can be
// hand-edited (project files of a finished task), o.del what deleting it sends to.
async function fbShow(path, o) {
  fbPath = path; fbText = '';
  const type = fbType(path), view = o.base + '&view=1';
  $('fb-title').textContent = o.title || path;
  $('fb-body').textContent = 'Loading…';
  fbMedia('');
  $('fb-dl').classList.remove('hidden'); $('fb-dl').href = o.dl;
  $('fb-edit').classList.add('hidden'); $('fb-preview').classList.add('hidden');
  $('fb-open').classList.toggle('hidden', type !== 'pdf'); $('fb-open').href = view;
  const del = $('fb-del');
  del.classList.toggle('hidden', !o.del);
  if (o.del) { del.dataset.path = path; del.dataset.src = o.del; delete del.dataset.dir; }
  const alt = esc(path.split('/').pop());
  if (type === 'img') { fbMedia(`<img class="fb-img" src="${esc(view)}" alt="${alt}">`); return; }
  if (type === 'audio') { fbMedia(`<audio controls preload="metadata" src="${esc(view)}"></audio>`); return; }
  if (type === 'video') { fbMedia(`<video controls preload="metadata" src="${esc(view)}"></video>`); return; }
  try {
    if (type === 'stl') {
      const r = await fetch(view, { credentials: 'same-origin' });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const buf = await r.arrayBuffer();
      if (fbPath !== path) return;
      fbMedia('<canvas class="fb-3d" id="fb-3d" aria-label="3D model; drag to turn it"></canvas>');
      fb3d = typeof Viewer3D === 'function' ? Viewer3D($('fb-3d')) : null;
      if (!fb3d) { fbMedia('<p class="muted">This browser cannot show 3D (no WebGL). Use Download file.</p>'); return; }
      fb3d.load(buf); return;
    }
    const r = await fetch(type === 'pdf' || type === 'doc' ? o.base + '&text=1' : view, { credentials: 'same-origin' });
    const text = await r.text();
    if (fbPath !== path) return; // another file was picked meanwhile
    if (!r.ok) { let m = text; try { m = JSON.parse(text).error || text; } catch (e) { /* plain text */ } $('fb-body').textContent = m; return; }
    if (type === 'pdf' || type === 'doc') { $('fb-body').textContent = text || '(no text in this document)'; return; }
    if (text.includes('\u0000')) { setHTML('fb-body', '<span class="muted">A binary file, so there is nothing to read here. Use Download file to save it.</span>'); return; }
    fbText = text;
    if (type === 'csv') {
      const rows = fbParseCSV(text, /\.tsv$/i.test(path) ? '\t' : ',', 1001);
      const more = rows.length > 1000 ? '<p class="hint">Showing the first 1000 rows. Download the file for all of them.</p>' : '';
      const [h, ...body] = rows.slice(0, 1000);
      fbMedia(`<div class="fb-table"><table><thead><tr>${(h || []).map((c) => `<th>${esc(c)}</th>`).join('')}</tr></thead><tbody>${body.map((r2) => `<tr>${r2.map((c) => `<td>${esc(c)}</td>`).join('')}</tr>`).join('')}</tbody></table></div>${more}`);
      return;
    }
    setHTML('fb-body', numbered(text, path));
    $('fb-preview').classList.toggle('hidden', !pvKindOf(path));
    $('fb-edit').classList.toggle('hidden', !(o.edit && text.length < 1e6));
  } catch (e) { $('fb-body').textContent = e.message; }
}

// ---------- the project's uploaded artifacts ----------
function fbModeTask(on) {
  ['files-dl', 'files-zip'].forEach((id) => $(id).classList.toggle('hidden', !on));
  ['fb-up-f', 'fb-up-d'].forEach((id) => $(id).classList.toggle('hidden', on));
  $('files-title').textContent = on ? 'Files' : 'Artifacts';
}
async function openProjArts(pid, path, isDir) {
  fbCtx = { kind: 'proj', id: pid };
  fbModeTask(false);
  fbEditing(false);
  open('modal-files');
  fbPath = null; fbMedia(''); $('fb-title').textContent = ''; $('fb-body').textContent = 'Pick a file, or upload files or a folder.';
  ['fb-dl', 'fb-edit', 'fb-preview', 'fb-open', 'fb-del'].forEach((id) => $(id).classList.add('hidden'));
  await renderProjArts();
  if (path && !isDir) fbShowUpload(path);
}
async function renderProjArts() {
  const pid = fbCtx.id;
  setHTML('tv-files', '<p class="hint">Loading…</p>');
  try {
    const l = await api(`/api/projects/${encodeURIComponent(pid)}/artifacts`);
    if (fbCtx.kind !== 'proj' || fbCtx.id !== pid) return;
    const n = l.files.length;
    setHTML('files-hint', n ? `${n} file${n === 1 ? '' : 's'} uploaded to this project (${bytes(l.bytes)}), kept on the controller. Click one to open it.${l.cut ? ' Only the first ' + n + ' are listed.' : ''}` : 'Nothing uploaded yet. Upload files or a whole folder; they stay on the controller and open here.');
    setHTML('tv-files', n ? fbTree(l.files, { src: 'up', del: true }) : '<p class="hint">No files yet.</p>');
  } catch (e) { setHTML('tv-files', `<p class="hint">${esc(e.message)}</p>`); }
}
function fbShowUpload(path) {
  const base = `/api/projects/${encodeURIComponent(fbCtx.id)}/artifacts/file?path=${encodeURIComponent(path)}`;
  fbShow(path, { base, dl: base + '&download=1', del: 'up' });
}
// Upload: each file goes with its folder path (webkitRelativePath) so a folder keeps its shape.
async function fbUpload(files) {
  if (!files.length || fbCtx.kind !== 'proj') return;
  const fd = new FormData();
  let total = 0;
  for (const f of files) { fd.append('files', f, f.webkitRelativePath || f.name); total += f.size; }
  toast(`Uploading ${files.length} file${files.length === 1 ? '' : 's'} (${bytes(total)})…`, 60000);
  try {
    const r = await fetch(`/api/projects/${encodeURIComponent(fbCtx.id)}/artifacts`, { method: 'POST', body: fd, credentials: 'same-origin' });
    const b = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(b.error || r.statusText);
    toast(`Uploaded ${b.saved.length} file${b.saved.length === 1 ? '' : 's'}`);
  } catch (e) { toast('Upload failed: ' + e.message, 5000); }
  await renderProjArts();
  if (typeof pjFetch === 'function' && page === 'project') { await pjFetch(true); renderProjectPage(); }
}
$('fb-up-f').onclick = () => $('fb-up-files').click();
$('fb-up-d').onclick = () => $('fb-up-dir').click();
['fb-up-files', 'fb-up-dir'].forEach((id) => { $(id).onchange = (e) => { const fl = [...e.target.files]; e.target.value = ''; fbUpload(fl); }; });

// Delete a build output or an upload: one file, or a folder with everything in it.
async function fbDelete(el) {
  const p = el.dataset.path, dir = !!el.dataset.dir, n = +el.dataset.n || 0;
  const what = dir ? `the folder ${p}/ and the ${n} file${n === 1 ? '' : 's'} in it` : p;
  if (!await askConfirm(`Delete ${what}? This cannot be undone.`)) return;
  const url = el.dataset.src === 'up' ? `/api/projects/${encodeURIComponent(fbCtx.id)}/artifacts?path=${encodeURIComponent(p)}` : `/api/tasks/${curTask()}/artifact?path=${encodeURIComponent(p)}`;
  try { await api(url, { method: 'DELETE' }); } catch (e) { toast(e.message, 4000); return; }
  toast('Deleted');
  if (fbPath && (fbPath === p || fbPath.startsWith(p + '/'))) { fbPath = null; fbMedia(''); $('fb-title').textContent = ''; $('fb-body').textContent = 'Pick a file.'; ['fb-dl', 'fb-edit', 'fb-preview', 'fb-open', 'fb-del'].forEach((id) => $(id).classList.add('hidden')); }
  if (el.dataset.src === 'up') await renderProjArts();
  else if (taskDetail) { taskDetail.artifacts = (taskDetail.artifacts || []).filter((a) => a.path !== p && !a.path.startsWith(p + '/')); renderFiles(taskDetail); pollTask(); }
  if (typeof pjFetch === 'function' && page === 'project') { await pjFetch(true); renderProjectPage(); }
}
