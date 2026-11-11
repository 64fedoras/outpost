'use strict';
// New task page, saved and scheduled tasks. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- new task ----------
function plannerOptions(selected) {
  const opts = [];
  opts.push(`<option value="auto" ${selected === 'auto' ? 'selected' : ''}>Planner: Auto (best ready node)</option>`);
  opts.push(`<option value="claude" ${state.has_claude_key ? '' : 'disabled'} ${selected === 'claude' ? 'selected' : ''}>Planner: Claude${state.has_claude_key ? '' : ' (add an API key in Settings)'}</option>`);
  const orOk = state.has_openrouter_key && state.openrouter_model;
  opts.push(`<option value="openrouter" ${orOk ? '' : 'disabled'} ${selected === 'openrouter' ? 'selected' : ''}>Planner: ${state.has_openrouter_key ? (state.openrouter_model ? esc(state.openrouter_label) : 'OpenRouter (pick a model in Settings)') : 'OpenRouter (add an API key in Settings)'}</option>`);
  for (const n of state.nodes) {
    const hb = n.online && n.heartbeat, model = hb && hb.loaded_model;
    const v = 'node:' + n.id;
    opts.push(`<option value="${v}" ${selected === v ? 'selected' : ''}>Planner: ${esc(nodeName(n))}${model ? ' (' + esc(model) + ')' : ' (no model loaded)'}</option>`);
  }
  return opts.join('');
}
function claudeModelOptions(selected, short) {
  const list = (settings && settings.claude_models) || [{ id: 'claude-sonnet-5-5', label: 'Claude Sonnet 5.5', in: 2, out: 10 }, { id: 'claude-opus-5-5', label: 'Claude Opus 5.5', in: 4, out: 20 }, { id: 'claude-haiku-5-5', label: 'Claude Haiku 5.5', in: 0.1, out: 0.5 }];
  return list.map((m) => `<option value="${m.id}" ${m.id === selected ? 'selected' : ''} title="$${m.in} in / $${m.out} out per million tokens">${esc(short ? m.label.replace(/^Claude /, '') : m.label)}${short ? '' : ` ($${m.in} / $${m.out} per M tokens)`}</option>`).join('');
}
const STARTERS = [
  ['Four small text tools', 'Four independent Python command-line tools in separate files: word count, find-duplicate-lines, CSV-to-JSON and JSON-to-CSV. Each is under 60 lines, with its own small unit test.'],
  ['A tiny to-do web app', 'A to-do list app in four small independent parts: a Go REST API with in-memory storage, a plain HTML/JS page that calls it, a Go test file for the handlers, and a short README with curl examples.'],
  ['A unit converter in 4 languages', 'The same small unit converter (length, weight, temperature) written separately in Python, Go, JavaScript and Bash, each as one file under 80 lines with a few built-in test cases.'],
  ['A string utility library', 'A small Python package with four independent modules (slugify, truncate-with-ellipsis, case conversion, simple templating), each with its own pytest file.'],
  ['A log summariser', 'Four small independent scripts that read a sample nginx access log: top paths, status code counts, top client IPs and requests per hour. Include the sample log and one test per script.'],
  ['A mini static site', 'A tiny static website made of four independent pieces: index.html, about.html, one shared style.css and a small Python script that checks all internal links work.'],
  ['Four classic algorithms', 'Four separate Python files, each with one algorithm and a few tests: binary search, merge sort, Dijkstra on a small graph, and an LRU cache.'],
  ['A habit tracker page', 'A single-page habit tracker split into four files: index.html, style.css, app.js for adding and ticking habits in localStorage, and streak.js for the streak counting with a small Node test.'],
  ['Small data cleaning scripts', 'Four independent Python scripts that each clean one aspect of a messy sample CSV: trim whitespace, normalise dates, remove duplicate rows and flag missing values. Include the sample file and a test each.'],
  ['A flashcard quiz', 'A terminal flashcard quiz in four small parts: a JSON card loader, a quiz loop with random order, a score tracker that saves results, and a sample deck, each with a small test.'],
  ['A simple bank ledger', 'A tiny ledger in Go split into four small files: account type with deposit and withdraw, transaction history, a text report formatter and a small CLI main, with one test file for the first three.'],
  ['A markdown note toolkit', 'Four independent Python scripts working on a folder of markdown notes: list all headings, find broken links, count words per note and build a table of contents file. Include three sample notes and a test each.'],
];
// The new-task screen shows a random few of these each time the page loads.
const STARTERS_SHOWN = 3;
let ntInit = false;
let ntFiles = [];
function renderNew() {
  const sel = $('nt-planner');
  const cur = sel.value || state.default_planner || 'claude';
  setHTML(sel, plannerOptions(state.has_claude_key || cur !== 'claude' ? cur : (state.nodes[0] ? 'node:' + state.nodes[0].id : 'claude')));
  const df = $('nt-fix').options[0], dt = $('nt-testmin').options[0];
  if (state.default_fix_rounds) df.textContent = `Default (${state.default_fix_rounds})`;
  if (state.default_test_minutes) dt.textContent = `Default (${state.default_test_minutes} min)`;
  if (!ntInit) {
    ntInit = true;
    $('nt-cap').value = state.default_cap_usd || 2;
    if (!settings) api('/api/settings').then((s) => { settings = s; setHTML('nt-model', claudeModelOptions(state.claude_model, true)); renderOptLine(); }).catch(() => {});
    setHTML('nt-model', claudeModelOptions(state.claude_model, true));
    const order = STARTERS.map((_, i) => i);
    for (let i = order.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [order[i], order[j]] = [order[j], order[i]]; }
    setHTML('nt-starters', order.slice(0, STARTERS_SHOWN).map((i) => `<button class="starter" data-act="starter" data-i="${i}"><b>${esc(STARTERS[i][0])}</b><span>${esc(STARTERS[i][1])}</span></button>`).join(''));
  }
  const psel = $('nt-project'), pcur = psel.value || localStorage.getItem('nt-project') || '';
  setHTML(psel, `<option value="">No project</option>` + projects.map((p) => `<option value="${p.id}" ${p.id === pcur ? 'selected' : ''}>Project: ${esc(p.name)}</option>`).join(''));
  const labels = state.labels || [], lcur = $('nt-label').value;
  setHTML('nt-label', `<option value="">any</option>` + labels.map((l) => `<option value="${esc(l)}" ${l === lcur ? 'selected' : ''}>labelled ${esc(l)}</option>`).join(''));
  $('nt-label-wrap').classList.toggle('hidden', !labels.length);
  renderSaved();
  $('nt-model').classList.toggle('hidden', sel.value !== 'claude');
  $('nt-cap-wrap').classList.toggle('hidden', sel.value !== 'claude' && sel.value !== 'openrouter');
  renderOptLine();
  const w = workerCount(), ex = execCount(), on = state.nodes.filter((n) => n.online).length;
  setHTML('new-cluster', state.nodes.length
    ? `${on} of ${state.nodes.length} nodes online, ${w} with a model ready${ex ? `, ${ex} can run tests` : '. No node can run tests yet (tick exec on the Nodes page)'}.`
    : 'No nodes yet. Add one on the Nodes page, then come back here.');
  const steps = [];
  if (!state.nodes.length) steps.push(`Add a node on the <a href="#/nodes">Nodes</a> page.`);
  if (models && !models.models.some((m) => m.status === 'ready')) steps.push(`Add a model on the <a href="#/models">Models</a> page.`);
  if (state.nodes.length && !w) steps.push(`Load a model on a node (<a href="#/nodes">Nodes</a> → Load model).`);
  if (!state.has_claude_key && !state.has_openrouter_key) steps.push(`Optional: add a Claude or OpenRouter API key in <a href="#/settings">Settings</a> to plan with a cloud model.`);
  if (!state.nodes.some((n) => n.exec_enabled)) steps.push(`Optional: tick <b>exec</b> on a node so tasks can run their tests.`);
  setHTML('home-setup', steps.length ? `<div class="setup"><b>Before your first task</b><ol>${steps.map((s) => `<li>${s}</li>`).join('')}</ol></div>` : '');
}
// One line that says what "Plan it" will use; the controls sit behind "Change options".
function renderOptLine() {
  const sel = $('nt-planner'), opt = sel.selectedOptions[0];
  const claude = sel.value === 'claude', paid = claude || sel.value === 'openrouter';
  const model = $('nt-model').selectedOptions[0];
  const parts = [claude ? 'Claude ' + (model ? model.textContent : '') : opt ? opt.textContent.replace(/^Planner: /, '') : 'no planner'];
  if (paid) parts.push('cap $' + ($('nt-cap').value || 0));
  parts.push($('nt-review').checked ? 'peer review on' : 'peer review off');
  if ($('nt-mockup').checked) parts.push('mockups first');
  if ($('nt-testfirst').checked) parts.push('tests first');
  if (+$('nt-fix').value) parts.push(`${$('nt-fix').value} fix round${$('nt-fix').value === '1' ? '' : 's'}`);
  if (+$('nt-testmin').value) parts.push(`tests up to ${$('nt-testmin').value} min`);
  if ($('nt-label').value) parts.push(`only nodes labelled ${$('nt-label').value}`);
  setHTML('nt-optline', esc(parts.join(' · ')));
}
$('nt-opt-toggle').addEventListener('click', () => {
  const box = $('nt-opts'), show = box.classList.contains('hidden');
  box.classList.toggle('hidden', !show);
  $('nt-opt-toggle').setAttribute('aria-expanded', show);
  $('nt-opt-toggle').textContent = show ? 'Hide options' : 'Change options';
});
['nt-model', 'nt-cap', 'nt-review', 'nt-mockup', 'nt-testfirst', 'nt-fix', 'nt-testmin', 'nt-label'].forEach((id) => $(id).addEventListener('change', renderOptLine));
$('nt-cap').addEventListener('input', renderOptLine);
$('nt-planner').addEventListener('change', () => { renderNew(); ntImg.refresh(); });
$('nt-project').addEventListener('change', () => { try { localStorage.setItem('nt-project', $('nt-project').value); } catch (e) {} });
$('nt-files').addEventListener('change', () => {
  ntFiles = Array.from($('nt-files').files);
  $('nt-attach-n').textContent = ntFiles.length ? `${ntFiles.length} file${ntFiles.length === 1 ? '' : 's'}` : 'Attach';
});
async function startTask() {
  $('nt-err').classList.add('hidden');
  const body = { prompt: $('nt-prompt').value, planner: $('nt-planner').value, claude_model: $('nt-model').value, cap_usd: parseFloat($('nt-cap').value) || 0, review: $('nt-review').checked, mockup: $('nt-mockup').checked, test_first: $('nt-testfirst').checked, fix_rounds: +$('nt-fix').value, test_minutes: +$('nt-testmin').value, project: $('nt-project').value, node_label: $('nt-label').value, images: ntImg.payload() };
  if (!body.prompt.trim()) { $('nt-prompt').focus(); return; }
  $('nt-go').disabled = true;
  try {
    let r;
    if (ntFiles.length) {
      const fd = new FormData();
      fd.append('payload', JSON.stringify(body));
      ntFiles.forEach((f) => fd.append('files', f, f.name));
      const resp = await fetch('/api/tasks', { method: 'POST', credentials: 'same-origin', body: fd });
      r = await resp.json().catch(() => ({}));
      if (!resp.ok) throw new Error(r.error || resp.statusText);
    } else r = await post('/api/tasks', body);
    ntFiles = []; $('nt-files').value = ''; $('nt-attach-n').textContent = 'Attach';
    $('nt-prompt').value = ''; ntDraft.done(); ntImg.clear();
    location.hash = '#/tasks/' + r.id;
    refresh();
  } catch (e) { $('nt-err').textContent = e.message; $('nt-err').classList.remove('hidden'); }
  $('nt-go').disabled = false;
}
$('nt-go').onclick = startTask;

