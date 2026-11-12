'use strict';
// Chat page. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- chat (chat.go) ----------
let chatES = null, chatCur = null, chatLive = {};
async function chatOpen(id) {
  chatCur = null; chatLive = {};
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
  setHTML('chat-list', list.map((c) => `<a href="#/chat/${esc(c.id)}" class="${c.id === pageArg ? 'active' : ''}"><span class="cm"><span class="ct">${esc(c.title)}</span><span class="cs">${c.messages} message${c.messages === 1 ? '' : 's'}</span></span><span class="cr">${c.busy ? '<span class="dot" aria-label="answering"></span>' : esc(ago(c.updated))}</span></a>`).join(''));
  if (!list.length) setHTML('chat-list', '');
  const p = document.querySelector('.chat-panel > .muted');
  if (!list.length && !p) $('chat-list').insertAdjacentHTML('beforebegin', '<p class="muted u6">No chats yet.</p>');
  else if (list.length && p) p.remove();
}
// The chats panel: open by default on a wide screen (remembered), a full-screen sheet on a phone.
const chatPhone = () => window.matchMedia('(max-width: 860px)').matches;
function chatPanelSet(open, reset) {
  const w = $('chat-wrap');
  if (chatPhone()) w.classList.toggle('sheet', open);
  else {
    if (reset) { let v = null; try { v = localStorage.getItem('outpost-chat-panel'); } catch (e) { /* storage optional */ } open = v !== 'closed'; }
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
  return [{ id: 'auto', t: 'Auto (local, strongest free node)', s: 'The best free node picks up your message', tone: 'on', note: '' }].concat(nodes.map((n) => {
    const h = n.heartbeat, busy = h.inference_busy > 0, paused = h.paused;
    return { id: n.id, t: h.loaded_model, s: nodeName(n), tone: busy || paused ? 'needs' : 'on', note: paused ? 'paused' : busy ? 'busy' : h.inference_ready === false ? 'loading' : 'free' };
  }));
}
function fillChatNodes() {
  const cur = $('chat-node').value || (chatCur && chatCur.node) || 'auto';
  setHTML('chat-node', chatChoices().map((c) => `<option value="${esc(c.id)}">${esc(c.id === 'auto' ? c.t : `${c.t} on ${c.s}`)}</option>`).join(''));
  $('chat-node').value = [...$('chat-node').options].some((o) => o.value === cur) ? cur : 'auto';
  if (!chatCur && page === 'chat') chatPickRender();
}
// Empty state: one card per way to answer; picking one sets the model box under the composer.
function chatPickRender() {
  if (chatCur) return;
  const sel = $('chat-node').value || 'auto', ch = chatChoices(), ready = ch.length - 1;
  const card = (c) => `<button type="button" class="mcard ${c.id === sel ? 'sel' : ''}" data-model="${esc(c.id)}" aria-pressed="${c.id === sel}"><span class="tdot ${c.tone === 'on' ? 'work' : 'needs'}"></span><span class="mt">${esc(c.t)}</span><span class="mr">${c.id === sel ? '✓ selected' : esc(c.note)}</span><span class="ms">${esc(c.s)}</span></button>`;
  setHTML('chat-msgs', `<div class="chat-pick-h"><span>Answer with</span><span class="grow"></span><span>${ready} ${ready === 1 ? 'model' : 'models'} ready</span></div><div class="chat-pick">${ch.map(card).join('')}</div>`
    + (ready ? '' : '<p class="chat-none">No node has a model loaded yet, so a chat has nowhere to run. Load one on the Models page, then ask.</p>'));
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
}
function chatBubble(m) {
  if (m.role === 'user') return `<div class="bubble-row"><div class="bubble">${esc(m.text)}</div></div>`;
  const meta = [m.model && `${esc(m.model)}`, m.node && `on ${esc(m.node)}`, m.tok_s && `${m.tok_s} tok/s`].filter(Boolean).join(' · ');
  if (m.error) return `<div class="chat-answer error-msg">${m.error === 'stopped' ? 'Stopped.' : esc(m.error)}</div>`;
  return `<div class="chat-answer"><div class="chat-text">${esc(m.text)}</div>${meta ? `<div class="sub">${meta}</div>` : ''}</div>`;
}
function chatRender() {
  if (!chatCur) return;
  const box = $('chat-msgs'), stick = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
  setHTML('chat-msgs', chatCur.messages.map(chatBubble).join('') + '<div id="chat-live"></div>');
  chatRenderLive();
  chatButtons();
  if (stick) box.scrollTop = box.scrollHeight;
}
function chatRenderLive() {
  const el = $('chat-live');
  if (!el || !chatCur) return;
  const calls = Object.values(chatLive);
  if (!calls.length) { el.innerHTML = chatCur.busy === 'waiting' ? '<p class="muted">Waiting for a free node…</p>' : ''; return; }
  const c = calls[0];
  const rate = liveRate(Object.assign({}, c, { text: c.text }));
  setHTML('chat-live', `<div class="chat-answer"><div class="chat-text">${esc(c.text) || '<span class="muted">…</span>'}</div><div class="sub">${esc(c.node || '')}${rate ? ' · ' + esc(rate) : ''}</div></div>`);
  const box = $('chat-msgs');
  if (box.scrollHeight - box.scrollTop - box.clientHeight < 120) box.scrollTop = box.scrollHeight;
}
async function chatSend() {
  const text = $('chat-text').value.trim();
  if (!text) return;
  $('chat-send').disabled = true;
  try {
    if (!chatCur) {
      const r = await post('/api/chats', { text, node: $('chat-node').value });
      $('chat-text').value = ''; $('chat-text').style.height = '';
      location.hash = '#/chat/' + r.id;
      return;
    }
    await post(`/api/chats/${chatCur.id}/messages`, { text, node: $('chat-node').value });
    $('chat-text').value = ''; $('chat-text').style.height = '';
    await chatLoad(chatCur.id);
  } catch (e) { toast(e.message); }
  chatButtons();
}
$('chat-send').onclick = chatSend;
$('chat-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); chatSend(); } });
const chatNew = () => { if (location.hash === '#/chat') chatOpen(null); else location.hash = '#/chat'; };
$('chat-new').onclick = chatNew; $('chat-new2').onclick = chatNew;
$('chat-panel-btn').onclick = () => chatPanelSet(!chatPanelOpen());
$('chat-panel-x').onclick = () => chatPanelSet(false);
$('chat-node').addEventListener('change', chatPickRender);
$('chat-msgs').addEventListener('click', (e) => { const b = e.target.closest('[data-model]'); if (b) { $('chat-node').value = b.dataset.model; chatPickRender(); $('chat-text').focus(); } });
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
