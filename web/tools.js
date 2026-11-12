'use strict';
// Image attachments, Check cluster, node logs, diagnostics, storage. Plain script, no modules: shares globals with app.js and loads after it (see index.html).


// ---------- image attachments (row 19) ----------
// Images become data URIs in memory and go with the message; the controller
// keeps them with the task. Big photos are scaled down first (Claude takes 5 MB per image).
const IMG_MAX = 5, IMG_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/gif'];
function readImage(file) {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onerror = () => reject(new Error(file.name + ': could not read'));
    fr.onload = () => {
      if (file.size <= 4 * 1024 * 1024 || file.type === 'image/gif') return resolve({ name: file.name || 'image', data: fr.result });
      const img = new Image();
      img.onerror = () => resolve({ name: file.name, data: fr.result });
      img.onload = () => {
        const k = Math.min(1, 2048 / Math.max(img.width, img.height)), c = document.createElement('canvas');
        c.width = Math.round(img.width * k); c.height = Math.round(img.height * k);
        c.getContext('2d').drawImage(img, 0, 0, c.width, c.height);
        resolve({ name: (file.name || 'image').replace(/\.\w+$/, '') + '.jpg', data: c.toDataURL('image/jpeg', 0.85) });
      };
      img.src = fr.result;
    };
    fr.readAsDataURL(file);
  });
}
// visionProblem says why a planner cannot read images, or '' if it can (or we cannot tell here; the controller checks again).
function visionProblem(planner) {
  if (!planner || !planner.startsWith('node:')) return '';
  const n = state && state.nodes.find((x) => 'node:' + x.id === planner), hb = n && n.heartbeat;
  const name = hb && hb.loaded_model;
  if (!name) return 'The local planner has no model loaded, so it cannot read images.';
  const m = models && models.models.find((x) => x.name === name);
  if (m && m.vision) return '';
  if (!models) return '';
  return `${name} cannot read images. Load a Vision model (Qwen3.6 or Gemma 4 from the Models page) on that node, or plan with Claude or OpenRouter.`;
}
function imageAttach(pre, getPlanner, warnEl) {
  let items = [];
  const strip = $(pre + '-imgs'), file = $(pre + '-file');
  function refresh() {
    setHTML(strip, items.map((it, i) => `<span class="img-chip"><img src="${it.data}" alt=""><button type="button" data-i="${i}" aria-label="Remove image">×</button></span>`).join(''));
    const w = items.length ? visionProblem(getPlanner()) : '';
    warnEl.textContent = w; warnEl.classList.toggle('hidden', !w);
  }
  async function add(files) {
    for (const f of files) {
      if (!IMG_TYPES.includes(f.type)) { toast(`${f.name || 'That file'} is not a PNG, JPEG, WebP or GIF image`, 4000); continue; }
      if (items.length >= IMG_MAX) { toast(`At most ${IMG_MAX} images per message`, 4000); break; }
      try { items.push(await readImage(f)); } catch (e) { toast(e.message, 4000); }
    }
    refresh();
  }
  $(pre + '-attach').onclick = () => file.click();
  file.onchange = () => { add([...file.files]); file.value = ''; };
  strip.onclick = (e) => { const b = e.target.closest('button[data-i]'); if (b) { items.splice(+b.dataset.i, 1); refresh(); } };
  const box = pre === 'nt' ? $('nt-prompt') : $('tm-text');
  box.addEventListener('paste', (e) => { const fs = [...(e.clipboardData ? e.clipboardData.files : [])].filter((f) => f.type.startsWith('image/')); if (fs.length) { e.preventDefault(); add(fs); } });
  const comp = box.closest('.composer');
  comp.addEventListener('dragover', (e) => { if ([...(e.dataTransfer.types || [])].includes('Files')) e.preventDefault(); });
  comp.addEventListener('drop', (e) => { const fs = [...e.dataTransfer.files].filter((f) => f.type.startsWith('image/')); if (fs.length) { e.preventDefault(); add(fs); } });
  return { payload: () => items.map((i) => ({ name: i.name, data: i.data })), clear: () => { items = []; refresh(); }, refresh };
}
const ntImg = imageAttach('nt', () => $('nt-planner').value, $('nt-img-warn'));
const tmImg = imageAttach('tm', () => taskDetail && taskDetail.planner, $('tm-img-warn'));
const ntDraft = Drafts.bind($('nt-prompt'), $('nt-draft')), tmDraft = Drafts.bind($('tm-text'), $('tm-draft'));

