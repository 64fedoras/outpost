'use strict';
// Update bar and Details panel on the Nodes page. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- updates ----------
const UPD_BUSY = ['sent', 'downloading', 'installing', 'restarting'];
let ctlUpdating = false;
let updPanel = null; // Details panel: null opens it by itself while an update runs or fails, true/false is the user's choice
let updIndiv = false; // per-machine Update buttons are hidden until "Choose individually…"
let ctlWaiting = false;
function nodeUpdRow(n) {
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
    const canUpdate = updIndiv && n.update_to && n.online && !busy;
    return `<tr><td><span class="dot ${n.online ? 'on' : ''}" title="${n.online ? 'service online' : 'offline'}"></span></td><td><b>${esc(nodeName(n))}</b><div class="sub u2">${n.online ? 'online' : n.last_seen ? 'offline · seen ' + ago(n.last_seen) : 'never connected'}</div></td>
      <td class="hide-ph">${esc(h.os ? h.os + '-' + h.arch : '?')}</td><td>${h.agent_version ? 'v' + esc(h.agent_version) : '?'}</td>
      <td>${n.update_to ? 'v' + esc(n.update_to) : '<span class="muted">' + (!h.os ? 'waiting for first connection' : state.releases.some((r) => r.platform === (h.os + '-' + h.arch)) ? 'up to date' : 'no release uploaded') + '</span>'}</td>
      <td>${prog}</td>
      <td class="r">${canUpdate ? `<button class="small primary" data-act="node-update" data-id="${n.id}" ${n.running_jobs ? 'disabled title="Waits until running jobs finish"' : ''}>Update</button>` : ''}
        ${h.rollback_to && n.online && !busy ? `<button class="small" data-act="node-rollback" data-id="${n.id}" title="Restart this node on the previous version">Roll back to v${esc(h.rollback_to)}</button>` : ''}</td></tr>`;
}

