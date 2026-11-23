'use strict';
// Nodes page. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- nodes ----------
// benchLine shows a node's measured speed, a running or failed benchmark, and the re-run link.
function benchLine(n, loaded, ready) {
  if (!loaded || !n.online) return '';
  const b = n.bench && n.bench.model === loaded ? n.bench : null;
  const f = n.bench_fail && n.bench_fail.model === loaded ? n.bench_fail : null;
  const label = b || f ? 'Re-run' : 'Run';
  const rerun = n.benching ? '' : ready ? ` · <a href="#" data-act="benchmark" data-id="${n.id}" title="Measure this node's speed again">${label} benchmark</a>`
    : ` · <span class="muted" title="The benchmark can run once the model has finished loading">${label} benchmark (model loading…)</span>`;
  let txt;
  if (n.benching) txt = 'Benchmarking…';
  else if (b) txt = `Speed ${b.tok_per_s.toFixed(1)} tok/s${b.prompt_tps ? ' · reads ' + Math.round(b.prompt_tps) + ' tok/s' : ''}${b.samples ? ' · ' + b.samples + ' subtask' + (b.samples === 1 ? '' : 's') : b.light ? ' · short benchmark' : ' · benchmark'}`;
  else if (f) txt = `<span class="muted" title="${esc(f.error)}">Benchmark failed (${esc(f.error.length > 60 ? f.error.slice(0, 60) + '…' : f.error)}); retries in a few minutes with a shorter test. Real subtasks fill in the speed meanwhile.</span>`;
  else txt = '<span class="muted">Not benchmarked yet</span>';
  const p = n.load_prefs && n.load_prefs.name === loaded ? n.load_prefs : {};
  const serve = [p.cache_type ? (p.cache_type === 'q8_0' ? '8-bit' : '4-bit') + ' cache' : '', p.parallel > 1 ? p.parallel + ' calls at once' : '', p.draft ? 'draft ' + esc(p.draft) : ''].filter(Boolean).join(' · ');
  return `<div class="sub wrap u15" title="Benchmark when the model loaded, then a rolling average of real subtasks">${txt}${rerun}${serve ? ` <span class="muted">· ${serve}</span>` : ''}</div>`;
}

// Nodes page views: Grid (cards), List (grouped two-line rows) and Detailed list
// (short list on the left, the selected node's full card on the right).
const NODE_VIEWS = ['grid', 'list', 'detail'];
let nodeView = 'grid', nodeSel = null, nodeLabel = '';
let nodeQuery = '', nodeChip = 'all', nodeSort = 'status';
try { const v = localStorage.getItem('outpost.nodeSort'); if (['status', 'name', 'speed'].includes(v)) nodeSort = v; } catch (e) { /* storage blocked */ }
// Clusters of NODE_FILTER_MIN nodes or more get the filter bar and the compact cards and rows.
const nodesDense = (nodes) => nodes.length >= NODE_FILTER_MIN;
// One update banner replaces the same note on every card when several nodes can update.
const updateBannerOn = (nodes) => nodes.filter((n) => n.online && n.update_to).length >= 3;
let nodeDrawer = null; // node whose details panel is open beside the Grid view
const nodeOpen = new Set(); // nodes expanded under their List row
// The Detailed list is optional (Settings → Display and alerts); kept in this browser only.
let showDetailed = false;
try { showDetailed = localStorage.getItem('outpost.detailedList') === '1'; } catch (e) { /* storage blocked */ }
try { const v = localStorage.getItem('outpost.nodeView'); if (NODE_VIEWS.includes(v)) nodeView = v; } catch (e) { /* storage blocked */ }
if (nodeView === 'detail' && !showDetailed) nodeView = 'list';
function setNodeView(v) {
  if (!NODE_VIEWS.includes(v)) return;
  if (v === 'detail' && !showDetailed) v = 'list';
  nodeView = v;
  try { localStorage.setItem('outpost.nodeView', v); } catch (e) { /* storage blocked */ }
  renderNodes();
}

// nodeFacts gathers what the list views show for one node.
function nodeFacts(n) {
  const h = n.hello || {}, hb = n.online ? n.heartbeat : null;
  const loaded = hb && hb.loaded_model, ready = hb && hb.inference_ready;
  const g = (h.gpus || [])[0];
  const vram = g && hb && hb.vram_used_bytes && g.vram_bytes ? 100 * hb.vram_used_bytes[0] / g.vram_bytes : null;
  const b = n.bench && loaded && n.bench.model === loaded ? n.bench : null;
  const issues = [];
  const o = state.ops && state.ops[n.id];
  if (n.health) issues.push({ lvl: n.health === 'red' ? 'bad' : 'warn', short: n.health === 'red' ? 'Unreliable' : 'Shaky', text: n.health_note, act: 'health-reset', fix: 'Reset health' });
  if (!n.online) issues.push({ lvl: 'off', short: 'Offline', text: n.last_seen ? `Last seen ${ago(n.last_seen)}.` : 'Never connected.', ...(n.can_wake ? { act: 'wake', fix: 'Wake' } : {}) });
  else {
    if (n.off_hours) issues.push({ lvl: 'info', short: 'Off hours', text: `Outside its working hours (${hoursText(n.hours)}): takes no new work.`, act: 'hours', fix: 'Hours…' });
    const lf = o && o.last_failure && (Date.now() - new Date(o.last_failure.at)) < 3600000 ? o.last_failure : null;
    if (lf) issues.push({ lvl: 'bad', short: 'Failed', text: lf.error || 'The last model operation failed.', act: 'load', fix: 'Load model…' });
    else if (hb && hb.llama_error && !ready) issues.push({ lvl: 'bad', short: 'Model error', text: hb.llama_error, act: 'load', fix: 'Load model…' });
    if (hb && hb.paused) issues.push({ lvl: 'warn', short: 'Paused', text: pauseText(n), ...(canPause(n) ? { act: 'node-resume', fix: 'Resume' } : {}) });
    if (h.os === 'linux' && (h.gpus || []).some((x) => x.vendor !== 'apple') && !h.vulkan_available) issues.push({ lvl: 'warn', short: 'No Vulkan', text: 'GPU found, but no Vulkan driver, so models run on CPU.' });
    if (h.models_free_bytes && h.models_free_bytes < 10 * 1024 ** 3) issues.push({ lvl: 'warn', short: 'Low disk', text: `${bytes(h.models_free_bytes)} free for models. The next model copy may need a cleanup.`, act: 'clean', fix: 'Clean up…' });
    const sf = storageFacts(n);
    if (sf && sf.full) issues.push({ lvl: 'warn', short: 'At limit', text: `Outpost uses ${bytes(sf.u.total)} of its ${bytes(sf.lim)} storage limit. New models are ${n.storage_full === 'refuse' ? 'refused' : 'only copied after old ones are removed'}.`, act: 'clean', fix: 'Clean up…' });
    if (n.update_to) issues.push({ lvl: 'info', short: 'Update', text: `Runs v${h.agent_version || '?'}; v${n.update_to} is available.`, act: 'node-update', fix: 'Update' });
  }
  const worst = ['bad', 'warn', 'info', 'off'].find((l) => issues.some((i) => i.lvl === l)) || '';
  let model;
  if (n.lending_to) model = `<span class="muted">Lending memory to <b class="nv-ink">${esc(n.lending_to)}</b></span>`;
  else if (!n.online) model = '<span class="muted">—</span>';
  else if (!loaded) model = opActive(n) ? '<span class="muted">Setting up a model…</span>' : '<span class="muted">No model loaded</span>';
  else model = `<b>${esc(loaded)}</b> <span class="muted">· ${hb.idle_unloaded ? 'idle, unloaded' : ready ? (hb.inference_busy ? 'working' : 'ready') : 'starting'}</span>`;
  const dot = !n.online ? '' : worst === 'bad' ? 'bad' : worst === 'warn' ? 'warn' : ready ? 'on' : (loaded || n.lending_to) ? 'busy' : 'idle';
  return {
    h, hb, loaded, ready, issues, worst, model, dot,
    cpu: hb ? hb.cpu_pct : 0, ram: hb && hb.ram_total_bytes ? 100 * hb.ram_used_bytes / hb.ram_total_bytes : 0, vram,
    tps: b && n.online && !n.lending_to ? b.tok_per_s : null,
    disk: storageFacts(n),
  };
}
const nvTps = (f, big) => f.tps == null ? '<span class="muted">—</span>' : big ? `<span class="nv-tps">${f.tps.toFixed(1)}</span><span class="nv-tpsu">tok/s</span>` : `<span class="nv-spd">${f.tps.toFixed(1)} tok/s</span>`;
const nvChips = (f) => f.issues.map((i) => `<span class="nv-chip nv-${i.lvl}" title="${esc(i.text)}">${esc(i.short)}</span>`).join(' ');

function nodeRow(n, dense, hideUpd) {
  const f = nodeFacts(n), h = f.h;
  const issues = hideUpd ? f.issues.filter((i) => i.act !== 'node-update') : f.issues;
  const fixIssue = issues.find((i) => i.act);
  const first = issues.find((i) => i.lvl !== 'off');
  const open = nodeOpen.has(n.id);
  if (dense) {
    const [st] = nodeStatus(n, f);
    const model = n.lending_to ? `<span class="muted">Lending to ${esc(n.lending_to)}</span>` : !n.online ? `<span class="muted">${n.last_seen ? 'Offline · seen ' + ago(n.last_seen) : 'Never connected'}</span>`
      : f.loaded ? `<b>${esc(f.loaded)}</b> <span class="muted">· ${esc(st.toLowerCase())}</span>` : '<span class="muted">No model loaded</span>';
    const chip = first && first.lvl !== 'info' ? ` ${nvChips({ issues: [first] })}` : '';
    const act = n.online ? (fixIssue && fixIssue.lvl !== 'info' ? `<button class="small primary" data-act="${fixIssue.act}" data-id="${n.id}">${esc(fixIssue.fix)}</button>` : `<button class="small" data-act="node-toggle" data-id="${n.id}" aria-expanded="${open}">${open ? 'Hide' : 'Details'}</button>`)
      : n.can_wake ? `<button class="small" data-act="wake" data-id="${n.id}">Wake</button>` : `<button class="small" data-act="node-toggle" data-id="${n.id}" aria-expanded="${open}">${open ? 'Hide' : 'Details'}</button>`;
    return `<div class="nv-row dn nv-s-${f.worst}${open ? ' open' : ''}" data-act="node-toggle" data-id="${n.id}" aria-expanded="${open}">
      <span class="dot ${f.dot}"></span><b class="nv-ell" title="${esc(nodeName(n))}">${esc(nodeName(n))}</b>
      <span class="nv-ell muted dn-sys">${esc(h.os || '?')}${h.agent_version ? ' · v' + esc(h.agent_version) : ''}</span>
      <span class="nv-ell">${model}${chip}</span>
      ${n.online ? `<span class="dn-bars"><span>CPU${bar(f.cpu)}</span><span>RAM${bar(f.ram)}</span><span>${f.vram != null ? 'VRAM' + bar(f.vram) : ''}</span></span>` : '<span class="dn-bars"></span>'}
      <span class="r dn-tps">${f.tps != null ? Math.round(f.tps) + ' tok/s' : '<span class="muted">—</span>'}</span>
      <span class="nv-acts">${act}</span></div>${open ? `<div class="nv-exp">${nodeCard(n)}</div>` : ''}`;
  }
  const sub = first ? `${nvChips({ issues })} ${esc(first.text)}` : opActive(n) ? opView(n).replace(/<[^>]+>/g, ' ').trim() : n.online
    ? `${n.running_jobs}/${n.max_jobs} jobs${n.pool_names && n.pool_names.length ? ' · pooled with ' + n.pool_names.map(esc).join(', ') : ''} · exec ${n.exec_enabled ? (n.sandbox ? 'on, sandbox ' + n.sandbox : 'on') : 'off'}` : '';
  const action = fixIssue ? `<button class="small primary" data-act="${fixIssue.act}" data-id="${n.id}">${esc(fixIssue.fix)}</button>`
    : n.online && !n.lending_to ? `${bestBtn(n)}<button class="small" data-act="load" data-id="${n.id}">${f.loaded ? 'Change…' : 'Load model…'}</button>` : '';
  return `<div class="nv-row nv-s-${f.worst}${open ? ' open' : ''}" data-act="node-toggle" data-id="${n.id}" aria-expanded="${open}">
    <div class="nv-c1"><span class="dot ${f.dot}"></span><div class="nv-min"><b>${esc(nodeName(n))}</b><div class="sub nv-sub">${esc(h.os ? h.os + ' ' + h.arch : 'unknown')}${h.agent_version ? ' · v' + esc(h.agent_version) : ''}${n.addr ? ' · ' + esc(n.addr.split(':')[0]) : ''}</div>${labelChips(n)}</div></div>
    <div class="nv-min"><div class="nv-ell">${f.model}</div><div class="sub nv-sub nv-wrap">${sub}</div></div>
    <div>${n.online ? `<div class="nv-bars"><span>CPU</span>${bar(f.cpu)}<span>RAM</span>${bar(f.ram)}${f.vram != null ? `<span>VRAM</span>${bar(f.vram)}` : ''}${f.disk ? `<span title="${esc(f.disk.text)}">DISK</span><span title="${esc(f.disk.text)}">${bar(f.disk.pct, f.disk.full ? 'hot' : '')}</span>` : ''}</div>` : `<span class="sub">${n.last_seen ? 'Last seen ' + ago(n.last_seen) : 'Never connected'}</span>`}</div>
    <div class="r">${nvTps(f, true)}</div>
    <div class="nv-acts">${action}<button class="small" data-act="node-toggle" data-id="${n.id}" aria-expanded="${open}" title="Show or hide all details and controls for this node">${open ? 'Hide' : 'Details'}</button></div>
  </div>${open ? `<div class="nv-exp">${nodeCard(n)}</div>` : ''}`;
}

