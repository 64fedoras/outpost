// New image page and the Pictures card in Settings. Loaded after app.js (uses its helpers).
// No inline styles anywhere (the CSP forbids them): sizes and progress use the w0..w100 classes.
let imgModels = null, imgGens = [], imgTimer = null, imgBusyNow = false, imgSet = null;

function imgRoute(p) {
  clearTimeout(imgTimer);
  if (p === 'settings') imgLoadSettings();
  if (p !== 'image') return;
  imgPoll();
  setTimeout(() => { const t = $('img-prompt'); if (t) t.focus(); }, 0);
}

async function imgPoll() {
  clearTimeout(imgTimer);
  if (page !== 'image') return;
  try {
    const [m, g] = await Promise.all([api('/api/images/models'), api('/api/images?limit=60')]);
    imgModels = m.models; imgGens = g.generations || [];
    imgRender();
  } catch (e) { /* the login screen or a hiccup: try again below */ }
  const live = imgGens.some((x) => x.status === 'queued' || x.status === 'running') || (state && Object.values(state.ops || {}).some((o) => o.target && String(o.target).startsWith('img-') && !['done', 'ready', 'failed'].includes(o.state)));
  imgTimer = setTimeout(imgPoll, live ? 1500 : 6000);
}

const imgSel = () => (imgModels || []).find((m) => m.id === $('img-model').value);
const imgGenPct = (g) => {
  const its = g.items || [];
  if (!its.length) return 0;
  return its.reduce((a, it) => a + (it.status === 'done' ? 100 : it.status === 'running' && it.steps ? Math.min(99, 100 * it.step / it.steps) : 0), 0) / its.length;
};

function imgRender() {
  if (!imgModels) return;
  // Model picker: keep the choice across refreshes.
  const sel = $('img-model'), cur = sel.value;
  const opts = imgModels.map((m) => {
    const ready = m.nodes.some((n) => n.has_model);
    return `<option value="${esc(m.id)}">${esc(m.label)}${ready ? '' : ' · not downloaded'}</option>`;
  }).join('');
  setHTML(sel, opts);
  if (cur && imgModels.some((m) => m.id === cur)) sel.value = cur;
  const m = imgSel();

  const nodes = (m ? m.nodes : []).filter((n) => n.online);
  const can = nodes.filter((n) => n.can_run).length;
  $('img-sub').textContent = !m ? '' : can ? `${can} of ${nodes.length} online node${nodes.length === 1 ? '' : 's'} can draw with ${m.label} right now.`
    : nodes.length ? `No online node can draw with ${m.label} yet. Download it to a node below.` : 'No node is online.';
  $('img-optline').textContent = m ? `${m.label} · default ${m.steps} steps${m.cfg ? ', cfg ' + m.cfg : ''}` : '';
  $('img-neg').classList.toggle('hidden', !(m && m.negative));

  // Licence gate.
  setHTML('img-accept', m && !m.accepted
    ? `<div class="card gen-accept"><b>${esc(m.label)}</b> is released under <b>${esc(m.license)}</b>, which limits how you may use the pictures. <a href="${esc(m.license_url)}" target="_blank" rel="noopener">Read the licence</a>.
       <div class="card-actions"><button type="button" class="primary" data-act="accept" data-id="${esc(m.id)}">I have read it and accept</button></div></div>` : '');

  // Running generations.
  const busy = imgGens.filter((g) => g.status === 'queued' || g.status === 'running');
  imgBusyNow = busy.length > 0;
  setHTML('img-busy', busy.length ? `<div class="section-head"><h2>Now generating</h2></div>` + busy.map((g) => {
    const its = g.items || [], done = its.filter((i) => i.status === 'done').length;
    const run = its.find((i) => i.status === 'running'), prep = its.find((i) => i.status === 'preparing');
    const where = run ? `${esc(run.node_name || 'a node')} · step ${run.step || 0} of ${run.steps || '?'}` : prep ? `Getting ${esc(prep.node_name || 'a node')} ready (download or load)…` : 'Waiting for a free node…';
    return `<div class="card gen-card"><div class="rowline"><b class="gen-prompt">${esc(g.prompt)}</b><span class="spacer"></span><button type="button" class="small" data-act="cancel" data-id="${esc(g.id)}">Stop</button></div>
      <div class="sub">${esc(g.label)} · ${g.width}×${g.height} · ${done} of ${its.length} done · ${where}</div>${bar(imgGenPct(g), 'good')}</div>`;
  }).join('') : '');

  // Finished generations: newest three big, the rest as a compact grid.
  const fin = imgGens.filter((g) => !(g.status === 'queued' || g.status === 'running'));
  setHTML('img-results', fin.length ? `<div class="section-head"><h2>Results</h2></div>` + fin.slice(0, 3).map(imgCard).join('') : '');
  setHTML('img-earlier', fin.length > 3 ? `<div class="section-head"><h2>Earlier images</h2></div><div class="gen-grid small">` + fin.slice(3).map((g) =>
    (g.items || []).filter((i) => i.status === 'done').map((i) => `<a class="gen-shot" href="/api/images/${esc(g.id)}/files/${i.index}" target="_blank" rel="noopener" title="${esc(g.prompt)}"><img src="/api/images/${esc(g.id)}/files/${i.index}" alt="${esc(g.prompt)}" loading="lazy"></a>`).join('')).join('') + '</div>' : '');

  imgModelsCard();
}

