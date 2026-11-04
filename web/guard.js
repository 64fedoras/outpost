'use strict';
// Unsaved-changes guard and text drafts. Loaded before app.js; it reads app.js globals
// (page, pageArg, planEdit, editing, taskDetail) only when called, never at load time.

// ---------- drafts ----------
// Typed text in the new-task box, the reply box and answer boxes is kept in this browser's
// localStorage (nothing goes to the controller or the nodes). Never used for key or token fields.
const Drafts = (() => {
  const P = 'outpost.draft.', MAX_CHARS = 20000, MAX_KEEP = 60, MAX_AGE = 30 * 864e5;
  const ok = (fn, dflt) => { try { return fn(); } catch (e) { return dflt; } };
  const keys = () => ok(() => Object.keys(localStorage).filter((k) => k.startsWith(P)), []);
  function get(k) {
    return ok(() => {
      const o = JSON.parse(localStorage.getItem(P + k) || 'null');
      return o && typeof o.v === 'string' ? o.v : '';
    }, '');
  }
  function clear(k) { ok(() => localStorage.removeItem(P + k)); }
  function clearPrefix(pre) { keys().filter((k) => k.startsWith(P + pre)).forEach((k) => ok(() => localStorage.removeItem(k))); }
  function set(k, v) {
    if (!v || !v.trim()) { clear(k); return; }
    ok(() => localStorage.setItem(P + k, JSON.stringify({ t: Date.now(), v: v.slice(0, MAX_CHARS) })));
  }
  function prune() {
    const all = keys().map((k) => ({ k, t: ok(() => JSON.parse(localStorage.getItem(k)).t, 0) || 0 })).sort((a, b) => b.t - a.t);
    all.forEach((d, i) => { if (i >= MAX_KEEP || Date.now() - d.t > MAX_AGE) ok(() => localStorage.removeItem(d.k)); });
  }
  prune();
  // Bind a text box to a draft key. Returns { restore(key), discard() }.
  function bind(box, note) {
    let key = null, timer = 0;
    const save = () => { if (key) set(key, box.value); };
    box.addEventListener('input', () => {
      clearTimeout(timer); timer = setTimeout(save, 250);
      if (!box.value.trim()) note.classList.add('hidden');
    });
    box.addEventListener('blur', () => { clearTimeout(timer); save(); });
    addEventListener('pagehide', () => { clearTimeout(timer); save(); });
    const discard = () => { clearTimeout(timer); if (key) clear(key); box.value = ''; note.classList.add('hidden'); box.dispatchEvent(new Event('input')); box.focus(); };
    note.querySelector('button').onclick = discard;
    return {
      // Point the box at a draft key; a saved draft fills it when it is empty.
      restore(k) {
        clearTimeout(timer);
        key = k;
        const d = get(k);
        if (d && !box.value) { box.value = d; box.dispatchEvent(new Event('input')); note.classList.remove('hidden'); }
        else note.classList.add('hidden');
      },
      flush() { clearTimeout(timer); save(); },   // call before the box is reused for another key
      done() { clearTimeout(timer); if (key) clear(key); note.classList.add('hidden'); },
    };
  }
  return { get, set, clear, clearPrefix, bind };
})();