function nodeListView(nodes, dense, hideUpd) {
  const groups = [
    ['Needs attention', nodes.filter((n) => { const w = nodeFacts(n).worst; return w === 'bad' || w === 'warn'; })],
    ['Healthy', nodes.filter((n) => { const w = nodeFacts(n).worst; return n.online && w !== 'bad' && w !== 'warn'; })],
    ['Offline', nodes.filter((n) => !n.online)],
  ];
  const head = '<div class="nv-row dn nv-head" aria-hidden="true"><span></span><span>Node</span><span>System</span><span>Model</span><span>Load</span><span class="r">Speed</span><span></span></div>';
  return groups.filter((g) => g[1].length).map(([t, ns]) => `<div class="nv-grp">${t} · ${ns.length}</div><div class="nv-box${dense ? ' dense' : ''}">${dense ? head : ''}${ns.map((n) => nodeRow(n, dense, hideUpd)).join('')}</div>`).join('');
}

function nodeDetailView(nodes) {
  const rank = { bad: 0, warn: 1, info: 2, '': 3, off: 4 };
  const sorted = [...nodes].sort((a, b) => rank[nodeFacts(a).worst] - rank[nodeFacts(b).worst]);
  if (!nodeSel || !nodes.some((n) => n.id === nodeSel)) nodeSel = sorted[0].id;
  const sel = nodeById(nodeSel);
  const list = sorted.map((n) => {
    const f = nodeFacts(n);
    return `<button class="nv-item nv-s-${f.worst}${n.id === nodeSel ? ' on' : ''}" data-act="node-select" data-id="${n.id}">
      <span class="dot ${f.dot}"></span><span class="nv-min nv-grow"><b>${esc(nodeName(n))}</b><span class="sub nv-sub nv-ell">${n.lending_to ? 'Lending to ' + esc(n.lending_to) : f.loaded ? esc(f.loaded) : !n.online ? 'Offline' : 'No model'}</span></span>
      <span class="nv-right">${nvTps(f)}<span>${nvChips(f)}</span></span></button>`;
  }).join('');
  return `<div class="nv-split"><div class="nv-list">${list}</div>
    <div class="nv-pane">${nodeCard(sel)}${nodeJobs(sel)}</div></div>`;
}
// nodeJobs lists a node's latest jobs, under its full card (Detailed list and the Grid details panel).
function nodeJobs(n) {
  const jobs = (state.jobs || []).filter((j) => j.node_id === n.id || (!j.node_id && j.node_name === nodeName(n))).slice(0, 6);
  return `<div class="section-head"><h2>Recent jobs on ${esc(nodeName(n))}</h2></div>
      ${jobs.length ? `<div class="table-wrap"><table><tbody>${jobs.map((j) => `<tr class="click" data-act="open-job" data-id="${j.id}"><td>${pill(j.status)}</td><td class="cmd">${esc(j.command)}</td><td>${ago(j.created)}</td></tr>`).join('')}</tbody></table></div>` : '<div class="empty">No recent jobs on this node.</div>'}`;
}

// Grid view: cluster summary strip, then gauge cards. Nodes that share memory
// (a pool: the main node and its helpers) sit together in one frame.
function ring(pct, label, sub) {
  const p = Math.max(0, Math.min(100, Math.round(pct || 0))), r = 22, c = 2 * Math.PI * r;
  return `<div class="ring"><svg width="58" height="58" viewBox="0 0 58 58" aria-hidden="true"><circle class="ring-bg" cx="29" cy="29" r="${r}"/>
    ${p ? `<circle class="ring-fg${p > 85 ? ' hot' : ''}" cx="29" cy="29" r="${r}" stroke-dasharray="${(c * p / 100).toFixed(1)} ${c.toFixed(1)}" transform="rotate(-90 29 29)"/>` : ''}
    <text x="29" y="33" text-anchor="middle">${p}%</text></svg><div class="sub">${label}</div><div class="sub ring-s">${sub}</div></div>`;
}
const ringNone = (label) => `<div class="ring ring-off"><svg width="58" height="58" viewBox="0 0 58 58" aria-hidden="true"><circle class="ring-bg dashed" cx="29" cy="29" r="22"/></svg><div class="sub">${label}</div></div>`;
function nodeStatus(n, f) {
  if (!n.online) return ['Offline', 'off'];
  if (f.worst === 'bad') return ['Failed', 'bad'];
  if (f.issues.some((i) => i.short === 'Paused')) return ['Paused', 'warn'];
  if (n.off_hours) return ['Off hours', 'idle'];
  if (n.lending_to) return ['Lending memory', 'busy'];
  if (opActive(n)) return ['Setting up', 'busy'];
  if (!f.loaded) return ['No model', 'idle'];
  if (f.hb.gpu_yielding) return ['GPU in use', 'idle'];
  if (f.hb.idle_unloaded) return ['Idle, unloaded', 'idle'];
  if (!f.ready) return ['Starting', 'busy'];
  return f.hb.inference_busy ? ['Working', 'on'] : ['Ready', 'on'];
}
function gaugeCard(n, o = {}) {
  const dense = !!o.dense;
  const f = nodeFacts(n), h = f.h, hb = f.hb, cpu = h.cpu || {};
  const [st, cls] = nodeStatus(n, f);
  const gpus = h.gpus || [];
  const g = gpus[0];
  const ramTot = hb && hb.ram_total_bytes;
  const notes = f.issues.filter((i) => i.lvl !== 'off' && !(o.hideUpd && i.act === 'node-update')).map((i) => `<div class="note ${i.lvl === 'info' ? '' : i.lvl} nv-note"><span class="nv-grow">${esc(i.text)}</span>${i.act ? `<button class="small" data-act="${i.act}" data-id="${n.id}">${esc(i.fix)}</button>` : ''}</div>`).join('');
  const btns = n.online && !n.lending_to ? (dense ? (f.loaded ? `<button class="small" data-act="load" data-id="${n.id}">Change…</button>` : bestBtn(n)) : `${bestBtn(n)}<button class="small" data-act="load" data-id="${n.id}">${f.loaded ? 'Change…' : 'Load model…'}</button>${f.loaded ? `<button class="small" data-act="unload" data-id="${n.id}">Unload</button>` : ''}`) : n.can_wake ? `<button class="small" data-act="wake" data-id="${n.id}" title="Send a Wake-on-LAN packet">Wake</button>` : '';
  const on = nodeDrawer === n.id;
  const gname = g ? String(g.name || '').replace(/^(AMD|NVIDIA|Intel)\s+(Radeon|GeForce)?\s*/i, '') : '';
  const hw = dense ? `${cpu.cores || '?'}C/${cpu.threads || '?'}T${ramTot ? ' · ' + bytes(ramTot) : ''}${gname ? ' · ' + esc(gname) : ''} · ${esc(h.os || '?')}`
    : `${esc(cpu.model || 'CPU')} · ${esc(h.os ? h.os + ' ' + h.arch : 'unknown')}${h.agent_version ? ' · v' + esc(h.agent_version) : ''}`;
  return `<div class="card gc${dense ? ' dense' : ''} nv-s-${f.worst}${on ? ' on' : ''}" data-act="node-detail" data-id="${n.id}">
    <div class="node-top"><span class="node-name" title="${esc(n.id)}">${esc(nodeName(n))}</span><span class="nv-pill nv-p-${cls}"><i></i>${st}</span>
      <button class="small" data-act="node-detail" data-id="${n.id}" aria-expanded="${on}" aria-controls="node-drawer" title="All details and controls for this node (Unload, benchmark, logs and more)">${dense ? '⋯' : 'Details'}</button></div>
    <div class="sub nv-ell">${hw}</div>
    ${dense ? '' : labelChips(n)}
    <div class="gc-rings">
      ${ring(f.cpu, 'CPU', `${cpu.cores || '?'}C/${cpu.threads || '?'}T`)}
      ${ring(f.ram, 'RAM', ramTot ? bytes(ramTot) : '—')}
      ${g && !g.backend ? ringNone('GPU not used') : g ? ring(f.vram || 0, g.vendor === 'apple' ? 'GPU' : 'VRAM', bytes(g.vram_bytes) + (hb && hb.gpu_temp_c && hb.gpu_temp_c[0] > 0 ? ` · ${hb.gpu_temp_c[0]} °C` : '')) : ringNone('no GPU')}
      <div class="ring gc-speed"><div>${nvTps(f, true)}</div><div class="sub">${n.online ? `${n.running_jobs}/${n.max_jobs} jobs` : n.last_seen ? 'seen ' + ago(n.last_seen) : 'never connected'}</div></div>
    </div>
    ${dense ? '' : gaugeSparks(n)}
    ${notes}${opActive(n) ? opView(n) : ''}
    <div class="gc-model"><span class="nv-grow nv-ell">${f.model}</span>${btns}</div>
  </div>`;
}
function clusterSummary(nodes) {
  const on = nodes.filter((n) => n.online);
  const facts = on.map((n) => [n, nodeFacts(n)]);
  const loaded = facts.filter(([n, f]) => f.loaded && !n.lending_to).length;
  const pooled = on.filter((n) => n.pool_names && n.pool_names.length).length;
  const setting = on.filter(opActive).length;
  const jobs = on.reduce((a, n) => a + (n.running_jobs || 0), 0), slots = on.reduce((a, n) => a + (n.max_jobs || 0), 0);
  const speed = facts.reduce((a, [, f]) => a + (f.tps || 0), 0);
  let vTot = 0, vUsed = 0, nG = 0;
  on.forEach((n) => (((n.hello || {}).gpus) || []).forEach((g, i) => { if (g.vendor === 'apple') return; nG++; vTot += g.vram_bytes || 0; vUsed += (n.heartbeat && n.heartbeat.vram_used_bytes && n.heartbeat.vram_used_bytes[i]) || 0; }));
  const att = nodes.map((n) => [n, nodeFacts(n)]).filter(([, f]) => f.worst === 'bad' || f.worst === 'warn');
  const off = nodes.filter((n) => !n.online);
  const cell = (v, k, tip, cls = '') => `<div class="${cls}" title="${esc(tip || '')}"><b>${v}</b><span>${esc(k)}</span></div>`;
  return `<div class="strip">
    ${cell(`${on.length} / ${nodes.length}`, off.length ? `online · ${off.length} offline` : 'online', off.map(nodeName).join(', ') + (off.length ? ' offline' : ''))}
    ${cell(loaded, 'models loaded', [pooled && pooled + ' pooled', setting && setting + ' setting up'].filter(Boolean).join(' · '))}
    ${cell(`${jobs} / ${slots}`, 'jobs running')}
    ${cell(speed ? `${Math.round(speed).toLocaleString()} tok/s` : '—', 'cluster speed', 'sum of loaded models')}
    ${cell(nG ? bytes(vTot - vUsed) : '—', nG ? `free VRAM on ${nG} GPU${nG > 1 ? 's' : ''}` : 'no GPUs', nG ? `of ${bytes(vTot)}` : '')}
    ${cell(att.length, 'need attention', att.map(([n, f]) => `${nodeName(n)}: ${f.issues.find((i) => i.lvl === 'bad' || i.lvl === 'warn').short.toLowerCase()}`).join(' · '), att.length ? 'w' : '')}
  </div>`;
}
function offlineStrip(nodes) {
  if (!nodes.length) return '';
  return `<div class="off-strip"><span class="off-h">Offline · ${nodes.length}</span>${nodes.map((n) => `<span class="off-chip" data-act="node-detail" data-id="${n.id}" title="Open details"><b>${esc(nodeName(n))}</b><span class="muted">${n.last_seen ? ago(n.last_seen) : 'never connected'}</span>${n.can_wake ? `<button class="small" data-act="wake" data-id="${n.id}" title="Send a Wake-on-LAN packet">Wake</button>` : ''}</span>`).join('')}</div>`;
}
function nodeGridView(nodes, o = {}) {
  const dense = !!o.dense;
  const byName = (nm) => nodes.find((x) => nodeName(x) === nm);
  const inPool = new Set(), pools = [];
  nodes.forEach((n) => {
    if (!n.pool_names || !n.pool_names.length) return;
    const helpers = n.pool_names.map(byName).filter(Boolean);
    [n, ...helpers].forEach((x) => inPool.add(x.id));
    pools.push([n, helpers]);
  });
  const card = (n) => gaugeCard(n, o);
  const pool = ([n, helpers]) => {
    const f = nodeFacts(n);
    return `<div class="pool"><div class="pool-h"><span class="pool-ic">⇄</span><b>Memory pool</b>
      <span class="sub nv-grow">${f.loaded ? esc(f.loaded) + ' · ' : ''}runs on <b class="nv-ink">${esc(nodeName(n))}</b>, ${helpers.map((x) => `<b class="nv-ink">${esc(nodeName(x))}</b>`).join(', ')} lend${helpers.length === 1 ? 's' : ''} memory</span>
      ${nvTps(f)}${f.loaded ? `<button class="small" data-act="unload" data-id="${n.id}" title="Unload the model; the helpers get their memory back">Unload pool</button>` : ''}</div>
      <div class="pool-cards">${[n, ...helpers].map(card).join('')}</div></div>`;
  };
  // The caller sorts; with "Sort: status" nodes that need attention come first (the sort puts them there), then pools.
  const free = nodes.filter((n) => !inPool.has(n.id));
  const off = dense ? free.filter((n) => !n.online) : [];
  const shown = dense ? free.filter((n) => n.online) : free;
  const att = o.attentionFirst ? shown.filter((n) => ['bad', 'warn'].includes(nodeFacts(n).worst)) : [];
  const rest = shown.filter((n) => !att.includes(n));
  return `<div class="gc-grid${dense ? ' dense' : ''}">${att.map(card).join('')}${pools.map(pool).join('')}${rest.map(card).join('')}</div>${offlineStrip(off)}`;
}

