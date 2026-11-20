// Outpost live preview frame. It runs in a sandboxed iframe (opaque origin: no cookies,
// no access to the controller's pages) with a CSP that blocks all network requests.
// The parent page posts one message, {kind: html | page | mermaid, ...}, and this page shows it:
// a whole HTML document is written in place; Mermaid source is drawn with the bundled
// Mermaid library (mermaid.min.js, loaded only for diagrams).
(function () {
  var shown = false;
  function write(html) { document.open(); document.write(html); document.close(); }
  function fail(msg) {
    var p = document.createElement('pre');
    p.textContent = msg;
    p.style.cssText = 'color:#c0392b;font:13px/1.5 ui-monospace,monospace;white-space:pre-wrap;padding:16px';
    document.body.appendChild(p);
  }
  function mermaidDraw(code, dark) {
    document.body.style.cssText = 'margin:0;padding:16px;background:' + (dark ? '#1b1c20' : '#fff') + ';display:flex;justify-content:center';
    var s = document.createElement('script');
    s.src = 'mermaid.js';
    s.onerror = function () { fail('Could not load the diagram library.'); };
    s.onload = function () {
      try {
        window.mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: dark ? 'dark' : 'default' });
        window.mermaid.render('outpost-diagram', code).then(function (r) {
          var d = document.createElement('div');
          d.innerHTML = r.svg;
          document.body.appendChild(d);
        }, function (e) { fail('This diagram has an error:\n' + (e && e.message ? e.message : e)); });
      } catch (e) { fail(String(e)); }
    };
    document.head.appendChild(s);
  }
  window.addEventListener('message', function (e) {
    if (e.source !== window.parent || shown) return;
    var d = e.data || {};
    shown = true;
    if (d.kind === 'mermaid') mermaidDraw(String(d.code || ''), !!d.dark);
    else if (d.kind === 'page') write('<!doctype html><meta charset="utf-8"><style>' + String(d.css || '') + '</style>' + String(d.body || ''));
    else write(String(d.html || ''));
  });
  window.parent.postMessage({ outpostPreview: 'ready' }, '*');
})();