// ---------- unsaved changes ----------
const Guard = (() => {
  const SECRET = new Set(['tls-key']);   // pasted private key: never counted, never stored
  const fieldKey = (el) => el.id || (el.dataset && el.dataset.nf ? 'nf-' + el.dataset.nf : '');
  const trackable = (el) => el.matches('input, textarea, select') && el.type !== 'password' && el.type !== 'file' && el.type !== 'button' && fieldKey(el) && !SECRET.has(el.id);
  const settingsRoot = () => document.querySelector('section[data-page="settings"]');
  const val = (el) => (el.type === 'checkbox' || el.type === 'radio' ? el.checked : el.value);

  // Settings: remember the value a field had when the person first touched it.
  let touched = new Map();   // key -> { el, orig }
  const note = (el, orig) => {
    const root = settingsRoot();
    if (!root || !root.contains(el) || !trackable(el)) return;
    const k = fieldKey(el);
    if (!touched.has(k)) touched.set(k, { el, orig: orig === undefined ? val(el) : orig });
  };
  document.addEventListener('focusin', (e) => note(e.target), true);
  document.addEventListener('change', (e) => { if (e.isTrusted && e.target.type === 'checkbox') note(e.target, !e.target.checked); }, true);
  // Remember which card a button belongs to, so a successful save resets only that card.
  let lastCard = null, lastClick = 0;
  document.addEventListener('click', (e) => {
    const b = e.target.closest('button');
    const root = settingsRoot();
    if (b && root && root.contains(b)) { lastCard = b.closest('.card') || root; lastClick = Date.now(); }
  }, true);
  function settingsSaved() {   // call after a settings save went through
    if (!lastCard || Date.now() - lastClick > 15000) return;
    for (const [k, t] of touched) if (lastCard.contains(t.el)) touched.delete(k);
  }
  const settingsReset = () => touched.clear();   // fields were reloaded from the server
  function settingsDirty() {
    const out = [];
    for (const [k, t] of touched) {
      if (!t.el.isConnected) { touched.delete(k); continue; }
      if (val(t.el) !== t.orig) {
        const sec = t.el.closest('.set-sec');
        const title = sec && sec.querySelector('.set-sec-title');
        const name = title ? title.textContent.trim() : 'Settings';
        if (!out.includes(name)) out.push(name);
      }
    }
    return out;
  }

  // What is unsaved right now? { what: [...], save?: async fn, drop: fn } or null.
  function pending() {
    if (page === 'settings') {
      const s = settingsDirty();
      if (s.length) return { what: 'Settings (' + s.join(', ') + ')', drop: settingsReset };
    }
    if (page === 'task' && planEdit && $('plan-edit')) {
      readPlanEditor();
      if (JSON.stringify(planEdit.plan) !== planEdit.orig) {
        const id = pageArg, plan = planEdit.plan;
        return { what: 'your edits to the plan', drop: () => { planEdit = null; },
          save: async () => { await post(`/api/tasks/${id}/plan/edit`, { plan, run: false, cap_usd: 0 }); planEdit = null; } };
      }
    }
    if (page === 'task' && editing && $('msg-edit-text')) {
      const text = $('msg-edit-text').value;
      if (text !== editing.text) {
        const id = pageArg, mid = editing.id;
        return { what: 'your edit to this message', drop: () => { editing = null; },
          save: async () => {
            if (!text.trim()) throw new Error('Write a message first');
            await post(`/api/tasks/${id}/messages/${encodeURIComponent(mid)}/edit`, { text }); editing = null;
          } };
      }
    }
    if (page === 'projects') {
      const filled = ['np-name', 'np-url', 'np-branch', 'np-test'].some((i) => $(i) && $(i).value.trim());
      if (filled) return { what: 'the new project form', drop: () => { ['np-name', 'np-url', 'np-branch', 'np-test', 'np-token', 'np-key'].forEach((i) => { $(i).value = ''; }); } };
    }
    return null;
  }

  // ---- the prompt ----
  let asking = false;
  function ask(p) {
    return new Promise((resolve) => {
      const m = $('modal-leave'), stay = $('leave-stay'), drop = $('leave-discard'), save = $('leave-save');
      $('leave-what').textContent = 'You have unsaved changes in ' + p.what + '.';
      save.classList.toggle('hidden', !p.save);
      m.classList.remove('hidden'); asking = true;
      const prevFocus = document.activeElement;
      const done = (r) => { m.classList.add('hidden'); asking = false; m.onkeydown = null; stay.onclick = drop.onclick = save.onclick = null; if (prevFocus && prevFocus.focus) prevFocus.focus(); resolve(r); };
      stay.onclick = () => done('stay');
      drop.onclick = () => done('discard');
      save.onclick = async () => {
        save.disabled = true;
        try { await p.save(); done('saved'); } catch (e) { toast(e.message, 4000); }
        save.disabled = false;
      };
      m.onkeydown = (e) => {
        if (e.key === 'Escape') { e.preventDefault(); done('stay'); }
        if (e.key === 'Tab') {   // keep focus inside the dialog
          const bs = [...m.querySelectorAll('button:not(.hidden):not(:disabled)')];
          const i = bs.indexOf(document.activeElement);
          if (e.shiftKey && i <= 0) { e.preventDefault(); bs[bs.length - 1].focus(); }
          else if (!e.shiftKey && i === bs.length - 1) { e.preventDefault(); bs[0].focus(); }
        }
      };
      stay.focus();
    });
  }

  // ---- hash router hook ----
  // Each history entry is stamped with an index in history.state, so a back or forward press
  // can be undone with history.go(): no extra entries, no loop.
  let cur = 0, ignore = 0;
  function stamp() {
    const s = history.state;
    if (s && typeof s.oi === 'number') { cur = s.oi; return; }
    cur = 0; history.replaceState({ oi: 0 }, '');
  }
  async function onHashChange() {
    if (ignore > 0) { ignore--; return; }
    const s = history.state;
    const isNew = !(s && typeof s.oi === 'number');
    const idx = isNew ? cur + 1 : s.oi;
    const p = asking ? { what: 'this page', drop() {} } : pending();
    if (!p) { if (isNew) history.replaceState({ oi: idx }, ''); cur = idx; route(); return; }
    // Put the old page back first, then ask.
    if (isNew) history.replaceState({ oi: idx }, '');
    if (idx !== cur) { ignore++; history.go(cur - idx); setTimeout(() => { ignore = 0; }, 600); }
    if (asking) return;
    const r = await ask(p);
    if (r === 'stay') return;
    p.drop();
    if (r === 'discard' || r === 'saved') {
      if (idx !== cur) { ignore++; history.go(idx - cur); }
      cur = idx;
      route();
    }
  }
  // Links in the page: ask before the hash changes at all (no history entry is made).
  document.addEventListener('click', async (e) => {
    const a = e.target.closest('a[href^="#/"]');
    if (!a || e.defaultPrevented || e.button || e.metaKey || e.ctrlKey || e.shiftKey || a.target) return;
    const p = pending();
    if (!p) return;
    e.preventDefault();
    if (asking) return;
    const target = a.getAttribute('href');
    const r = await ask(p);
    if (r === 'stay') return;
    p.drop(); location.hash = target;
  }, true);

  addEventListener('beforeunload', (e) => {
    if (pending()) { e.preventDefault(); e.returnValue = ''; }
  });

  return { stamp, onHashChange, settingsSaved, settingsReset };
})();