// The filter bar sits above #nodes (outside what renderNodes redraws), so typing in it keeps focus.
function renderNodeFilter(nodes, rows, labels, dense) {
  const bar = $('node-filter');
  bar.classList.toggle('hidden', !dense);
  if (!dense) return;
  const counts = nodeChipCounts(rows);
  const chip = (id, label) => `<button class="chip${nodeChip === id ? ' on' : ''}" data-act="node-chip" data-chip="${id}" aria-pressed="${nodeChip === id}">${label}<i>${counts[id]}</i></button>`;
  const lab = labels.length ? '<span class="nf-sep"></span>' + labels.map((l) => `<button class="chip${l === nodeLabel ? ' on' : ''}" data-act="label-filter" data-label="${l === nodeLabel ? '' : esc(l)}" aria-pressed="${l === nodeLabel}">${esc(l)}<i>${nodes.filter((n) => (n.labels || []).includes(l)).length}</i></button>`).join('') : '';
  // Quick filters with nothing to show (no GPU nodes, nothing to update) stay out of the way, except the active one.
  setHTML('nf-chips', NODE_CHIPS.filter(([id]) => id === 'all' || id === nodeChip || counts[id] > 0 && (id !== 'update' || counts[id] < nodes.length || nodeChip === id)).map(([id, label]) => chip(id, label)).join('') + lab);
  if ($('nf-q').value !== nodeQuery) $('nf-q').value = nodeQuery;
  if ($('nf-sort').value !== nodeSort) $('nf-sort').value = nodeSort;
}
function renderNodes() {
  const a = document.activeElement;
  if (a && a.matches && a.matches('[data-storage-limit], [data-rec-node]') && document.body.contains(a)) return; // typing a limit: redraw after
  const nodes = state.nodes;
  const online = nodes.filter((n) => n.online).length;
  setHTML('node-count', nodes.length ? `${online} of ${nodes.length} online` : '');
  if (nodeView === 'detail' && !showDetailed) nodeView = 'list';
  document.querySelectorAll('[data-node-view]').forEach((b) => { b.classList.toggle('on', b.dataset.nodeView === nodeView); if (b.dataset.nodeView === 'detail') b.hidden = !showDetailed; });
  if (!nodes.length) { setHTML('node-summary', ''); $('node-filter').classList.add('hidden'); setHTML('nodes', `<div class="empty u16">No nodes yet. Click <b>Add node</b> to get the install command.</div>`); return; }
  const labels = state.labels || [];
  if (nodeLabel && !labels.includes(nodeLabel)) nodeLabel = '';
  const dense = nodesDense(nodes);
  if (!dense) { nodeQuery = ''; nodeChip = 'all'; }
  const rows = nodes.map((n) => ({ n, f: nodeFacts(n) }));
  const shownRows = nodeSorted(rows.filter(({ n, f }) => nodeMatches(n, f, nodeQuery, nodeChip, nodeLabel)), nodeSort);
  const shown = shownRows.map((r) => r.n);
  const filtering = !!(nodeQuery || nodeChip !== 'all' || nodeLabel);
  // Below the filter bar's size, labels keep their old chip row.
  const chips = !dense && labels.length ? `<div class="label-filter" role="group" aria-label="Show nodes with label">${['', ...labels].map((l) => `<button class="small${l === nodeLabel ? ' on' : ''}" data-act="label-filter" data-label="${esc(l)}" aria-pressed="${l === nodeLabel}">${l ? esc(l) + ' · ' + nodes.filter((n) => (n.labels || []).includes(l)).length : 'All nodes'}</button>`).join('')}</div>` : '';
  const upd = nodes.filter((n) => n.online && n.update_to), banner = updateBannerOn(nodes);
  const bannerHTML = banner ? `<div class="upd-banner"><span aria-hidden="true">⬆</span><span class="nv-grow"><b>${upd.length} nodes</b> run an older version; <b>v${esc(upd[0].update_to)}</b> is available.</span>
    ${nodeChip === 'update' ? '' : '<button class="small" data-act="node-chip" data-chip="update">Show them</button>'}<a class="small-link" href="#/updates">Updates page</a></div>` : '';
  if (nodeView !== 'grid' || !nodeById(nodeDrawer)) nodeDrawer = null;
  loadNodeHistory();
  renderNodeFilter(nodes, rows, labels, dense);
  setHTML('node-summary', clusterSummary(nodes));
  const body = !shown.length ? `<div class="empty u16">No nodes match. <button class="small" data-act="node-clear">Clear filters</button></div>`
    : nodeView === 'list' ? nodeListView(shown, dense, banner) : nodeView === 'detail' ? nodeDetailView(shown) : nodeGridView(shown, { dense, hideUpd: banner, attentionFirst: nodeSort === 'status' });
  setHTML('nodes', chips + toolRecBanners() + bannerHTML + (filtering && shown.length ? `<div class="sub nf-count">Showing ${shown.length} of ${nodes.length} nodes · <a href="#" data-act="node-clear">Clear filters</a></div>` : '') + body);
  renderNodeDrawer(shown);
}

// Grid details panel: clicking a card opens its full node card (the same nodeCard the
// List and Detailed list show) in a panel on the right, so the grid stays in view.
// Not modal: clicking another card switches the panel to that node.
function openNodeDrawer(id) {
  const was = nodeDrawer;
  nodeDrawer = nodeDrawer === id ? null : id;
  renderNodes();
  if (nodeDrawer && nodeDrawer !== was) { const d = $('node-drawer'); d.scrollTop = 0; const x = d.querySelector('.nd-x'); if (x) x.focus({ preventScroll: true }); }
}
function closeNodeDrawer() {
  const id = nodeDrawer;
  if (!id) return;
  nodeDrawer = null; renderNodes();
  const card = document.querySelector(`.gc[data-id="${CSS.escape(id)}"] button[data-act="node-detail"]`);
  if (card) card.focus({ preventScroll: true });
}
function renderNodeDrawer(shown) {
  const d = $('node-drawer');
  const n = nodeDrawer && nodeById(nodeDrawer);
  d.classList.toggle('hidden', !n);
  document.body.classList.toggle('nd-open', !!n);
  if (!n) { setHTML(d, ''); return; }
  const f = nodeFacts(n), h = f.h, [st, cls] = nodeStatus(n, f);
  // Previous and next follow the order of the cards in the grid.
  const order = [...document.querySelectorAll('#nodes .gc[data-id]')].map((c) => c.dataset.id).filter((id, i, a) => a.indexOf(id) === i);
  const at = order.indexOf(n.id), prev = order[at - 1], next = order[at + 1];
  const nav = (id, label, txt) => `<button class="small" data-act="node-drawer-go" data-id="${id || ''}" ${id ? '' : 'disabled'} title="${label}" aria-label="${label}">${txt}</button>`;
  setHTML(d, `<div class="nd-head"><span class="dot ${f.dot}"></span><div class="nv-min nv-grow"><div class="nd-title"><span class="node-name" title="${esc(n.id)}">${esc(nodeName(n))}</span><span class="nv-pill nv-p-${cls}"><i></i>${st}</span></div>
      <div class="sub nv-ell">${h.agent_version ? 'v' + esc(h.agent_version) + ' · ' : ''}${esc(h.os ? h.os + '/' + h.arch : 'unknown')}${shown.length > 1 && at >= 0 ? ` · ${at + 1} of ${order.length}` : ''}</div></div>
      ${order.length > 1 ? nav(prev, 'Previous node', '‹') + nav(next, 'Next node', '›') : ''}
      <button class="small nd-x" data-act="node-drawer-close" title="Close (Esc)" aria-label="Close node details">✕</button></div>
    <div class="nd-body">${nodeCard(n)}${nodeJobs(n)}</div>`);
}
document.addEventListener('keydown', (e) => {
  // Dialogs opened from the panel (Load model, Logs…) close first; they handle their own Escape.
  if (e.key !== 'Escape' || !nodeDrawer || document.querySelector('.overlay:not(.hidden)')) return;
  closeNodeDrawer();
});

const toolName = (k) => ({ go: 'Go', chromium: 'Chromium', openscad: 'OpenSCAD' }[k] || k);
const recHidden = {};
// "No node has X" notices with a recommended node and one Install button (controller_toolinstall.go).
function toolRecBanners() {
  return (state.tool_recs || []).filter((r) => {
    if (recHidden[r.tool] === undefined) { try { recHidden[r.tool] = !!localStorage.getItem('outpost-rec-' + r.tool); } catch (e) { recHidden[r.tool] = false; } }
    return !recHidden[r.tool];
  }).map((r) => {
    const busy = Object.values(state.ops || {}).some((o) => o && o.op === 'toolchain' && o.target === r.tool && !['done', 'ready', 'failed'].includes(o.state));
    if (!r.nodes || !r.nodes.length) return `<div class="upd-banner"><span class="nv-grow"><b>No node has ${esc(r.label)}.</b> ${esc(r.why)} ${esc(r.reason || '')}</span><button class="small" data-act="rec-dismiss" data-tool="${esc(r.tool)}">Not now</button></div>`;
    const opts = r.nodes.map((c, i) => `<option value="${esc(c.id)}">${esc(c.name)}${i ? '' : ' (recommended)'}</option>`).join('');
    return `<div class="upd-banner"><span class="nv-grow"><b>No node has ${esc(r.label)}.</b> ${esc(r.why)} <span class="muted">Download ${esc(r.size)}.</span></span>
      <select data-rec-node="${esc(r.tool)}" aria-label="Node to install ${esc(r.label)} on" class="rec-sel">${opts}</select>
      <button class="small primary" data-act="rec-install" data-tool="${esc(r.tool)}"${busy ? ' disabled' : ''}>${busy ? 'Installing…' : 'Install ' + esc(r.label)}</button>
      <button class="small" data-act="rec-dismiss" data-tool="${esc(r.tool)}">Not now</button></div>`;
  }).join('');
}

