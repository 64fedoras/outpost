'use strict';
// Settings search. Plain script, loads after settings.js (see index.html). While the box has text, every section
// is shown and only the matching cards stay, best match first, each headed "Section › Card". Clearing the box
// puts back the section that was open. Matches are marked with the CSS Custom Highlight API (no DOM changes).
(function settingsSearch() {
  const q = $('set-q'), main = document.querySelector('.set-main'), nav = $('set-nav');
  if (!q || !main) return;
  // Words people use that the card text does not contain.
  const KW = {
    'Sign-in security': 'session sessions lockout sign out logout browsers',
    'API tokens': 'api token bearer script key mcp chat openai',
    'Connect a tool': 'api token mcp openai anthropic ollama cors claude code desktop integration',
    'Claude': 'anthropic api key model cap budget cost spending',
    'OpenRouter': 'api key model',
    'Hugging Face': 'hf token gated api key',
    'Brave Search': 'api key web search',
    'Appearance': 'theme dark light mode enter ctrl phone',
    'Notifications': 'alerts ntfy webhook push discord home assistant',
    'Backup': 'restore export passphrase nightly',
    'Admin password': 'login reset change password',
    'Idle unload': 'unload ram vram timeout memory',
    'Memory pooling': 'rpc pool vram ram',
  };
  const secs = [...main.querySelectorAll('.set-sec')];
  const units = [];
  const textOf = (el) => { const c = el.cloneNode(true); c.querySelectorAll('select, script').forEach((x) => x.remove()); return c.textContent.replace(/\s+/g, ' '); };
  secs.forEach((sec) => {
    const title = sec.querySelector('.set-sec-title').textContent.trim();
    let head = null;
    [...sec.children].forEach((el) => {
      if (el.classList.contains('section-head')) { head = el; return; }
      if (!el.classList.contains('card')) return;
      const name = head ? head.textContent.trim() : title;
      const kw = (KW[name] || '').toLowerCase();
      if (head) head.querySelector('h2').dataset.sec = title;
      units.push({
        sec, head, card: el, name: name.toLowerCase(), kw, idx: units.length,
        lab: [...el.querySelectorAll('label, button')].map((x) => x.textContent).join(' ').toLowerCase(),
        text: (name + ' ' + textOf(el) + ' ' + kw).toLowerCase(),
      });
      head = null;
    });
    // A section that is one card with the same name as the section: no repeated title in results.
    const heads = sec.querySelectorAll('.section-head');
    if (heads.length === 1 && heads[0].textContent.trim() === title) sec.classList.add('ss-single');
    else if (!heads.length) sec.classList.add('ss-single');
  });
  secs.forEach((sec) => { sec.dataset.t = sec.querySelector('.set-sec-title').textContent.trim(); });
  let saved = null; // which sections were hidden before searching
  let ranges = [];
  const canHL = !!(window.CSS && CSS.highlights && window.Highlight);
  function mark(root, words) {
    const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    while (w.nextNode()) {
      const t = w.currentNode;
      if (t.parentNode.closest('option, select, textarea, script')) continue;
      const low = t.data.toLowerCase();
      words.forEach((x) => { let i = low.indexOf(x); while (i >= 0) { const r = new Range(); r.setStart(t, i); r.setEnd(t, i + x.length); ranges.push(r); i = low.indexOf(x, i + x.length); } });
    }
  }
  function run() {
    const s = q.value.trim().toLowerCase(), words = s.split(/\s+/).filter(Boolean), on = !!words.length;
    ranges = []; if (canHL) CSS.highlights.delete('set-q');
    main.querySelectorAll('.clamp.ss-open').forEach((h) => h.classList.remove('open', 'ss-open'));
    main.querySelectorAll('.more-link.ss-hide').forEach((m) => m.classList.remove('ss-hide'));
    $('set-q-x').classList.toggle('hidden', !on);
    q.parentNode.querySelector('.ss-kbd').classList.toggle('hidden', on);
    main.classList.toggle('ss-on', on); nav.classList.toggle('ss-nav', on);
    if (on && !saved) saved = secs.map((x) => x.classList.contains('hidden'));
    const counts = {}, best = {}; let n = 0;
    units.forEach((u) => {
      u.ok = !on || words.every((w) => u.text.includes(w));
      u.score = on ? words.reduce((a, w) => a + (u.name.includes(w) ? 100 : 0) + (u.lab.includes(w) ? 10 : 0) + (u.kw.includes(w) ? 8 : 0) + 1, 0) : 0;
    });
    const ranked = [...units].sort((a, b) => b.score - a.score || a.idx - b.idx);
    ranked.forEach((u, i) => {
      u.card.style.order = on ? 2 * i + 1 : ''; if (u.head) u.head.style.order = on ? 2 * i : '';
      u.card.classList.toggle('ss-hide', !u.ok); if (u.head) u.head.classList.toggle('ss-hide', !u.ok);
      if (!u.ok) return;
      n++; counts[u.sec.id] = (counts[u.sec.id] || 0) + 1; if (!(u.sec.id in best)) best[u.sec.id] = i;
    });
    secs.forEach((sec) => {
      sec.style.order = on ? (best[sec.id] ?? 999) : '';
      sec.querySelector('.set-sec-title').style.order = -1;
      if (on) { sec.classList.remove('hidden'); sec.classList.toggle('ss-hide', !counts[sec.id]); }
      else sec.classList.remove('ss-hide');
    });
    if (!on) { if (saved) secs.forEach((sec, i) => sec.classList.toggle('hidden', saved[i])); saved = null; }
    nav.querySelectorAll('a').forEach((a) => {
      const id = a.getAttribute('href').slice(1);
      a.classList.toggle('ss-dim', on && !counts[id]);
      let c = a.querySelector('.ss-c'); if (!c) { c = document.createElement('span'); c.className = 'ss-c'; a.appendChild(c); }
      c.textContent = on && counts[id] ? counts[id] : '';
    });
    if (on) {
      if (typeof clampHints === 'function') clampHints();
      main.querySelectorAll('.clamp').forEach((h) => {
        if (!h.offsetParent || h.classList.contains('open') || !words.some((x) => h.textContent.toLowerCase().includes(x))) return;
        h.classList.add('open', 'ss-open'); const m = h.nextElementSibling; if (m && m.classList.contains('more-link')) m.classList.add('ss-hide');
      });
      // Connect a tool shows one tab at a time: a word that names a tab opens it.
      const tabs = [...document.querySelectorAll('#ct-tabs button')];
      if (!$('connect-card').classList.contains('ss-hide') && !tabs.some((b) => b.classList.contains('on') && words.some((x) => b.textContent.toLowerCase().includes(x)))) {
        const t = tabs.find((b) => words.some((x) => b.textContent.toLowerCase().includes(x))); if (t) t.click();
      }
      units.forEach((u) => { if (u.ok) { mark(u.card, words); if (u.head) mark(u.head, words); } });
      if (canHL && ranges.length) CSS.highlights.set('set-q', new Highlight(...ranges));
    }
    const none = $('set-none');
    none.classList.toggle('hidden', !on || n > 0);
    none.textContent = '';
    if (on && !n) { none.append('No settings match “'); const b = document.createElement('b'); b.textContent = q.value.trim(); none.append(b, '”. Try one word, like token or backup.'); }
  }
  function clear(focus) { q.value = ''; run(); if (focus) q.focus(); }
  q.addEventListener('input', () => { run(); scrollTo({ top: 0 }); });
  q.addEventListener('keydown', (e) => { if (e.key === 'Escape' && q.value) { e.preventDefault(); e.stopPropagation(); clear(false); } });
  $('set-q-x').addEventListener('click', () => clear(true));
  // Picking a section on the left leaves the search.
  nav.addEventListener('click', () => { if (q.value) { saved = null; q.value = ''; run(); } }, true);
  // Links inside Settings that name a section (data-goto) click that section's entry on the left.
  main.addEventListener('click', (e) => {
    const a = e.target.closest('a[data-goto]'); if (!a) return;
    e.preventDefault(); const l = nav.querySelector('a[data-sec="' + a.dataset.goto + '"]'); if (l) l.click();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
    if (typeof page === 'undefined' || page !== 'settings') return;
    const a = document.activeElement;
    if (a && (/^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName) || a.isContentEditable)) return;
    if (document.querySelector('.overlay:not(.hidden)')) return;
    e.preventDefault(); q.focus(); q.select();
  });
})();