// Grouped table for big clusters. A group is the nodes of one platform that are online; offline nodes form their own group.
const updOpen = new Set(), updAll = new Set(); // expanded groups; groups showing every row (not just the first 8)
let updSeeded = false;
const UPD_ROWS = 8;
function updGroups(nodes) {
  const g = new Map();
  for (const n of nodes) {
    const h = n.hello || {};
    const key = !n.online ? 'offline' : (h.os ? h.os + '-' + h.arch : 'unknown');
    if (!g.has(key)) g.set(key, []);
    g.get(key).push(n);
  }
  // offline last, platforms by size
  return [...g.entries()].sort((a, b) => (a[0] === 'offline') - (b[0] === 'offline') || b[1].length - a[1].length || a[0].localeCompare(b[0]));
}
function updGroupRows(nodes) {
  const groups = updGroups(nodes);
  if (!updSeeded && groups.length) { updOpen.add(groups[0][0]); updSeeded = true; }
  const out = [];
  for (const [key, list] of groups) {
    const busy = list.some((n) => n.update && (UPD_BUSY.includes(n.update.state) || n.update.state === 'failed'));
    const open = updOpen.has(key) || busy;
    const vers = [...new Set(list.map((n) => (n.hello || {}).agent_version).filter(Boolean))];
    const avail = [...new Set(list.map((n) => n.update_to).filter(Boolean))];
    const can = list.filter((n) => n.update_to && n.online && !(n.update && UPD_BUSY.includes(n.update.state)));
    const label = key === 'offline' ? `${list.length} offline` : `${list.length} node${list.length === 1 ? '' : 's'}`;
    const nodeIds = can.map((n) => n.id).join(',');
    out.push(`<tr class="upd-g click" data-act="upd-group" data-key="${esc(key)}" aria-expanded="${open}"><td><span class="chev">${open ? '▾' : '▸'}</span></td><td><b>${label}</b></td>
      <td class="hide-ph">${key === 'offline' ? '<span class="muted">mixed</span>' : esc(key)}</td><td>${vers.length ? vers.map((v) => 'v' + esc(v)).join(', ') : '?'}</td>
      <td>${avail.length ? avail.map((v) => 'v' + esc(v)).join(', ') : key === 'offline' ? '<span class="muted">when back online</span>' : '<span class="muted">up to date</span>'}</td><td></td>
      <td class="r">${updIndiv && can.length ? `<button class="small primary" data-act="upd-group-go" data-ids="${nodeIds}" ${can.some((n) => n.running_jobs) ? 'title="Nodes with running jobs wait until they finish"' : ''}>Update ${can.length === 1 ? 'this node' : 'these ' + can.length}</button>` : ''}</td></tr>`);
    if (!open) continue;
    const sorted = [...list].sort((a, b) => (!!b.update_to - !!a.update_to) || nodeName(a).localeCompare(nodeName(b), undefined, { numeric: true }));
    const shown = updAll.has(key) ? sorted : sorted.slice(0, UPD_ROWS);
    shown.forEach((n) => out.push(nodeUpdRow(n)));
    if (shown.length < sorted.length) out.push(`<tr><td></td><td colspan="6"><a href="#" data-act="upd-more" data-key="${esc(key)}">Show ${sorted.length - shown.length} more</a></td></tr>`);
  }
  return out;
}
function renderUpdates() {
  renderGitHub();
  const rows = [];
  const cu = state.controller_update_to;
  rows.push(`<tr><td><span class="dot on"></span></td><td><b>Controller</b></td><td class="hide-ph">${esc(state.platform)}</td><td>v${esc(state.version)}</td>
    <td>${cu ? 'v' + esc(cu) : '<span class="muted">up to date</span>'}</td>
    <td>${ctlUpdating ? `<div class="prog"><span>installing and restarting…</span>${bar(100, 'good')}</div>` : ''}</td>
    <td class="r">${updIndiv && cu && !ctlUpdating ? `<button class="small primary" data-act="ctl-update">Update</button>` : ''}
      ${state.controller_rollback_to && !ctlUpdating ? `<button class="small" data-act="ctl-rollback" title="Restart the controller on the previous version">Roll back to v${esc(state.controller_rollback_to)}</button>` : ''}</td></tr>`);
  // Many nodes: groups by platform (collapsed except the first and any with work in progress), one line per group.
  const grouped = state.nodes.length >= NODE_FILTER_MIN;
  if (!grouped) for (const n of state.nodes) rows.push(nodeUpdRow(n));
  else rows.push(...updGroupRows(state.nodes));
  setHTML('upd-table', `<table><thead><tr><th class="u26"></th><th>Machine</th><th class="hide-ph">Platform</th><th>Running</th><th>Available</th><th>Progress</th><th></th></tr></thead><tbody>${rows.join('')}</tbody></table>`);
  const uc = updateCounts(), ro = state.rollout, ue = state.update_everything;
  const roRunning = ro && !ro.ended, ueRunning = ue && !ue.ended;
  const recent = (x) => x && x.ended && Date.now() - new Date(x.ended) < 24 * 3600e3;
  // One line for the whole run when Update everything is running or just ended badly, else the node rollout.
  const note = ueRunning || (ue && ue.state === 'failed' && recent(ue)) ? { text: (roRunning && ueRunning ? `${ro.text}${ro.rest ? `; ${ro.rest} more wait until it passes a quick check` : ''}` : ue.text) + (ueRunning ? '…' : ''), bad: ue.state === 'failed' }
    : roRunning ? { text: `${ro.text}${ro.rest ? `; ${ro.rest} more wait until it passes a quick check` : ''}…`, bad: false }
    : recent(ro) && ro.state === 'failed' ? { text: `${ro.text} (${ago(ro.ended)})`, bad: true } : null;
  const total = uc.nodes + (uc.ctl ? 1 : 0), working = roRunning || ueRunning || ctlUpdating;
  const parts = [uc.nodes ? `${uc.nodes} node${uc.nodes === 1 ? '' : 's'}` : '', uc.ctl ? 'the controller' : ''].filter(Boolean);
  const to = state.controller_update_to || (state.nodes.find((n) => n.update_to) || {}).update_to;
  const g = state.github_updates || {};
  const open = updPanel === null ? !!(working || (note && note.bad)) : updPanel;
  const det = `<button type="button" class="small link-btn" data-act="upd-toggle" aria-expanded="${open}">${open ? '▾' : '▸'} Details</button>`;
  let bar0;
  if (working) {
    // Share of the machines that were told to update and are already done.
    const flagged = state.nodes.filter((n) => n.update && (n.update_to || n.update.state === 'done') && (UPD_BUSY.includes(n.update.state) || n.update.state === 'done') && Date.now() - new Date(n.update.updated) < 3600e3);
    const done = flagged.filter((n) => n.update.state === 'done').length;
    const pct = flagged.length ? Math.max(5, 100 * done / flagged.length) : 5;
    bar0 = `<div class="upd-bar avail"><span class="ic" aria-hidden="true">⬆</span><span class="g"><b>Updating${to ? ' to v' + esc(to) : ''}</b>${note ? ': ' + esc(note.text) : ctlUpdating ? ': the controller is installing and restarting…' : ''}<div class="upd-prog">${bar(pct, 'good')}</div></span>${det}<button id="btn-upd-all" disabled>Updating…</button></div>`;
  } else if (note && note.bad) {
    bar0 = `<div class="upd-bar bad"><span class="ic" aria-hidden="true">⚠</span><span class="g"><b>Update stopped</b>: ${esc(note.text)}</span>${det}${total ? `<button id="btn-upd-all" class="primary" data-act="upd-all">Try again</button>` : ''}</div>`;
  } else if (total) {
    const sub = `One node goes first and is checked, then the rest, then the controller (it restarts).${uc.offline ? ` ${uc.offline} offline node${uc.offline === 1 ? '' : 's'} update when back.` : ''}`;
    bar0 = `<div class="upd-bar avail"><span class="ic" aria-hidden="true">⬆</span><span class="g"><b>${to ? 'v' + esc(to) + ' is available' : 'Updates are available'}</b> for ${parts.join(' and ')}<span class="sub">${esc(sub)}${g.checked && g.checked > '2000' ? ' Checked ' + ago(g.checked) + '.' : ''}</span></span>${det}<button id="btn-upd-all" class="primary" data-act="upd-all" title="Updates ${esc(parts.join(' and '))}: one node first, a quick check, the other nodes, and the controller last (it restarts).">Update everything</button></div>`;
  } else {
    const src = !g.enabled ? 'automatic checks are off' : g.branch ? `following the ${esc(g.branch)} branch` : 'Stable channel';
    bar0 = `<div class="upd-bar ok"><span class="ic" aria-hidden="true">✓</span><span class="g">Everything runs <b>v${esc(state.version)}</b>${g.checked && g.checked > '2000' ? ' · checked ' + ago(g.checked) : ''} · ${src}</span>${det}<button class="small" data-act="gh-check" title="Looks for a newer release now">Check now</button></div>`;
  }
  setHTML('upd-bar', bar0);
  $('upd-panel').classList.toggle('hidden', !open);
  $('btn-upd-indiv').setAttribute('aria-pressed', updIndiv);
  $('btn-upd-indiv').textContent = updIndiv ? 'Hide per-machine buttons' : 'Choose individually…';
  if (ueRunning && ue.state === 'controller' && !ctlWaiting) { ctlWaiting = true; ctlUpdating = true; waitControllerBack(state.controller_update_to, 'update', () => { ctlWaiting = false; }); }
  const rel = state.releases || [];
  $('upl-n').textContent = rel.length ? `· ${rel.length} uploaded` : '';
  setHTML('releases', rel.length ? `<div class="table-wrap"><table><thead><tr><th>Platform</th><th>Version</th><th>Size</th><th>SHA-256</th><th>Uploaded</th><th></th></tr></thead><tbody>${
    rel.map((r) => `<tr><td>${esc(r.platform)}</td><td>v${esc(r.version)}</td><td>${bytes(r.bytes)}</td><td class="mono" title="${esc(r.sha256)}">${esc(r.sha256.slice(0, 12))}…</td><td>${ago(r.uploaded)}</td>
      <td class="r"><button class="small danger" data-act="del-release" data-platform="${esc(r.platform)}">Delete</button></td></tr>`).join('')}</tbody></table></div>` : '');
}
function renderGitHub() {
  const g = state.github_updates || {};
  let t;
  if (g.busy) t = g.busy.startsWith('checking') ? `Checking ${g.host || 'GitHub'}…` : 'Downloading ' + g.busy.replace('downloading ', '') + '…';
  else {
    const mode = !g.enabled ? 'Automatic checks are off (Settings)' : g.auto_install ? 'Checks every 6 hours and installs automatically' : 'Checks every 6 hours; you press Update everything';
    t = `${mode}. ` + (g.repo ? `Repository: ${g.repo} (${g.host}). ` : '') + (g.checked && g.checked > '2000' ? `Last checked ${ago(g.checked)}${g.latest ? ', latest is v' + g.latest : ''}.` : 'Not checked yet.');
    if (g.hold) t += ` Auto-install skips v${g.hold} and older after a rollback.`;
    t += g.catalog ? ` Suggested models: list from v${g.catalog}.` : ' Suggested models: the list built into this version.';
  }
  $('gh-info').textContent = t;
  if (g.error) { const e = document.createElement('span'); e.className = 'error-msg'; e.style.display = 'block'; e.textContent = g.error; $('gh-info').appendChild(e); }
  $('btn-gh-check').disabled = !!g.busy;
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
async function updateEverything() {
  const uc = updateCounts();
  const parts = [uc.nodes ? `${uc.nodes} node${uc.nodes === 1 ? '' : 's'}` : '', uc.ctl ? 'the controller' : ''].filter(Boolean).join(' and ');
  const order = uc.nodes ? 'One node goes first and is checked; if it fails it is rolled back and nothing else is touched. ' : '';
  const ctl = uc.ctl ? 'The controller updates last and restarts: tasks on a local planner pick up where they stopped, Claude and OpenRouter tasks wait for you to press Resume, and it waits for running tasks to finish first. ' : '';
  if (!await askConfirm(`Update ${parts}? ${order}${ctl}`.trim(), { ok: 'Update everything' })) return;
  try {
    const r = await post('/api/update-everything');
    toast(r.nodes ? `Updating ${r.canary} first${r.nodes > 1 ? `; ${r.nodes - 1} more follow once it passes` : ''}${r.controller ? ', then the controller' : ''}` : 'Updating the controller', 6000);
    refresh();
  } catch (e) { toast(e.message, 6000); }
}
$('btn-upd-indiv').onclick = () => { updIndiv = !updIndiv; renderUpdates(); };
// Wait for the restarted controller to answer on the expected version, then reload the page.
async function waitControllerBack(version, word, done) {
  ctlUpdating = true; renderUpdates(); toast('Controller restarting…');
  for (let i = 0; i < 90; i++) {
    await new Promise((r) => setTimeout(r, 1000));
    try {
      const r = await fetch('/api/state', { credentials: 'same-origin' });
      if (r.ok && (await r.json()).version === version) { location.reload(); return; }
    } catch (e) {}
  }
  ctlUpdating = false;
  if (done) done();
  toast(`The controller did not come back on the ${word === 'rollback' ? 'old' : 'new'} version; check journalctl -u outpost-controller`, 6000);
}
async function rollbackController() {
  const to = state.controller_rollback_to;
  if (!await askConfirm(`Roll the controller back to v${to}? It restarts on that version and stays there until you install an update.`)) return;
  try { await post('/api/controller/rollback'); } catch (e) { toast(e.message); return; }
  await waitControllerBack(to, 'rollback');
}

async function updateController() {
  if (!await askConfirm(`Update the controller to v${state.controller_update_to} and restart it? Working tasks on a local planner pick up where they stopped once their node is back; Claude and OpenRouter tasks wait for you to press Resume.`)) return;
  const target = state.controller_update_to;
  try { await post('/api/controller/update'); } catch (e) { toast(e.message); return; }
  await waitControllerBack(target, 'update');
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
  updPanel = true; $('upl').open = true; renderUpdates(); // show the list the new files land in
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
  window.addEventListener('dragenter', (e) => { if (page !== 'nodes' || !hasFiles(e)) return; e.preventDefault(); depth++; ov.classList.remove('hidden'); });
  window.addEventListener('dragover', (e) => { if (page !== 'nodes' || !hasFiles(e)) return; e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; });
  window.addEventListener('dragleave', (e) => { if (page !== 'nodes' || !hasFiles(e)) return; if (--depth <= 0) hide(); });
  window.addEventListener('drop', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault(); // never let the browser navigate to a dropped file
    const onUpdates = page === 'nodes';
    hide();
    if (onUpdates) uploadFiles([...e.dataTransfer.files]);
  });
  window.addEventListener('blur', hide);
  $('drop-input').onchange = (e) => { uploadFiles([...e.target.files]); e.target.value = ''; };
})();