function opView(n) {
  const o = state.ops && state.ops[n.id];
  if (!o || !o.op) return '';
  const age = (Date.now() - new Date(o.updated)) / 1000;
  const what = { runtime: 'llama.cpp', toolchain: 'Installing ' + toolName(o.target), 'model.push': 'Copying model from controller', 'model.fetch': 'Downloading from Hugging Face', rpc: 'Lending memory', 'model.load': 'Loading model', 'model.unload': 'Unloading', 'model.remove': 'Removing model', 'model.cleanup': 'Cleaning up models' }[o.op] || o.op;
  if (o.op === 'model.cleanup' && o.state === 'done') return age < 600 && o.detail ? `<div class="sub u17">Cleanup: ${esc(o.detail)}</div>` : '';
  const lf = o.last_failure && (Date.now() - new Date(o.last_failure.at)) < 3600000 ? o.last_failure : null;
  if (lf) {
    // One steady message: it names the failed step, counts the tries and does not change while retries run.
    const lwhat = { runtime: 'llama.cpp', 'model.load': 'Loading model', 'model.push': 'Copying model from controller', 'model.fetch': 'Downloading from Hugging Face' }[lf.op] || lf.op;
    const tries = lf.count > 1 ? ` (${lf.count} tries, last ${new Date(lf.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })})` : '';
    return `<div class="note bad u17">${esc(lwhat)} failed${tries}: ${esc(lf.error)}</div>`;
  }
  if (o.state === 'failed') return age < 3600 ? `<div class="note bad u17">${esc(what)} failed: ${esc(o.error)}${o.hint === 'libs' && n.online ? ` <button class="small" data-act="install-tool" data-tool="${esc(o.target)}" data-libs="1" data-id="${n.id}">Install libraries with apt</button>` : ''}</div>` : '';
  if (['done', 'ready'].includes(o.state)) return '';
  const pct = o.bytes_total ? 100 * o.bytes_done / o.bytes_total : (o.state === 'loading' ? 100 : 0);
  return `<div class="prog u6"><span>${esc(what)}${o.target ? ' · ' + esc(o.target) : ''}: ${esc(o.state)}${o.bytes_total ? ` ${bytes(o.bytes_done)} of ${bytes(o.bytes_total)}` : '…'}</span>${bar(pct, 'good')}</div>`;
}

// "Recommended for this node" row: the best coding model that fits, one click to load.
function opActive(n) {
  const o = state.ops && state.ops[n.id];
  return !!(o && o.op && !['done', 'ready', 'failed'].includes(o.state) && (Date.now() - new Date(o.updated)) < 600000);
}
function recRow(n, loaded) {
  const rec = n.online && models && models.recommended && models.recommended[n.id];
  if (!rec || (loaded && rec.name === loaded) || n.lending_to || opActive(n)) return '';
  const where = rec.in_library ? '' : ' · adds to library';
  return `<div class="rec-row"><span class="pill st-ready">Recommended</span> <b>${esc(rec.label)}</b>
    <button class="small primary" data-act="load-rec" data-id="${n.id}" data-name="${esc(rec.name)}" data-lib="${rec.in_library ? 1 : 0}">${loaded ? 'Switch to this' : 'Load'}</button>
    ${rec.est_tok_s ? `<span class="pill ${rec.speed_low ? 'st-warn' : 'st-ready'}">~${rec.est_tok_s < 10 ? rec.est_tok_s.toFixed(1) : Math.round(rec.est_tok_s)} tok/s</span>` : ''}
    <div class="sub wrap u18">${esc(rec.reason)} · ${bytes(rec.bytes)}${where}</div>
    ${rec.speed_note ? `<div class="sub wrap u18${rec.speed_low ? ' speed-low' : ''}">${esc(rec.speed_note)}</div>` : ''}</div>`;
}
function hoursText(w) {
  if (!w) return 'any time';
  const d = !w.days || !w.days.length ? 'every day' : w.days.length === 5 && [1, 2, 3, 4, 5].every((x) => w.days.includes(x)) ? 'weekdays' : w.days.length === 2 && w.days.includes(0) && w.days.includes(6) ? 'weekends' : [...w.days].sort((a, b) => ((a + 6) % 7) - ((b + 6) % 7)).map((x) => DAYS[x]).join(', ');
  return `${w.from}–${w.to}, ${d}`;
}
const labelChips = (n) => (n.labels || []).length ? `<div class="label-row">${n.labels.map((l) => `<span class="label-chip">${esc(l)}</span>`).join('')}</div>` : '';
async function editLabels(id) {
  const n = nodeById(id);
  const v = await askText(`Labels for ${nodeName(n)}`, (n.labels || []).join(', '), { body: 'Separated by commas, for example gpu, fast, office. A task or project can ask for one label.' });
  if (v === null) return;
  try { await post(`/api/nodes/${id}/settings`, { labels: v.split(',').map((x) => x.trim()).filter(Boolean) }); toast('Labels saved'); } catch (e) { toast(e.message); }
  refresh();
}
let hoursNode = null;
function openHours(id) {
  const n = nodeById(id); hoursNode = id;
  const w = n.hours || { from: '22:00', to: '07:00', days: [] };
  $('hours-node').textContent = nodeName(n);
  setHTML('hours-days', [1, 2, 3, 4, 5, 6, 0].map((d) => `<label class="toggle"><input type="checkbox" value="${d}" ${!w.days || !w.days.length || w.days.includes(d) ? 'checked' : ''}> ${DAYS[d]}</label>`).join(''));
  $('hours-from').value = w.from; $('hours-to').value = w.to;
  $('hours-err').classList.add('hidden');
  open('modal-hours');
}
async function saveHours(clear) {
  const days = [...document.querySelectorAll('#hours-days input:checked')].map((i) => +i.value);
  try {
    if (!clear && !days.length) throw new Error('Pick at least one day, or choose Work any time.');
    await post(`/api/nodes/${hoursNode}/settings`, clear ? { clear_hours: true } : { hours: { days, from: $('hours-from').value, to: $('hours-to').value } });
    close('modal-hours'); toast(clear ? 'Works any time' : 'Working hours saved'); refresh();
  } catch (e) { $('hours-err').textContent = e.message; $('hours-err').classList.remove('hidden'); }
}
$('hours-go').onclick = () => saveHours(false);
$('hours-clear').onclick = () => saveHours(true);
function nodeCard(n) {
  const h = n.hello || {}, hb = n.online ? n.heartbeat : null, cpu = h.cpu || {};
  const os = h.os ? `${h.os}/${h.arch}` : 'unknown';
  const ramPct = hb && hb.ram_total_bytes ? (100 * hb.ram_used_bytes / hb.ram_total_bytes) : 0;
  const gpus = (h.gpus || []).map((g, i) => {
    const used = hb && hb.vram_used_bytes ? hb.vram_used_bytes[i] : 0;
    const util = hb && hb.gpu_util_pct ? hb.gpu_util_pct[i] : 0;
    const temp = hb && hb.gpu_temp_c && hb.gpu_temp_c[i] > 0 ? hb.gpu_temp_c[i] : 0;
    return `<div class="metric"><span class="k">GPU</span>${bar(util)}<span class="v">${util || 0}% · ${bytes(used)}/${bytes(g.vram_bytes)}${temp ? ` · <span class="${temp >= 85 ? 'temp-hot' : ''}">${temp} °C</span>` : ''}</span></div>
      <div class="sub u19">${esc(g.name)}${g.backend ? '' : ' · not used by this llama.cpp build'}</div>`;
  }).join('');
  const status = n.online ? `online · ${n.running_jobs}/${n.max_jobs} jobs` : (n.last_seen ? `offline · seen ${ago(n.last_seen)}` : 'never connected');
  const paused = n.online && hb && hb.paused;
  const loaded = hb && hb.loaded_model, ready = hb && hb.inference_ready;
  const stored = (h.models || []);
  const inUse = new Set([loaded, n.load_prefs && n.load_prefs.name].filter(Boolean));
  const needsVulkan = h.os === 'linux' && (h.gpus || []).some((g) => g.vendor !== 'apple') && !h.vulkan_available;
  const modelBox = n.lending_to ? `<div class="model-box">
      <div class="row"><span class="dot ${hb && hb.rpc_endpoint ? 'on' : 'busy'}"></span><span class="grow">Lending memory to <b>${esc(n.lending_to)}</b>${hb && hb.rpc_endpoint ? '' : ' · starting'}</span></div>
      <div class="sub wrap u15">Its RAM${(h.gpus || []).length ? ' and VRAM' : ''} hold part of ${esc(n.lending_to)}'s model. Unload that model to get this node back.</div>
      ${opView(n)}
    </div>` : `<div class="model-box">
      <div class="row"><span class="dot ${ready ? 'on' : loaded ? 'busy' : ''}"></span><span class="grow">${loaded ? `<b>${esc(loaded)}</b> · ${hb.gpu_yielding ? '<span title="Unloaded because another program is using the GPU. It loads again after five quiet minutes.">GPU in use by another program</span>' : hb.idle_unloaded ? '<span title="Stopped after sitting idle to free memory. It loads again on the next request.">idle, unloaded</span>' : ready ? (hb.inference_busy ? 'working' : 'ready') : 'starting'}` : '<span class="muted">No model loaded</span>'}</span>
        ${n.online ? `${bestBtn(n)}<button class="small" data-act="load" data-id="${n.id}">${loaded ? 'Change' : 'Load model'}…</button>${loaded ? `<button class="small" data-act="unload" data-id="${n.id}">Unload</button>` : ''}` : ''}</div>
      ${recRow(n, loaded)}
      ${h.freed_note && Date.now() / 1000 - h.freed_at < 86400 ? `<div class="sub wrap u15" title="Removed automatically to make room for a model; it can be copied or downloaded again.">${esc(h.freed_note)} · ${ago(new Date(h.freed_at * 1000).toISOString())}</div>` : ''}
      ${n.online ? `<div class="sub u15">Unloads when idle: ${esc(n.idle_unload_effective || '1h')}${n.idle_unload ? '' : ' (default)'} · <a href="#" data-act="idle" data-id="${n.id}">change</a></div>` : ''}
      ${n.online ? `<div class="sub u15" title="When you use Auto in chat and no loaded model suits the message (coding, or a picture), chat may load a model here: one already on this disk first, then a download (it asks you first). It never removes a model without asking.">Let chat switch models here: ${n.chat_switch ? 'on' : 'off'} · <a href="#" data-act="chat-switch" data-id="${n.id}">${n.chat_switch ? 'turn off' : 'turn on'}</a></div>` : ''}
      ${n.online ? `<div class="sub u15" title="A task with Pick models for each node on may load its best-fit model here before work starts, and puts the previous one back afterwards.">Let tasks change my model: ${n.task_models ? 'on' : 'off'} · <a href="#" data-act="task-models" data-id="${n.id}">${n.task_models ? 'turn off' : 'turn on'}</a></div>` : ''}
      ${n.online && h.os === 'darwin' && h.arch === 'amd64' && h.no_avx2 && (h.gpus || []).length ? `<div class="sub u15" title="Runs models on the Mac's AMD GPUs through llama.cpp's Metal backend. Old cards (Metal 2) may not work; if the build keeps failing to start, Outpost switches back to the CPU build.">Use GPU (Metal, experimental): ${n.metal_gpu ? 'on' : 'off'} · <a href="#" data-act="metal" data-id="${n.id}">${n.metal_gpu ? 'turn off' : 'turn on'}</a>${h.metal_failed ? ` · <span style="color:var(--warn)">${esc(h.metal_failed)}; using the CPU build</span>` : ''}</div>` : ''}
      ${n.online ? `<div class="sub u15" title="Unloads the model while a game or other program keeps the GPU busy, and loads it again after five quiet minutes.">Yield GPU to other programs: ${n.yield_gpu ? 'on' : 'off'} · <a href="#" data-act="yield" data-id="${n.id}">${n.yield_gpu ? 'turn off' : 'turn on'}</a></div>` : ''}
      ${benchLine(n, loaded, ready)}
      ${n.pool_names && n.pool_names.length ? `<div class="sub wrap u15">Pooled with <b>${n.pool_names.map(esc).join(', ')}</b></div>` : ''}
      <div class="sub u15">${h.runtime ? `llama.cpp ${esc(h.runtime.build)} · ${esc(h.runtime.variant)}` : 'llama.cpp installs on first load'}${stored.length ? ' · stored: ' + stored.map((m) => `${esc(m.name)}${inUse.has(m.name) ? '' : ` <a href="#" data-act="rm-node-model" data-id="${n.id}" data-name="${esc(m.name)}" title="Delete from this node">×</a>`}`).join(', ') + (n.online ? ` · <a href="#" data-act="clean" data-id="${n.id}">clean up…</a>` : '') : ''}</div>
      ${hb && hb.llama_error && !ready ? `<div class="note bad u15">${esc(hb.llama_error)}</div>` : ''}
      ${needsVulkan ? `<div class="note warn u15">GPU found, but no Vulkan driver, so models run on CPU. On the node: <code>sudo apt install libvulkan1 mesa-vulkan-drivers</code>, then load the model again.</div>` : ''}
      ${opView(n)}
    </div>`;
  return `<div class="card">
    <div class="node-top"><span class="dot ${n.online ? 'on' : ''}"></span><span class="node-name" title="${esc(n.id)}">${esc(nodeName(n))}</span><span class="tag">${h.agent_version ? 'v' + esc(h.agent_version) + ' · ' : ''}${esc(os)}</span></div>
    <div class="sub">${esc(cpu.model || 'CPU')} · ${(cpu.sockets || 1) > 1 ? cpu.sockets + ' sockets · ' : ''}${cpu.cores || '?'}C/${cpu.threads || '?'}T</div>
    <div class="metric"><span class="k">CPU</span>${bar(hb ? hb.cpu_pct : 0)}<span class="v">${hb ? hb.cpu_pct.toFixed(1) + '%' : '—'}</span></div>
    <div class="metric"><span class="k">RAM</span>${bar(ramPct)}<span class="v">${hb ? bytes(hb.ram_used_bytes) + '/' + bytes(hb.ram_total_bytes) : '—'}</span></div>
    ${gpus}
    ${histBox(n)}
    ${paused ? `<div class="note warn u17 nv-note"><span class="nv-grow">${esc(pauseText(n))}</span>${canPause(n) ? `<button class="small" data-act="node-resume" data-id="${n.id}">Resume</button>` : ''}</div>` : ''}
    <div class="sub u17">${status}${hb && hb.swap_used_bytes ? ` · swap ${bytes(hb.swap_used_bytes)}` : ''}${n.addr ? ` · ${esc(n.addr.split(':')[0])}` : ''}${!n.online && n.can_wake ? ` · <a href="#" data-act="wake" data-id="${n.id}">Wake</a>` : ''}</div>
    <div class="sub u17 wrap">Labels: ${(n.labels || []).length ? (n.labels || []).map((l) => `<span class="label-chip">${esc(l)}</span>`).join(' ') : '<span class="muted">none</span>'} · <a href="#" data-act="labels" data-id="${n.id}">edit</a>
      · Works: ${n.hours ? esc(hoursText(n.hours)) + (n.off_hours ? ' <span class="nv-chip nv-info">off hours now</span>' : '') : 'any time'} · <a href="#" data-act="hours" data-id="${n.id}">set hours</a></div>
    ${modelBox}
    ${storageBox(n)}
    ${toolsBox(n)}
    <div class="node-foot">
      <label class="toggle" title="Allow this node to run shell commands (needed for tests)"><input type="checkbox" data-exec="${n.id}" ${n.exec_enabled ? 'checked' : ''}> exec</label>
      ${sandboxPicker(n)}
      <span class="u20"></span>
      ${n.online && !paused && canPause(n) ? pausePicker(n) : ''}
      <button class="small" data-act="restart" data-id="${n.id}" ${n.online && !n.restarting ? '' : 'disabled'} title="Restart the Outpost service on this node">${n.restarting ? 'Restarting…' : 'Restart'}</button>
      <button class="small" data-act="logs" data-id="${n.id}" ${n.online ? '' : 'disabled'} title="Show this node's recent log">Logs</button>
      <button class="small" data-act="rename" data-id="${n.id}">Rename</button>
      <button class="small danger" data-act="remove" data-id="${n.id}">Remove</button>
      <button class="small" data-act="run" data-id="${n.id}" ${n.online && n.exec_enabled ? '' : 'disabled'} title="${n.exec_enabled ? '' : 'Enable exec first'}">Run…</button>
    </div>
  </div>`;
}

