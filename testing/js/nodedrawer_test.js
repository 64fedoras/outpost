// Renders the Nodes page details panel (renderNodeDrawer in web/nodes.js) with stubbed helpers, so a
// name the panel uses but never defines (like the "uc is not defined" bug after the Updates merge) fails
// here instead of in the browser. Run with: node testing/js/nodedrawer_test.js
'use strict';
const fs = require('fs'), path = require('path'), vm = require('vm');
const src = fs.readFileSync(path.join(__dirname, '..', '..', 'web', 'nodes.js'), 'utf8');
const el = () => ({ classList: { toggle() {} }, querySelector: () => null, addEventListener() {}, scrollTop: 0, html: '' });
const drawer = el();
const box = {
  localStorage: { getItem: () => null }, NODE_FILTER_MIN: 8, UPD_BUSY: ['sent', 'downloading', 'installing', 'restarting'],
  document: { addEventListener() {}, querySelectorAll: () => [], body: el() },
  state: { nodes: [], rollout: null, update_everything: null },
  $: (id) => (id === 'node-drawer' ? drawer : el()), setHTML: (d, h) => { d.html = h; },
  esc: (s) => String(s), bar: () => '', nodeName: (n) => n.name || n.id,
  nodeFacts: (n) => ({ h: n.hello || {}, dot: 'ok' }), nodeStatus: () => ['online', 'ok'],
};
vm.createContext(box);
// nodeCard and nodeJobs need the whole page; this test is about the panel around them.
vm.runInContext(src + '\nnodeCard = () => "<div class=card></div>"; nodeJobs = () => "";\nthis.api = { renderNodeDrawer, open: (id) => { nodeDrawer = id; } };', box);
const A = box.api;
let failed = 0, n = 0;
function ok(name, cond, info) { n++; if (!cond) { failed++; console.error(`FAIL ${name}${info ? '\n  ' + info : ''}`); } }

const node = { id: 'n1', name: 'lab-1', online: true, hello: { os: 'linux', arch: 'amd64', agent_version: '0.19.5' } };
box.state.nodes = [node];
box.nodeById = (id) => box.state.nodes.find((x) => x.id === id);
A.open('n1');
try { A.renderNodeDrawer([node]); ok('panel renders', drawer.html.includes('lab-1'), drawer.html.slice(0, 200)); } catch (e) { ok('panel renders', false, e.stack); }
node.update_to = '0.19.6';
try { A.renderNodeDrawer([node]); ok('panel shows the update chip', drawer.html.includes('upd-chip'), drawer.html.slice(0, 300)); } catch (e) { ok('panel shows the update chip', false, e.stack); }
A.open(null);
try { A.renderNodeDrawer([node]); ok('closed panel is empty', drawer.html === ''); } catch (e) { ok('closed panel is empty', false, e.stack); }
if (failed) { console.error(`${failed} of ${n} failed`); process.exit(1); }
console.log(`nodedrawer: ${n} checks passed`);