// ---- Check cluster (clustercheck.go) ----
let checkTimer = null;
const CHECK_COLS = [['model', 'Model call'], ['command', 'Command'], ['port', 'File port'], ['disk', 'Disk'], ['clock', 'Clock'], ['version', 'Version']];
const CHECK_ICON = { ok: '✓', warn: '!', fail: '✗', skip: '–', pending: '·', running: '…' };
function renderCheck(ck) {
  const rows = ck.rows || [];
  if (!rows.length) { setHTML('check-body', '<p class="muted">No nodes yet.</p>'); setHTML('check-notes', ''); return; }
  const cell = (st) => {
    if (!st) return '<td></td>';
    const cls = { ok: 'done', warn: 'warn', fail: 'failed', skip: 'pending', pending: 'pending', running: 'running' }[st.state] || 'pending';
    return `<td class="ck-cell st-${cls}"><span class="ck-icon" aria-hidden="true">${CHECK_ICON[st.state] || ''}</span> ${esc(st.text || (st.state === 'running' ? 'checking' : st.state === 'pending' ? 'waiting' : ''))}<span class="sr-only"> (${esc(st.state)})</span></td>`;
  };
  setHTML('check-body', `<div class="ck-wrap"><table class="ck-table"><thead><tr><th>Node</th>${CHECK_COLS.map(([, l]) => `<th>${l}</th>`).join('')}</tr></thead><tbody>${rows.map((r) =>
    r.online ? `<tr><td><b>${esc(r.name)}</b></td>${CHECK_COLS.map(([k]) => cell(r.steps[k])).join('')}</tr>`
      : `<tr class="muted"><td><b>${esc(r.name)}</b></td><td colspan="${CHECK_COLS.length}">${esc(r.offline || 'offline')}: not checked</td></tr>`).join('')}</tbody></table></div>`);
  const notes = rows.flatMap((r) => r.notes || []);
  setHTML('check-notes', notes.length ? notes.map((n) => `<div class="note ${n.level === 'bad' ? 'bad' : 'warn'} u6">${esc(n.text)}${n.fix ? `<div class="codebox u4"><span>${esc(n.fix)}</span><button data-copytext="${esc(n.fix)}">Copy</button></div>` : ''}</div>`).join('')
    : (ck.running ? '' : '<div class="note good u6">Everything checked is working.</div>'));
  $('check-when').textContent = ck.running ? 'Checking…' : ck.ended ? `Checked ${ago(ck.ended)}` : '';
  $('check-again').disabled = !!ck.running;
}
async function pollCheck() {
  clearTimeout(checkTimer);
  if ($('modal-check').classList.contains('hidden')) return;
  try { const ck = await api('/api/cluster/check'); renderCheck(ck); if (ck.running) checkTimer = setTimeout(pollCheck, 1000); } catch (e) { toast(e.message); }
}
async function startCheck() {
  try { renderCheck(await post('/api/cluster/check')); } catch (e) { toast(e.message); }
  pollCheck();
}
$('btn-check').onclick = async () => {
  open('modal-check');
  let ck = null;
  try { ck = await api('/api/cluster/check'); } catch (e) {}
  // Show the last run if it is recent or still going; otherwise start a new one.
  if (ck && (ck.running || (ck.ended && Date.now() - new Date(ck.ended) < 5 * 60e3))) { renderCheck(ck); pollCheck(); } else startCheck();
};
$('check-again').onclick = startCheck;
$('modal-check').addEventListener('click', (e) => { const b = e.target.closest('[data-copytext]'); if (b) { copyText(b.dataset.copytext); toast('Copied'); } });

// ---- Node logs (node_ask.go) ----
let logsTimer = null, logsNodeId = null;
function openLogs(id) {
  logsNodeId = id;
  $('logs-node').textContent = nodeName(nodeById(id));
  $('logs-out').textContent = 'Loading…';
  open('modal-logs'); fetchLogs(true);
}
async function fetchLogs(scrollEnd) {
  clearTimeout(logsTimer);
  const id = logsNodeId;
  if (!id) return;
  const out = $('logs-out'), atBottom = scrollEnd || out.scrollTop + out.clientHeight >= out.scrollHeight - 20;
  try {
    const r = await api(`/api/nodes/${id}/logs?source=${$('logs-src').value}&lines=${$('logs-lines').value}`);
    if (id !== logsNodeId) return;
    $('logs-note').textContent = r.note || ''; $('logs-note').classList.toggle('hidden', !r.note);
    if (out.textContent !== (r.text || '')) { out.textContent = r.text || ''; if (atBottom) out.scrollTop = out.scrollHeight; }
  } catch (e) {
    if (id !== logsNodeId) return;
    $('logs-note').textContent = e.message; $('logs-note').classList.remove('hidden');
  }
  if ($('logs-follow').checked && logsNodeId === id) logsTimer = setTimeout(() => fetchLogs(false), 2000);
}
$('logs-src').onchange = () => fetchLogs(true);
$('logs-lines').onchange = () => fetchLogs(true);
$('logs-follow').onchange = () => fetchLogs(false);
$('logs-copy').onclick = () => { copyText($('logs-out').textContent); toast('Copied'); };