// ---------- last hour (nodehist.go) ----------
// The controller keeps 30 s averages of each node's CPU, RAM, GPU, VRAM, GPU
// temperature and measured speed for an hour, in memory. The Nodes page fetches
// them every 30 s and draws sparklines on the Grid cards and in the full card.
let nodeHist = {}, nodeHistAt = 0, nodeHistBusy = false;
function loadNodeHistory(force) {
  if (nodeHistBusy || (!force && Date.now() - nodeHistAt < 30000)) return;
  nodeHistBusy = true; nodeHistAt = Date.now();
  api('/api/node-history').then((r) => { nodeHist = r.nodes || {}; if (page === 'nodes') renderNodes(); }).catch(() => {}).finally(() => { nodeHistBusy = false; });
}
const HIST_SERIES = {
  cpu: { label: 'CPU', unit: '%', max: () => 100 },
  ram: { label: 'RAM', unit: '%', max: () => 100 },
  gpu: { label: 'GPU', unit: '%', max: () => 100 },
  vram: { label: 'VRAM', unit: '%', max: () => 100 },
  temp: { label: 'GPU temp', unit: ' °C', max: () => 100, min: 20, hot: 85 },
  tps: { label: 'Speed', unit: ' tok/s', max: (pts) => Math.max(1, ...pts.map((p) => p.tps)) * 1.15 },
};
function histHas(pts, key) { return pts.some((p) => p[key] >= 0); }
// spark draws one series as an SVG line with a light fill, gaps where it was not
// reported. The time axis covers the data shown (at least 10 minutes, at most the
// hour), so the first minutes after a restart are not squeezed into one corner.
function spark(pts, key, h) {
  const s = HIST_SERIES[key], W = 120, lo = s.min || 0, hi = s.max(pts);
  const end = pts.length ? pts[pts.length - 1].t + 30 : 0, start = Math.min(pts.length ? pts[0].t : end, end - 600);
  const xs = (t) => Math.max(0, Math.min(W, (t + 15 - start) / (end - start) * W));
  const ys = (v) => h - 1 - Math.max(0, Math.min(1, (v - lo) / (hi - lo))) * (h - 2);
  let line = '', fill = '', run = [];
  const flush = () => {
    if (!run.length) return;
    line += run.map(([x, y], i) => `${i ? 'L' : 'M'}${x} ${y}`).join('');
    fill += `M${run[0][0]} ${h}` + run.map(([x, y]) => `L${x} ${y}`).join('') + `L${run[run.length - 1][0]} ${h}Z`;
    run = [];
  };
  for (const p of pts) {
    if (p[key] < 0) { flush(); continue; }
    run.push([xs(p.t).toFixed(1), ys(p[key]).toFixed(1)]);
  }
  flush();
  const hot = s.hot && pts.some((p) => p[key] >= s.hot) ? ' hot' : '';
  return `<svg class="spark" viewBox="0 0 ${W} ${h}" preserveAspectRatio="none" aria-hidden="true"><path class="spark-f${hot}" d="${fill}"></path><path class="spark-l${hot}" d="${line}"></path></svg>`;
}
function histLast(pts, key) { for (let i = pts.length - 1; i >= 0; i--) if (pts[i][key] >= 0) return pts[i][key]; return -1; }
const histFmt = (key, v) => v < 0 ? '—' : key === 'tps' ? v.toFixed(1) + HIST_SERIES[key].unit : Math.round(v) + HIST_SERIES[key].unit;
// Grid card: a strip of small sparklines (CPU, GPU or RAM, VRAM, speed).
function gaugeSparks(n) {
  const pts = nodeHist[n.id] || [];
  if (pts.length < 2 || !n.online) return '';
  const keys = ['cpu', histHas(pts, 'gpu') ? 'gpu' : 'ram', histHas(pts, 'vram') ? 'vram' : null, histHas(pts, 'tps') ? 'tps' : null].filter(Boolean);
  return `<div class="gc-sparks" title="Last hour, 30-second averages">${keys.map((k) => `<div class="gc-spark"><div class="gc-spark-k"><span>${HIST_SERIES[k].label}</span><b>${histFmt(k, histLast(pts, k))}</b></div>${spark(pts, k, 22)}</div>`).join('')}</div>`;
}
// Full node card: every series the node reports, with its range over the hour.
function histBox(n) {
  const pts = nodeHist[n.id] || [];
  if (!n.online) return '';
  if (pts.length < 2) return `<div class="hist-box"><div class="sub">Last hour: the charts start after a minute of heartbeats (kept in the controller's memory only).</div></div>`;
  const span = Math.round((pts[pts.length - 1].t - pts[0].t) / 60);
  const rows = Object.keys(HIST_SERIES).filter((k) => histHas(pts, k)).map((k) => {
    const vs = pts.map((p) => p[k]).filter((v) => v >= 0);
    return `<div class="hist-row"><span class="k">${HIST_SERIES[k].label}</span>${spark(pts, k, 30)}<span class="v">${histFmt(k, histLast(pts, k))}<span class="sub"> · ${histFmt(k, Math.min(...vs))}–${histFmt(k, Math.max(...vs))}</span></span></div>`;
  }).join('');
  return `<div class="hist-box"><div class="sub">Last ${span < 59 ? span + ' min' : 'hour'} · 30-second averages · latest, then the range</div>${rows}</div>`;
}

// Pause from the web UI (node_control.go applyPause). Older nodes only pause from their tray icon.
function canPause(n) { return n.online && ((n.hello || {}).features || []).includes('node.pause'); }
function pauseText(n) {
  const hb = n.heartbeat || {};
  const by = hb.paused_by === 'web' ? 'from the web UI' : 'from its tray icon';
  const until = hb.paused_until ? ` until ${new Date(hb.paused_until).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' })}` : '';
  const how = canPause(n) ? 'Resume here or with Accept new work in its tray icon.' : 'Choose Accept new work in its tray icon to resume.';
  return `Paused ${by}${until}: it takes no new work. ${how}`;
}
function pausePicker(n) {
  return `<select class="small" data-pause="${n.id}" aria-label="Pause this node" title="Stop sending new work to this node">
    <option value="">Pause…</option>
    <option value="drain">Finish current work, then pause</option>
    <option value="now">Pause now (running work moves to other nodes)</option>
    <option value="drain:60">Pause for 1 hour</option>
    <option value="drain:480">Pause for 8 hours</option></select>`;
}
async function pauseNode(id, v) {
  const [mode, mins] = v.split(':');
  const n = nodeById(id);
  if (mode === 'now' && n.running_jobs && !await askConfirm(`Pause ${nodeName(n)} now? Its ${n.running_jobs} running job(s) stop and go to other nodes.`)) { renderNodes(); return; }
  try { await post(`/api/nodes/${id}/pause`, { mode, minutes: parseInt(mins || '0', 10) }); toast(mode === 'resume' ? 'Resumed' : 'Paused'); refresh(); }
  catch (e) { toast(e.message); renderNodes(); }
}
function sandboxPicker(n) {
  if (!n.exec_enabled) return '';
  const h = n.hello || {};
  const eff = h.sandbox || '';
  const layers = (h.sandbox_layers || []).join(', ');
  const badge = !n.online ? '' : !eff ? 'Off (node is older than the sandbox)' : eff === 'off' ? 'Off' : (layers.includes('landlock') ? 'Sandboxed' : 'Partly') + ' · ' + eff;
  const sel = (v, t) => `<option value="${v}" ${(n.sandbox || '') === v ? 'selected' : ''}>${t}</option>`;
  return `<label class="toggle" title="${esc(layers ? 'Enforced here: ' + layers : 'How commands run on this node. Default follows Settings.')}">sandbox
    <select data-sandbox="${n.id}">${sel('', 'default')}${sel('off', 'off')}${sel('basic', 'basic')}${sel('strict', 'strict')}</select>
    ${badge ? `<span class="muted">${esc(badge)}</span>` : ''}</label>`;
}
async function setSandbox(id, v) {
  try { await post(`/api/nodes/${id}/settings`, { sandbox: v }); toast('Sandbox ' + (v || 'default')); refresh(); } catch (e) { toast(e.message); }
}

function toolsBox(n) {
  const h = n.hello || {};
  if (!n.exec_enabled || !h.shell) return n.exec_enabled ? '<div class="sub u17">Tools: unknown until the node updates</div>' : '';
  const tools = h.tools || [];
  const chips = tools.map((t) => `<span class="tag" title="${esc(t.path || '')}${t.managed ? ' (installed by Outpost)' : ''}">${esc(t.name)}${t.version ? ' ' + esc(t.version) : ''}</span>`).join(' ');
  const links = n.online ? ['go', 'chromium', 'openscad'].map((k) => {
    const t = tools.find((x) => x.name === k);
    if (t && !t.managed) return '';  // found on the system: detected, never touched
    return t ? `<a href="#" data-act="install-tool" data-tool="${k}" data-id="${n.id}">Reinstall ${toolName(k)}</a> <a href="#" data-act="remove-tool" data-tool="${k}" data-id="${n.id}">Remove</a>`
      : `<a href="#" data-act="install-tool" data-tool="${k}" data-id="${n.id}">Install ${toolName(k)}</a>`;
  }).filter(Boolean).join(' · ') : '';
  return `<div class="sub wrap u17">Runs commands in ${esc(h.shell)} · ${chips || 'no tools found'}
    ${links}</div>
${storageBox(n) ? '' : pkgStoreLine(n)}`;
}

