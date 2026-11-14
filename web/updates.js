'use strict';
// Updates page. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- updates ----------
const UPD_BUSY = ['sent', 'downloading', 'installing', 'restarting'];
let ctlUpdating = false;
function renderUpdates() {
  renderGitHub();
  const rows = [];
  const cu = state.controller_update_to;
  rows.push(`<tr><td><span class="dot on"></span></td><td><b>Controller</b></td><td>${esc(state.platform)}</td><td>v${esc(state.version)}</td>
    <td>${cu ? 'v' + esc(cu) : '<span class="muted">up to date</span>'}</td>
    <td>${ctlUpdating ? `<div class="prog"><span>installing and restarting…</span>${bar(100, 'good')}</div>` : ''}</td>
    <td class="r">${cu && !ctlUpdating ? `<button class="small primary" data-act="ctl-update">Update</button>` : ''}
      ${state.controller_rollback_to && !ctlUpdating ? `<button class="small" data-act="ctl-rollback" title="Restart the controller on the previous version">Roll back to v${esc(state.controller_rollback_to)}</button>` : ''}</td></tr>`);
  for (const n of state.nodes) {
    const h = n.hello || {}, u = n.update;
    let prog = '';
    if (u && UPD_BUSY.includes(u.state) && (Date.now() - new Date(u.updated)) < 600000) {
      const pct = u.state === 'downloading' && u.bytes_total ? 100 * u.bytes_done / u.bytes_total : u.state === 'sent' ? 2 : u.state === 'downloading' ? 5 : u.state === 'installing' ? 85 : 95;
      const what = { sent: 'requested', downloading: u.bytes_total ? `downloading ${bytes(u.bytes_done)} of ${bytes(u.bytes_total)}` : 'downloading', installing: 'verifying and installing', restarting: 'restarting' }[u.state];
      prog = `<div class="prog"><span>${esc(what)}</span>${bar(pct, 'good')}</div>`;
    } else if (u && u.state === 'failed') prog = `<span class="error-msg u2">${esc(u.error)}</span>`;
    else if (u && u.state === 'done' && (Date.now() - new Date(u.updated)) < 600000) prog = `<span class="st-done">✓ updated</span>`;
    const ro0 = state.rollout;
    if (ro0 && !ro0.ended && ro0.state === 'checking' && ro0.canary === n.id && u && u.state === 'done') prog = `<span class="st-done">updated, checking…</span>`;
    const busy = u && UPD_BUSY.includes(u.state);
    const canUpdate = n.update_to && n.online && !busy;
    rows.push(`<tr><td><span class="dot ${n.online ? 'on' : ''}" title="${n.online ? 'service online' : 'offline'}"></span></td><td><b>${esc(nodeName(n))}</b><div class="sub u2">${n.online ? 'online' : n.last_seen ? 'offline · seen ' + ago(n.last_seen) : 'never connected'}</div></td>
      <td>${esc(h.os ? h.os + '-' + h.arch : '?')}</td><td>${h.agent_version ? 'v' + esc(h.agent_version) : '?'}</td>
      <td>${n.update_to ? 'v' + esc(n.update_to) : '<span class="muted">' + (!h.os ? 'waiting for first connection' : state.releases.some((r) => r.platform === (h.os + '-' + h.arch)) ? 'up to date' : 'no release uploaded') + '</span>'}</td>
      <td>${prog}</td>
      <td class="r">${canUpdate ? `<button class="small primary" data-act="node-update" data-id="${n.id}" ${n.running_jobs ? 'disabled title="Waits until running jobs finish"' : ''}>Update</button>` : ''}
        ${h.rollback_to && n.online && !busy ? `<button class="small" data-act="node-rollback" data-id="${n.id}" title="Restart this node on the previous version">Roll back to v${esc(h.rollback_to)}</button>` : ''}</td></tr>`);
  }
  setHTML('upd-table', `<table><thead><tr><th class="u26"></th><th>Machine</th><th>Platform</th><th>Running</th><th>Available</th><th>Progress</th><th></th></tr></thead><tbody>${rows.join('')}</tbody></table>`);
  const outdated = state.nodes.filter((n) => n.online && n.update_to && !(n.update && UPD_BUSY.includes(n.update.state))).length;
  const ro = state.rollout, roRunning = ro && !ro.ended, roRecent = ro && ro.ended && Date.now() - new Date(ro.ended) < 24 * 3600e3;
  const rn = $('rollout-note');
  rn.classList.toggle('hidden', !(roRunning || roRecent));
  if (roRunning || roRecent) {
    rn.className = `note ${ro.state === 'failed' ? 'bad' : ro.state === 'done' ? 'good' : ''}`;
    rn.textContent = roRunning ? `${ro.text}${ro.rest ? `; ${ro.rest} more wait until it passes a quick check` : ''}…` : `${ro.text} (${ago(ro.ended)})`;
  }
  const b = $('btn-upd-all');
  b.disabled = !outdated || roRunning; b.className = outdated && !roRunning ? 'primary' : '';
  b.textContent = roRunning ? 'Updating…' : outdated ? (outdated === 1 ? 'Update 1 node' : `Update ${outdated} nodes, one first`) : 'All nodes up to date';
  b.title = outdated > 1 ? 'Updates one node, checks it, then the rest. If the first node fails its check it is rolled back and the rest are left alone.' : '';
  const rel = state.releases || [];
  setHTML('releases', rel.length ? `<div class="table-wrap"><table><thead><tr><th>Platform</th><th>Version</th><th>Size</th><th>SHA-256</th><th>Uploaded</th><th></th></tr></thead><tbody>${
    rel.map((r) => `<tr><td>${esc(r.platform)}</td><td>v${esc(r.version)}</td><td>${bytes(r.bytes)}</td><td class="mono" title="${esc(r.sha256)}">${esc(r.sha256.slice(0, 12))}…</td><td>${ago(r.uploaded)}</td>
      <td class="r"><button class="small danger" data-act="del-release" data-platform="${esc(r.platform)}">Delete</button></td></tr>`).join('')}</tbody></table></div>` : '');
}
// everyText turns the controller's interval ("6h", "1d", "15m") into words.
function everyText(e) {
  const m = /^(\d+)([dhm])$/.exec(e || '6h');
  if (!m) return e;
  const n = +m[1], u = { d: 'day', h: 'hour', m: 'minute' }[m[2]];
  if (m[2] === 'd' && n === 7) return 'week';
  return n === 1 ? u : `${n} ${u}s`;
}
function renderGitHub() {
  const g = state.github_updates || {};
  let t;
  if (g.busy) t = g.busy.startsWith('checking') ? `Checking ${g.host || 'GitHub'}…` : 'Downloading ' + g.busy.replace('downloading ', '') + '…';
  else {
    const mode = !g.enabled ? 'Automatic checks are off (Settings)' : g.auto_install ? `Checks every ${everyText(g.every)} and installs automatically` : `Checks every ${everyText(g.every)}; you click Update`;
    t = `${mode}. ` + (g.repo ? `Repository: ${g.repo} (${g.host}). ` : '') + (g.checked && g.checked > '2000' ? `Last checked ${ago(g.checked)}${g.latest ? ', latest is v' + g.latest : ''}.` : 'Not checked yet.');
    if (g.hold) t += ` Auto-install skips v${g.hold} and older after a rollback.`;
    t += g.catalog ? ` Suggested models: list from v${g.catalog}.` : ' Suggested models: the list built into this version.';
  }
  $('gh-info').textContent = t;
  if (g.error) { const e = document.createElement('span'); e.className = 'error-msg'; e.style.display = 'block'; e.textContent = g.error; $('gh-info').appendChild(e); }
  $('btn-gh-check').disabled = $('btn-gh-install').disabled = !!g.busy;
  const sel = $('gh-channel');
  if (document.activeElement !== sel) ensureChannelOption(g.branch || '');
  $('gh-channel-note').textContent = g.branch ? 'Branch builds are untested code: nothing installs by itself, and Roll back is kept.' : '';
}
// The dropdown always shows the current choice; the other branches are read from the repository when it opens.
function ensureChannelOption(branch) {
  const sel = $('gh-channel');
  if (branch && ![...sel.options].some((o) => o.value === branch)) sel.add(new Option(branch + ' (branch build)', branch));
  if (sel.value !== branch) sel.value = branch;
}
let ghBranchesLoading = false;
async function loadBranches() {
  if (ghBranchesLoading) return;
  ghBranchesLoading = true;
  const sel = $('gh-channel');
  try {
    const r = await api('/api/github-updates/branches');
    if (r.error) throw new Error(r.error);
    const keep = sel.value;
    sel.length = 1;
    for (const b of r.branches || []) sel.add(new Option(`${b.branch} (v${b.version})`, b.branch));
    ensureChannelOption(r.selected || keep);
    sel.value = keep;
  } catch (e) { toast('Could not list branches: ' + e.message, 6000); }
  ghBranchesLoading = false;
}
$('gh-channel').addEventListener('focus', loadBranches);
$('gh-channel').addEventListener('mousedown', loadBranches);
$('gh-channel').onchange = async () => {
  const b = $('gh-channel').value;
  if (b && !await askConfirm(`Follow branch "${b}"? Its builds are untested code. Nothing installs until you click Update, and Roll back stays available.`)) { ensureChannelOption((state.github_updates || {}).branch || ''); return; }
  try { await post('/api/github-updates/channel', { branch: b }); toast(b ? 'Following ' + b : 'Back on Stable'); } catch (e) { toast(e.message, 6000); ensureChannelOption((state.github_updates || {}).branch || ''); }
  refresh();
};
async function ghAction(path, msg) {
  try { await post(path); toast(msg); } catch (e) { toast(e.message, 6000); }
  refresh();
}
$('btn-gh-check').onclick = () => ghAction('/api/github-updates/check', 'Checked ' + ((state.github_updates || {}).host || 'GitHub'));
$('btn-gh-install').onclick = async () => { if (await askConfirm('Check for a new release and update idle nodes and the controller to it now?')) ghAction('/api/github-updates/install', 'Installing the latest release'); };
$('btn-upd-all').onclick = async () => {
  try { const r = await post('/api/update-all'); toast(r.waiting ? `Updating ${r.canary} first; ${r.waiting} more follow once it passes` : `Updating ${r.canary}`, 6000); refresh(); } catch (e) { toast(e.message); }
};
async function rollbackController() {
  const to = state.controller_rollback_to;
  if (!await askConfirm(`Roll the controller back to v${to}? It restarts on that version and stays there until you install an update.`)) return;
  try { await post('/api/controller/rollback'); } catch (e) { toast(e.message); return; }
  ctlUpdating = true; renderUpdates(); toast('Controller restarting…');
  for (let i = 0; i < 60; i++) {
    await new Promise((r) => setTimeout(r, 1000));
    try {
      const r = await fetch('/api/state', { credentials: 'same-origin' });
      if (r.ok && (await r.json()).version === to) { location.reload(); return; }
    } catch (e) {}
  }
  ctlUpdating = false;
  toast('The controller did not come back on the old version; check journalctl -u outpost-controller', 6000);
}

