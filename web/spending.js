'use strict';
// Spending page: what Claude and OpenRouter cost per day and per task, from the
// controller's ledger (spend.go), and the monthly cap for all cloud planner calls.

let spendMonth = '';
let spendData = null;
let spendAt = 0; // when the page last fetched, so polling refreshes it every 10 s

async function loadSpending() {
  spendAt = Date.now();
  try {
    spendData = await api('/api/spending' + (spendMonth ? '?month=' + encodeURIComponent(spendMonth) : ''));
  } catch (e) {
    setHTML('sp-tiles', `<div class="error-msg">${esc(e.message)}</div>`);
    return;
  }
  renderSpending();
}

function monthLabel(key) {
  const [y, m] = key.split('-').map(Number);
  return new Date(y, m - 1, 1).toLocaleDateString(undefined, { month: 'long', year: 'numeric' });
}

function spendTokens(n) {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return Math.round(n / 1e3) + 'k';
  return String(n || 0);
}

function renderSpending() {
  const d = spendData;
  if (!d) return;
  const sel = $('sp-month');
  const opts = d.months.map((m) => `<option value="${esc(m)}"${m === d.month ? ' selected' : ''}>${esc(monthLabel(m))}</option>`).join('');
  if (sel._h !== opts) { sel.innerHTML = opts; sel._h = opts; }
  sel.value = d.month;

  const capOn = d.cap_usd > 0;
  const pct = capOn ? Math.min(100, Math.round((d.total / d.cap_usd) * 100)) : 0;
  const capLine = capOn
    ? (d.current ? `of ${usd(d.cap_usd)} monthly cap` : `monthly cap is now ${usd(d.cap_usd)}`)
    : 'no monthly cap';
  const bar = capOn && d.current ? `<progress class="sp-bar${pct >= 100 ? ' over' : pct >= 80 ? ' near' : ''}" max="100" value="${pct}" aria-label="${pct}% of the monthly cap"></progress>` : '';
  const avg = d.tasks ? d.total / d.tasks : 0;
  setHTML('sp-tiles', `
    <div class="tile"><span class="tile-k">${d.current ? 'This month' : esc(monthLabel(d.month))}</span><span class="tile-v">${usd(d.total)}</span><span class="sub">${capLine}</span>${bar}</div>
    <div class="tile"><span class="tile-k">Tasks that used the cloud</span><span class="tile-v">${d.tasks}</span><span class="sub">${d.calls} call${d.calls === 1 ? '' : 's'}</span></div>
    <div class="tile"><span class="tile-k">Average per cloud task</span><span class="tile-v">${usd(avg)}</span></div>
    <div class="tile"><span class="tile-k">Claude · OpenRouter</span><span class="tile-v">${usd(d.claude)}</span><span class="sub">OpenRouter ${usd(d.openrouter)}</span></div>`);

  const paused = $('sp-paused');
  paused.classList.toggle('hidden', !d.paused || !d.current);
  if (d.paused) paused.textContent = `${d.paused} task${d.paused === 1 ? ' is' : 's are'} paused at the monthly cap. Raise the cap below, or open the task and finish it with a local planner.`;

  // Bar chart: one stacked column per day, drawn as SVG (no inline styles under the CSP).
  const max = Math.max(...d.days.map((x) => x.claude + x.openrouter), 0);
  const W = 100 / d.days.length;
  const H = 120;
  const cols = d.days.map((x, i) => {
    const c = max ? (x.claude / max) * H : 0;
    const o = max ? (x.openrouter / max) * H : 0;
    const tip = `${x.day}: ${usd(x.claude + x.openrouter)}${x.openrouter ? ` (OpenRouter ${usd(x.openrouter)})` : ''}`;
    const xpos = (i * W + W * 0.12).toFixed(3);
    const w = (W * 0.76).toFixed(3);
    return `<g><title>${esc(tip)}</title>
      <rect class="sp-c" x="${xpos}" y="${(H - c).toFixed(2)}" width="${w}" height="${c.toFixed(2)}"></rect>
      <rect class="sp-o" x="${xpos}" y="${(H - c - o).toFixed(2)}" width="${w}" height="${o.toFixed(2)}"></rect>
      <rect class="sp-hit" x="${(i * W).toFixed(3)}" y="0" width="${W.toFixed(3)}" height="${H}"></rect></g>`;
  }).join('');
  const last = d.days.length;
  const mon = monthLabel(d.month).split(' ')[0].slice(0, 3);
  setHTML('sp-chart', max
    ? `<svg viewBox="0 0 100 ${H}" preserveAspectRatio="none" role="img" aria-label="Cloud spend per day">${cols}<line class="sp-base" x1="0" x2="100" y1="${H}" y2="${H}"></line></svg>
       <div class="sp-axis"><span>1 ${esc(mon)}</span><span>${last} ${esc(mon)}</span></div>`
    : '<div class="empty">No cloud planner calls this month.</div>');
  setHTML('sp-peak', max ? 'busiest day ' + usd(max) : '');

  setHTML('sp-top', d.top.length
    ? `<div class="table-wrap"><table><thead><tr><th>Task</th><th class="sp-wide">Planner</th><th class="r sp-wide">Calls</th><th class="r sp-wide">Tokens in / out</th><th class="r">Cost</th></tr></thead><tbody>${
      d.top.map((t) => `<tr${t.deleted ? '' : ` class="click" data-act="sp-open" data-id="${esc(t.id)}"`}><td>${esc(t.title || 'Untitled task')}${t.deleted ? ' <span class="sub">(deleted)</span>' : ''}</td><td class="sp-wide">${esc(t.planner)}</td><td class="r sp-wide">${t.calls}</td><td class="r sp-wide">${spendTokens(t.in)} / ${spendTokens(t.out)}</td><td class="r">${usd(t.usd)}</td></tr>`).join('')
    }</tbody></table></div>`
    : '<div class="empty">No task used a cloud planner this month.</div>');

  const capIn = $('sp-cap');
  if (document.activeElement !== capIn) capIn.value = capOn ? String(d.cap_usd) : '';
}

$('sp-month').addEventListener('change', (e) => { spendMonth = e.target.value; loadSpending(); });
$('sp-top').addEventListener('click', (e) => {
  const row = e.target.closest('[data-act="sp-open"]');
  if (row) location.hash = '#/tasks/' + row.dataset.id;
});
$('sp-cap-save').addEventListener('click', async () => {
  const raw = $('sp-cap').value.trim();
  const v = raw === '' ? 0 : Number(raw);
  const msg = $('sp-cap-msg');
  if (!Number.isFinite(v) || v < 0) { msg.textContent = 'Enter a dollar amount, or leave it empty for no cap.'; return; }
  try {
    spendData = await post('/api/spending/cap', { cap_usd: v });
    if (spendMonth && spendMonth !== spendData.month) await loadSpending(); else renderSpending();
    msg.textContent = v ? 'Saved. The cap is ' + usd(v) + ' a month.' : 'Saved. No monthly cap.';
  } catch (e) { msg.textContent = e.message; }
});
$('sp-cap').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('sp-cap-save').click(); });
