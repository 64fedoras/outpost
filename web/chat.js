'use strict';
// Chat page. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- chat (chat.go) ----------
let chatES = null, chatCur = null, chatLive = {}, chatPickOpen = false, chatEditing = null, chatPendingInstr = null, chatPendingRun = false;
// On a phone: Enter makes a new line (the Send button sends); Ctrl+Enter still sends from a keyboard.
const chatTouch = () => window.matchMedia('(pointer: coarse)').matches;
async function chatOpen(id) {
  chatCur = null; chatLive = {}; chatPickOpen = false; chatEditing = null; chatPendingInstr = null; chatPendingRun = false;
  chatPlaceholder();
  chatPanelSet(false, true);
  $('chat-title').textContent = 'New chat';
  await chatList();
  fillChatNodes();
  if (id) await chatLoad(id); else { chatPickRender(); chatButtons(); }
  setTimeout(() => $('chat-text').focus(), 0);
}
async function chatList() {
  let list = [];
  try { list = await api('/api/chats'); } catch (e) { /* offline */ }
  const n = $('chat-count'); n.textContent = list.length; n.classList.toggle('hidden', !list.length);
  const q = $('chat-find').value.trim();
  let snips = {};
  if (q) {
    // Title matches as you type; message text through the same search the sidebar uses.
    let hits = [];
    try { hits = await api('/api/search?q=' + encodeURIComponent(q)); } catch (e) { /* offline */ }
    if ($('chat-find').value.trim() !== q) return; // a newer search is on its way
    (hits || []).filter((h) => h.kind === 'chat').forEach((h) => { snips[h.id] = h.snippet || ''; });
    const words = q.toLowerCase().split(/\s+/);
    list = list.filter((c) => c.id in snips || words.every((w) => (c.title || '').toLowerCase().includes(w)));
  }
  setHTML('chat-list', list.map((c) => `<a href="#/chat/${esc(c.id)}" class="${c.id === pageArg ? 'active' : ''}"><span class="cm"><span class="ct">${esc(c.title)}</span>${snips[c.id] ? `<span class="snip">${esc(snips[c.id])}</span>` : `<span class="cs">${c.messages} message${c.messages === 1 ? '' : 's'}</span>`}</span><span class="cr">${c.busy ? '<span class="dot" aria-label="answering"></span>' : esc(ago(c.updated))}</span></a>`).join(''));
  if (!list.length) setHTML('chat-list', '');
  const p = document.querySelector('.chat-panel > .muted');
  const empty = q ? 'No chats match.' : 'No chats yet.';
  if (!list.length && !p) $('chat-list').insertAdjacentHTML('beforebegin', `<p class="muted u6">${empty}</p>`);
  else if (!list.length && p) p.textContent = empty;
  else if (list.length && p) p.remove();
}
let chatFindT = null;
$('chat-find').addEventListener('input', () => { clearTimeout(chatFindT); chatFindT = setTimeout(chatList, 200); });
// The chats panel: open by default on a wide screen (remembered), a full-screen sheet on a phone.
const chatPhone = () => window.matchMedia('(max-width: 860px)').matches;
function chatPanelSet(open, reset) {
  const w = $('chat-wrap');
  if (chatPhone()) w.classList.toggle('sheet', open);
  else {
    if (reset) { let v = null; try { v = localStorage.getItem('outpost-chat-panel'); } catch (e) { /* storage optional */ } open = v === 'open'; } // closed until the person opens it: the sidebar already lists the chats
    else { try { localStorage.setItem('outpost-chat-panel', open ? 'open' : 'closed'); } catch (e) { /* storage optional */ } }
    w.classList.toggle('closed', !open);
  }
  $('chat-panel-btn').classList.toggle('on', open && !chatPhone());
  $('chat-panel-btn').setAttribute('aria-expanded', String(open));
}
function chatPanelOpen() { return chatPhone() ? $('chat-wrap').classList.contains('sheet') : !$('chat-wrap').classList.contains('closed'); }
// Model choice: Auto (the strongest free node) or a node with a model ready.
function chatChoices() {
  const nodes = (state ? state.nodes : []).filter((n) => n.online && n.heartbeat && n.heartbeat.loaded_model);
  // The dot is grey when nothing could answer: no loaded model and no node that may load one for Auto.
  const canAnswer = nodes.length > 0 || (state ? state.nodes : []).some((n) => n.online && n.chat_switch);
  const auto = { id: 'auto', t: 'Auto (local, strongest free node)', s: 'The best free node picks up your message', tone: canAnswer ? 'on' : 'needs', note: '' };
  const research = { id: 'research', t: 'Research (across nodes)', s: 'Splits the question, searches the web on several nodes, writes a report with sources', tone: canAnswer ? 'on' : 'needs', note: 'web' };
  return [auto, research].concat(nodes.map((n) => {
    const h = n.heartbeat, busy = h.inference_busy > 0, paused = h.paused;
    return { id: n.id, t: h.loaded_model, s: nodeName(n), tone: busy || paused ? 'needs' : 'on', note: paused ? 'paused' : busy ? 'busy' : h.inference_ready === false ? 'loading' : 'local' };
  }));
}
// Models grouped: one entry per loaded model with the nodes that have it. A model on several nodes answers
// from any free one ("model:<name>"); on a single node the choice is that node.
function chatModelGroups() {
  const by = new Map();
  chatChoices().slice(2).forEach((c) => { if (!by.has(c.t)) by.set(c.t, []); by.get(c.t).push(c); });
  return [...by].map(([name, nodes]) => {
    const free = nodes.filter((c) => c.tone === 'on' && c.note !== 'loading').length;
    return { name, nodes, free, id: nodes.length > 1 ? 'model:' + name : nodes[0].id };
  });
}
let chatSpecific = null; // model name whose per-node list is open in the picker
function fillChatNodes() {
  const cur = $('chat-node').value || (chatCur && chatCur.node) || 'auto';
  const groups = chatModelGroups().filter((g) => g.nodes.length > 1);
  setHTML('chat-node', chatChoices().map((c) => `<option value="${esc(c.id)}">${esc(c.id === 'auto' || c.id === 'research' ? c.t : `${c.t} on ${c.s}`)}</option>`)
    .concat(groups.map((g) => `<option value="${esc(g.id)}">${esc(g.name)} (any of ${g.nodes.length} nodes)</option>`)).join(''));
  $('chat-node').value = [...$('chat-node').options].some((o) => o.value === cur) ? cur : 'auto';
  if (!chatCur && page === 'chat') chatPickRender();
}
// Empty state: one card per way to answer; picking one sets the model box under the composer.
function chatPickRender() {
  if (chatCur) return;
  const sel = $('chat-node').value || 'auto', ch = chatChoices(), groups = chatModelGroups(), ready = groups.length;
  const card = (c, extra) => `<button type="button" class="mcard ${c.id === sel ? 'sel' : ''}" data-model="${esc(c.id)}" aria-pressed="${c.id === sel}"><span class="tdot ${c.tone === 'on' ? 'work' : 'needs'}"></span><span class="mt">${esc(c.t)}</span><span class="mr">${c.id === sel ? '✓ selected' : esc(c.note)}</span><span class="ms">${esc(c.s)}${extra || ''}</span></button>`;
  const gcard = (g) => {
    if (g.nodes.length === 1) return card(g.nodes[0]);
    const on = g.id === sel || g.nodes.some((n) => n.id === sel);
    const sub = `<br><span class="ms-n">on ${g.nodes.length} nodes · ${g.free} free</span>`;
    const c = { id: g.id, t: g.name, s: 'Any free node with it', tone: g.free ? 'on' : 'needs', note: g.free ? 'local' : 'busy' };
    return '<div class="mgroup">' + card(c, sub).replace('class="mcard ', `class="mcard ${on ? 'sel ' : ''}`)
      + `<div class="mpick"><button type="button" class="link-btn" data-specific="${esc(g.name)}" aria-expanded="${chatSpecific === g.name}">Pick a specific node…</button>`
      + (chatSpecific === g.name ? `<div class="mpick-list">${g.nodes.map((n) => `<button type="button" class="mpick-n ${n.id === sel ? 'sel' : ''}" data-model="${esc(n.id)}">${esc(n.s)}${n.note !== 'local' ? ' · ' + esc(n.note) : ''}</button>`).join('')}</div>` : '') + '</div></div>';
  };
  const lead = ch.slice(0, 2).map((c) => card(c));
  const all = lead.concat(groups.map(gcard));
  const selIdx = ch.findIndex((c) => c.id === sel);
  const phoneOne = () => (selIdx >= 0 && selIdx < 2 ? [lead[selIdx]] : [gcard(groups.find((g) => g.id === sel || g.nodes.some((n) => n.id === sel)) || groups[0])].filter((x) => x && x !== 'undefined'));
  const shown = chatPhone() && !chatPickOpen && ready ? phoneOne() : all;
  const more = chatPhone() ? `<button type="button" class="chat-pick-more" id="chat-pick-more" aria-expanded="${chatPickOpen}">${chatPickOpen ? 'Hide' : ready ? 'Change' : ''}</button>` : '';
  setHTML('chat-msgs', `<div class="chat-pick-h"><span>Answer with</span><span class="grow"></span><span>${ready} ${ready === 1 ? 'model' : 'models'} ready</span>${ready ? more : ''}</div><div class="chat-pick">${shown.join('')}</div>`
    + (ready ? '' : (() => {
      const first = (state ? state.nodes : []).find((n) => n.online);
      return first ? `<p class="chat-none">No model is loaded yet. <button type="button" class="small primary" data-act="best" data-id="${esc(first.id)}">Set up a model on ${esc(nodeName(first))}</button><br><span class="hint">Outpost suggests the best one that fits that machine; you confirm before anything downloads.</span></p>`
        : '<p class="chat-none">No node is online yet. <a href="#/nodes">Add one on the Nodes page</a>, then ask.</p>';
    })()));
}
async function chatLoad(id) {
  try { chatCur = await api('/api/chats/' + id); } catch (e) { setHTML('chat-msgs', `<p class="error-msg">${esc(e.message)}</p>`); return; }
  if (pageArg !== id) return;
  $('chat-node').value = [...$('chat-node').options].some((o) => o.value === chatCur.node) ? chatCur.node : 'auto';
  $('chat-title').textContent = chatCur.title || 'Chat';
  chatRender();
  if (!chatES && window.EventSource) {
    chatES = new EventSource(`/api/chats/${id}/events`);
    chatES.onmessage = (e) => {
      let ev; try { ev = JSON.parse(e.data); } catch (_) { return; }
      if (ev.t === 'start') chatLive[ev.call] = { text: ev.text || '', node: ev.node, tps: ev.tps || 0, read: ev.read || 0, total: ev.total || 0, left: ev.left || 0 };
      else if (ev.t === 'end') delete chatLive[ev.call];
      else if (ev.t === 'progress' && chatLive[ev.call]) Object.assign(chatLive[ev.call], { read: ev.read, total: ev.total, left: ev.left });
      else if (ev.t === 'delta' && chatLive[ev.call]) {
        const c = chatLive[ev.call];
        if (!ev.reason) c.text += ev.text;
        c.tps = ev.tps;
      } else if (ev.t === 'changed') { chatLoadSoon(id); return; }
      chatRenderLive();
    };
  }
}
let chatKick = null;
function chatLoadSoon(id) { clearTimeout(chatKick); chatKick = setTimeout(() => { if (page === 'chat' && pageArg === id) { chatLoad(id); chatList(); } }, 150); }
function chatButtons() {
  const busy = chatCur && chatCur.busy;
  $('chat-stop').classList.toggle('hidden', !busy);
  $('chat-send').disabled = !!busy;
  $('chat-totask').classList.toggle('hidden', !(chatCur && chatCur.messages.some((m) => m.role === 'assistant' && m.text)));
  const instr = chatCur ? chatCur.instructions : chatPendingInstr;
  $('chat-instr-btn').classList.remove('hidden');
  $('chat-instr-btn').classList.toggle('on', !!instr);
  const run = chatCur ? !!chatCur.run_code : chatPendingRun;
  $('chat-run').classList.toggle('on', run);
  $('chat-run').setAttribute('aria-pressed', String(run));
  setHTML('chat-run', `▶<span class="hide-ph"> Run code${run ? ': on' : ''}</span>`);
  $('chat-run').setAttribute('aria-label', 'Run code');
}
async function chatRunToggle() {
  if (!chatCur) { chatPendingRun = !chatPendingRun; chatButtons(); if (chatPendingRun) chatRunHint(); return; }
  try {
    const r = await post(`/api/chats/${chatCur.id}/runcode`, { on: !chatCur.run_code });
    chatCur.run_code = !chatCur.run_code; chatButtons();
    if (chatCur.run_code) chatRunHint(r.langs);
  } catch (e) { toast(e.message); }
}
function chatRunHint(langs) {
  if (!execCount()) { toast('Run code is on, but no online node allows commands. Turn on exec for a node on the Nodes page.', 6000); return; }
  toast(langs && langs.length ? `Run code on: the model can run ${langs.join(', ')}` : 'Run code on', 3500);
}
$('chat-run').onclick = chatRunToggle;

