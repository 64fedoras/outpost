// Unit tests for web/markdown.js. Run with: node testing/js/markdown_test.js
// The web UI has no build step and no test framework, so this loads app.js's esc() and
// markdown.js into a bare sandbox and checks the HTML that md() returns.
'use strict';
const fs = require('fs'), path = require('path'), vm = require('vm');
const root = path.join(__dirname, '..', '..', 'web');
const app = fs.readFileSync(path.join(root, 'app.js'), 'utf8');
const escLine = app.split('\n').find((l) => l.startsWith('const esc = '));
if (!escLine) { console.error('esc() not found in app.js'); process.exit(1); }
const box = {};
vm.createContext(box);
vm.runInContext(escLine + '\n' + fs.readFileSync(path.join(root, 'markdown.js'), 'utf8') + '\nthis.md = md;', box);
const md = box.md;

let failed = 0, n = 0;
function has(name, src, ...want) {
  const out = md(src); n++;
  for (const w of want) if (!(w instanceof RegExp ? w.test(out) : out.includes(w))) { failed++; console.error(`FAIL ${name}\n  wanted: ${w}\n  got:    ${out}`); }
  return out;
}
function not(name, src, ...bad) {
  const out = md(src); n++;
  for (const w of bad) if (w instanceof RegExp ? w.test(out) : out.includes(w)) { failed++; console.error(`FAIL ${name}\n  must not contain: ${w}\n  got: ${out}`); }
}

// what chat models write
has('heading with bold', '### 🧅 **Quick Fixes**', '<h3 class="md-h md-h3">🧅 <strong>Quick Fixes</strong></h3>');
has('bold italic code', 'a **b** *c* _d_ `e*f*` ~~g~~', '<strong>b</strong>', '<em>c</em>', '<em>d</em>', '<code>e*f*</code>', '<del>g</del>');
has('rule', 'a\n\n---\n\nb', '<p>a</p><hr><p>b</p>');
has('line break kept', 'one\ntwo', '<p>one<br>two</p>');
has('list with continuation', '- **Caramelize them**\n  Thinly slice.\n\n- **Jam**\n  Cook down.\n\n---', '<ul><li><strong>Caramelize them</strong><br>Thinly slice.</li><li><strong>Jam</strong><br>Cook down.</li></ul><hr>');
has('list right after a paragraph', 'Options:\n- a\n- b', '<p>Options:</p><ul><li>a</li><li>b</li></ul>');
has('nested list', '1. one\n   - in\n   - in2\n2. two', '<ol><li>one<ul><li>in</li><li>in2</li></ul></li><li>two</li></ol>');
has('numbered start', '3. c\n4. d', '<ol start="3"><li>c</li><li>d</li></ol>');
has('task list', '- [x] done\n- [ ] todo', '☑', '☐');
has('quote', '> hi\n> there', '<blockquote><p>hi<br>there</p></blockquote>');
has('table', '| a | b |\n|---|--:|\n| 1 | 2 |', '<th>a</th>', '<th class="md-r">b</th>', '<td class="md-r">2</td>');
has('snake_case is not italic', 'use my_var_name and 2 * 3 * 4', 'my_var_name', '2 * 3 * 4');
not('snake_case is not italic 2', 'use my_var_name and 2 * 3 * 4', '<em>');
has('escaped star', '\\*not italic\\*', '*not italic*');

// code
has('fenced code', 'x\n```go\nfunc main() { a := "<b>" }\n```\ny', '<span>go</span>', '<pre><code>func main() { a := &quot;&lt;b&gt;&quot; }</code></pre>', '<p>y</p>');
has('code keeps markdown characters', '```\n# not a heading\n**not bold**\n```', '# not a heading\n**not bold**');
not('code keeps markdown characters 2', '```\n# not a heading\n**not bold**\n```', '<h1', '<strong>');
has('unfinished code block while the answer is still arriving', 'see\n```python\nprint(1)', '<pre><code>print(1)</code></pre>');
has('tilde fence', '~~~\na\n~~~', '<pre><code>a</code></pre>');