function imgCard(g) {
  const its = g.items || [];
  const shots = its.map((i) => {
    if (i.status === 'done') return `<figure class="gen-shot"><a href="/api/images/${esc(g.id)}/files/${i.index}" target="_blank" rel="noopener"><img src="/api/images/${esc(g.id)}/files/${i.index}" alt="${esc(g.prompt)}" loading="lazy"></a>
      <figcaption><span>seed ${i.seed}${i.seconds ? ' · ' + Math.round(i.seconds) + 's' : ''}</span><a href="/api/images/${esc(g.id)}/files/${i.index}?download=1" download>Download</a></figcaption></figure>`;
    return `<figure class="gen-shot wait"><div class="gen-fail">${i.status === 'failed' ? esc(i.error || 'failed') : esc(i.status)}</div></figure>`;
  }).join('');
  const failed = its.some((i) => i.status === 'failed' || i.status === 'cancelled');
  return `<div class="card gen-card"><div class="rowline"><b class="gen-prompt">${esc(g.prompt)}</b></div>
    <div class="sub">${esc(g.label)} · ${g.width}×${g.height} · ${g.steps} steps · ${ago(g.created)}${g.source && g.source !== 'web' ? ' · ' + esc(g.source) : ''}</div>
    <div class="gen-grid">${shots}</div>
    <div class="card-actions">
      <button type="button" class="small" data-act="again" data-id="${esc(g.id)}" title="Same settings, new random seeds">Variations</button>
      <button type="button" class="small" data-act="reuse" data-id="${esc(g.id)}" title="Put this prompt and these settings back in the box">Use prompt</button>
      ${failed ? `<button type="button" class="small" data-act="retry" data-id="${esc(g.id)}">Retry failed</button>` : ''}
      <button type="button" class="small" data-act="keep" data-id="${esc(g.id)}" data-keep="${g.keep ? '' : '1'}" title="Kept pictures are never deleted automatically">${g.keep ? '★ Kept' : '☆ Keep'}</button>
      <button type="button" class="small" data-act="del" data-id="${esc(g.id)}">Delete</button>
    </div></div>`;
}

function imgModelsCard() {
  const ops = (state && state.ops) || {};
  setHTML('img-models', (imgModels || []).map((m) => {
    const online = m.nodes.filter((n) => n.online);
    const have = online.filter((n) => n.has_model);
    const best = m.nodes.filter((n) => n.can_run)[0] || m.nodes.filter((n) => n.fit && n.fit !== 'no')[0];
    const fitTxt = best ? `fits ${esc(best.name)}${best.offload ? ' (with CPU offload, slower)' : ''}` : 'fits no online node';
    const targets = online.filter((n) => !n.has_model);
    const progress = online.map((n) => {
      const o = ops[n.id];
      if (!o || o.target !== 'img-' + m.id || ['done', 'ready'].includes(o.state)) return '';
      if (o.state === 'failed') return `<div class="note bad">${esc(n.name)}: ${esc(o.error || 'failed')}</div>`;
      const pct = o.bytes_total ? 100 * o.bytes_done / o.bytes_total : 0;
      return `<div class="prog"><span>${esc(n.name)}: ${esc(o.state)}${o.bytes_total ? ` ${bytes(o.bytes_done)} of ${bytes(o.bytes_total)}` : '…'}</span>${bar(pct, 'good')}</div>`;
    }).join('');
    return `<div class="card gen-model"><div class="rowline"><b>${esc(m.label)}</b> <span class="pill ${m.restricted ? 'st-warn' : 'st-pending'}" title="${m.restricted ? 'Limits how you may use the pictures' : 'Open licence'}">${esc(m.license)}</span>
        <span class="spacer"></span><span class="sub">${bytes(m.bytes)}</span></div>
      <div class="sub">${esc(m.about)}</div>
      <div class="sub">${fitTxt}${have.length ? ' · downloaded on ' + have.map((n) => esc(n.name)).join(', ') : ''}</div>${progress}
      ${targets.length ? `<div class="card-actions"><select class="small" data-nodesel="${esc(m.id)}" aria-label="Node to download to">${targets.map((n) => `<option value="${esc(n.id)}">${esc(n.name)}${n.fit === 'no' ? ' (too small)' : ''}</option>`).join('')}</select>
        <button type="button" class="small" data-act="install" data-id="${esc(m.id)}">Download to node</button></div>` : ''}</div>`;
  }).join(''));
}

