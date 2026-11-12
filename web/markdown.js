'use strict';
// Markdown for model answers. Plain script, no modules: loads after app.js and uses its esc().
//
// md(text) returns HTML that is safe to put in the page: every piece of the model's text goes
// through esc() before it is joined into a tag, the only tags are the ones written in this file,
// and the only attribute that carries model text is a link's href, which must be http, https or
// mailto. Raw HTML in an answer is shown as text. Nothing is fetched: an image becomes a link.
// There are no inline styles or scripts, so the page's Content-Security-Policy is untouched.
//
// It covers what chat models write: headings, bold, italic, strikethrough, inline code, fenced
// code, lists (nested, numbered, task boxes), block quotes, rules, tables and links. A single
// line break stays a line break, as it did when answers were shown as plain text.

const MD_MAX = 400000; // longer than this is shown as plain text, to keep typing smooth
const MD_DEPTH = 8; // lists and quotes nested deeper than this are shown as plain text

const mdFence = (l) => /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)[^`]*$/.exec(l);
const mdHeading = (l) => /^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$/.exec(l);
const mdRule = (l) => /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/.test(l);
const mdQuote = (l) => /^ {0,3}>/.test(l);
const mdItem = (l) => /^( *)([-*+]|\d{1,9}[.)])(?:[ \t]+(.*)|[ \t]*)$/.exec(l);
const mdTableRule = (l) => l.includes('|') && /^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)*\|?\s*$/.test(l);
const mdIndent = (l) => l.length - l.replace(/^ +/, '').length;

function mdSafeUrl(u) {
  u = String(u).trim();
  return /^(https?:\/\/|mailto:)[^\s\u0000-\u001f]+$/i.test(u) ? u : '';
}
function mdLink(url, labelHtml) {
  return `<a href="${esc(url)}" target="_blank" rel="noopener noreferrer nofollow">${labelHtml}</a>`;
}

// Bold, italic and strikethrough on text that is already escaped.
function mdEmphasis(s) {
  return s
    .replace(/\*\*\*(?=\S)(.+?\S|\S)\*\*\*/g, '<strong><em>$1</em></strong>')
    .replace(/\*\*(?=\S)(.+?\S|\S)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[^\w])__(?=\S)(.+?\S|\S)__(?!\w)/g, '$1<strong>$2</strong>')
    .replace(/\*(?=[^\s*])([^*]*?[^\s*]|[^\s*])\*/g, '<em>$1</em>')
    .replace(/(^|[^\w])_(?=[^\s_])([^_]*?[^\s_]|[^\s_])_(?!\w)/g, '$1<em>$2</em>')
    .replace(/~~(?=\S)(.+?\S|\S)~~/g, '<del>$1</del>');
}

// One line (or a few joined lines) of running text.
function mdInline(src, noLinks) {
  const keep = [];
  const stash = (html) => { keep.push(html); return `\u0000${keep.length - 1}\u0000`; };
  let s = String(src).replace(/\u0000/g, ''), out = '', i = 0;
  // First pass, on the raw text: take out code, escaped characters and links, so that nothing
  // inside them is read as emphasis and nothing in them can be changed by the passes below.
  while (i < s.length) {
    const c = s[i];
    if (c === '\\' && i + 1 < s.length && /[\\`*_{}[\]()#+\-.!|~>]/.test(s[i + 1])) { out += stash(esc(s[i + 1])); i += 2; continue; }
    if (c === '`') {
      let n = 1; while (s[i + n] === '`') n++;
      const ticks = '`'.repeat(n);
      let j = s.indexOf(ticks, i + n);
      while (j >= 0 && s[j + n] === '`') { let k = j; while (s[k] === '`') k++; j = s.indexOf(ticks, k); }
      if (j >= 0) {
        let body = s.slice(i + n, j).replace(/\n/g, ' ');
        if (body.length > 1 && body[0] === ' ' && body[body.length - 1] === ' ' && body.trim()) body = body.slice(1, -1);
        out += stash(`<code>${esc(body)}</code>`); i = j + n; continue;
      }
      out += stash(esc(ticks)); i += n; continue;
    }
    if (!noLinks && (c === '[' || (c === '!' && s[i + 1] === '['))) {
      const m = /^!?\[((?:[^\[\]\n]|\\.)*)\]\(\s*<?([^\s()<>]*(?:\([^\s()<>]*\)[^\s()<>]*)*)>?(?:\s+(?:"[^"\n]*"|'[^'\n]*'))?\s*\)/.exec(s.slice(i));
      if (m) {
        const url = mdSafeUrl(m[2]), label = mdInline(m[1] || m[2], true);
        out += stash(url ? mdLink(url, label) : label); i += m[0].length; continue;
      }
    }
    if (!noLinks && (c === 'h' || c === 'H') && (i === 0 || !/[\w/]/.test(s[i - 1]))) {
      const m = /^https?:\/\/[^\s<>"`\u0000]+/i.exec(s.slice(i));
      if (m) {
        let u = m[0].replace(/[.,;:!?'*_~]+$/, '');
        while (u.endsWith(')') && u.split('(').length <= u.split(')').length - 1) u = u.slice(0, -1);
        if (mdSafeUrl(u)) { out += stash(mdLink(u, esc(u))); i += u.length; continue; }
      }
    }
    out += c; i++;
  }
  out = mdEmphasis(esc(out)).replace(/\n/g, '<br>');
  // Put the stashed pieces back. A stashed link label can hold stashes of its own, so repeat.
  for (let n = 0; n < 4 && out.includes('\u0000'); n++) out = out.replace(/\u0000(\d+)\u0000/g, (_, k) => keep[Number(k)] ?? '');
  return out.replace(/\u0000/g, '');
}

function mdCells(line) {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1);
  const cells = []; let cur = '', code = false;
  for (let i = 0; i < s.length; i++) {
    if (s[i] === '\\' && s[i + 1] === '|') { cur += '|'; i++; continue; }
    if (s[i] === '`') code = !code;
    if (s[i] === '|' && !code) { cells.push(cur.trim()); cur = ''; continue; }
    cur += s[i];
  }
  cells.push(cur.trim());
  return cells;
}

// A line that starts something new, so it ends the paragraph above it without a blank line.
function mdStartsBlock(lines, i) {
  const l = lines[i];
  if (mdFence(l) || mdHeading(l) || mdRule(l) || mdQuote(l)) return true;
  const it = mdItem(l);
  if (it && it[3] !== undefined && it[1].length <= 3 && (/^[-*+]$/.test(it[2]) || /^1[.)]$/.test(it[2]))) return true;
  return l.includes('|') && i + 1 < lines.length && mdTableRule(lines[i + 1]) && mdCells(lines[i + 1]).length === mdCells(l).length;
}

// Blocks. Returns a list of {p: is a plain paragraph, html}.
function mdBlocks(lines, depth) {
  const out = [];
  if (depth > MD_DEPTH) { out.push({ p: true, html: `<p>${esc(lines.join('\n')).replace(/\n/g, '<br>')}</p>` }); return out; }
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }

    const f = mdFence(line);
    if (f) {
      const ch = f[1][0], len = f[1].length, pad = mdIndent(line), body = [];
      i++;
      // An answer that is still being written has no closing fence yet: the code runs to the end.
      for (; i < lines.length; i++) {
        const c = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(lines[i]);
        if (c && c[1][0] === ch && c[1].length >= len) { i++; break; }
        body.push(lines[i].slice(Math.min(pad, mdIndent(lines[i]))));
      }
      const lang = f[2].replace(/[^\w+#.-]/g, '').slice(0, 24);
      out.push({ html: `<div class="md-code"><div class="md-code-bar"><span>${esc(lang || 'code')}</span><button type="button" class="md-copy" data-md-copy>Copy</button></div><pre><code>${esc(body.join('\n'))}</code></pre></div>` });
      continue;
    }

    const h = mdHeading(line);
    if (h) { const n = h[1].length; out.push({ html: `<h${n} class="md-h md-h${n}">${mdInline(h[2])}</h${n}>` }); i++; continue; }

    if (mdRule(line)) { out.push({ html: '<hr>' }); i++; continue; }

    if (mdQuote(line)) {
      const body = [];
      for (; i < lines.length; i++) {
        if (mdQuote(lines[i])) body.push(lines[i].replace(/^ {0,3}> ?/, ''));
        else if (lines[i].trim() && !mdStartsBlock(lines, i)) body.push(lines[i]);
        else break;
      }
      out.push({ html: `<blockquote>${mdJoin(mdBlocks(body, depth + 1))}</blockquote>` });
      continue;
    }

    if (line.includes('|') && i + 1 < lines.length && mdTableRule(lines[i + 1])) {
      const head = mdCells(line), rule = mdCells(lines[i + 1]);
      if (head.length === rule.length) {
        const al = rule.map((c) => (/^:-+:$/.test(c) ? ' class="md-c"' : /-:$/.test(c) ? ' class="md-r"' : ''));
        let html = `<div class="md-table"><table><thead><tr>${head.map((c, k) => `<th${al[k]}>${mdInline(c)}</th>`).join('')}</tr></thead><tbody>`;
        for (i += 2; i < lines.length && lines[i].trim() && lines[i].includes('|'); i++) {
          const row = mdCells(lines[i]);
          html += `<tr>${head.map((_, k) => `<td${al[k]}>${mdInline(row[k] || '')}</td>`).join('')}</tr>`;
        }
        out.push({ html: html + '</tbody></table></div>' });
        continue;
      }
    }

    const first = mdItem(line);
    if (first && first[1].length <= 3) {
      const base = first[1].length, ordered = /\d/.test(first[2]);
      const same = (m) => m && m[1].length >= base && m[1].length <= base + 1 && /\d/.test(m[2]) === ordered;
      const items = []; let cur = null, loose = false;
      for (; i < lines.length; i++) {
        const l = lines[i], m = mdItem(l);
        if (!l.trim()) {
          // A blank line stays in the list only if the list goes on after it.
          let j = i + 1; while (j < lines.length && !lines[j].trim()) j++;
          if (j >= lines.length) break;
          const nm = mdItem(lines[j]);
          if (mdIndent(lines[j]) > base || (same(nm) && !mdRule(lines[j]))) { cur.lines.push(''); if (same(nm)) loose = true; continue; }
          break;
        }
        if (same(m) && !mdRule(l)) {
          const width = m[1].length + m[2].length + 1;
          cur = { lines: [m[3] || ''], width, num: m[2] };
          items.push(cur);
          continue;
        }
        if (mdIndent(l) > base) { cur.lines.push(l.slice(Math.min(mdIndent(l), cur.width))); continue; }
        // Not indented and not an item: it belongs to the item only as the next line of its text.
        if (cur.lines[cur.lines.length - 1] !== '' && !mdStartsBlock(lines, i) && !m) { cur.lines.push(l.trim()); continue; }
        break;
      }
      const lis = items.map((it) => {
        let box = '';
        const t = /^\[([ xX])\][ \t]+/.exec(it.lines[0]);
        if (t) { box = `<span class="md-box" aria-hidden="true">${t[1] === ' ' ? '☐' : '☑'}</span> `; it.lines[0] = it.lines[0].slice(t[0].length); }
        const blocks = mdBlocks(it.lines, depth + 1);
        // A short item is shown without paragraph spacing.
        if (blocks.length && blocks[0].p && !(loose && blocks.filter((b) => b.p).length > 1)) blocks[0] = { html: box + blocks[0].html.slice(3, -4) };
        else if (box && blocks.length) blocks[0] = { html: box + blocks[0].html };
        return `<li${t ? ' class="md-task"' : ''}>${mdJoin(blocks)}</li>`;
      }).join('');
      const start = ordered ? parseInt(items[0].num, 10) : 1;
      out.push({ html: ordered ? `<ol${start !== 1 ? ` start="${start}"` : ''}>${lis}</ol>` : `<ul>${lis}</ul>` });
      continue;
    }

    const para = [line.trim()];
    for (i++; i < lines.length && lines[i].trim() && !mdStartsBlock(lines, i); i++) para.push(lines[i].trim());
    out.push({ p: true, html: `<p>${mdInline(para.join('\n'))}</p>` });
  }
  return out;
}
const mdJoin = (blocks) => blocks.map((b) => b.html).join('');

// md turns a model's answer into safe HTML. Wrap the result in an element with class "md".
function md(text) {
  text = String(text ?? '').replace(/\r\n?/g, '\n').replace(/\u0000/g, '').replace(/\t/g, '    ');
  if (!text.trim()) return '';
  if (text.length > MD_MAX) return `<p class="md-plain">${esc(text)}</p>`;
  try { return mdJoin(mdBlocks(text.split('\n'), 0)); } catch (e) { return `<p class="md-plain">${esc(text)}</p>`; }
}

// Copy button on a code block.
if (typeof document !== 'undefined') {
  document.addEventListener('click', async (e) => {
    const b = e.target.closest && e.target.closest('[data-md-copy]');
    if (!b) return;
    const code = b.closest('.md-code').querySelector('pre');
    try { await navigator.clipboard.writeText(code.textContent); b.textContent = 'Copied'; }
    catch (err) { const r = document.createRange(); r.selectNodeContents(code); const s = getSelection(); s.removeAllRanges(); s.addRange(r); b.textContent = 'Press Ctrl+C'; }
    setTimeout(() => { b.textContent = 'Copy'; }, 1500);
  });
}
