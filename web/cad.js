'use strict';
// New 3D model page (cad.go): a chat model writes an OpenSCAD file, an exec node
// with OpenSCAD renders it to STL, and the viewer (viewer3d.js) shows it.
// Loaded after app.js and viewer3d.js. No inline styles (the CSP forbids them).
let cadList = [], cadInfo = null, cadSel = null, cadDetail = null, cadTimer = null, cadViewer = null, cadShown = '', cadBuiltFor = '';

function cadRoute(p) {
  clearTimeout(cadTimer);
  if (p !== 'cad') return;
  cadPoll();
  setTimeout(() => { const t = $('cad-prompt'); if (t && !cadList.length) t.focus(); }, 0);
}

const cadBusy = (m) => m && (m.status === 'queued' || m.status === 'working');
const cadMM = (s) => s ? `${(+s[0]).toFixed(1)} × ${(+s[1]).toFixed(1)} × ${(+s[2]).toFixed(1)} mm` : '';

async function cadPoll() {
  clearTimeout(cadTimer);
  if (page !== 'cad') return;
  try {
    cadInfo = await api('/api/cad');
    cadList = cadInfo.models || [];
    if (cadSel && !cadList.some((m) => m.id === cadSel)) cadSel = null;
    if (!cadSel && cadList.length) cadSel = cadList[0].id;
    const m = cadList.find((x) => x.id === cadSel);
    if (m && (!cadDetail || cadDetail.model.id !== m.id || cadDetail.model.updated !== m.updated)) cadDetail = await api('/api/cad/' + m.id);
    if (!m) cadDetail = null;
    cadRender();
  } catch (e) { /* the login screen or a hiccup: try again below */ }
  cadTimer = setTimeout(cadPoll, cadList.some(cadBusy) ? 1500 : 6000);
}

function cadRender() {
  if (!cadInfo) return;
  // Which node's model writes the file.
  const sel = $('cad-node'), cur = sel.value || 'auto';
  const nodes = cadInfo.model_nodes || [];
  setHTML(sel, `<option value="auto">Model: strongest free node</option>` + nodes.map((n) => `<option value="${esc(n.id)}">Model: ${esc(n.model || 'loaded model')} (${esc(n.name)})</option>`).join(''));
  sel.value = nodes.some((n) => n.id === cur) ? cur : 'auto';
  const r = cadInfo.render_nodes || [];
  $('cad-optline').textContent = (r.length ? `Renders on ${r.join(', ')} (OpenSCAD, CPU)` : 'No node can render yet') +
    ` · ${nodes.length ? nodes.length + ' node' + (nodes.length === 1 ? '' : 's') + ' with a model ready' : 'no node has a model loaded'} · kept like pictures (Settings)`;
  setHTML('cad-note', cadInfo.note ? `<div class="card note-card">${md(cadInfo.note)}</div>` : '');

  // Working now.
  const busy = cadList.filter(cadBusy);
  setHTML('cad-busy', busy.length ? `<div class="section-head"><h2>Now working</h2></div>` + busy.map((m) => {
    const pct = Math.min(95, 100 * Math.max(0, (m.round || 1) - 0.5) / (m.max_rounds || 4));
    return `<div class="card gen-card"><div class="rowline"><span class="dot busy"></span><b class="gen-prompt">${esc(m.title)}</b><span class="spacer"></span>
      <button type="button" class="small" data-cad="stop" data-id="${esc(m.id)}">Stop</button></div>
      <div class="sub">Round ${m.round || 1} of ${m.max_rounds} · ${esc(m.stage || m.status)}${m.node_name ? ' · model on ' + esc(m.node_name) : ''}${m.renderer ? ' · renders on ' + esc(m.renderer) : ''}</div>${bar(pct, 'good')}</div>`;
  }).join('') : '');

  cadResult();

  // Earlier models.
  const rest = cadList.filter((m) => m.id !== cadSel);
  setHTML('cad-earlier', rest.length ? `<div class="section-head"><h2>Earlier models</h2></div><div class="cad-list">` + rest.map((m) =>
    `<button type="button" class="cad-item" data-cad="pick" data-id="${esc(m.id)}"><b>${esc(m.title)}</b>
      <span class="sub">${m.tris ? cadMM(m.size) : esc(m.status)}${m.keep ? ' · ★' : ''} · ${ago(m.created)}</span></button>`).join('') + '</div>' : '');
}