// Buttons on the page.
document.addEventListener('click', async (e) => {
  const b = e.target.closest('[data-act]');
  if (!b || page !== 'image') return;
  const id = b.dataset.id, g = imgGens.find((x) => x.id === id);
  try {
    switch (b.dataset.act) {
      case 'accept': await post(`/api/images/models/${id}/accept`); break;
      case 'cancel': await post(`/api/images/${id}/cancel`); break;
      case 'retry': await post(`/api/images/${id}/retry`); break;
      case 'keep': await post(`/api/images/${id}/keep`, { keep: !!b.dataset.keep }); break;
      case 'del': if (!confirm('Delete these pictures?')) return; await api(`/api/images/${id}`, { method: 'DELETE' }); break;
      case 'again': if (g) { await post('/api/images', { model: g.model, prompt: g.prompt, negative: g.negative, width: g.width, height: g.height, steps: g.steps, cfg: g.cfg, sampler: g.sampler, count: g.count }); toast('Queued'); } break;
      case 'reuse': if (g) {
        $('img-prompt').value = g.prompt; $('img-neg').value = g.negative || '';
        if (imgModels.some((m) => m.id === g.model)) $('img-model').value = g.model;
        $('img-size').value = g.width + 'x' + g.height; $('img-prompt').focus();
      } break;
      case 'install': {
        const node = document.querySelector(`[data-nodesel="${id}"]`).value;
        await post(`/api/images/models/${id}/install`, { node }); toast('Download started');
        break;
      }
    }
  } catch (x) { toast(x.message); }
  imgPoll();
});

$('img-opt-toggle').onclick = () => {
  const open = $('img-opts').classList.toggle('hidden') === false;
  $('img-opt-toggle').setAttribute('aria-expanded', open ? 'true' : 'false');
};

$('img-go').onclick = async () => {
  const err = $('img-err'); err.classList.add('hidden');
  const prompt = $('img-prompt').value.trim();
  if (!prompt) { $('img-prompt').focus(); return; }
  const [w, h] = $('img-size').value.split('x').map(Number);
  const body = { model: $('img-model').value, prompt, negative: $('img-neg').value.trim(), width: w, height: h, count: +$('img-count').value };
  if ($('img-steps').value) body.steps = +$('img-steps').value;
  if ($('img-cfg').value) body.cfg = +$('img-cfg').value;
  if ($('img-seed').value) body.seed = +$('img-seed').value;
  $('img-go').disabled = true;
  try { await post('/api/images', body); imgPoll(); }
  catch (x) { err.textContent = x.message; err.classList.remove('hidden'); }
  $('img-go').disabled = false;
};
$('img-prompt').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $('img-go').click(); } });
$('img-model').addEventListener('change', imgRender);

// Settings: how long and how much to keep.
async function imgLoadSettings() {
  try { imgSet = await api('/api/images/settings'); } catch (e) { return; }
  if (document.activeElement !== $('set-img-days')) $('set-img-days').value = imgSet.retention_days;
  if (document.activeElement !== $('set-img-gb')) $('set-img-gb').value = imgSet.max_gb;
  if (document.activeElement !== $('set-img-api')) $('set-img-api').checked = !!imgSet.api_enabled;
  $('set-img-used').textContent = `Using ${bytes(imgSet.used_bytes)} now.`;
}
$('set-img-save').onclick = async () => {
  try {
    imgSet = await post('/api/images/settings', { retention_days: +$('set-img-days').value || 0, max_gb: +$('set-img-gb').value || 0, api_enabled: $('set-img-api').checked });
    toast('Picture limits saved'); imgLoadSettings();
  } catch (e) { toast(e.message); }
};
