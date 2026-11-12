'use strict';
// Sidebar groups (groups.go). Loads after app.js and shares its globals.
// The sidebar list has two views: By status (Pinned, Needs you, Running, Earlier)
// and By group (Pinned, Needs you, one collapsible section per group, No group).
// Tasks and chats both live in it. Move an item into a group by dragging its row onto
// a group, or from the row's menu: Move to group ▸.

let groups = [], sideChats = [], groupsAt = 0, sideSeq = 0;
const lsGet = (k) => { try { return localStorage.getItem(k); } catch (e) { return null; } };
const lsSet = (k, v) => { try { localStorage.setItem(k, v); } catch (e) {} };
let sideMode = lsGet('outpost.sidemode') === 'group' ? 'group' : 'status';
let groupClosed = (() => { try { return JSON.parse(lsGet('outpost.groupclosed') || '{}') || {}; } catch (e) { return {}; } })();
let sideDragging = false, addingGroup = false, pendingMove = null, renamingGroupId = null, ctxGroup = null, ctxChat = null;
const GROUP_COLORS = ['blue', 'green', 'purple', 'amber', 'red', 'teal'];
const groupById = (id) => groups.find((g) => g.id === id);
const chatById = (id) => sideChats.find((c) => c.id === id);

// refreshGroups fetches the group list and the chat list for the sidebar (at most every 2.5 s unless forced).
async function refreshGroups(force) {
  if (!force && Date.now() - groupsAt < 2500) return;
  groupsAt = Date.now();
  const seq = sideSeq;
  const [g, c] = await Promise.all([api('/api/groups').catch(() => null), api('/api/chats').catch(() => null)]);
  if (seq !== sideSeq) return; // an edit happened meanwhile; the next poll has it
  if (g) groups = g;
  if (c) sideChats = c;
  renderSideTasks();
}

function groupTag(g) { return g ? `<span class="gtag gc-${esc(g.color)}"><i></i>${esc(g.name)}</span> · ` : ''; }

function sideChatHTML(c, tagGroup) {
  const cls = [page === 'chat' && pageArg === c.id ? 'active' : '', ctxChat === c.id ? 'menu-open' : ''].join(' ');
  return `<div class="side-task side-chat ${cls}" draggable="true" data-chat="${esc(c.id)}">
    <a href="#/chat/${esc(c.id)}" class="side-task-link" draggable="false" aria-keyshortcuts="Shift+F10 Delete">
      <span class="cdot" aria-hidden="true">💬</span>
      <span class="side-task-text"><span class="side-task-title">${esc(c.title)}</span>
      <span class="side-task-meta">${groupTag(tagGroup)}${c.busy ? 'Answering' : 'Chat'} · ${ago(c.updated)}</span></span></a>
    <button class="side-task-more" data-chat-menu="${esc(c.id)}" aria-haspopup="menu" aria-expanded="${ctxChat === c.id}" aria-label="Actions for ${esc(c.title)}" title="Actions">⋯</button></div>`;
}

function groupHeadHTML(g, count, needs, closed) {
  const name = renamingGroupId === g.id
    ? `<input class="side-rename" id="group-rename" value="${esc(g.name)}" maxlength="40" aria-label="New name for this group">`
    : `<span class="gname">${esc(g.name)}</span>`;
  const badge = needs ? `<span class="gamb" title="${needs} waiting on you">${needs}</span>` : `<span class="gct">${count}</span>`;
  return `<div class="gsec-h gc-${esc(g.color)}${ctxGroup === g.id ? ' menu-open' : ''}" data-gtoggle="${esc(g.id)}" tabindex="0" role="button" aria-expanded="${!closed}" aria-keyshortcuts="Shift+F10 F2 Delete">
    <span class="gchev" aria-hidden="true">${closed ? '▸' : '▾'}</span><i class="gsq" aria-hidden="true"></i>${name}${badge}
    <button class="gmore" data-group-menu="${esc(g.id)}" aria-haspopup="menu" aria-label="Actions for ${esc(g.name)}" title="Actions">⋯</button></div>`;
}