// ---- Diagnostics (diagnostics.go) ----
$('dg-download').onclick = async () => {
  const b = $('dg-download'); b.disabled = true; toast('Collecting diagnostics…', 8000);
  try {
    const r = await fetch('/api/diagnostics', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ node_logs: $('dg-node').checked, tray_logs: $('dg-tray').checked, prompts: $('dg-prompts').checked }) });
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
    const a = document.createElement('a'); a.href = URL.createObjectURL(await r.blob());
    a.download = (r.headers.get('Content-Disposition') || '').split('filename=')[1]?.replace(/"/g, '') || 'outpost-diagnostics.zip'; a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 10e3);
    toast('Diagnostics downloaded');
  } catch (e) { toast(e.message); } finally { b.disabled = false; }
};

// ---- Storage (storage.go) ----
const ST_PARTS = [['tasks', 'Tasks'], ['models', 'Models'], ['releases', 'Releases and updates'], ['runtime', 'llama.cpp builds'], ['projects', 'Projects'], ['other', 'Settings, logs and keys']];
// Hover, keyboard focus or tap on a bar segment shows a tooltip (native title is slow and absent on touch).
(function () {
  const bar = $('st-bar'), host = bar.parentElement; host.classList.add('st-host');
  const tip = document.createElement('div'); tip.className = 'st-tip hidden'; host.appendChild(tip);
  const show = (e) => {
    const sp = e.target.closest && e.target.closest('#st-bar > span'); if (!sp) return;
    tip.textContent = sp.dataset.tip; tip.classList.remove('hidden');
    const h = host.getBoundingClientRect(), r = sp.getBoundingClientRect(), w = tip.offsetWidth;
    tip.style.left = Math.max(0, Math.min(h.width - w, r.left - h.left + r.width / 2 - w / 2)) + 'px';
    tip.style.top = (bar.offsetTop - tip.offsetHeight - 6) + 'px';
  };
  const hide = () => tip.classList.add('hidden');
  bar.addEventListener('mouseover', show); bar.addEventListener('focusin', show); bar.addEventListener('click', show);
  bar.addEventListener('mouseleave', hide); bar.addEventListener('focusout', hide);
  document.addEventListener('click', (e) => { if (!bar.contains(e.target)) hide(); });
})();
function renderStorage(s) {
  const used = s.total ? s.total - s.free : 0, ours = Object.values(s.parts || {}).reduce((a, b) => a + b, 0);
  const others = Math.max(0, used - ours);
  const segs = ST_PARTS.map(([k, l], i) => [l, (s.parts || {})[k] || 0, `st-c${i}`]).concat([['Other files on this disk', others, 'st-cx'], ['Free', s.free, 'st-free']]);
  const pct = (b) => s.total ? (100 * b / s.total) : 0;
  setHTML('st-bar', segs.filter(([, b]) => b > 0).map(([l, b, c]) => `<span class="${c} w${Math.max(1, Math.round(pct(b)))}" tabindex="0" data-tip="${esc(l)}: ${bytes(b)} (${pct(b) < 1 ? '<1' : pct(b).toFixed(0)}% of disk)"></span>`).join(''));
  setHTML('st-legend', `<span class="${s.low ? 'st-failed' : ''}"><b>${bytes(s.free)}</b> free of ${bytes(s.total)} (${pct(s.free).toFixed(0)}%)</span>` +
    segs.slice(0, -1).filter(([, b]) => b > 0).map(([l, b, c]) => `<span><i class="st-dot ${c}"></i>${esc(l)} ${bytes(b)}</span>`).join(''));
  $('st-on').checked = s.cleanup_days > 0; $('st-days').value = s.cleanup_days || 30; $('st-builds').checked = !!s.cleanup_builds;
  const parts = [];
  if (s.cleanup_days > 0) parts.push(s.reclaimable ? `${bytes(s.reclaimable)} can be freed now.` : 'Nothing to clean up right now.');
  if (s.last_cleanup) parts.push(`Last clean-up ${ago(s.last_cleanup)}: freed ${bytes(s.last_freed)} from ${s.last_tasks} task(s).`);
  $('st-status').textContent = parts.join(' ');
  $('st-run').disabled = !(s.cleanup_days > 0);
}
async function loadStorage() { try { renderStorage(await api('/api/storage')); } catch (e) {} }
async function saveStorage(run) {
  const days = $('st-on').checked ? parseInt($('st-days').value, 10) || 0 : 0;
  if (run && !await askConfirm(`Remove old round copies${$('st-builds').checked ? ', build outputs' : ''} and screenshots of tasks finished more than ${days} days ago?`)) return;
  try { const s = await post('/api/storage', { cleanup_days: days, cleanup_builds: $('st-builds').checked, run }); renderStorage(s); toast(run ? `Freed ${bytes(s.last_freed)}` : 'Saved'); }
  catch (e) { toast(e.message); }
}
$('st-save').onclick = () => saveStorage(false);
$('st-run').onclick = () => saveStorage(true);
