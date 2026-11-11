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
  };
}
const nvTps = (f, big) => f.tps == null ? '<span class="muted">—</span>' : big ? `<span class="nv-tps">${f.tps.toFixed(1)}</span><span class="nv-tpsu">tok/s</span>` : `<span class="nv-spd">${f.tps.toFixed(1)} tok/s</span>`;
const nvChips = (f) => f.issues.map((i) => `<span class="nv-chip nv-${i.lvl}" title="${esc(i.text)}">${esc(i.short)}</span>`).join(' ');

function nodeRow(n) {
  const f = nodeFacts(n), h = f.h;
  const fixIssue = f.issues.find((i) => i.act);
  const first = f.issues.find((i) => i.lvl !== 'off');
  const sub = first ? `${nvChips(f)} ${esc(first.text)}` : opActive(n) ? opView(n).replace(/<[^>]+>/g, ' ').trim() : n.online
    ? `${n.running_jobs}/${n.max_jobs} jobs${n.pool_names && n.pool_names.length ? ' · pooled with ' + n.pool_names.map(esc).join(', ') : ''} · exec ${n.exec_enabled ? (n.sandbox ? 'on, sandbox ' + n.sandbox : 'on') : 'off'}` : '';
  const action = fixIssue ? `<button class="small primary" data-act="${fixIssue.act}" data-id="${n.id}">${esc(fixIssue.fix)}</button>`
    : n.online && !n.lending_to ? `<button class="small" data-act="load" data-id="${n.id}">${f.loaded ? 'Change…' : 'Load model…'}</button>` : '';
  const open = nodeOpen.has(n.id);
  return `<div class="nv-row nv-s-${f.worst}${open ? ' open' : ''}" data-act="node-toggle" data-id="${n.id}" aria-expanded="${open}">
    <div class="nv-c1"><span class="dot ${f.dot}"></span><div class="nv-min"><b>${esc(nodeName(n))}</b><div class="sub nv-sub">${esc(h.os ? h.os + ' ' + h.arch : 'unknown')}${h.agent_version ? ' · v' + esc(h.agent_version) : ''}${n.addr ? ' · ' + esc(n.addr.split(':')[0]) : ''}</div>${labelChips(n)}</div></div>
    <div class="nv-min"><div class="nv-ell">${f.model}</div><div class="sub nv-sub nv-wrap">${sub}</div></div>
    <div>${n.online ? `<div class="nv-bars"><span>CPU</span>${bar(f.cpu)}<span>RAM</span>${bar(f.ram)}${f.vram != null ? `<span>VRAM</span>${bar(f.vram)}` : ''}</div>` : `<span class="sub">${n.last_seen ? 'Last seen ' + ago(n.last_seen) : 'Never connected'}</span>`}</div>
    <div class="r">${nvTps(f, true)}</div>
    <div class="nv-acts">${action}<button class="small" data-act="node-toggle" data-id="${n.id}" aria-expanded="${open}" title="Show or hide all details and controls for this node">${open ? 'Hide' : 'Details'}</button></div>
  </div>${open ? `<div class="nv-exp">${nodeCard(n)}</div>` : ''}`;
}