// renderSideTasks draws the sidebar list. A rename or a drag in progress must survive polling.
function renderSideTasks(force) {
  if (!tasks || ((renamingId || renamingGroupId) && !force) || sideDragging) return;
  renderGroupBar();
  if (!tasks.length && !sideChats.length && !groups.length) { setHTML('side-tasks', `<p class="side-empty">Tasks and chats you start show up here.</p>`); return; }
  const pnames = {};
  (projects || []).forEach((p) => { pnames[p.id] = p.name; });
  const gid = (x) => (groupById(x.group_id) ? x.group_id : '');
  const items = tasks.map((t) => ({ t, g: gid(t), time: new Date(t.created).getTime() }))
    .concat(sideChats.map((c) => ({ c, g: gid(c), time: new Date(c.updated).getTime() })));
  const byNewest = (a, b) => b.time - a.time;
  const html = (it, tag) => it.t ? sideTaskHTML(it.t, pnames[it.t.project_id], tag ? groupById(it.g) : null) : sideChatHTML(it.c, tag ? groupById(it.g) : null);
  const tone = (it) => it.t && taskTone(it.t);
  const pinned = items.filter((i) => i.t && i.t.pinned);
  const rest = items.filter((i) => !(i.t && i.t.pinned));
  const needs = rest.filter((i) => tone(i) === 'needs');
  const others = rest.filter((i) => tone(i) !== 'needs');
  const running = (i) => i.t ? ACTIVE.includes(i.t.status) : !!i.c.busy;
  const section = (name, list, cls, tag) => list.length ? `<div class="side-group ${cls || ''}">${name}</div>` + list.sort(byNewest).map((i) => html(i, tag)).join('') : '';
  let out = section('Pinned', pinned, '', true) + section('Needs you', needs, 'needs', true);
  if (sideMode === 'status') {
    out += section('Running', others.filter(running), '', true) + section('Earlier', others.filter((i) => !running(i)), '', true);
  } else {
    const sec = (g) => {
      const id = g ? g.id : '';
      const list = others.filter((i) => i.g === id).sort((a, b) => (running(b) - running(a)) || byNewest(a, b));
      const nd = needs.filter((i) => i.g === id).length;
      if (!g && !list.length) return '';
      const closed = !!groupClosed[id || '_none'];
      const head = g ? groupHeadHTML(g, list.length, nd, closed)
        : `<div class="gsec-h gc-none" data-gtoggle="_none" tabindex="0" role="button" aria-expanded="${!closed}"><span class="gchev" aria-hidden="true">${closed ? '▸' : '▾'}</span><i class="gsq" aria-hidden="true"></i><span class="gname">No group</span><span class="gct">${list.length}</span></div>`;
      const body = list.length ? list.map((i) => html(i, false)).join('') : (g ? `<p class="side-empty gempty">Drag a task or chat here.</p>` : '');
      return `<div class="gsec${closed ? ' closed' : ''}" data-gsec="${esc(id)}">${head}${body}</div>`;
    };
    out += groups.map(sec).join('') + sec(null);
  }
  setHTML('side-tasks', out || `<p class="side-empty">Nothing here yet.</p>`);
}

