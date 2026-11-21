'use strict';
// Nodes page filter and sort. Plain script, no modules, no globals used: the functions take
// each node with its facts ({ n, f } from nodeFacts) so testing/js/nodefilter_test.js can run them alone.

// The filter bar shows once a cluster has this many nodes (or any labels); smaller clusters stay as they were.
const NODE_FILTER_MIN = 8;
// Quick filters, in the order the bar shows them. Each test gets the node and its facts.
const NODE_CHIPS = [
  ['all', 'All', () => true],
  ['online', 'Online', (n) => n.online],
  ['offline', 'Offline', (n) => !n.online],
  ['attention', 'Needs attention', (n, f) => f.worst === 'bad' || f.worst === 'warn'],
  ['gpu', 'Has GPU', (n) => ((n.hello || {}).gpus || []).length > 0],
  ['nomodel', 'No model', (n, f) => n.online && !f.loaded && !n.lending_to],
  ['update', 'Can update', (n) => !!n.update_to],
];
const nodeChipTest = (id) => (NODE_CHIPS.find((c) => c[0] === id) || NODE_CHIPS[0])[2];

// nodeText is what the search box matches: name, host, OS, CPU and GPU names, the loaded model and labels.
function nodeText(n, f) {
  const h = n.hello || {};
  return [n.name, h.hostname, h.os, h.arch, (h.cpu || {}).model, ...(h.gpus || []).map((g) => g.name), f.loaded, ...(n.labels || [])]
    .filter(Boolean).join(' ').toLowerCase();
}
// nodeMatches: every word of the query must appear somewhere, the quick filter must pass, and
// the label (if one is picked) must be on the node.
function nodeMatches(n, f, q, chip, label) {
  if (label && !(n.labels || []).includes(label)) return false;
  if (!nodeChipTest(chip)(n, f)) return false;
  const words = String(q || '').toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return true;
  const t = nodeText(n, f);
  return words.every((w) => t.includes(w));
}
// nodeChipCounts counts each quick filter over the whole cluster (the counts do not change as you type).
function nodeChipCounts(rows) {
  const c = {};
  for (const [id, , test] of NODE_CHIPS) c[id] = rows.filter(({ n, f }) => test(n, f)).length;
  return c;
}
// nodeSorted: 'status' puts problems first (then online, then offline), 'name' is alphabetical,
// 'speed' is fastest loaded model first. Ties keep the name order so the grid does not jump.
const NODE_RANK = { bad: 0, warn: 1, info: 2, '': 2, off: 4 };
function nodeSorted(rows, by) {
  const name = (r) => String(r.n.name || r.n.id || '').toLowerCase();
  const byName = (a, b) => name(a).localeCompare(name(b), undefined, { numeric: true });
  const cmp = {
    name: byName,
    speed: (a, b) => (b.f.tps || 0) - (a.f.tps || 0) || byName(a, b),
    status: (a, b) => (NODE_RANK[a.f.worst] ?? 2) - (NODE_RANK[b.f.worst] ?? 2) || byName(a, b),
  }[by] || ((a, b) => (NODE_RANK[a.f.worst] ?? 2) - (NODE_RANK[b.f.worst] ?? 2));
  return [...rows].sort(cmp);
}