// Storage Outpost uses on a node and its optional limit (node_storage.go). The
// bar is measured against the limit when one is set, else against the disk.
function storageFacts(n) {
  const u = n.online && n.heartbeat && n.heartbeat.storage;
  if (!u) return null;
  const lim = n.storage_limit_gb ? n.storage_limit_gb * 1024 ** 3 : 0;
  const cap = lim ? Math.max(lim, u.total) : u.total + (u.disk_free || 0);
  const pct = cap ? 100 * u.total / cap : 0;
  const text = lim ? `Outpost uses ${bytes(u.total)} of its ${bytes(lim)} limit here` : `Outpost uses ${bytes(u.total)} here; no limit, ${bytes(u.disk_free)} free on the disk`;
  return { u, lim, cap, pct, text, full: !!lim && u.total >= lim * 0.98 };
}

function storageBox(n) {
  const h = n.hello || {};
  if (!n.online) return '';
  if (!(h.features || []).includes('storage.limit')) return '<div class="sub u17">Storage: usage and a limit show once this node runs the latest version.</div>';
  const sf = storageFacts(n);
  if (!sf) return '<div class="stor"><span class="muted">Storage: measuring…</span></div>';
  const { u, lim, cap, pct } = sf;
  const w = (b) => cap ? Math.min(100, Math.round(100 * b / cap)) : 0;
  const seg = [['models', u.models, 'Models', n.online ? `<a href="#" data-act="clean" data-id="${n.id}">clean up</a>` : ''],
    ['pkgs', u.packages, 'Package cache', u.packages ? `<a href="#" data-act="clear-pkg" data-id="${n.id}" title="Verified Go modules and npm packages that later tasks reuse. Each task's own build cache is deleted when the task ends.">clear</a>` : ''],
    ['rt', u.runtimes, 'Toolchains, llama.cpp', ''],
    ['other', u.other, 'Agent, other', '']];
  const of = lim ? `of ${bytes(lim)} limit · ${Math.round(pct)}%${u.disk_free ? ' · disk has ' + bytes(u.disk_free) + ' free' : ''}` : `no limit · ${bytes(u.disk_free)} free on the disk`;
  const recent = h.limit_note && Date.now() / 1000 - h.limit_at < 86400;
  const gb = n.storage_limit_gb ? String(+n.storage_limit_gb.toFixed(1)) : '';
  return `<div class="stor"><div class="stor-g">
    <div class="stor-main">
      <div class="stor-h"><span class="stor-t">Storage</span><span class="stor-n">${bytes(u.total)}</span><span class="muted">${of}</span>${sf.full ? '<span class="nv-chip nv-warn">At limit</span>' : ''}</div>
      <div class="stor-bar${lim ? ' lim' : ''}" title="${esc(sf.text)}">${seg.map(([k, b]) => b ? `<i class="sg-${k}${sf.full && k === 'models' ? ' hot' : ''} w${w(b)}"></i>` : '').join('')}</div>
      <div class="stor-leg">${seg.map(([k, b, t, a]) => `<div><b class="sg-${k}"></b><span>${t}</span><em>${bytes(b)}</em>${a}</div>`).join('')}</div>
    </div>
    <div class="stor-c">
      <label class="stor-r" title="The most disk Outpost may use on this node: models, caches, toolchains, llama.cpp and the agent. Job folders are not counted; they are deleted after each job. Empty means no limit."><span>Limit</span><span class="stor-in"><input data-storage-limit="${n.id}" inputmode="decimal" value="${gb}" placeholder="none" aria-label="Storage limit in GB"> GB</span></label>
      <label class="stor-r" title="What happens when a model copy or download would go over the limit. Lowering the limit never deletes anything by itself."><span>When full</span><select data-storage-full="${n.id}" ${lim ? '' : 'disabled'} aria-label="When the storage limit is reached">
        <option value="" ${n.storage_full ? '' : 'selected'}>Remove old models</option><option value="refuse" ${n.storage_full === 'refuse' ? 'selected' : ''}>Refuse download</option></select></label>
    </div>
    ${recent ? `<div class="note warn stor-note"><span>${esc(h.limit_note)} · ${ago(new Date(h.limit_at * 1000).toISOString())}</span><button class="small" data-act="clean" data-id="${n.id}">Clean up…</button><button class="small" data-act="storage-raise" data-id="${n.id}">Raise limit</button></div>` : ''}
  </div></div>`;
}
async function setStorage(id, body, msg) {
  try { await post(`/api/nodes/${id}/settings`, body); toast(msg); refresh(); } catch (e) { toast(e.message); }
}

// The node's clean package store (verified Go modules and npm packages that
// tasks reuse; node_pkgcache.go). Each task's own build cache is deleted when it ends.
function pkgStoreLine(n) {
  if (!(n.hello && n.hello.features || []).includes('cache.scope')) return '';
  const b = (n.heartbeat || {}).package_store_bytes || 0;
  return `<div class="sub u17" title="Verified Go modules and npm packages that later tasks reuse. Each task's own build cache is deleted when the task ends.">Package cache: ${b ? bytes(b) : 'empty'}${b && n.online ? ` · <a href="#" data-act="clear-pkg" data-id="${n.id}">clear</a>` : ''}</div>`;
}

async function setExec(id, on) {
  try { await post(`/api/nodes/${id}/settings`, { exec_enabled: on }); toast(`exec ${on ? 'enabled' : 'disabled'}`); refresh(); }
  catch (e) { toast(e.message); }
}
async function renameNode(id) {
  const n = nodeById(id);
  const name = await askText('Node name', nodeName(n));
  if (name === null) return;
  await post(`/api/nodes/${id}/settings`, { name }).catch((e) => toast(e.message));
  refresh();
}
async function setNodeIdle(id) {
  const n = nodeById(id);
  const v = await askText('Unload this node\'s model after it has been idle for', n.idle_unload || '', { body: 'For example 30s, 5m, 1hr or 2d. 0 means never; leave it empty for the default in Settings.' });
  if (v === null) return;
  try { await post(`/api/nodes/${id}/settings`, { idle_unload: v }); toast(v.trim() ? 'Saved' : 'Using the default'); } catch (e) { toast(e.message); }
  refresh();
}
async function toggleYield(id) {
  const n = nodeById(id);
  try { await post(`/api/nodes/${id}/settings`, { yield_gpu: !n.yield_gpu }); toast(n.yield_gpu ? 'Off' : 'On: the model unloads while another program keeps the GPU busy'); } catch (e) { toast(e.message); }
  refresh();
}
async function toggleMetal(id) {
  const n = nodeById(id); if (!n) return;
  try { await post(`/api/nodes/${id}/settings`, { metal_gpu: !n.metal_gpu }); toast(n.metal_gpu ? 'Off: the CPU build is used from the next load' : 'On: the Metal build is installed on the next model load'); } catch (e) { toast(e.message); }
  refresh();
}
async function toggleChatSwitch(id) {
  const n = nodeById(id); if (!n) return;
  try { await post(`/api/nodes/${id}/settings`, { chat_switch: !n.chat_switch }); toast(n.chat_switch ? 'Off: chat only uses models already loaded here' : 'On: chat may load a model here for Auto (it asks before downloading or removing anything)'); } catch (e) { toast(e.message); }
  refresh();
}
async function toggleTaskModels(id) {
  const n = nodeById(id);
  try { await post(`/api/nodes/${id}/settings`, { task_models: !n.task_models }); toast(n.task_models ? 'Off: tasks leave this node\'s model alone' : 'On: a task may load its best-fit model here and puts the old one back after'); } catch (e) { toast(e.message); }
  refresh();
}
async function removeNode(id) {
  const n = nodeById(id);
  if (!await askConfirm(`Remove ${nodeName(n)}? Its key is revoked; if it is online it wipes its work directory and config and stops. Downloaded models stay on its disk.`)) return;
  await api(`/api/nodes/${id}`, { method: 'DELETE' }).catch((e) => toast(e.message));
  refresh();
}

// Clean-up dialog: pick stored models to delete from a node's disk
let cleanNodeId = null;
function openClean(id) {
  cleanNodeId = id;
  const n = nodeById(id);
  const keep = new Set([n.heartbeat && n.heartbeat.loaded_model, n.load_prefs && n.load_prefs.name].filter(Boolean));
  $('clean-node').textContent = nodeName(n);
  $('clean-err').classList.add('hidden');
  $('clean-partial').checked = false;
  $('clean-list').innerHTML = ((n.hello && n.hello.models) || []).map((m) => `<label class="toggle u21"><input type="checkbox" class="clean-cb" value="${esc(m.name)}" data-bytes="${m.bytes}" ${keep.has(m.name) ? 'disabled' : 'checked'}>
    ${esc(m.name)} <span class="muted">· ${bytes(m.bytes)}${keep.has(m.name) ? ' · in use, kept' : ''}</span></label>`).join('') || '<span class="muted">No models stored.</span>';
  $('clean-list').querySelectorAll('.clean-cb').forEach((cb) => cb.addEventListener('change', updateClean));
  $('clean-partial').onchange = updateClean;
  updateClean();
  open('modal-clean');
}
function updateClean() {
  const sel = [...document.querySelectorAll('.clean-cb:checked')];
  const total = sel.reduce((a, cb) => a + parseInt(cb.dataset.bytes, 10), 0);
  $('clean-go').textContent = sel.length ? `Delete ${sel.length} (${bytes(total)})` : 'Delete';
  $('clean-go').disabled = !sel.length && !$('clean-partial').checked;
}
$('clean-go').onclick = async () => {
  const names = [...document.querySelectorAll('.clean-cb:checked')].map((cb) => cb.value);
  try { await post(`/api/nodes/${cleanNodeId}/models/cleanup`, { names, incomplete: $('clean-partial').checked }); close('modal-clean'); toast('Cleaning up'); refresh(); }
  catch (e) { $('clean-err').textContent = e.message; $('clean-err').classList.remove('hidden'); }
};