// ---------- the view switch and "New group" ----------
function renderGroupBar() {
  const add = sideMode !== 'group' ? '' : addingGroup
    ? `<div class="gadd"><input id="group-new" maxlength="40" placeholder="Group name, Enter to save" aria-label="Name for the new group"></div>`
    : `<div class="gadd"><button class="add-group" data-group-add>＋ New group</button></div>`;
  const seg = `<div class="seg2" role="group" aria-label="Sidebar view"><button data-side-mode="status" class="${sideMode === 'status' ? 'on' : ''}" aria-pressed="${sideMode === 'status'}">By status</button><button data-side-mode="group" class="${sideMode === 'group' ? 'on' : ''}" aria-pressed="${sideMode === 'group'}">By group</button></div>`;
  const el = $('side-groupbar');
  const fresh = el._h !== seg + add;
  setHTML(el, seg + add);
  if (fresh && addingGroup && $('group-new')) wireNewGroupInput();
}
function wireNewGroupInput() {
  const inp = $('group-new');
  inp.focus();
  let done = false;
  const finish = async (save) => {
    if (done) return; done = true;
    const name = inp.value.trim(), move = pendingMove;
    addingGroup = false; pendingMove = null;
    if (save && name) {
      try {
        sideSeq++;
        const g = await post('/api/groups', { name });
        groups.push(g);
        if (move) await moveItem(move.kind, move.id, g.id); else toast(`Group “${g.name}” created`);
      } catch (e) { toast(e.message); }
    }
    renderSideTasks(true);
  };
  inp.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); finish(false); }
  });
  inp.addEventListener('blur', () => finish(true));
}
function setSideMode(m) {
  sideMode = m; lsSet('outpost.sidemode', m); addingGroup = false; pendingMove = null;
  closeGroupCtx(false); closeTaskMenu(false); renderSideTasks(true);
}
function startNewGroup(move) {
  if (sideMode !== 'group') setSideMode('group');
  addingGroup = true; pendingMove = move || null; renderSideTasks(true);
  if ($('side-groupbar') && $('group-new')) $('group-new').scrollIntoView({ block: 'nearest' });
}

// ---------- moving items ----------
async function moveItem(kind, id, groupId) {
  const url = kind === 'chat' ? `/api/chats/${id}/meta` : `/api/tasks/${id}/meta`;
  const obj = kind === 'chat' ? chatById(id) : taskById(id);
  if (!obj || (obj.group_id || '') === (groupId || '')) { renderSideTasks(true); return; }
  try {
    const s = await post(url, { group: groupId || '' });
    sideSeq++;
    if (kind === 'chat') obj.group_id = groupId || ''; else { tasksSeq++; Object.assign(obj, s); }
    toast(groupId ? `Moved to ${groupById(groupId).name}` : 'Taken out of its group');
  } catch (e) { toast(e.message); }
  renderSideTasks(true);
}

// ---------- group menus (chat rows, group headers) and the Move to group fold-out ----------
function placeMenu(m, x, y) {
  m.classList.remove('hidden');
  const r = m.getBoundingClientRect(), pad = 6;
  m.style.left = Math.max(pad, Math.min(x, innerWidth - r.width - pad)) + 'px';
  m.style.top = Math.max(pad, Math.min(y, innerHeight - r.height - pad)) + 'px';
}
function openChatMenu(id, x, y, from) {
  const c = chatById(id);
  if (!c) return;
  closeTaskMenu(false); closeProjMenu(false); closeGroupCtx(false);
  ctxChat = id; ctxReturn = from || null;
  const item = (act, text, extra = '') => `<button role="menuitem" tabindex="-1" data-gctx="${act}" ${extra}>${text}</button>`;
  const m = $('group-ctx');
  m.innerHTML = [item('open-tab', 'Open in new tab'), item('group', 'Move to group<span class="hint-r">▸</span>', 'aria-haspopup="menu"'), item('copy-link', 'Copy link'), '<hr>', item('delete', 'Delete…<span class="kbd">Del</span>', 'class="danger"')].join('');
  m.setAttribute('aria-label', `Actions for ${c.title}`);
  m.dataset.kind = 'chat'; m.dataset.id = id;
  placeMenu(m, x, y);
  renderSideTasks(true);
  m.querySelector('button').focus();
}
function openGroupMenu(id, x, y, from) {
  const g = groupById(id);
  if (!g) return;
  closeTaskMenu(false); closeProjMenu(false); closeGroupCtx(false);
  ctxGroup = id; ctxReturn = from || null;
  const item = (act, text, extra = '') => `<button role="menuitem" tabindex="-1" data-gctx="${act}" ${extra}>${text}</button>`;
  const i = groups.indexOf(g);
  const m = $('group-ctx');
  m.innerHTML = [item('rename', 'Rename…<span class="kbd">F2</span>'),
    `<div class="swatches" role="group" aria-label="Colour">${GROUP_COLORS.map((c) => `<button role="menuitemradio" tabindex="-1" class="sw gc-${c}${g.color === c ? ' on' : ''}" data-gcolor="${c}" aria-checked="${g.color === c}" aria-label="${c}"><i></i></button>`).join('')}</div>`,
    i > 0 ? item('up', 'Move up') : '', i < groups.length - 1 ? item('down', 'Move down') : '', '<hr>',
    item('delete', 'Delete group…<span class="kbd">Del</span>', 'class="danger"')].join('');
  m.setAttribute('aria-label', `Actions for ${g.name}`);
  m.dataset.kind = 'group'; m.dataset.id = id;
  placeMenu(m, x, y);
  renderSideTasks(true);
  m.querySelector('button').focus();
}
function closeGroupCtx(restoreFocus = true) {
  closeGroupSub();
  const m = $('group-ctx');
  if (!m || m.classList.contains('hidden')) return;
  m.classList.add('hidden'); m.innerHTML = '';
  ctxChat = ctxGroup = null;
  renderSideTasks(true);
  if (restoreFocus && ctxReturn && document.body.contains(ctxReturn)) ctxReturn.focus();
  ctxReturn = null;
}

