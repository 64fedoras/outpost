'use strict';
// Models page. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- models ----------
function recFor(name) {
  const nodes = Object.entries((models && models.recommended) || {}).filter(([, r]) => r.name === name).map(([id]) => nodeById(id)).filter(Boolean);
  return nodes.length ? ` <span class="pill st-ready" title="Best coding model that fits: ${esc(nodes.map(nodeName).join(', '))}">Recommended · ${esc(nodes.map(nodeName).join(', '))}</span>` : '';
}
async function waitModelReady(name) {
  for (let i = 0; i < 90; i++) {
    const m = (await api('/api/models')).models.find((x) => x.name === name);
    if (m && m.status === 'ready') return;
    if (m && m.status === 'failed') throw new Error(`${name} could not be added: ${m.error}`);
    await new Promise((r) => setTimeout(r, 1000));
  }
  throw new Error(`Timed out adding ${name} to the library`);
}
// Load a model on a node; adds it to the library first (nodes download it) when it is only in the catalog.
async function loadOnNode(nodeId, name, addFirst) {
  try {
    if (addFirst) { toast('Adding to the library…'); await post('/api/models', { catalog_id: name, storage: 'direct' }).catch((e) => { if (!/already in the library/.test(e.message)) throw e; }); await waitModelReady(name); }
    await post(`/api/nodes/${nodeId}/load`, { name });
    toast('Setting up the model; progress shows on the node card');
  } catch (e) { toast(e.message, 6000); throw e; }
  refresh();
}
function openPick(name, isCatalog) {
  const entry = isCatalog ? models.catalog.find((c) => c.id === name) : models.models.find((m) => m.name === name);
  $('pick-model').textContent = entry ? (entry.label || entry.name) : name;
  $('pick-err').classList.add('hidden');
  const fits = ((isCatalog ? models.catalog_fits : models.fits) || {})[name] || [];
  const rank = { vram: 0, split: 1, ram: 2, no: 3 };
  setHTML('pick-list', fits.length ? [...fits].sort((a, b) => rank[a.fit] - rank[b.fit] || a.name.localeCompare(b.name)).map((f) => {
    const n = state.nodes.find((x) => x.id === f.node_id), cur = n && n.heartbeat && n.heartbeat.loaded_model;
    const rec = (models.recommended || {})[f.node_id];
    const why = !f.online ? 'offline' : !f.can_load ? 'busy' : f.fit === 'no' ? 'too big for this node' : '';
    return `<div class="pick-row"><div class="grow"><b>${esc(f.name)}</b> <span class="pill ${FIT_CLS[f.fit]}">${FIT_TEXT[f.fit]}</span>${rec && rec.name === name ? ' <span class="pill st-ready">Recommended</span>' : ''}
      <div class="sub wrap u22">needs about ${bytes(f.need_bytes)}${cur && cur !== name ? ` · replaces ${esc(cur)}` : ''}${why ? ' · ' + why : ''}</div></div>
      <button class="small primary" data-pick-node="${f.node_id}" ${why ? 'disabled' : ''}>Load</button></div>`;
  }).join('') : '<div class="empty">No nodes yet.</div>');
  $('pick-list').onclick = async (e) => {
    const b = e.target.closest('[data-pick-node]'); if (!b) return;
    try { await loadOnNode(b.dataset.pickNode, name, isCatalog); close('modal-pick'); }
    catch (err) { $('pick-err').textContent = err.message; $('pick-err').classList.remove('hidden'); }
  };
  open('modal-pick');
}
const isDirect = (m) => m.storage === 'direct';
function storageLabel(m) {
  return isDirect(m) ? '<span title="Each node downloads it from Hugging Face when you load it">Nodes download</span>'
    : '<span title="Downloaded once to the controller and copied to nodes over your network">On controller</span>';
}
const FIT_TEXT = { vram: 'fits in VRAM', split: 'split VRAM + RAM (slower)', ram: 'runs on CPU', no: 'does not fit' };
const FIT_CLS = { vram: 'st-done', split: 'st-loading', ram: 'st-loading', no: 'st-failed' };
function fitBadges(fits) {
  if (!fits || !fits.length) return '';
  return `<div class="fit-badges u23">${fits.map((f) => `<span class="pill ${FIT_CLS[f.fit]}" title="${esc(f.name)}: ${FIT_TEXT[f.fit]}; needs about ${bytes(f.need_bytes)}${f.online ? '' : ' (node offline)'}">${esc(f.name)} · ${FIT_TEXT[f.fit]}</span>`).join('')}</div>`;
}
function renderModels() {
  if (!models) return;
  setHTML('models-disk', `${bytes(models.disk_free)} free on the controller`);
  const holders = (name) => state.nodes.filter((n) => n.hello && (n.hello.models || []).some((m) => m.name === name)).map(nodeName);
  const list = models.models;
  setHTML('models-list', !list.length ? `<div class="empty">No models yet. Pick one below.</div>` :
    `<div class="table-wrap"><table><thead><tr><th>Model</th><th>Size</th><th>Tier</th><th>Stored</th><th>Status</th><th>On nodes</th><th></th></tr></thead><tbody>${list.map((m) => {
      const pct = m.bytes_total ? 100 * m.bytes_done / m.bytes_total : 0;
      const status = m.status === 'downloading' ? `<div class="prog"><span>downloading to controller ${bytes(m.bytes_done)} of ${bytes(m.bytes_total)}</span>${bar(pct, 'good')}</div>`
        : m.status === 'resolving' ? pill('loading', 'checking Hugging Face')
        : m.status === 'failed' ? `${pill('failed')} <span class="muted">${esc(m.error)}</span>` : pill('ready');
      const h = holders(m.name), nm = esc(m.name);
      const acts = [];
      if (m.status === 'failed') acts.push(`<button class="small" data-act="model-retry" data-name="${nm}">Retry</button>`);
      if (isDirect(m) && m.status === 'ready') acts.push(`<button class="small" data-act="model-store" data-name="${nm}" title="Download it to the controller too, so nodes copy it over your network instead of from the internet">Store on controller</button>`);
      if (!isDirect(m) && m.status === 'ready') acts.push(`<button class="small" data-act="model-release" data-name="${nm}" title="Copy it to the node with the most free disk, then free ${bytes(m.bytes_total)} on the controller. Other nodes copy it from that node.">Release to a node</button>`);
      if (!isDirect(m) && m.status !== 'failed') acts.push(`<button class="small" data-act="model-unstore" data-name="${nm}" title="Free ${bytes(m.bytes_total)} on the controller; nodes download it from Hugging Face instead">Free controller space</button>`);
      const vf = ((models.fits || {})[m.name] || []).filter((f) => f.fit === 'vram' && f.can_load);
      if (m.status === 'ready' && vf.length) acts.unshift(`<button class="small primary" data-act="model-load-fit" data-name="${nm}" title="Loads it on: ${esc(vf.map((f) => f.name).join(', '))}. Replaces any model those nodes have loaded.">Load on ${vf.length} node${vf.length > 1 ? 's' : ''} it fits</button>`);
      if (m.status === 'ready') acts.unshift(`<button class="small" data-act="pick-model" data-name="${nm}">Load on…</button>`);
      acts.push(`<button class="small danger" data-act="model-delete" data-name="${nm}">${m.status === 'downloading' ? 'Cancel' : 'Remove'}</button>`);
      return `<tr><td><b>${esc(m.label || m.name)}</b>${m.vision ? ' <span class="pill st-ready" title="Reads images: loaded with its vision projector">Vision</span>' : ''}${recFor(m.name)}<div class="sub u22">${esc(m.repo)}</div>${m.status === 'ready' ? fitBadges((models.fits || {})[m.name]) : ''}</td><td>${m.bytes_total ? bytes(m.bytes_total) : '—'}</td><td><select class="small" data-tier="${nm}" aria-label="Model strength" title="Used to route hard subtasks to the strongest model">${[['', 'auto (' + (m.tier_guess || 'medium') + ')'], ['small', 'Small'], ['medium', 'Medium'], ['large', 'Large']].map(([v, l]) => `<option value="${v}" ${(m.tier || '') === v ? 'selected' : ''}>${l}</option>`).join('')}</select></td><td>${storageLabel(m)}</td><td>${status}</td>
        <td>${h.length ? h.map(esc).join(', ') : '<span class="muted">none</span>'}</td><td class="r"><div class="btns u24">${acts.join('')}</div></td></tr>`;
    }).join('')}</tbody></table></div>`);
  const have = {}; list.forEach((m) => { have[m.name] = m; });
  // Discover: one compact row per catalog model; suggested ones first. Per-node fit
  // lives in the "Add and load on…" picker; the row says how many online nodes it fits.
  const onlineN = state.nodes.filter((n) => n.online).length;
  const catFits = (id) => ((models.catalog_fits || {})[id] || []).filter((f) => f.online);
  const sugg = (models.suggested || []).map((sg) => sg.id);
  const order = [...models.catalog].sort((a, b) => {
    const ia = sugg.indexOf(a.id), ib = sugg.indexOf(b.id);
    return (ia < 0 ? 1e9 : ia) - (ib < 0 ? 1e9 : ib);
  });
  const q = catQuery.trim().toLowerCase();
  const shown = order.filter((c) => {
    const fits = catFits(c.id);
    if (catFilter === 'fits' && !fits.some((f) => f.fit !== 'no')) return false;
    if (catFilter === 'vram' && !fits.some((f) => f.fit === 'vram')) return false;
    if (catFilter === 'vision' && !c.mmproj) return false;
    return !q || `${c.label} ${c.for} ${c.notes}`.toLowerCase().includes(q);
  });
  document.querySelectorAll('[data-cat-filter]').forEach((b) => b.classList.toggle('on', b.dataset.catFilter === catFilter));
  const catRow = (c) => {
    const m = have[c.id];
    const fits = catFits(c.id), ok = fits.filter((f) => f.fit !== 'no'), vr = fits.filter((f) => f.fit === 'vram');
    const fitTxt = !onlineN ? 'no nodes online' : `fits ${ok.length} of ${onlineN} online${vr.length ? ` · ${vr.length} in VRAM` : ''}`;
    const fitTitle = fits.map((f) => `${f.name}: ${FIT_TEXT[f.fit]}`).join('\n');
    const act = m ? (m.status === 'downloading' ? `<button class="small" disabled>Downloading ${m.bytes_total ? Math.floor(100 * m.bytes_done / m.bytes_total) : 0}%</button>`
        : m.status === 'failed' ? `<button class="small" data-act="model-retry" data-name="${esc(c.id)}">Retry</button>`
        : `<button class="small" data-act="pick-model" data-name="${esc(c.id)}" title="In the library (${isDirect(m) ? 'nodes download' : 'on the controller'})">Load on…</button>`)
      : `<div class="split-btn"><button class="small primary" data-act="pick-model" data-name="${esc(c.id)}" data-catalog="1" title="Adds it to the library (nodes download it) and loads it on the node you pick">Add and load on…</button><details class="split-more"><summary class="small primary" aria-label="More ways to add">▾</summary><div class="split-menu">
          <button data-act="catalog-add" data-id="${esc(c.id)}" data-storage="direct"><b>Add · nodes download</b><span class="sub">Each node fetches it from Hugging Face when you load it. Uses no controller space.</span></button>
          <button data-act="catalog-add" data-id="${esc(c.id)}" data-storage="controller"><b>Store on controller</b><span class="sub">Download once (${bytes(c.bytes)}), then copy to nodes over your network.</span></button></div></details></div>`;
    return `<div class="cat-row"><div class="nv-min"><b>${esc(c.label)}</b>${c.mmproj ? ' <span class="pill st-ready" title="Reads images">Vision</span>' : ''}${recFor(c.id)}${m ? ' <span class="pill st-pending">In library</span>' : ''}
      <div class="sub nv-wrap">${esc(c.for)} · ${esc(c.notes)}</div></div>
      <span class="sub" title="needs about ${c.min_ram_gb} GB RAM+VRAM">${bytes(c.bytes)}</span><span class="sub" title="${esc(fitTitle)}">${fitTxt}</span><div class="cat-act">${act}</div></div>`;
  };
  setHTML('catalog', shown.length ? `<div class="cat-rows">${shown.map(catRow).join('')}</div>` : `<div class="empty">${onlineN || catFilter === 'all' || catFilter === 'vision' ? 'No models match.' : 'No nodes are online, so nothing can be sized.'}</div>`);
  const rts = models.runtimes || [];
  setHTML('runtimes', !rts.length ? `<div class="empty">None downloaded yet.</div>` :
    `<div class="table-wrap"><table><thead><tr><th>Build</th><th>For</th><th>Size</th><th>Downloaded</th></tr></thead><tbody>${rts.map((r) => `<tr><td>${esc(r.build)}</td><td>${esc(r.variant)}</td><td>${bytes(r.bytes)}</td><td>${ago(r.downloaded)}</td></tr>`).join('')}</tbody></table></div>`);
}
let hfRepo = '', hfGroups = [];
let catFilter = 'all', catQuery = '';
$('cat-search').addEventListener('input', (e) => { catQuery = e.target.value; renderModels(); });
$('hf-list').onclick = async () => {
  setHTML('hf-files', '<span class="muted">Loading…</span>');
  try {
    const r = await api('/api/hf/files?repo=' + encodeURIComponent($('hf-repo').value.trim()));
    hfRepo = r.repo; hfGroups = r.groups;
    setHTML('hf-files', !r.groups.length ? '<span class="muted">No .gguf files in that repo.</span>' :
      `<div class="table-wrap"><table><tbody>${r.mmproj && r.mmproj.length ? `<tr><td colspan="3"><label class="muted">Vision projector (optional, makes the model read images) <select id="hf-mmproj"><option value="">none</option>${r.mmproj.map((f) => `<option value="${esc(f)}">${esc(f)}</option>`).join('')}</select></label></td></tr>` : ''}${r.groups.map((g, i) => `<tr><td class="mono">${esc(g.name)}${g.files.length > 1 ? ` <span class="muted">(${g.files.length} parts)</span>` : ''}${fitBadges(g.fits)}</td><td>${bytes(g.bytes)}</td><td class="r"><div class="btns u24"><button class="small primary" data-act="hf-add" data-i="${i}" data-storage="direct">Add · nodes download</button><button class="small" data-act="hf-add" data-i="${i}" data-storage="controller">Store on controller</button></div></td></tr>`).join('')}</tbody></table></div>`);
  } catch (e) { setHTML('hf-files', `<span class="error-msg">${esc(e.message)}</span>`); }
};
$('hf-repo').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('hf-list').click(); });
$('rt-check').onclick = async () => {
  try { const r = await post('/api/runtime/check'); toast(`Newest llama.cpp with binaries: ${r.latest}. Nodes pick it up the next time a model loads.`, 5000); }
  catch (e) { toast(e.message); }
};