// Load-model dialog
function openLoad(id, prefOverride) {
  loadNodeId = id;
  const n = nodeById(id);
  $('load-node').textContent = nodeName(n);
  $('load-err').classList.add('hidden');
  const ready = (models && models.models || []).filter((m) => m.status === 'ready');
  const pref = prefOverride || n.load_prefs || {};
  fillProfiles(n);
  $('load-model').innerHTML = ready.length ? ready.map((m) => `<option value="${esc(m.name)}" ${pref.name === m.name ? 'selected' : ''}>${esc(m.label || m.name)} · ${bytes(m.bytes_total)} · ${isDirect(m) ? 'node downloads' : 'from controller'}</option>`).join('')
    : '<option value="">No models in the library yet; add one on the Models tab</option>';
  $('load-ngl').value = pref.gpu_layers || '';
  $('load-threads').value = pref.threads || 0;
  $('load-extra').value = (pref.extra_args || []).join(' ');
  $('load-cache').value = pref.cache_type || '';
  $('load-slots').value = String(pref.parallel > 1 ? pref.parallel : 0);
  $('load-fa').value = pref.flash_attn || '';
  const pm = pref.cpu_moe || '';
  $('load-moe').value = pm === '' || pm === 'all' ? pm : 'n';
  $('load-moe-n').value = pm && pm !== 'all' ? pm : 20;
  loadDraftPref = pref.draft || '';
  $('load-draft').innerHTML = '';
  const helpers = state.nodes.filter((x) => x.id !== id && !(x.pool_names && x.pool_names.length));
  const inPool = new Set(n.pool || []);
  $('load-pool').innerHTML = helpers.length ? helpers.map((x) => {
    const hx = x.hello || {}, vram = (hx.gpus || []).filter((g) => g.vendor !== 'apple').reduce((a, g) => a + g.vram_bytes, 0);
    const busy = x.lending_to && x.lending_to !== nodeName(n);
    const own = x.heartbeat && x.heartbeat.loaded_model;
    return `<label class="toggle u21"><input type="checkbox" class="pool-cb" value="${x.id}" ${inPool.has(x.id) ? 'checked' : ''} ${x.online && !busy ? '' : 'disabled'}>
      ${esc(nodeName(x))} <span class="muted">· ${bytes(hx.ram_bytes)} RAM${vram ? ' + ' + bytes(vram) + ' VRAM' : ''}${!x.online ? ' · offline' : busy ? ' · lending to ' + esc(x.lending_to) : own ? ' · stops its own model (' + esc(own) + ')' : ''}</span></label>`;
  }).join('') : '<span class="muted">No other nodes.</span>';
  $('load-pool-wrap').classList.toggle('hidden', !state.pooling_enabled);
  $('load-pool-wrap').open = inPool.size > 0;
  $('load-pool').querySelectorAll('.pool-cb').forEach((cb) => cb.addEventListener('change', () => onLoadModelChange(parseInt($('load-ctx').value, 10))));
  onLoadModelChange(pref.ctx);
  const tgt = n.target_tps || 8;
  tpsSet('load-tps', tgt);
  setHTML('load-tps-hint', '');
  api(`/api/nodes/${id}/load-options`).then((r) => { if (id === loadNodeId) setHTML('load-tps-hint', tpsHint(r.options, r.target_tps || tgt)); }).catch(() => {});
  open('modal-load');
}
// Memory available to a load on node n, including ticked pool helpers.
function loadMem(n) {
  let vram = (n.hello.gpus || []).filter((g) => g.vendor !== 'apple').reduce((a, g) => a + g.vram_bytes, 0);
  let ram = n.hello.ram_bytes || 0;
  const pool = [...document.querySelectorAll('.pool-cb:checked')].map((cb) => nodeById(cb.value)).filter(Boolean);
  for (const p of pool) {
    const hp = p.hello || {};
    ram += hp.ram_bytes || 0;
    vram += (hp.gpus || []).filter((g) => g.vendor !== 'apple').reduce((a, g) => a + g.vram_bytes, 0);
  }
  return { vram, ram, pool };
}
// KV cache estimate. With the model's GGUF header (m.shape, read by the controller) it uses the real layer and
// head counts; otherwise the rule of thumb from estimateModelBytes in model_fit.go.
// The cache type and parallel slots scale it: each slot keeps its own full context.
const KV_FACTOR = { '': 1, q8_0: 34 / 64, q4_0: 18 / 64 };
function kvEstimate(m, ctx) {
  const s = m.shape;
  const f = (KV_FACTOR[$('load-cache').value] || 1) * Math.max(1, parseInt($('load-slots').value, 10) || 1);
  if (s) return { kv: f * (s.full_bytes_per_token * ctx + s.swa_bytes_per_token * Math.min(ctx, s.swa_window || ctx)), exact: true };
  return { kv: f * m.bytes_total / (1 << 30) * (12 << 10) * ctx, exact: false };
}
// Experts in system RAM (--cpu-moe / --n-cpu-moe): the setting as sent to the node, and the bytes it moves.
function moeMode() {
  const v = $('load-moe').value;
  if (!v) return '';
  if (v === 'all') return 'all';
  const n = parseInt($('load-moe-n').value, 10);
  return n >= 1 ? String(Math.min(n, 999)) : '';
}
function moeBytes(m) {
  const s = m && m.shape, mode = moeMode();
  if (!s || !s.expert_bytes || !mode) return 0;
  if (mode === 'all' || !s.layers || parseInt(mode, 10) >= s.layers) return s.expert_bytes;
  return Math.floor(s.expert_bytes * parseInt(mode, 10) / s.layers);
}
function showMoe(m) {
  const on = !!(m && m.shape && m.shape.expert_bytes);
  $('load-moe-wrap').classList.toggle('hidden', !on);
  if (!on) $('load-moe').value = '';
  $('load-moe-n-wrap').classList.toggle('hidden', !on || $('load-moe').value !== 'n');
}
// Weights plus the draft model plus a fixed allowance for buffers.
function loadWeights(m) {
  const d = (models && models.models || []).find((x) => x.name === $('load-draft').value);
  return m.bytes_total + (d ? d.bytes_total : 0) + (600 << 20);
}
// Draft models: ready library models under half the main model's size.
let loadDraftPref = '';
function fillDrafts(m) {
  const cur = $('load-draft').value || loadDraftPref;
  const opts = (models && models.models || []).filter((x) => x.status === 'ready' && m && x.name !== m.name && x.bytes_total < m.bytes_total / 2)
    .sort((a, b) => a.bytes_total - b.bytes_total);
  setHTML('load-draft', `<option value="">None (default)</option>${opts.map((x) => `<option value="${esc(x.name)}" ${x.name === cur ? 'selected' : ''}>${esc(x.label || x.name)} · ${bytes(x.bytes_total)}</option>`).join('')}`);
}
// "Largest that fits": the biggest context whose estimate stays within the memory the node would use,
// with 10% spare because the estimate is not exact.
function largestFit() {
  const m = (models && models.models || []).find((x) => x.name === $('load-model').value);
  const n = nodeById(loadNodeId);
  if (!m || !n || !n.hello || !m.bytes_total) return 0;
  const { vram, ram } = loadMem(n), moe = vram ? moeBytes(m) : 0, w = loadWeights(m) - moe;
  let room;
  if (moe) room = moe <= ram * 0.75 ? vram * 0.9 : 0; // experts live in RAM; the rest and the cache must fit the GPU
  else if (vram && w < vram * 0.9) room = vram * 0.9;
  else if (vram) room = (vram + ram) * 0.8;
  else room = ram * 0.75;
  const max = CTX_STOPS[CTX_STOPS.length - 1] || 131072;
  if (w + kvEstimate(m, 2048).kv > room) return 0;
  let lo = 2048, hi = max;
  while (hi - lo > 256) { const mid = Math.floor((lo + hi) / 2); if (w + kvEstimate(m, mid).kv <= room) lo = mid; else hi = mid; }
  return lo >= max - 256 ? max : Math.floor(lo / 1024) * 1024;
}
$('load-ctx-fit').onclick = () => {
  const v = largestFit();
  if (!v) { toast('Even the smallest context does not fit with these settings'); return; }
  $('load-ctx').value = v; syncCtxRange(); updateCtxHint();
};
['load-cache', 'load-slots', 'load-draft'].forEach((id) => $(id).addEventListener('change', updateCtxHint));
['load-moe', 'load-moe-n'].forEach((id) => $(id).addEventListener('input', () => onLoadModelChange(parseInt($('load-ctx').value, 10))));
function updateCtxHint() {
  const el = $('load-ctx-hint');
  const m = (models && models.models || []).find((x) => x.name === $('load-model').value);
  const n = nodeById(loadNodeId), ctx = parseInt($('load-ctx').value, 10) || 0;
  if (!m || !n || !n.hello || !m.bytes_total || !ctx) { el.classList.add('hidden'); return; }
  const { kv, exact } = kvEstimate(m, ctx), need = loadWeights(m) + kv;
  const { vram, ram } = loadMem(n), moe = vram ? moeBytes(m) : 0;
  let txt = `Context cache at ${ctx.toLocaleString()} tokens: about ${bytes(kv)}. Weights plus cache need roughly ${bytes(need)}.`, cls = 'hint';
  if (moe) {
    const gpu = need - moe;
    txt += ` With experts in system RAM: about ${bytes(gpu)} on the GPU and ${bytes(moe)} in RAM.`;
    if (gpu > vram * 0.92) { txt += ' The GPU part still does not fit: lower the context size.'; cls = 'hint warn-text'; }
    else if (moe > ram * 0.75) { txt += ' The experts do not fit in this node\'s RAM.'; cls = 'hint warn-text'; }
  } else if (vram && need > vram * 0.92) { txt += need > (vram + ram) * 0.9 ? ' More than this node can hold: lower the context size.' : ' More than the GPU memory, so part will run from system RAM (slower).'; cls = 'hint warn-text'; }
  else if (!vram && need > ram * 0.9) { txt += ' More than this node can hold: lower the context size.'; cls = 'hint warn-text'; }
  else if (!vram && need > ram * 0.75) { txt += ' Close to the RAM limit: lower the context size.'; cls = 'hint warn-text'; }
  const train = m.shape && m.shape.train_ctx;
  if (train && ctx > train) { txt += ` The model was trained for ${train.toLocaleString()} tokens; more may give poor answers.`; cls = 'hint warn-text'; }
  const cacheName = { '': '16-bit', q8_0: '8-bit', q4_0: '4-bit' }[$('load-cache').value] || '16-bit';
  const slots = parseInt($('load-slots').value, 10) || 1;
  txt += exact ? ` Worked out from the model file (${cacheName} cache${slots > 1 ? `, ${slots} calls at once, each with the full context` : ''}).` : ' Rough estimate: the model file has not been read yet.';
  el.className = cls; el.textContent = txt;
}
function onLoadModelChange(ctx) {
  const m = (models && models.models || []).find((x) => x.name === $('load-model').value);
  const n = nodeById(loadNodeId);
  $('load-ctx').value = ctx || (m && m.ctx) || 8192;
  fillDrafts(m);
  setCtxStops(m); syncCtxRange(); updateCtxHint();
  const fit = $('load-fit');
  if (!m || !n || !n.hello) { fit.classList.add('hidden'); return; }
  const { vram, ram, pool } = loadMem(n);
  const size = m.bytes_total || 0;
  showMoe(m);
  const moe = vram ? moeBytes(m) : 0;
  let msg = '', cls = 'note';
  if (moe) {
    if (moe > ram * 0.75 || size - moe > vram * 0.9) { msg = `Even with experts in system RAM (${bytes(moe)}), the rest (${bytes(size - moe)}) or the experts do not fit in ${size - moe > vram * 0.9 ? 'VRAM' : 'RAM'}.`; cls = 'note bad'; }
    else msg = `Attention and shared weights (${bytes(size - moe)}) stay in ${bytes(vram)} of VRAM; ${bytes(moe)} of experts go to system RAM. Slower than all-VRAM, usually much faster than a plain spill.`;
  } else if (size > ram + vram) { msg = `This model (${bytes(size)}) is larger than this node's RAM${vram ? ' plus VRAM' : ''} (${bytes(ram + vram)}). It will not load.`; cls = 'note bad'; }
  else if (vram && size <= vram * 0.9) msg = `Fits in this node's ${bytes(vram)} of VRAM: fast.`;
  else if (vram) msg = `Larger than this node's ${bytes(vram)} of VRAM; llama.cpp keeps the rest in system RAM. Slower, but works.`;
  else if (size > ram * 0.8) { msg = `Uses most of this node's ${bytes(ram)} of RAM; expect swapping.`; cls = 'note warn'; }
  else msg = `Runs on CPU with ${bytes(ram)} of RAM.`;
  if (pool.length) msg = `Pooled with ${pool.map(nodeName).join(', ')}: ${bytes(ram)} RAM${vram ? ' + ' + bytes(vram) + ' VRAM' : ''} in total. ` + msg.replace(/this node's/g, 'the pool\'s');
  const onNode = (n.hello.models || []).some((x) => x.name === m.name);
  const free = n.hello.models_free_bytes || 0;
  if (!onNode) {
    msg += isDirect(m) ? ` The node downloads ${bytes(size)} from Hugging Face first (it needs internet access).` : ` The controller copies ${bytes(size)} to the node first.`;
    if (free && size > free) { msg += ` The node has only ${bytes(free)} free, so this will fail.`; cls = 'note bad'; }
  } else msg += ' Already on this node.';
  fit.className = cls; fit.textContent = msg;
}
// Context size slider: stops are powers of two from 2048 up to the model's trained context (from its GGUF
// header) or 131072 when that is not known; the number box beside it keeps exact values.
let CTX_STOPS = [];
function setCtxStops(m) {
  const max = (m && m.shape && m.shape.train_ctx) || 131072;
  CTX_STOPS = [];
  for (let v = 2048; v < max; v *= 2) CTX_STOPS.push(v);
  CTX_STOPS.push(Math.max(max, 2048));
  const r = $('load-ctx-range');
  r.max = CTX_STOPS.length - 1;
  const k = (v) => v >= 1024 ? Math.round(v / 1024) + 'K' : String(v);
  $('load-ctx-max').textContent = k(CTX_STOPS[CTX_STOPS.length - 1]) + (m && m.shape && m.shape.train_ctx ? ' (model max)' : '');
}
function syncCtxRange() {
  const v = parseInt($('load-ctx').value, 10) || 0;
  let best = 0;
  CTX_STOPS.forEach((s, i) => { if (Math.abs(s - v) < Math.abs(CTX_STOPS[best] - v)) best = i; });
  $('load-ctx-range').value = best;
}
$('load-ctx-range').addEventListener('input', () => { $('load-ctx').value = CTX_STOPS[parseInt($('load-ctx-range').value, 10)]; updateCtxHint(); });
$('load-ctx').addEventListener('input', () => { syncCtxRange(); updateCtxHint(); });