// The fold-out: the groups an item can go to. Beside the menu on a desktop, a bottom sheet on a phone.
let subFor = null;
function openGroupSub(kind, id, menuEl, focusFirst) {
  const obj = kind === 'chat' ? chatById(id) : taskById(id);
  if (!obj) return;
  subFor = { kind, id };
  const cur = obj.group_id || '';
  const s = $('group-sub');
  s.innerHTML = `<div class="sub-title">Move to group</div>` + groups.map((g) => `<button role="menuitemradio" tabindex="-1" data-mv="${esc(g.id)}" aria-checked="${cur === g.id}"><i class="gsq gc-${esc(g.color)}" aria-hidden="true"></i>${esc(g.name)}${cur === g.id ? '<span class="ck">✓</span>' : ''}</button>`).join('')
    + (groups.length ? '<hr>' : '') + `<button role="menuitem" tabindex="-1" data-mv-new>＋ New group…</button>` + (cur ? `<button role="menuitem" tabindex="-1" data-mv="">Remove from group</button>` : '');
  s.classList.remove('hidden');
  const phone = matchMedia('(max-width: 860px)').matches;
  $('group-sub-scrim').classList.toggle('hidden', !phone);
  if (!phone) {
    const r = menuEl.getBoundingClientRect(), sr = s.getBoundingClientRect(), row = menuEl.querySelector('[data-ctx="group"], [data-gctx="group"]');
    const rr = row ? row.getBoundingClientRect() : r;
    const left = r.right + 4 + sr.width > innerWidth - 6 ? Math.max(6, r.left - sr.width - 4) : r.right + 4;
    s.style.left = left + 'px';
    s.style.top = Math.max(6, Math.min(rr.top - 6, innerHeight - sr.height - 6)) + 'px';
  } else { s.style.left = s.style.top = ''; }
  const row = menuEl.querySelector('[data-ctx="group"], [data-gctx="group"]');
  if (row) { row.setAttribute('aria-expanded', 'true'); row.classList.add('sub-open'); }
  if (focusFirst) s.querySelector('button').focus();
}
function closeGroupSub(focusRow) {
  const s = $('group-sub');
  if (!s || s.classList.contains('hidden')) return;
  s.classList.add('hidden'); s.innerHTML = ''; subFor = null;
  $('group-sub-scrim').classList.add('hidden');
  document.querySelectorAll('.sub-open').forEach((b) => { b.classList.remove('sub-open'); b.setAttribute('aria-expanded', 'false'); if (focusRow) b.focus(); });
}