// Research mode (research.go): lanes while it works, and a record under the report.
const RS_ICON = { waiting: '·', working: '…', done: '✓', failed: '✗', stopped: '–' };
const RS_STAGE = { planning: 'Splitting the question…', lanes: 'Researching sub-questions…', writing: 'Writing the report…', done: 'Done', stopped: 'Stopped', failed: 'Failed' };
function researchLanes(r, live) {
  return `<ol class="rs-lanes">${(r.lanes || []).map((l) => {
    const stats = [l.node && esc(l.node), l.searches && `${l.searches} search${l.searches === 1 ? '' : 'es'}`, l.pages && `${l.pages} page${l.pages === 1 ? '' : 's'} read`, l.status === 'waiting' && 'waiting for a free node', l.note && esc(l.note)].filter(Boolean).join(' · ');
    const body = !live && l.findings ? `<details><summary>${esc(l.q)}</summary><div class="md rs-find">${md(l.findings)}</div></details>` : `<span class="rs-q">${esc(l.q)}</span>`;
    return `<li class="rs-lane rs-${esc(l.status)}"><span class="rs-ic" aria-hidden="true">${RS_ICON[l.status] || ''}</span><div class="rs-t">${body}${stats ? `<div class="muted rs-s">${stats}</div>` : ''}</div></li>`;
  }).join('')}</ol>`;
}
function researchLive(r) {
  return `<div class="rs-box"><div class="rs-h">🔎 Research <span class="muted">· ${esc(RS_STAGE[r.stage] || r.stage)}</span></div>${researchLanes(r, true)}</div>`;
}
function researchCard(m) {
  const r = m.research;
  if (!r) return '';
  const n = (r.lanes || []).length;
  return `<details class="rs-box rs-rec"><summary>🔎 Research: ${n} sub-question${n === 1 ? '' : 's'}${r.stage !== 'done' ? ' · ' + esc(RS_STAGE[r.stage] || r.stage) : ''}</summary>${researchLanes(r, false)}</details>`;
}