// ---------- saved tasks and schedules (templates.go) ----------
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const DAYS_LONG = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
function schedText(t) {
  const s = t.schedule;
  if (!s) return '';
  return (s.every === 'weekly' ? `Every ${DAYS_LONG[s.weekday]}` : 'Every day') + ` at ${s.at}`;
}
function renderSaved() {
  const list = state.templates || [];
  if (!list.length) { setHTML('nt-saved', ''); return; }
  setHTML('nt-saved', `<div class="section-head"><h2>Saved tasks</h2></div><div class="card saved-list">${list.map((t) => {
    const last = t.last_task ? ` · last run <a href="#/tasks/${esc(t.last_task)}">${ago(t.last_run)}</a>${t.last_status ? ' ' + pill(t.last_status) : ''}` : '';
    const sched = t.schedule ? `<span class="pill st-ready">${esc(schedText(t))}</span> ` : '';
    return `<div class="rowline"><span class="nv-min nv-grow"><b>${esc(t.name)}</b> ${sched}<span class="sub">${t.request.project ? esc((projects.find((p) => p.id === t.request.project) || {}).name || 'project') + ' · ' : ''}${t.request.node_label ? 'nodes labelled ' + esc(t.request.node_label) + ' · ' : ''}${t.next_run ? 'next ' + new Date(t.next_run).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' }) : 'not scheduled'}${last}</span>${t.last_note ? `<div class="sub warn-text">${esc(t.last_note)}</div>` : ''}</span>
      <button class="small" data-act="tpl-use" data-id="${esc(t.id)}" title="Put the request in the box above to change it first">Edit first</button><button class="small" data-act="tpl-sched" data-id="${esc(t.id)}">Schedule…</button><button class="small primary" data-act="tpl-run" data-id="${esc(t.id)}">Run now</button><button class="small danger" data-act="tpl-del" data-id="${esc(t.id)}" aria-label="Delete saved task">×</button></div>`;
  }).join('')}</div>`);
}
$('nt-save-tpl').onclick = async () => {
  const prompt0 = $('nt-prompt').value.trim();
  if (!prompt0) { $('nt-prompt').focus(); toast('Write the request first'); return; }
  const name = prompt('Name for this saved task', prompt0.split('\n')[0].slice(0, 60));
  if (name === null) return;
  const request = { prompt: prompt0, planner: $('nt-planner').value, claude_model: $('nt-model').value, cap_usd: parseFloat($('nt-cap').value) || 0, review: $('nt-review').checked, mockup: $('nt-mockup').checked, test_first: $('nt-testfirst').checked, fix_rounds: +$('nt-fix').value, test_minutes: +$('nt-testmin').value, project: $('nt-project').value, node_label: $('nt-label').value };
  try { await post('/api/templates', { name, request }); toast(ntFiles.length || ntImg.payload().length ? 'Saved (attachments and images are not kept)' : 'Saved under Saved tasks'); refresh(); } catch (e) { toast(e.message); }
};
let schedTpl = null;
function openSched(id) {
  schedTpl = (state.templates || []).find((t) => t.id === id);
  if (!schedTpl) return;
  const s = schedTpl.schedule || {};
  $('sched-name').textContent = schedTpl.name;
  $('sched-every').value = s.every || ''; $('sched-day').value = String(s.weekday || 1); $('sched-at').value = s.at || '02:00';
  $('sched-err').classList.add('hidden');
  schedShow(); open('modal-sched');
}
function schedShow() { $('sched-day-wrap').classList.toggle('hidden', $('sched-every').value !== 'weekly'); }
$('sched-every').addEventListener('change', schedShow);
$('sched-go').onclick = async () => {
  const every = $('sched-every').value;
  try {
    await post(`/api/templates/${schedTpl.id}`, { schedule: { every, at: $('sched-at').value, weekday: +$('sched-day').value } });
    close('modal-sched'); toast(every ? 'Scheduled' : 'Schedule removed'); refresh();
  } catch (e) { $('sched-err').textContent = e.message; $('sched-err').classList.remove('hidden'); }
};
async function templateAction(act, id) {
  const t = (state.templates || []).find((x) => x.id === id);
  if (!t) return;
  if (act === 'tpl-run') { const r = await post(`/api/templates/${id}/run`, {}); location.hash = '#/tasks/' + r.id; refresh(); }
  else if (act === 'tpl-sched') openSched(id);
  else if (act === 'tpl-del') { if (confirm(`Delete the saved task "${t.name}"${t.schedule ? ' and its schedule' : ''}?`)) { await api('/api/templates/' + id, { method: 'DELETE' }); toast('Deleted'); refresh(); } }
  else if (act === 'tpl-use') ntFill(t.request);
}

// ntFill puts a whole request (a saved task, or a task being duplicated) into the
// new-task form: prompt and every option. A planner, project or label that no
// longer exists falls back to the form's current choice. Returns what was not kept.
function ntFill(q) {
  renderNew(); // first visit: set the defaults before overwriting them
  const has = (id, v) => v && [...$(id).options].some((o) => o.value === v);
  const missed = [];
  const box = $('nt-prompt'); box.value = q.prompt || ''; autoGrow(box);
  if (has('nt-planner', q.planner)) $('nt-planner').value = q.planner; else if (q.planner) missed.push('planner');
  renderNew(); // the model and cap boxes follow the planner
  if (has('nt-model', q.claude_model)) $('nt-model').value = q.claude_model;
  if (q.project && has('nt-project', q.project)) $('nt-project').value = q.project; else { $('nt-project').value = ''; if (q.project) missed.push('project'); }
  $('nt-review').checked = q.review !== false; $('nt-mockup').checked = !!q.mockup; $('nt-testfirst').checked = !!q.test_first;
  $('nt-fix').value = String(q.fix_rounds || 0); $('nt-testmin').value = String(q.test_minutes || 0);
  if (q.cap_usd) $('nt-cap').value = q.cap_usd;
  if (!q.node_label || has('nt-label', q.node_label)) $('nt-label').value = q.node_label || ''; else missed.push('node label');
  renderNew(); box.focus(); window.scrollTo(0, 0);
  return missed;
}
$('nt-prompt').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) startTask(); });