async function updateController() {
  if (!await askConfirm(`Update the controller to v${state.controller_update_to} and restart it? Working tasks on a local planner pick up where they stopped once their node is back; Claude and OpenRouter tasks wait for you to press Resume.`)) return;
  const target = state.controller_update_to;
  try { await post('/api/controller/update'); } catch (e) { toast(e.message); return; }
  ctlUpdating = true; renderUpdates(); toast('Controller restarting…');
  for (let i = 0; i < 60; i++) {
    await new Promise((r) => setTimeout(r, 1000));
    try {
      const r = await fetch('/api/state', { credentials: 'same-origin' });
      if (r.ok && (await r.json()).version === target) { location.reload(); return; }
    } catch (e) {}
  }
  ctlUpdating = false;
  toast('The controller did not come back on the new version; check journalctl -u outpost-controller', 6000);
}
function uploadRelease(f, onProgress) {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open('POST', '/api/releases');
    x.withCredentials = true;
    x.setRequestHeader('X-Filename', f.name);
    x.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(Math.round(100 * e.loaded / e.total)); };
    x.onerror = () => reject(new Error('network error'));
    x.onload = () => {
      let body = {}; try { body = JSON.parse(x.responseText); } catch (e) {}
      x.status >= 200 && x.status < 300 ? resolve(body) : reject(new Error(body.error || x.statusText || 'upload failed'));
    };
    x.send(f);
  });
}
async function uploadFiles(files) {
  const n = files.length;
  for (const [i, f] of files.entries()) {
    const tag = (n > 1 ? `[${i + 1}/${n}] ` : '') + f.name;
    toast(`Uploading ${tag}…`, 60000);
    try {
      const body = await uploadRelease(f, (p) => toast(`Uploading ${tag}… ${p}%`, 60000));
      toast(`${tag}: ${body.platform} v${body.version}`);
    } catch (e) { toast(`${tag}: ${e.message}`, 4000); await new Promise((r) => setTimeout(r, 2500)); }
  }
  refresh();
}
(() => {
  const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes('Files');
  const ov = $('drop-overlay');
  let depth = 0;
  const hide = () => { depth = 0; ov.classList.add('hidden'); };
  // Whole page is the drop zone while Updates is showing; text drags are ignored.
  window.addEventListener('dragenter', (e) => { if (page !== 'updates' || !hasFiles(e)) return; e.preventDefault(); depth++; ov.classList.remove('hidden'); });
  window.addEventListener('dragover', (e) => { if (page !== 'updates' || !hasFiles(e)) return; e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; });
  window.addEventListener('dragleave', (e) => { if (page !== 'updates' || !hasFiles(e)) return; if (--depth <= 0) hide(); });
  window.addEventListener('drop', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault(); // never let the browser navigate to a dropped file
    const onUpdates = page === 'updates';
    hide();
    if (onUpdates) uploadFiles([...e.dataTransfer.files]);
  });
  window.addEventListener('blur', hide);
  $('drop-input').onchange = (e) => { uploadFiles([...e.target.files]); e.target.value = ''; };
})();