function cadResult() {
  const box = $('cad-result');
  if (!cadDetail) { if (cadViewer) { cadViewer.dispose(); cadViewer = null; } box.innerHTML = ''; cadBuiltFor = cadShown = ''; return; }
  const m = cadDetail.model;
  if (cadBuiltFor !== m.id) {
    if (cadViewer) { cadViewer.dispose(); cadViewer = null; }
    box.innerHTML = `<div class="section-head"><h2>Result</h2><span class="spacer"></span><span class="sub" id="cad-r-meta"></span></div>
      <div class="card cad-card"><div class="rowline"><b class="gen-prompt" id="cad-r-title"></b></div>
      <div class="sub" id="cad-r-sub"></div>
      <div id="cad-refs"></div>
      <div class="cad-two">
        <div class="cad-view"><canvas id="cad-canvas" aria-label="3D view of the model"></canvas>
          <span class="cad-hint">Drag to rotate · scroll or pinch to zoom · double-click to reset</span>
          <div class="cad-empty hidden" id="cad-empty"></div>
          <div class="cad-vbar"><button type="button" class="small" id="cad-wire" aria-pressed="false">Wireframe</button><button type="button" class="small" id="cad-reset">Reset view</button></div></div>
        <pre class="cad-code" id="cad-code"></pre>
      </div>
      <div id="cad-params"></div>
      <div class="cad-ask"><input type="text" id="cad-ask-text" autocomplete="off" placeholder="Ask for changes, for example: make the slot 14 mm and round the top edge" aria-label="Ask for changes"><button type="button" class="small" id="cad-ask-go">Ask for changes</button></div>
      <div class="card-actions"><a class="cad-dl" id="cad-dl-stl" download>Download STL</a><a class="cad-dl" id="cad-dl-scad" download>Download .scad</a><span id="cad-actions" class="cad-actions"></span></div>
      <details class="cad-steps"><summary>Steps</summary><div id="cad-log" class="sub"></div></details></div>`;
    cadBuiltFor = m.id; cadShown = '';
    cadViewer = Viewer3D($('cad-canvas'));
    if (!cadViewer) { $('cad-empty').textContent = 'This browser has no WebGL, so the model cannot be shown here. Download the STL instead.'; $('cad-empty').classList.remove('hidden'); }
    $('cad-wire').onclick = () => { if (!cadViewer) return; cadViewer.setWire(!cadViewer.wire); $('cad-wire').setAttribute('aria-pressed', cadViewer.wire ? 'true' : 'false'); $('cad-wire').classList.toggle('on', cadViewer.wire); };
    $('cad-reset').onclick = () => cadViewer && cadViewer.reset();
    $('cad-ask-go').onclick = cadAsk;
    $('cad-ask-text').addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); cadAsk(); } });
  }
  $('cad-r-title').textContent = m.title;
  $('cad-r-meta').textContent = m.tris ? `${cadMM(m.size)} · ${m.tris.toLocaleString()} triangles · ${bytes(m.stl_bytes)}` : '';
  $('cad-r-sub').textContent = [m.model_name && 'by ' + m.model_name + (m.node_name ? ' on ' + m.node_name : ''), m.renderer && 'rendered on ' + m.renderer, ago(m.created),
    m.status === 'failed' ? 'failed: ' + (m.error || '') : m.status === 'stopped' ? 'stopped' : ''].filter(Boolean).join(' · ');
  // Measurements looked up on the web before writing (cad_lookup.go).
  const refs = m.refs || [];
  setHTML('cad-refs', refs.length ? `<div class="cad-refs"><div class="cad-refs-head"><b>Measurements used</b><span class="cad-refs-tag">from the web, check before printing</span></div>`
    + refs.map((r) => `<div class="cad-ref"><span>${esc(r.name)}</span><span>${esc(r.value)}${r.source ? ' · ' + esc(r.source) : ''}</span></div>`).join('') + '</div>' : '');
  const code = cadDetail.code || '';
  if ($('cad-code').textContent !== code) $('cad-code').textContent = code || (cadBusy(m) ? 'The model is writing the file…' : 'No file yet.');
  $('cad-dl-stl').href = `/api/cad/${m.id}/model.stl?download=1`; $('cad-dl-stl').classList.toggle('hidden', !m.tris);
  $('cad-dl-scad').href = `/api/cad/${m.id}/model.scad?download=1`; $('cad-dl-scad').classList.toggle('hidden', !code);
  // Load the STL when it changed.
  const key = m.id + ':' + m.version;
  const empty = $('cad-empty');
  if (cadViewer && m.tris && cadShown !== key) {
    cadShown = key;
    fetch(`/api/cad/${m.id}/model.stl?v=${m.version}`, { credentials: 'same-origin' }).then((r) => { if (!r.ok) throw new Error('HTTP ' + r.status); return r.arrayBuffer(); })
      .then((b) => { cadViewer.load(b); empty.classList.add('hidden'); })
      .catch((e) => { empty.textContent = 'Could not load the STL: ' + e.message; empty.classList.remove('hidden'); });
  } else if (cadViewer && !m.tris) {
    empty.textContent = cadBusy(m) ? 'Rendering…' : m.status === 'failed' ? 'Nothing rendered. ' + (m.error || '') : 'Nothing rendered yet.';
    empty.classList.remove('hidden');
  }
  // Parameters.
  const ps = m.params || [], ov = m.overrides || {};
  setHTML('cad-params', ps.length ? `<div class="cad-params-head"><b>Parameters</b><span class="sub">change and render again without the model</span></div><div class="cad-params">` + ps.map((p) => {
    const val = ov[p.name] ?? p.value;
    const tip = esc(p.comment || '');
    if (p.bool) return `<label class="check" title="${tip}"><input type="checkbox" data-param="${esc(p.name)}" data-bool="1"${val === 'true' ? ' checked' : ''}> ${esc(p.name)}</label>`;
    return `<label class="chip-input" title="${tip}">${esc(p.name)} <input type="number" data-param="${esc(p.name)}" value="${esc(val)}"${p.min != null ? ` min="${p.min}"` : ''}${p.max != null ? ` max="${p.max}"` : ''} step="${p.step != null ? p.step : 'any'}" aria-label="${esc(p.name)}"></label>`;
  }).join('') + `</div><div class="card-actions"><button type="button" class="small" data-cad="params" data-id="${esc(m.id)}"${cadBusy(m) ? ' disabled' : ''}>Render with these values</button>${Object.keys(ov).length ? `<button type="button" class="small" data-cad="params-reset" data-id="${esc(m.id)}">Back to the file's values</button>` : ''}</div>` : '');
  $('cad-ask-go').disabled = cadBusy(m);
  setHTML('cad-actions', `${cadBusy(m) ? `<button type="button" class="small" data-cad="stop" data-id="${esc(m.id)}">Stop</button>` : ''}
    <button type="button" class="small" data-cad="reuse" data-id="${esc(m.id)}" title="Put this description back in the box">Use description</button>
    <button type="button" class="small" data-cad="keep" data-id="${esc(m.id)}" data-keep="${m.keep ? '' : '1'}" title="Kept models are never deleted automatically">${m.keep ? '★ Kept' : '☆ Keep'}</button>
    <button type="button" class="small" data-cad="del" data-id="${esc(m.id)}">Delete</button>`);
  const asks = (m.asks || []).slice(1).map((a) => `<div>Asked: ${esc(a.text)}</div>`).join('');
  setHTML('cad-log', asks + (cadDetail.model.log || []).map((l) => `<div>${esc(l)}</div>`).join('') || 'No steps yet.');
}

