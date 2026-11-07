'use strict';
// Chat page. Plain script, no modules: shares globals with app.js and loads after it (see index.html).

// ---------- chat (chat.go) ----------
let chatES = null, chatCur = null, chatLive = {};
async function chatOpen(id) {
  chatCur = null; chatLive = {};
  document.querySelector('[data-page="chat"]').classList.toggle('chat-fresh', !id);
  await chatList();
  fillChatNodes();
  if (id) await chatLoad(id); else { setHTML('chat-msgs', '<p class="muted chat-empty">Start a new chat below.</p>'); chatButtons(); }
  setTimeout(() => $('chat-text').focus(), 0);
}
async function chatList() {
  let list = [];
  try { list = await api('/api/chats'); } catch (e) { /* offline */ }
  setHTML('chat-list', list.length ? list.map((c) => `<a href="#/chat/${esc(c.id)}" class="${c.id === pageArg ? 'active' : ''}">${c.busy ? '<span class="dot st-running" aria-label="answering"></span>' : ''}${esc(c.title)}</a>`).join('')
    : '<p class="muted u6">No chats yet.</p>');
}
// Model choice: Automatic (the strongest free node) or a node with a model ready.
function fillChatNodes() {
  const cur = $('chat-node').value || (chatCur && chatCur.node) || 'auto';
  const nodes = (state ? state.nodes : []).filter((n) => n.online && n.heartbeat && n.heartbeat.loaded_model);
  setHTML('chat-node', `<option value="auto">Automatic (strongest free node)</option>${nodes.map((n) => `<option value="${esc(n.id)}">${esc(n.heartbeat.loaded_model)} on ${esc(nodeName(n))}</option>`).join('')}`);
  $('chat-node').value = [...$('chat-node').options].some((o) => o.value === cur) ? cur : 'auto';
}
async function chatLoad(id) {
  try { chatCur = await api('/api/chats/' + id); } catch (e) { setHTML('chat-msgs', `<p class="error-msg">${esc(e.message)}</p>`); return; }
  if (pageArg !== id) return;
  $('chat-node').value = [...$('chat-node').options].some((o) => o.value === chatCur.node) ? chatCur.node : 'auto';
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
      $('chat-text').value = '';
      location.hash = '#/chat/' + r.id;
      return;
    }
    await post(`/api/chats/${chatCur.id}/messages`, { text, node: $('chat-node').value });
    $('chat-text').value = '';
    await chatLoad(chatCur.id);
  } catch (e) { toast(e.message); }
  chatButtons();
}
$('chat-send').onclick = chatSend;
$('chat-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); chatSend(); } });
$('chat-new').onclick = () => { if (location.hash === '#/chat') chatOpen(null); else location.hash = '#/chat'; };
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