// Target speed slider (Load dialog and Pick best dialog): the tok/s the owner wants from a node. It steers which
// model is recommended (best_fit.go) and is saved per node. Stops mirror the mockup; the number box keeps exact values.
const TPS_STOPS = [2, 4, 6, 8, 12, 16, 24, 32];
const tpsFmt = (v) => v < 10 ? v.toFixed(1) : String(Math.round(v));
function tpsWire(id, onChange) {
  const r = $(id + '-r'), num = $(id + '-n');
  let t = null;
  const fire = () => { clearTimeout(t); t = setTimeout(() => onChange(parseFloat(num.value) || 8), 250); };
  r.oninput = () => { num.value = TPS_STOPS[+r.value]; fire(); };
  num.oninput = () => { tpsSet(id, parseFloat(num.value) || 8, true); fire(); };
  $(id + '-reset').onclick = () => { tpsSet(id, 8); onChange(8); };
}
function tpsSet(id, v, keepBox) {
  let b = 0;
  TPS_STOPS.forEach((s, i) => { if (Math.abs(s - v) < Math.abs(TPS_STOPS[b] - v)) b = i; });
  $(id + '-r').value = b;
  if (!keepBox) $(id + '-n').value = v;
}
// The sentence under a slider: what the target picks, or why nothing meets it.
function tpsHint(opts, target) {
  const top = (opts || []).find((o) => o.rank === 1);
  if (!top) return 'No coding model in the library or catalog fits this node.';
  if (!top.est_tok_s) return `Picks the biggest coding model that fits: <b>${esc(top.label)}</b>. It runs entirely in VRAM, so the target does not limit it.`;
  const tps = tpsFmt(top.est_tok_s);
  if (top.est_tok_s >= target) return `Picks the biggest coding model estimated at <b>${target}</b> tok/s or more: <b>${esc(top.label)}</b>, ~${tps} tok/s (an estimate, not yet measured). Saved for this node.`;
  const lead = top.est_tok_s >= target / 2 ? `No model is estimated at ${target} tok/s here; the biggest at half that or better is <b>${esc(top.label)}</b>, ~${tps} tok/s.` : `No model is estimated at ${target} tok/s here; the fastest is <b>${esc(top.label)}</b>, ~${tps} tok/s.`;
  return `<span class="speed-low">${lead}</span> A node with a GPU would do better.`;
}
async function tpsSave(id, v) {
  try { await post(`/api/nodes/${id}/settings`, { target_tps: v }); } catch (e) { toast(e.message); }
}
function loadTpsRefresh(target, save = true) {
  const nid = loadNodeId;
  api(`/api/nodes/${nid}/load-options?target_tps=${target}`).then((r) => {
    if (nid !== loadNodeId) return;
    const sel = (r.options || []).find((o) => o.name === $('load-model').value);
    const own = sel && sel.est_tok_s ? ` The selected model is estimated at ~${tpsFmt(sel.est_tok_s)} tok/s.` : '';
    setHTML('load-tps-hint', tpsHint(r.options, target) + own);
  }).catch(() => {});
  if (save) tpsSave(nid, target);
}
tpsWire('load-tps', loadTpsRefresh);
$('load-model').addEventListener('change', () => { onLoadModelChange(); loadTpsRefresh(parseFloat($('load-tps-n').value) || 8, false); });
$('load-go').onclick = async () => {
  $('load-err').classList.add('hidden');
  const body = { name: $('load-model').value, ctx: parseInt($('load-ctx').value, 10) || 0, gpu_layers: $('load-ngl').value,
    threads: parseInt($('load-threads').value, 10) || 0, extra_args: $('load-extra').value, cpu_moe: moeMode(),
    cache_type: $('load-cache').value, flash_attn: $('load-fa').value, draft: $('load-draft').value, parallel: parseInt($('load-slots').value, 10) || 0,
    pool: [...document.querySelectorAll('.pool-cb:checked')].map((cb) => cb.value) };
  if (!body.name) return;
  try { await post(`/api/nodes/${loadNodeId}/load`, body); close('modal-load'); toast('Setting up the model; progress shows on the node card'); refresh(); }
  catch (e) { $('load-err').textContent = e.message; $('load-err').classList.remove('hidden'); }
};

async function resetHealth(id) {
  if (!await askConfirm('Reset this node\'s health to green? Its recent disconnects and failed calls are forgotten.')) return;
  try { await post(`/api/nodes/${id}/health/reset`, {}); toast('Health reset'); } catch (e) { toast(e.message); }
  refresh();
}

// Saved load settings per node (load_profiles.go)
function fillProfiles(n) {
  const list = n.load_profiles || [];
  $('load-prof-wrap').classList.toggle('hidden', !list.length);
  $('load-prof').innerHTML = '<option value="">Choose…</option>' + list.map((p) => `<option value="${esc(p.name)}">${esc(p.name)}</option>`).join('');
}
$('load-prof').onchange = () => {
  const p = (nodeById(loadNodeId).load_profiles || []).find((x) => x.name === $('load-prof').value);
  if (p) { openLoad(loadNodeId, p.load); $('load-prof').value = p.name; }
};
$('load-prof-del').onclick = async () => {
  const name = $('load-prof').value;
  if (!name) return;
  try { await fetch(`/api/nodes/${loadNodeId}/profiles/${encodeURIComponent(name)}`, { method: 'DELETE', credentials: 'same-origin' }); await refresh(); fillProfiles(nodeById(loadNodeId)); toast('Deleted'); } catch (e) { toast(e.message); }
};
$('load-prof-save').onclick = async () => {
  const name = await askText('Name for these settings', '', { body: 'For example "32K, experts in RAM".' });
  if (!name || !name.trim()) return;
  const body = { name: name.trim(), model: $('load-model').value, ctx: parseInt($('load-ctx').value, 10) || 0, gpu_layers: $('load-ngl').value,
    threads: parseInt($('load-threads').value, 10) || 0, extra_args: $('load-extra').value, cpu_moe: moeMode(),
    cache_type: $('load-cache').value, flash_attn: $('load-fa').value, draft: $('load-draft').value, parallel: parseInt($('load-slots').value, 10) || 0 };
  try { await post(`/api/nodes/${loadNodeId}/profiles`, body); await refresh(); fillProfiles(nodeById(loadNodeId)); $('load-prof').value = body.name; toast('Saved'); } catch (e) { toast(e.message); }
};

// "Pick best" button and dialog: the top-ranked coding models for one node, best first, one click to load.
function bestBtn(n) {
  if (!n.online || n.lending_to) return '';
  return `<button class="small best-btn" data-act="best" data-id="${n.id}" title="Pick the best coding model for this node's memory, disk and speed"><span aria-hidden="true">★</span> Pick best</button>`;
}
let bestNode = null, bestOpts = [];
async function openBest(id) {
  const n = nodeById(id); bestNode = id;
  $('best-node').textContent = nodeName(n);
  $('best-err').classList.add('hidden');
  setHTML('best-list', '<div class="sub">Checking what fits…</div>');
  $('best-go').disabled = true;
  tpsSet('best-tps', n.target_tps || 8);
  open('modal-best');
  await bestLoad(id, 0);
}
tpsWire('best-tps', (t) => { bestLoad(bestNode, t); tpsSave(bestNode, t); });
// Fetch and draw the picks for a target speed (0 = the node's saved one).
async function bestLoad(id, target) {
  const n = nodeById(id);
  const loaded = !n.lending_to && n.heartbeat && n.heartbeat.loaded_model;
  $('best-err').classList.add('hidden');
  $('best-go').disabled = true;
  let r;
  try { r = await api(`/api/nodes/${id}/load-options${target ? '?target_tps=' + target : ''}`); } catch (e) { $('best-err').textContent = e.message; $('best-err').classList.remove('hidden'); return; }
  if (id !== bestNode) return;
  setHTML('best-tps-hint', tpsHint(r.options, target || r.target_tps || 8));
  bestOpts = (r.options || []).filter((o) => o.rank > 0).sort((a, b) => a.rank - b.rank);
  const h = n.hello || {}, g = (h.gpus || [])[0], sf = storageFacts(n);
  const ram = n.heartbeat && n.heartbeat.ram_total_bytes;
  const apple = g && g.vendor === 'apple';
  const mem = apple ? `Apple Silicon, ${bytes(ram || g.vram_bytes)} unified memory` : `${g ? bytes(g.vram_bytes) + ' VRAM, ' : 'no GPU, '}${ram ? bytes(ram) + ' RAM' : ''}`;
  const how = apple ? 'The biggest model that should reach your target speed on its GPU comes first.' : g ? 'The biggest model that reaches your target speed comes first, even when part of it sits in system RAM.' : 'On a CPU-only node the biggest model that reaches your target speed comes first.';
  $('best-hint').textContent = `Ranked for this node: ${mem}${sf ? (sf.lim ? `, ${bytes(Math.max(0, sf.lim - sf.u.total))} left under its storage limit` : `, ${bytes(sf.u.disk_free)} free disk`) : ''}. ${how} Memory other programs hold right now is left alone.`;
  if (!bestOpts.length) {
    setHTML('best-list', `<div class="note warn">${r.build_ok ? 'No coding model in the library or catalog fits this node\'s memory and disk, or is expected to reach about 3.5 tok/s on it. Lower the target speed, or choose a model yourself with Load model.' : 'This node has no llama.cpp build yet, so nothing can be loaded.'}</div>`);
    return;
  }
  const FITP = { vram: ['st-done', 'all in VRAM'], split: ['st-loading', 'VRAM + RAM'], ram: ['st-loading', 'CPU'] };
  setHTML('best-list', bestOpts.map((o, i) => {
    const cur = loaded && o.name === loaded;
    const where = o.on_node ? 'Already on this node, loads right away' : o.in_library ? `In the library; copies ${bytes(o.bytes)} to this node` : `Not downloaded yet; downloads ${bytes(o.bytes)} to this node`;
    return `<label class="best-row${i === 0 ? ' top' : ''}"><input type="radio" name="best-pick" value="${i}" ${i === 0 ? 'checked' : ''}>
      <div class="grow"><div class="best-head"><b>${esc(o.label)}</b>${i === 0 ? '<span class="pill st-ready">Best pick</span>' : ''}${cur ? '<span class="pill">Loaded now</span>' : ''}
        <span class="pill ${o.unified ? 'st-done' : FITP[o.fit][0]}">${o.unified ? 'GPU, unified memory' : FITP[o.fit][1]}</span>${o.est_tok_s ? `<span class="pill ${o.speed_low ? 'st-warn' : 'st-ready'}">~${o.est_tok_s < 10 ? o.est_tok_s.toFixed(1) : Math.round(o.est_tok_s)} tok/s</span>` : ''}</div>
        <div class="sub wrap">${esc(o.reason)}</div>
        <div class="sub wrap">${where}${o.speed_note ? ' · ' + esc(o.speed_note) : ''}</div></div></label>`;
  }).join('') + (loaded ? `<div class="sub best-foot">Loading a model here unloads <b>${esc(loaded)}</b> first.</div>` : ''));
  const sync = () => { const o = bestOpts[+document.querySelector('input[name=best-pick]:checked').value]; $('best-go').disabled = !!(loaded && o.name === loaded); $('best-go').textContent = loaded && o.name === loaded ? 'Already loaded' : loaded ? 'Switch to this model' : 'Load this model'; };
  document.querySelectorAll('input[name=best-pick]').forEach((x) => { x.onchange = sync; });
  sync();
  $('best-go').onclick = async () => { const o = bestOpts[+document.querySelector('input[name=best-pick]:checked').value]; close('modal-best'); await loadOnNode(bestNode, o.name, !o.in_library); };
  $('best-all').onclick = () => { close('modal-best'); openLoad(bestNode); };
}

// Filter bar controls (static markup in index.html).
$('nf-q').addEventListener('input', () => { nodeQuery = $('nf-q').value; renderNodes(); });
$('nf-q').addEventListener('keydown', (e) => { if (e.key === 'Escape' && nodeQuery) { nodeQuery = ''; $('nf-q').value = ''; renderNodes(); } });
$('nf-sort').addEventListener('change', () => {
  nodeSort = $('nf-sort').value;
  try { localStorage.setItem('outpost.nodeSort', nodeSort); } catch (e) { /* storage blocked */ }
  renderNodes();
});