// Code the model ran for an answer (chat_run.go): code, output and returned files.
function chatRuns(m) {
  if (!(m.runs || []).length) return '';
  const src = (f, dl) => `/api/chats/${encodeURIComponent(chatCur.id)}/run-file?file=${encodeURIComponent(f)}${dl ? '&download=1' : ''}`;
  const lang = { python: 'Python', go: 'Go', sh: 'Shell' };
  return `<div class="runs">${m.runs.map((r) => {
    const ok = !r.error && r.exit === 0;
    const head = `<span class="run-st ${ok ? 'ok' : 'bad'}" aria-hidden="true">${ok ? '✓' : '✗'}</span> Ran ${lang[r.lang] || esc(r.lang)}${r.node ? ' on ' + esc(r.node) : ''} · ${r.error ? esc(r.error) : 'exit ' + r.exit}${r.ms ? ' · ' + (r.ms < 1000 ? r.ms + ' ms' : (r.ms / 1000).toFixed(1) + ' s') : ''}${(r.files || []).length ? ` · ${r.files.length} file${r.files.length === 1 ? '' : 's'}` : ''}`;
    const outText = [r.stdout && r.stdout.trim(), r.stderr && r.stderr.trim() && 'stderr:\n' + r.stderr.trim()].filter(Boolean).join('\n');
    const imgs = (r.files || []).filter((f) => /\.(png|jpe?g|gif|webp|svg)$/i.test(f.name));
    const files = (r.files || []).length ? `<div class="run-files">${imgs.map((f) => `<a href="${src(f.file)}" target="_blank" rel="noopener"><img src="${src(f.file)}" alt="${esc(f.name)}" loading="lazy"></a>`).join('')}<div class="run-dl">${r.files.map((f) => `<a href="${src(f.file, true)}" download="${esc(f.name)}">⤓ ${esc(f.name)}</a> <span class="muted">${bytes(f.bytes)}</span>`).join(' · ')}</div></div>` : '';
    return `<div class="run-card"><details><summary>${head}</summary><pre class="run-code">${esc(r.code)}</pre>${outText ? `<div class="run-lbl">Output</div><pre class="run-out">${esc(outText)}</pre>` : '<div class="run-lbl muted">No output</div>'}</details>${files}</div>`;
  }).join('')}</div>`;
}
// Version arrows (‹ 2/3 ›) on a message that has other versions of the conversation from there on.
function chatVer(m, busy) {
  const n = (m.alts || []).length + 1, v = m.ver || 0;
  if (n < 2) return '';
  return `<button data-ver="${v - 1}" data-msg="${esc(m.id)}" ${v <= 0 || busy ? 'disabled' : ''} aria-label="Previous version">‹</button><span class="ver">${v + 1} / ${n}</span><button data-ver="${v + 1}" data-msg="${esc(m.id)}" ${v >= n - 1 || busy ? 'disabled' : ''} aria-label="Next version">›</button>`;
}
function chatTryOn(m, busy) {
  const opts = chatChoices().filter((c) => c.id !== 'research').map((c) => `<option value="${esc(c.id)}">${esc(c.id === 'auto' ? 'Auto' : `${c.t} on ${c.s}`)}</option>`).join('');
  return `<select data-tryon="${esc(m.id)}" ${busy ? 'disabled' : ''} aria-label="Answer again on another node"><option value="">↻ Try on…</option>${opts}</select>`;
}
function chatBubble(m, i, last) {
  const busy = !!(chatCur && chatCur.busy);
  if (m.role === 'user') {
    if (chatEditing === m.id) {
      return `<div class="bubble-row"><div class="bubble-edit"><textarea id="chat-edit-text" rows="3" aria-label="Edit message">${esc(m.text)}</textarea>
        <div class="bubble-edit-actions"><span class="hint">Sending answers again from here. The old message and what followed stay as an earlier version.</span><button class="small" data-edit-cancel>Cancel</button><button class="small primary" data-edit-send="${esc(m.id)}">Send</button></div></div></div>`;
    }
    const src = (n) => `/api/chats/${encodeURIComponent(chatCur.id)}/image?name=${encodeURIComponent(n)}`;
    const pics = (m.images || []).length ? `<div class="bubble-imgs">${m.images.map((n) => `<a href="${src(n)}" target="_blank" rel="noopener"><img src="${src(n)}" alt="attached image" loading="lazy"></a>`).join('')}</div>` : '';
    const dsrc = (f) => `/api/chats/${encodeURIComponent(chatCur.id)}/doc?file=${encodeURIComponent(f)}`;
    const docs = (m.docs || []).length ? `<div class="bubble-docs">${m.docs.map((d) => {
      const what = [d.pages && `${d.pages} ${d.kind === 'xlsx' ? (d.pages === 1 ? 'sheet' : 'sheets') : (d.pages === 1 ? 'page' : 'pages')}`, `${(d.words || 0).toLocaleString()} words`, d.cut && 'cut to fit'].filter(Boolean).join(' · ');
      return `<span class="doc-chip${d.warning ? ' warn' : ''}"><span aria-hidden="true">📄</span> <span class="doc-n">${esc(d.name)}</span> <span class="muted">${esc(what)}</span>${d.file ? ` <a href="${dsrc(d.file)}" target="_blank" rel="noopener">Text</a>` : ''}${d.warning ? `<span class="doc-w">${esc(d.warning)}</span>` : ''}</span>`;
    }).join('')}</div>` : '';
    const acts = `<div class="chat-acts">${chatVer(m, busy)}<button data-edit="${esc(m.id)}" ${busy ? 'disabled' : ''} title="Change this message and answer again">✎ Edit</button><button data-copy-msg="${i}" title="Copy the text">⧉ Copy</button></div>`;
    return `<div class="bubble-row"><div class="bubble-col"><div class="bubble">${esc(m.text)}${pics}${docs}</div>${acts}</div></div>`;
  }
  const meta = [m.model && `${esc(m.model)}`, m.node && `on ${esc(m.node)}`, m.tok_s && `${m.tok_s} tok/s`, m.note && esc(m.note), chatNeedTag(m)].filter(Boolean).join(' · ');
  const acts = `<div class="chat-acts">${chatVer(m, busy)}<button data-regen="${esc(m.id)}" ${busy ? 'disabled' : ''} title="Answer again; this answer stays as an earlier version">↻ Regenerate</button>${chatTryOn(m, busy)}${m.text ? `<button data-copy-msg="${i}" title="Copy the answer">⧉ Copy</button>` : ''}${m.research && m.text ? `<button data-rs-open="${esc(m.id)}" title="Open the report in the side panel to download it or save it to a project">¶ Open report</button>` : ''}<button data-branch="${esc(m.id)}" title="Start a new chat with the conversation up to here">⑂ Branch</button></div>`;
  if (m.error) return `<div class="chat-answer">${chatRuns(m)}${researchCard(m)}<div class="error-msg">${m.error === 'stopped' ? 'Stopped.' : esc(m.error)}</div>${m.note ? `<div class="sub">${esc(m.note)}</div>` : ''}${acts}</div>`;
  return `<div class="chat-answer">${chatRuns(m)}${researchCard(m)}<div class="chat-text md">${md(m.text)}</div>${pvCards(pvCollect(m.text))}${meta ? `<div class="sub">${meta}</div>` : ''}${acts}${memSuggestCard(m, { chat: chatCur.id })}</div>`;
}
function chatRender() {
  if (!chatCur) return;
  const box = $('chat-msgs'), stick = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
  const instr = chatCur.instructions ? `<div class="note chat-instr">Instructions for this chat: <i>${esc(chatCur.instructions)}</i> <button class="link-btn" data-instr>Edit</button></div>` : '';
  pvReset();
  setHTML('chat-msgs', instr + chatCur.messages.map((m, i) => chatBubble(m, i)).join('') + '<div id="chat-live"></div>');
  chatRenderLive();
  chatButtons();
  if (stick) box.scrollTop = box.scrollHeight;
}
// The card shown while Auto gets a model: a question with a countdown, then a progress line.
let chatPromptKey = '', chatPromptAt = 0, chatPromptTimer = 0;
function chatPromptCard(p) {
  const key = chatCur.id + '|' + p.state + '|' + p.model + '|' + p.deadline_ms;
  if (key !== chatPromptKey) { chatPromptKey = key; chatPromptAt = Date.now(); }
  const size = bytes(p.bytes), where = `<b>${esc(p.node_name)}</b>`;
  if (p.state !== 'ask') return `<div class="chat-prompt"><div class="status">${esc(p.text || 'Loading…')}</div>${p.loaded ? '<div class="tag">The loaded model answers if this fails.</div>' : ''}</div>`;
  const rm = (p.removals || []).length;
  const title = rm ? `Make room for ${esc(p.label)} (${size}) on ${where}?` : `Download ${esc(p.label)} (${size}) to ${where}?`;
  const list = rm ? `<div class="list">${p.removals.map((r) => `<div><span>${esc(r.label || r.name)} <span class="pill">${esc(r.kind)}</span></span><span class="gone">remove ${bytes(r.bytes)}${r.restorable ? ' · can be copied back' : ''}</span></div>`).join('')}</div>` : '';
  const what = p.auto_go ? `Starting in <span id="chat-prompt-left">${p.seconds}</span> s unless you cancel` : `Waiting <span id="chat-prompt-left">${p.seconds}</span> s for your OK, then answering with the loaded model`;
  return `<div class="chat-prompt"><b>${title}</b><div class="tag">Needed for a ${esc(p.need)} message${rm ? '. Removed models are not kept.' : ''}</div>${list}<div class="status">${what}</div><div class="btns"><button class="btn p" data-prompt="go">${rm ? 'Do it now' : 'Download now'}</button>${p.loaded ? `<button class="btn" data-prompt="loaded">Use loaded model instead</button>` : ''}<button class="btn" data-prompt="cancel">Cancel</button></div></div>`;
}
function chatPromptTick() {
  clearInterval(chatPromptTimer);
  if (!chatCur || !chatCur.prompt || chatCur.prompt.state !== 'ask') return;
  const p = chatCur.prompt;
  chatPromptTimer = setInterval(() => {
    const el = document.getElementById('chat-prompt-left');
    if (!el) { clearInterval(chatPromptTimer); return; }
    el.textContent = Math.max(0, p.seconds - Math.floor((Date.now() - chatPromptAt) / 1000));
  }, 500);
}
function chatNeedTag(m) {
  if (!m.need) return '';
  const o = (v, t) => `<option value="${v}" ${m.need === v ? 'selected' : ''}>${t}</option>`;
  return `<span class="need-tag" title="Auto chose the model for this kind of message. Change it to answer again for another kind.">chosen for <select data-need="${esc(m.id)}" aria-label="Chosen for">${o('coding', 'coding')}${o('vision', 'vision')}${o('general', 'general')}</select></span>`;
}
function chatRenderLive() {
  const el = $('chat-live');
  if (!el || !chatCur) return;
  const calls = Object.values(chatLive);
  const rs = chatCur.research ? researchLive(chatCur.research) : '';
  if (rs && (chatCur.research.stage !== 'writing' || !calls.length)) { setHTML('chat-live', rs); return; }
  if (chatCur.prompt) { setHTML('chat-live', chatPromptCard(chatCur.prompt)); chatPromptTick(); return; }
  if (chatCur.busy === 'running') { setHTML('chat-live', `<p class="muted run-live">▶ Running code on ${esc(chatCur.run_on || 'a node')}…</p>`); return; }
  if (!calls.length) { el.innerHTML = chatCur.busy === 'waiting' ? '<p class="muted">Waiting for a free node…</p>' : ''; return; }
  const c = calls[0];
  const rate = liveRate(Object.assign({}, c, { text: c.text }));
  setHTML('chat-live', `${rs}<div class="chat-answer"><div class="chat-text md">${md(c.text) || '<span class="muted">…</span>'}</div><div class="sub">${esc(c.node || '')}${rate ? ' · ' + esc(rate) : ''}</div></div>`);
  const box = $('chat-msgs');
  if (box.scrollHeight - box.scrollTop - box.clientHeight < 120) box.scrollTop = box.scrollHeight;
}
async function chatSend() {
  const text = $('chat-text').value.trim(), images = chatImg.payload(), docs = chatImg.docs();
  if (!text && !images.length && !docs.length) return;
  $('chat-send').disabled = true;
  try {
    if (!chatCur) {
      const body = { text, node: $('chat-node').value, images, docs };
      if (chatPendingInstr !== null) body.instructions = chatPendingInstr;
      if (chatPendingRun) body.run_code = true;
      const r = await post('/api/chats', body);
      $('chat-text').value = ''; $('chat-text').style.height = ''; chatImg.clear();
      location.hash = '#/chat/' + r.id;
      return;
    }
    await post(`/api/chats/${chatCur.id}/messages`, { text, node: $('chat-node').value, images, docs });
    $('chat-text').value = ''; $('chat-text').style.height = ''; chatImg.clear();
    await chatLoad(chatCur.id);
  } catch (e) { toast(e.message); }
  chatButtons();
}
$('chat-send').onclick = chatSend;
// Desktop: Enter sends by default; Settings → Appearance (or the key badge by Send) switches to Ctrl+Enter. Shift+Enter is always a new line.
const chatCtrlSend = () => { try { return localStorage.getItem('outpost.chatSendKey') === 'ctrl'; } catch (e) { return false; } };
function chatSendKeySet(ctrl) {
  try { if (ctrl) localStorage.setItem('outpost.chatSendKey', 'ctrl'); else localStorage.removeItem('outpost.chatSendKey'); } catch (e) { /* storage optional */ }
  chatPlaceholder();
  const c = document.getElementById('set-chatkey'); if (c) c.checked = ctrl;
}
$('chat-text').addEventListener('keydown', (e) => {
  if (e.key !== 'Enter' || e.isComposing || e.shiftKey || chatTouch()) return;
  if (!chatCtrlSend() || e.ctrlKey || e.metaKey) { e.preventDefault(); chatSend(); }
});
$('chat-key').onclick = () => chatSendKeySet(!chatCtrlSend());
const chatNew = () => { if (location.hash === '#/chat') chatOpen(null); else location.hash = '#/chat'; };
$('chat-new').onclick = chatNew; $('chat-new2').onclick = chatNew;
$('chat-panel-btn').onclick = () => chatPanelSet(!chatPanelOpen());
$('chat-panel-x').onclick = () => chatPanelSet(false);
$('chat-node').addEventListener('change', () => { chatPickRender(); if (typeof chatImg !== 'undefined') chatImg.refresh(); });
$('chat-msgs').addEventListener('click', (e) => { const sp = e.target.closest('[data-specific]'); if (sp) { chatSpecific = chatSpecific === sp.dataset.specific ? null : sp.dataset.specific; chatPickRender(); return; }
  const b = e.target.closest('[data-model]'); if (b) { $('chat-node').value = b.dataset.model; chatPickOpen = false; chatPickRender(); $('chat-text').focus(); return; }
  if (e.target.closest('#chat-pick-more')) { chatPickOpen = !chatPickOpen; chatPickRender(); return; }
  chatAct(e.target); });