function nodeListView(nodes) {
  const groups = [
    ['Needs attention', nodes.filter((n) => { const w = nodeFacts(n).worst; return w === 'bad' || w === 'warn'; })],
    ['Healthy', nodes.filter((n) => { const w = nodeFacts(n).worst; return n.online && w !== 'bad' && w !== 'warn'; })],
    ['Offline', nodes.filter((n) => !n.online)],
  ];
  return groups.filter((g) => g[1].length).map(([t, ns]) => `<div class="nv-grp">${t} · ${ns.length}</div><div class="nv-box">${ns.map(nodeRow).join('')}</div>`).join('');
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
  const jobs = (state.jobs || []).filter((j) => j.node_id === sel.id || (!j.node_id && j.node_name === nodeName(sel))).slice(0, 6);
  return `<div class="nv-split"><div class="nv-list">${list}</div>
    <div class="nv-pane">${nodeCard(sel)}
      <div class="section-head"><h2>Recent jobs on ${esc(nodeName(sel))}</h2></div>
      ${jobs.length ? `<div class="table-wrap"><table><tbody>${jobs.map((j) => `<tr class="click" data-act="open-job" data-id="${j.id}"><td>${pill(j.status)}</td><td class="cmd">${esc(j.command)}</td><td>${ago(j.created)}</td></tr>`).join('')}</tbody></table></div>` : '<div class="empty">No recent jobs on this node.</div>'}
    </div></div>`;
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
function gaugeCard(n) {
  const f = nodeFacts(n), h = f.h, hb = f.hb, cpu = h.cpu || {};
  const [st, cls] = nodeStatus(n, f);
  const gpus = h.gpus || [];
  const g = gpus[0];
  const ramTot = hb && hb.ram_total_bytes;
  const notes = f.issues.filter((i) => i.lvl !== 'off').map((i) => `<div class="note ${i.lvl === 'info' ? '' : i.lvl} nv-note"><span class="nv-grow">${esc(i.text)}</span>${i.act ? `<button class="small" data-act="${i.act}" data-id="${n.id}">${esc(i.fix)}</button>` : ''}</div>`).join('');
  const btns = n.online && !n.lending_to ? `<button class="small" data-act="load" data-id="${n.id}">${f.loaded ? 'Change…' : 'Load model…'}</button>${f.loaded ? `<button class="small" data-act="unload" data-id="${n.id}">Unload</button>` : ''}` : n.can_wake ? `<button class="small" data-act="wake" data-id="${n.id}" title="Send a Wake-on-LAN packet">Wake</button>` : '';
  return `<div class="card gc nv-s-${f.worst}">
    <div class="node-top"><span class="node-name" title="${esc(n.id)}">${esc(nodeName(n))}</span><span class="nv-pill nv-p-${cls}"><i></i>${st}</span>
      <button class="small" data-act="node-detail" data-id="${n.id}" title="All details and controls for this node">Details</button></div>
    <div class="sub nv-ell">${esc(cpu.model || 'CPU')} · ${esc(h.os ? h.os + ' ' + h.arch : 'unknown')}${h.agent_version ? ' · v' + esc(h.agent_version) : ''}</div>
    ${labelChips(n)}
    <div class="gc-rings">
      ${ring(f.cpu, 'CPU', `${cpu.cores || '?'}C/${cpu.threads || '?'}T`)}
      ${ring(f.ram, 'RAM', ramTot ? bytes(ramTot) : '—')}
      ${g ? ring(f.vram || 0, g.vendor === 'apple' ? 'GPU' : 'VRAM', bytes(g.vram_bytes)) : ringNone('no GPU')}
      <div class="ring gc-speed"><div>${nvTps(f, true)}</div><div class="sub">${n.online ? `${n.running_jobs}/${n.max_jobs} jobs` : n.last_seen ? 'seen ' + ago(n.last_seen) : 'never connected'}</div></div>
    </div>
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
  const tile = (k, v, s, cls = '') => `<div class="card kpi ${cls}"><div class="sub">${k}</div><div class="kpi-v">${v}</div><div class="sub nv-ell" title="${esc(s)}">${esc(s)}</div></div>`;
  return `<div class="kpis">
    ${tile('Online', `${on.length} / ${nodes.length}`, off.length ? off.map(nodeName).join(', ') + ' offline' : 'all online')}
    ${tile('Models loaded', loaded, [pooled && pooled + ' pooled', setting && setting + ' setting up'].filter(Boolean).join(' · ') || ' ')}
    ${tile('Jobs', `${jobs} / ${slots}`, 'running / slots')}
    ${tile('Cluster speed', speed ? `${Math.round(speed)} tok/s` : '—', 'sum of loaded models')}
    ${tile('Free VRAM', nG ? bytes(vTot - vUsed) : '—', nG ? `of ${bytes(vTot)} on ${nG} GPU${nG > 1 ? 's' : ''}` : 'no GPUs')}
    ${tile('Needs attention', att.length, att.length ? att.map(([n, f]) => `${nodeName(n)}: ${f.issues.find((i) => i.lvl === 'bad' || i.lvl === 'warn').short.toLowerCase()}`).join(' · ') : 'nothing', att.length ? 'kpi-w' : '')}
  </div>`;
}
function nodeGridView(nodes) {
  const rank = { bad: 0, warn: 1, info: 2, '': 2, off: 4 };
  const sorted = [...nodes].sort((a, b) => rank[nodeFacts(a).worst] - rank[nodeFacts(b).worst]);
  const byName = (nm) => nodes.find((x) => nodeName(x) === nm);
  const inPool = new Set(), pools = [];
  nodes.forEach((n) => {
    if (!n.pool_names || !n.pool_names.length) return;
    const helpers = n.pool_names.map(byName).filter(Boolean);
    [n, ...helpers].forEach((x) => inPool.add(x.id));
    pools.push([n, helpers]);
  });
  const pool = ([n, helpers]) => {
    const f = nodeFacts(n);
    return `<div class="pool"><div class="pool-h"><span class="pool-ic">⇄</span><b>Memory pool</b>
      <span class="sub nv-grow">${f.loaded ? esc(f.loaded) + ' · ' : ''}runs on <b class="nv-ink">${esc(nodeName(n))}</b>, ${helpers.map((x) => `<b class="nv-ink">${esc(nodeName(x))}</b>`).join(', ')} lend${helpers.length === 1 ? 's' : ''} memory</span>
      ${nvTps(f)}${f.loaded ? `<button class="small" data-act="unload" data-id="${n.id}" title="Unload the model; the helpers get their memory back">Unload pool</button>` : ''}</div>
      <div class="pool-cards">${[n, ...helpers].map(gaugeCard).join('')}</div></div>`;
  };
  const att = sorted.filter((n) => !inPool.has(n.id) && ['bad', 'warn'].includes(nodeFacts(n).worst));
  const rest = sorted.filter((n) => !inPool.has(n.id) && !att.includes(n));
  return `<div class="gc-grid">${att.map(gaugeCard).join('')}${pools.map(pool).join('')}${rest.map(gaugeCard).join('')}</div>`;
}

function renderNodes() {
  const nodes = state.nodes;
  const online = nodes.filter((n) => n.online).length;
  setHTML('node-count', nodes.length ? `${online} of ${nodes.length} online` : '');
  if (nodeView === 'detail' && !showDetailed) nodeView = 'list';
  document.querySelectorAll('[data-node-view]').forEach((b) => { b.classList.toggle('on', b.dataset.nodeView === nodeView); if (b.dataset.nodeView === 'detail') b.hidden = !showDetailed; });
  if (!nodes.length) { setHTML('nodes', `<div class="empty u16">No nodes yet. Click <b>Add node</b> to get the install command.</div>`); return; }
  const labels = state.labels || [];
  if (nodeLabel && !labels.includes(nodeLabel)) nodeLabel = '';
  const shown = nodeLabel ? nodes.filter((n) => (n.labels || []).includes(nodeLabel)) : nodes;
  const chips = labels.length ? `<div class="label-filter" role="group" aria-label="Show nodes with label">${['', ...labels].map((l) => `<button class="small${l === nodeLabel ? ' on' : ''}" data-act="label-filter" data-label="${esc(l)}" aria-pressed="${l === nodeLabel}">${l ? esc(l) + ' · ' + nodes.filter((n) => (n.labels || []).includes(l)).length : 'All nodes'}</button>`).join('')}</div>` : '';
  setHTML('nodes', clusterSummary(nodes) + chips + (nodeView === 'list' ? nodeListView(shown) : nodeView === 'detail' ? nodeDetailView(shown) : nodeGridView(shown)));
}

function opView(n) {
  const o = state.ops && state.ops[n.id];
  if (!o || !o.op) return '';
  const age = (Date.now() - new Date(o.updated)) / 1000;
  const what = { runtime: 'llama.cpp', toolchain: 'Installing Go', 'model.push': 'Copying model from controller', 'model.fetch': 'Downloading from Hugging Face', rpc: 'Lending memory', 'model.load': 'Loading model', 'model.unload': 'Unloading', 'model.remove': 'Removing model', 'model.cleanup': 'Cleaning up models' }[o.op] || o.op;
  if (o.op === 'model.cleanup' && o.state === 'done') return age < 600 && o.detail ? `<div class="sub u17">Cleanup: ${esc(o.detail)}</div>` : '';
  const lf = o.last_failure && (Date.now() - new Date(o.last_failure.at)) < 3600000 ? o.last_failure : null;
  if (lf) {
    // One steady message: it names the failed step, counts the tries and does not change while retries run.
    const lwhat = { runtime: 'llama.cpp', 'model.load': 'Loading model', 'model.push': 'Copying model from controller', 'model.fetch': 'Downloading from Hugging Face' }[lf.op] || lf.op;
    const tries = lf.count > 1 ? ` (${lf.count} tries, last ${new Date(lf.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })})` : '';
    return `<div class="note bad u17">${esc(lwhat)} failed${tries}: ${esc(lf.error)}</div>`;
  }
  if (o.state === 'failed') return age < 3600 ? `<div class="note bad u17">${esc(what)} failed: ${esc(o.error)}</div>` : '';
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
    return `<div class="metric"><span class="k">GPU</span>${bar(util)}<span class="v">${util || 0}% · ${bytes(used)}/${bytes(g.vram_bytes)}</span></div>
      <div class="sub u19">${esc(g.name)}</div>`;
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
        ${n.online ? `<button class="small" data-act="load" data-id="${n.id}">${loaded ? 'Change' : 'Load model'}…</button>${loaded ? `<button class="small" data-act="unload" data-id="${n.id}">Unload</button>` : ''}` : ''}</div>
      ${recRow(n, loaded)}
      ${h.freed_note && Date.now() / 1000 - h.freed_at < 86400 ? `<div class="sub wrap u15" title="Removed automatically to make room for a model; it can be copied or downloaded again.">${esc(h.freed_note)} · ${ago(new Date(h.freed_at * 1000).toISOString())}</div>` : ''}
      ${n.online ? `<div class="sub u15">Unloads when idle: ${esc(n.idle_unload_effective || '1h')}${n.idle_unload ? '' : ' (default)'} · <a href="#" data-act="idle" data-id="${n.id}">change</a></div>` : ''}
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
    ${paused ? `<div class="note warn u17 nv-note"><span class="nv-grow">${esc(pauseText(n))}</span>${canPause(n) ? `<button class="small" data-act="node-resume" data-id="${n.id}">Resume</button>` : ''}</div>` : ''}
    <div class="sub u17">${status}${hb && hb.swap_used_bytes ? ` · swap ${bytes(hb.swap_used_bytes)}` : ''}${n.addr ? ` · ${esc(n.addr.split(':')[0])}` : ''}${!n.online && n.can_wake ? ` · <a href="#" data-act="wake" data-id="${n.id}">Wake</a>` : ''}</div>
    <div class="sub u17 wrap">Labels: ${(n.labels || []).length ? (n.labels || []).map((l) => `<span class="label-chip">${esc(l)}</span>`).join(' ') : '<span class="muted">none</span>'} · <a href="#" data-act="labels" data-id="${n.id}">edit</a>
      · Works: ${n.hours ? esc(hoursText(n.hours)) + (n.off_hours ? ' <span class="nv-chip nv-info">off hours now</span>' : '') : 'any time'} · <a href="#" data-act="hours" data-id="${n.id}">set hours</a></div>
    ${modelBox}
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
  const hasGo = tools.some((t) => t.name === 'go');
  return `<div class="sub wrap u17">Runs commands in ${esc(h.shell)} · ${chips || 'no tools found'}
    ${n.online ? `<a href="#" data-act="install-tool" data-tool="go" data-id="${n.id}">${hasGo ? 'Reinstall' : 'Install'} Go</a>` : ''}</div>
${pkgStoreLine(n)}`;
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
$('load-model').addEventListener('change', () => onLoadModelChange());
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
