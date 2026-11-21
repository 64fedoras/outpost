// Unit tests for web/nodefilter.js (the Nodes page filter and sort). Run with: node testing/js/nodefilter_test.js
'use strict';
const fs = require('fs'), path = require('path'), vm = require('vm');
const src = fs.readFileSync(path.join(__dirname, '..', '..', 'web', 'nodefilter.js'), 'utf8');
const box = {};
vm.createContext(box);
vm.runInContext(src + '\nthis.api = { NODE_FILTER_MIN, NODE_CHIPS, nodeMatches, nodeChipCounts, nodeSorted };', box);
const A = box.api;

const mk = (name, o = {}) => ({ n: { id: name, name, online: o.online !== false, labels: o.labels || [], update_to: o.update_to || '', hello: { os: o.os || 'linux', arch: 'amd64', cpu: { model: 'Xeon' }, gpus: o.gpu ? [{ name: 'RX 9070 XT' }] : [] } }, f: { worst: o.worst || '', loaded: o.model === undefined ? 'qwen' : o.model, tps: o.tps || 0 } });
const rows = [
  mk('lab-10', { gpu: true, labels: ['gpu'], tps: 50 }), mk('lab-2', { os: 'windows', tps: 10 }), mk('rack-1', { online: false, worst: 'off' }),
  mk('nuc-1', { model: '', worst: 'warn' }), mk('pi-1', { update_to: '0.9.0', model: 'gemma' }),
];
let failed = 0, n = 0;
function eq(name, got, want) { n++; if (JSON.stringify(got) !== JSON.stringify(want)) { failed++; console.error(`FAIL ${name}\n  want ${JSON.stringify(want)}\n  got  ${JSON.stringify(got)}`); } }
const names = (q, chip = 'all', label = '') => rows.filter((r) => A.nodeMatches(r.n, r.f, q, chip, label)).map((r) => r.n.name);

eq('no filter', names('').length, 5);
eq('name word', names('lab'), ['lab-10', 'lab-2']);
eq('all words must match', names('lab windows'), ['lab-2']);
eq('case insensitive', names('RACK'), ['rack-1']);
eq('matches loaded model', names('gemma'), ['pi-1']);
eq('matches gpu name', names('9070'), ['lab-10']);
eq('matches label', names('gpu'), ['lab-10']);
eq('chip online', names('', 'online').length, 4);
eq('chip offline', names('', 'offline'), ['rack-1']);
eq('chip attention', names('', 'attention'), ['nuc-1']);
eq('chip gpu', names('', 'gpu'), ['lab-10']);
eq('chip no model skips offline nodes', names('', 'nomodel'), ['nuc-1']);
eq('chip update', names('', 'update'), ['pi-1']);
eq('label exact', names('', 'all', 'gpu'), ['lab-10']);
eq('chip and query together', names('lab', 'gpu'), ['lab-10']);
eq('unknown chip falls back to all', names('', 'bogus').length, 5);
eq('counts', A.nodeChipCounts(rows), { all: 5, online: 4, offline: 1, attention: 1, gpu: 1, nomodel: 1, update: 1 });
eq('sort name is natural', A.nodeSorted(rows, 'name').map((r) => r.n.name), ['lab-2', 'lab-10', 'nuc-1', 'pi-1', 'rack-1']);
eq('sort speed', A.nodeSorted(rows, 'speed').map((r) => r.n.name).slice(0, 2), ['lab-10', 'lab-2']);
eq('sort status puts problems first, offline last', A.nodeSorted(rows, 'status').map((r) => r.n.name), ['nuc-1', 'lab-2', 'lab-10', 'pi-1', 'rack-1']);
eq('sort leaves input alone', rows[0].n.name, 'lab-10');
eq('filter bar threshold', A.NODE_FILTER_MIN, 8);
if (failed) { console.error(`${failed} of ${n} failed`); process.exit(1); }
console.log(`nodefilter: ${n} checks passed`);