$('chat-msgs').addEventListener('change', (e) => {
  const sel = e.target.closest('[data-tryon]');
  if (sel && sel.value) chatDo('regenerate', { msg_id: sel.dataset.tryon, node: sel.value });
  const nd = e.target.closest('[data-need]');
  if (nd && nd.value) chatDo('regenerate', { msg_id: nd.dataset.need, need: nd.value });
});
// Edit, regenerate, versions, copy, branch and the instructions strip.
async function chatAct(t) {
  if (!chatCur) return;
  const b = (k) => t.closest(`[data-${k}]`);
  let el;
  if ((el = b('edit'))) { chatEditing = el.dataset.edit; chatRender(); const ta = $('chat-edit-text'); if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); } }
  else if (b('edit-cancel')) { chatEditing = null; chatRender(); }
  else if ((el = b('edit-send'))) {
    const text = $('chat-edit-text').value.trim();
    if (!text) return;
    chatEditing = null;
    await chatDo('edit', { msg_id: el.dataset.editSend, text, node: $('chat-node').value });
  }
  else if ((el = b('prompt'))) await chatDo('prompt', { action: el.dataset.prompt });
  else if ((el = b('regen'))) await chatDo('regenerate', { msg_id: el.dataset.regen });
  else if ((el = b('ver')) && !el.disabled) await chatDo('version', { msg_id: el.dataset.msg, ver: Number(el.dataset.ver) });
  else if ((el = b('copy-msg'))) { const m = chatCur.messages[Number(el.dataset.copyMsg)]; if (m) { await copyText(m.text || ''); toast('Copied'); } }
  else if ((el = b('branch'))) {
    try { const r = await post(`/api/chats/${chatCur.id}/branch`, { msg_id: el.dataset.branch }); toast('Branched into a new chat'); location.hash = '#/chat/' + r.id; } catch (e) { toast(e.message); }
  }
  else if ((el = b('rs-open'))) {
    const m = chatCur.messages.find((x) => x.id === el.dataset.rsOpen);
    if (m) pvShow([{ kind: 'md', name: pvSlug(chatCur.title, 'research') + '-report.md', text: m.text, ver: 1 }], 0);
  }
  else if (b('instr')) chatInstrOpen();
}
async function chatDo(what, body) {
  try { await post(`/api/chats/${chatCur.id}/${what}`, body); await chatLoad(chatCur.id); chatList(); } catch (e) { toast(e.message); chatRender(); }
}
async function chatInstrOpen() {
  let val = chatCur ? chatCur.instructions || '' : chatPendingInstr;
  if (val === null) { try { val = (await api('/api/settings')).chat_instructions || ''; } catch (e) { val = ''; } }
  $('chat-instr-text').value = val;
  $('chat-instr-default').checked = false;
  open('modal-chat-instr');
  setTimeout(() => $('chat-instr-text').focus(), 0);
}
$('chat-instr-btn').onclick = chatInstrOpen;
$('chat-instr-save').onclick = async () => {
  const text = $('chat-instr-text').value.trim(), def = $('chat-instr-default').checked;
  try {
    if (chatCur) { await post(`/api/chats/${chatCur.id}/instructions`, { instructions: text, default: def }); await chatLoad(chatCur.id); }
    else {
      chatPendingInstr = text;
      if (def) await post('/api/settings', { chat_instructions: text });
      chatButtons();
    }
    close('modal-chat-instr');
    toast(text ? 'Instructions saved' : 'Instructions removed');
  } catch (e) { toast(e.message); }
};
$('chat-text').addEventListener('input', () => { const t = $('chat-text'); t.style.height = 'auto'; t.style.height = Math.min(t.scrollHeight, window.innerHeight * 0.4) + 'px'; });
$('chat-stop').onclick = async () => { if (chatCur) { try { await post(`/api/chats/${chatCur.id}/stop`, {}); } catch (e) { toast(e.message); } } };
// Make this a task: the conversation becomes the new task's request.
$('chat-totask').onclick = () => {
  if (!chatCur) return;
  const lines = chatCur.messages.filter((m) => m.text).map((m) => `${m.role === 'user' ? 'Me' : 'Assistant'}: ${m.text}`);
  $('nt-prompt').value = 'Build this, based on the conversation below.\n\n' + lines.join('\n\n');
  try { ntDraft.flush && ntDraft.flush(); } catch (e) { /* draft store optional */ }
  location.hash = '#/new';
  setTimeout(() => { $('nt-prompt').value = 'Build this, based on the conversation below.\n\n' + lines.join('\n\n'); autoGrow($('nt-prompt')); }, 50);
};

function chatPlaceholder() {
  $('chat-text').placeholder = chatTouch() ? 'Ask anything…' : chatCtrlSend() ? 'Ask anything… (Ctrl+Enter sends, Enter adds a line)' : 'Ask anything… (Enter sends, Shift+Enter adds a line)';
  $('chat-key').textContent = chatCtrlSend() ? 'Ctrl+Enter' : 'Enter';
  $('chat-key').title = chatCtrlSend() ? 'Ctrl+Enter sends. Click to make Enter send instead.' : 'Enter sends. Click to require Ctrl+Enter instead.';
}
chatPlaceholder();
// Keep the page the size of what is really visible: Chrome on Android lets the keyboard cover the page,
// which used to scroll the header away. Size the chat page to the visual viewport and keep the page at the top.
(function () {
  const vv = window.visualViewport;
  if (!vv) return;
  const fit = () => {
    document.documentElement.style.setProperty('--vvh', vv.height + 'px');
    if (page === 'chat' && chatPhone() && (vv.offsetTop || window.scrollY)) window.scrollTo(0, 0);
  };
  vv.addEventListener('resize', fit); vv.addEventListener('scroll', fit); fit();
})();