async function deleteGroup(id) {
  const g = groupById(id);
  if (!g || !await askConfirm(`Delete the group “${g.name}”? Its tasks and chats are kept, they just leave the group.`, { danger: true, ok: 'Delete' })) return;
  try {
    sideSeq++; tasksSeq++;
    await api(`/api/groups/${id}`, { method: 'DELETE' });
    groups = groups.filter((x) => x.id !== id);
    tasks.forEach((t) => { if (t.group_id === id) t.group_id = ''; });
    sideChats.forEach((c) => { if (c.group_id === id) c.group_id = ''; });
    toast('Group deleted');
  } catch (e) { toast(e.message); }
  renderSideTasks(true);
}
function startRenameGroup(id) {
  const g = groupById(id);
  if (!g) return;
  if (sideMode !== 'group') setSideMode('group');
  renamingGroupId = id; renderSideTasks(true);
  const inp = $('group-rename');
  if (!inp) { renamingGroupId = null; return; }
  inp.focus(); inp.select();
  let done = false;
  const finish = async (save) => {
    if (done) return; done = true;
    const name = inp.value.trim();
    renamingGroupId = null;
    if (save && name && name !== g.name) {
      try { sideSeq++; Object.assign(g, await post(`/api/groups/${id}`, { name })); } catch (e) { toast('Rename failed: ' + e.message); }
    }
    renderSideTasks(true);
    const h = document.querySelector(`[data-gtoggle="${CSS.escape(id)}"]`);
    if (h) h.focus();
  };
  inp.addEventListener('keydown', (e) => {
    e.stopPropagation();
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  inp.addEventListener('blur', () => finish(true));
  inp.addEventListener('click', (e) => { e.preventDefault(); e.stopPropagation(); });
}
async function patchGroup(id, body) {
  try {
    sideSeq++;
    await post(`/api/groups/${id}`, body);
    groups = await api('/api/groups');
  } catch (e) { toast(e.message); }
  renderSideTasks(true);
}

async function runGroupCtx(act, kind, id) {
  closeGroupCtx(act !== 'rename');
  try {
    if (kind === 'chat') {
      if (act === 'open-tab') window.open('#/chat/' + id, '_blank', 'noopener');
      else if (act === 'copy-link') { await copyText(location.origin + location.pathname + '#/chat/' + id); toast('Link copied'); }
      else if (act === 'delete') {
        if (!await askConfirm('Delete this chat?', { danger: true, ok: 'Delete' })) return;
        sideSeq++;
        await api(`/api/chats/${id}`, { method: 'DELETE' });
        sideChats = sideChats.filter((c) => c.id !== id);
        if (page === 'chat' && pageArg === id) location.hash = '#/chat';
        toast('Chat deleted'); renderSideTasks(true);
      }
      return;
    }
    const g = groupById(id);
    if (!g) return;
    if (act === 'rename') startRenameGroup(id);
    else if (act === 'up') await patchGroup(id, { position: groups.indexOf(g) - 1 });
    else if (act === 'down') await patchGroup(id, { position: groups.indexOf(g) + 1 });
    else if (act === 'delete') await deleteGroup(id);
  } catch (e) { toast(e.message); }
}

(function wireGroups() {
  const side = $('side-tasks'), bar = $('side-groupbar'), m = $('group-ctx'), sub = $('group-sub');
  bar.addEventListener('click', (e) => {
    const b = e.target.closest('[data-side-mode]');
    if (b) { setSideMode(b.dataset.sideMode); return; }
    if (e.target.closest('[data-group-add]')) startNewGroup();
  });
  // Rows
  side.addEventListener('click', (e) => {
    const cm = e.target.closest('[data-chat-menu]');
    if (cm) {
      e.preventDefault(); e.stopPropagation();
      if (ctxChat === cm.dataset.chatMenu) { closeGroupCtx(); return; }
      const r = cm.getBoundingClientRect();
      openChatMenu(cm.dataset.chatMenu, r.left, r.bottom + 4, cm);
      return;
    }
    const gm = e.target.closest('[data-group-menu]');
    if (gm) {
      e.preventDefault(); e.stopPropagation();
      if (ctxGroup === gm.dataset.groupMenu) { closeGroupCtx(); return; }
      const r = gm.getBoundingClientRect();
      openGroupMenu(gm.dataset.groupMenu, r.left, r.bottom + 4, gm.closest('.gsec-h'));
      return;
    }
    const h = e.target.closest('[data-gtoggle]');
    if (h && !e.target.closest('input')) {
      const id = h.dataset.gtoggle === '_none' ? '_none' : h.dataset.gtoggle;
      groupClosed[id] = !groupClosed[id]; lsSet('outpost.groupclosed', JSON.stringify(groupClosed));
      renderSideTasks(true);
      const again = document.querySelector(`[data-gtoggle="${CSS.escape(id)}"]`);
      if (again && e.detail === 0) again.focus();
    }
  });
  side.addEventListener('contextmenu', (e) => {
    const chat = e.target.closest('.side-chat'), head = e.target.closest('.gsec-h[data-gtoggle]');
    if (!chat && !(head && head.dataset.gtoggle !== '_none')) return;
    if (renamingId || renamingGroupId) return;
    e.preventDefault();
    const pos = (el) => (e.clientX || e.clientY) ? [e.clientX, e.clientY] : (() => { const r = el.getBoundingClientRect(); return [r.right - 24, r.top + 28]; })();
    if (chat) { const [x, y] = pos(chat); openChatMenu(chat.dataset.chat, x, y, chat.querySelector('.side-task-link')); }
    else { const [x, y] = pos(head); openGroupMenu(head.dataset.gtoggle, x, y, head); }
  });
  side.addEventListener('keydown', (e) => {
    if (e.target.tagName === 'INPUT') return;
    const chat = e.target.closest('.side-chat'), head = e.target.closest('.gsec-h[data-gtoggle]');
    if (chat) {
      if (e.key === 'Delete' && e.target.classList.contains('side-task-link')) { e.preventDefault(); runGroupCtx('delete', 'chat', chat.dataset.chat); }
    } else if (head) {
      const id = head.dataset.gtoggle;
      if ((e.key === 'Enter' || e.key === ' ') && e.target === head) { e.preventDefault(); head.click(); }
      else if (id !== '_none' && e.key === 'F2') { e.preventDefault(); startRenameGroup(id); }
      else if (id !== '_none' && e.key === 'Delete') { e.preventDefault(); deleteGroup(id); }
    }
  });
  longPress(side, '.side-chat, .gsec-h[data-gtoggle]:not([data-gtoggle="_none"])', (el, x, y) => {
    if (el.classList.contains('side-chat')) openChatMenu(el.dataset.chat, x, y, el.querySelector('.side-task-link'));
    else openGroupMenu(el.dataset.gtoggle, x, y, el);
  });

  // Menus
  m.addEventListener('click', (e) => {
    const sw = e.target.closest('[data-gcolor]');
    if (sw) { const id = m.dataset.id; closeGroupCtx(); patchGroup(id, { color: sw.dataset.gcolor }); return; }
    const b = e.target.closest('[data-gctx]');
    if (!b) return;
    if (b.dataset.gctx === 'group') { openGroupSub(m.dataset.kind, m.dataset.id, m, true); return; }
    runGroupCtx(b.dataset.gctx, m.dataset.kind, m.dataset.id);
  });
  menuKeys(m, closeGroupCtx);
  sub.addEventListener('click', async (e) => {
    const b = e.target.closest('[data-mv], [data-mv-new]');
    if (!b || !subFor) return;
    const { kind, id } = subFor;
    closeTaskMenu(false); closeGroupCtx(false);
    if (b.hasAttribute('data-mv-new')) startNewGroup({ kind, id });
    else await moveItem(kind, id, b.dataset.mv);
  });
  menuKeys(sub, () => closeGroupSub(true));
  // ArrowRight opens the fold-out from "Move to group", ArrowLeft closes it.
  document.addEventListener('keydown', (e) => {
    const row = e.target.closest && e.target.closest('[data-ctx="group"], [data-gctx="group"]');
    if (row && e.key === 'ArrowRight') {
      e.preventDefault();
      const menu = row.closest('.ctx-menu');
      if (menu.id === 'task-ctx') openGroupSub('task', ctxTaskId, menu, true); else openGroupSub(menu.dataset.kind, menu.dataset.id, menu, true);
    } else if (e.target.closest && e.target.closest('#group-sub') && e.key === 'ArrowLeft') { e.preventDefault(); closeGroupSub(true); }
  });
  // Hovering "Move to group" opens the fold-out (a pointer only; touch uses the tap).
  document.addEventListener('mouseover', (e) => {
    if (!matchMedia('(hover: hover)').matches) return;
    const menu = e.target.closest && e.target.closest('#task-ctx, #group-ctx');
    if (!menu || menu.classList.contains('hidden')) return;
    const row = e.target.closest('[data-ctx="group"], [data-gctx="group"]');
    if (row) { if (sub.classList.contains('hidden')) { if (menu.id === 'task-ctx') openGroupSub('task', ctxTaskId, menu, false); else openGroupSub(menu.dataset.kind, menu.dataset.id, menu, false); } }
    else if (!sub.classList.contains('hidden') && e.target.closest('button')) closeGroupSub();
  });
  document.addEventListener('pointerdown', (e) => {
    if (ctxChat || ctxGroup) { if (!m.contains(e.target) && !sub.contains(e.target) && !e.target.closest('[data-chat-menu], [data-group-menu]')) closeGroupCtx(false); }
    else if (!sub.classList.contains('hidden') && !sub.contains(e.target) && !e.target.closest('#task-ctx')) closeGroupSub();
  });
  side.addEventListener('scroll', () => closeGroupCtx(false));
  window.addEventListener('resize', () => closeGroupCtx(false));
  window.addEventListener('hashchange', () => closeGroupCtx(false));
  $('group-sub-scrim').addEventListener('click', () => { closeGroupSub(); closeTaskMenu(false); closeGroupCtx(false); });

  // Drag and drop: a task or chat row onto a group section (By group view).
  const dragOf = (e) => { const r = e.target.closest && e.target.closest('.side-task'); return r ? (r.dataset.task ? { kind: 'task', id: r.dataset.task } : r.dataset.chat ? { kind: 'chat', id: r.dataset.chat } : null) : null; };
  let drag = null;
  side.addEventListener('dragstart', (e) => {
    drag = dragOf(e);
    if (!drag) return;
    sideDragging = true;
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', drag.kind + ':' + drag.id);
    closeTaskMenu(false); closeGroupCtx(false);
    e.target.closest('.side-task').classList.add('dragging');
    side.classList.toggle('dragging-items', sideMode === 'group');
  });
  const secOf = (e) => e.target.closest && e.target.closest('.gsec');
  const clearOver = () => side.querySelectorAll('.gsec.dropping').forEach((x) => x.classList.remove('dropping'));
  side.addEventListener('dragover', (e) => {
    if (!drag) return;
    const s = secOf(e);
    if (!s) { clearOver(); return; }
    e.preventDefault(); e.dataTransfer.dropEffect = 'move';
    if (!s.classList.contains('dropping')) { clearOver(); s.classList.add('dropping'); }
  });
  side.addEventListener('dragleave', (e) => { if (!side.contains(e.relatedTarget)) clearOver(); });
  side.addEventListener('drop', (e) => {
    if (!drag) return;
    const s = secOf(e);
    if (!s) return;
    e.preventDefault();
    const d = drag; drag = null; sideDragging = false; side.classList.remove('dragging-items');
    moveItem(d.kind, d.id, s.dataset.gsec);
  });
  side.addEventListener('dragend', () => { drag = null; sideDragging = false; side.classList.remove('dragging-items'); clearOver(); renderSideTasks(true); });
})();