async function cadAsk() {
  const m = cadDetail && cadDetail.model, t = $('cad-ask-text');
  if (!m || !t.value.trim()) { t.focus(); return; }
  try { await post(`/api/cad/${m.id}/ask`, { text: t.value.trim() }); t.value = ''; cadPoll(); } catch (x) { toast(x.message); }
}

document.addEventListener('click', async (e) => {
  const b = e.target.closest('[data-cad]');
  if (!b || page !== 'cad') return;
  const id = b.dataset.id, m = cadList.find((x) => x.id === id);
  try {
    switch (b.dataset.cad) {
      case 'pick': cadSel = id; cadDetail = null; window.scrollTo({ top: 0, behavior: 'smooth' }); break;
      case 'stop': await post(`/api/cad/${id}/stop`); break;
      case 'keep': await post(`/api/cad/${id}/keep`, { keep: !!b.dataset.keep }); break;
      case 'del': if (!await askConfirm('Delete this 3D model?')) return; await api(`/api/cad/${id}`, { method: 'DELETE' }); cadSel = null; break;
      case 'reuse': if (m) { $('cad-prompt').value = m.prompt; $('cad-prompt').focus(); } return;
      case 'params': {
        const values = {};
        document.querySelectorAll('#cad-params [data-param]').forEach((i) => { values[i.dataset.param] = i.dataset.bool ? String(i.checked) : i.value; });
        await post(`/api/cad/${id}/params`, { values });
        break;
      }
      case 'params-reset': {
        const values = {};
        ((m && m.params) || []).forEach((p) => { values[p.name] = p.value; });
        await post(`/api/cad/${id}/params`, { values });
        break;
      }
    }
  } catch (x) { toast(x.message); }
  cadPoll();
});

$('cad-go').onclick = async () => {
  const err = $('cad-err'); err.classList.add('hidden');
  const prompt = $('cad-prompt').value.trim();
  if (!prompt) { $('cad-prompt').focus(); return; }
  $('cad-go').disabled = true;
  try {
    const r = await post('/api/cad', { prompt, node: $('cad-node').value, rounds: +$('cad-rounds').value, check: $('cad-check').value === '1' });
    cadSel = r.model.id; cadDetail = null; $('cad-prompt').value = '';
    cadPoll();
  } catch (x) { err.textContent = x.message; err.classList.remove('hidden'); }
  $('cad-go').disabled = false;
};
$('cad-prompt').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $('cad-go').click(); } });