// links
has('link', '[site](https://example.com/a?b=1&c=2)', '<a href="https://example.com/a?b=1&amp;c=2" target="_blank" rel="noopener noreferrer nofollow">site</a>');
has('bare link', 'see https://example.com/x.', '<a href="https://example.com/x" ', '>https://example.com/x</a>.');
has('image becomes a link, nothing is fetched', '![cat](https://example.com/cat.png)', '<a href="https://example.com/cat.png"');
not('image becomes a link 2', '![cat](https://example.com/cat.png)', '<img');

// safety: no tag or attribute may come from the model's text
const evil = [
  '<script>alert(1)</script>', '<img src=x onerror=alert(1)>', '[x](javascript:alert(1))', '[x](JaVaScRiPt:alert(1))',
  '[x](data:text/html,<script>alert(1)</script>)', '[x](vbscript:msgbox(1))', '[a](https://example.com/"onmouseover="alert(1))',
  '[a](https://example.com/ "t" onclick=alert(1))', '`<b>`', '```\n</code></pre><script>alert(1)</script>\n```',
  '```"><script>alert(1)</script>\ncode\n```', '# <svg onload=alert(1)>', '| <i>a</i> |\n|---|\n| <u>b</u> |',
  '> <iframe src=//example.com>', '- <a href=javascript:alert(1)>x</a>', '**<b onclick=alert(1)>x</b>**',
  '\u00000\u0000 <b>', '[<img src=x onerror=alert(1)>](https://example.com)', 'https://example.com/<script>', '<a href="javascript:alert(1)">x</a>',
  '[x](https://example.com/\u0000javascript:alert(1))', '![x](javascript:alert(1))', '&lt;script&gt; &amp;',
];
const allowed = /^<\/?(?:p|br|hr|h[1-6]|strong|em|del|code|pre|ul|ol|li|blockquote|div|span|button|table|thead|tbody|tr|th|td|a)(?=[\s>])/;
for (const src of evil) {
  const out = md(src); n++;
  const tags = out.match(/<[^>]*>/g) || [];
  for (const t of tags) {
    let bad = !allowed.test(t);
    // attributes: only the fixed ones this file writes, and href only http, https or mailto
    const attrs = t.replace(/^<\/?[\w]+/, '').replace(/>$/, '');
    const rest = attrs.replace(/\s(?:class|start|type|target|rel|aria-hidden)="[\w\s.-]*"/g, '').replace(/\sdata-md-copy/g, '')
      .replace(/\shref="(?:https?:\/\/|mailto:)[^"\s]*"/g, '');
    if (rest.trim()) bad = true;
    if (bad) { failed++; console.error(`FAIL unsafe output for ${JSON.stringify(src)}\n  tag: ${t}\n  got: ${out}`); }
  }
  if (/="?\s*(?:javascript|vbscript|data):/i.test(tags.join(''))) { failed++; console.error(`FAIL script URL in a tag for ${JSON.stringify(src)}: ${out}`); }
}

// never throws, never hangs
const t0 = Date.now();
md('*'.repeat(5000)); md('_'.repeat(5000)); md('`'.repeat(5001)); md('['.repeat(5000)); md('> '.repeat(3000)); md('- '.repeat(3000));
md(Array(200).fill(0).map((_, i) => ' '.repeat(i) + '- x').join('\n')); md('**a '.repeat(3000)); md('|'.repeat(5000) + '\n' + '|-'.repeat(2500));
md('x'.repeat(500000)); md(null); md(undefined); md(42);
if (Date.now() - t0 > 3000) { failed++; console.error(`FAIL slow: ${Date.now() - t0} ms`); }
if (md('') !== '' || md('  \n ') !== '') { failed++; console.error('FAIL empty text should give empty HTML'); }

console.log(failed ? `${failed} failed` : `markdown: ${n} checks passed in ${Date.now() - t0} ms (stress part)`);
process.exit(failed ? 1 : 0);
