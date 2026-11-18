const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '../internal/bridge/ui/index.html'), 'utf8');
function section(start, end) {
  const from = html.indexOf(start);
  const to = html.indexOf(end, from);
  assert.ok(from >= 0 && to > from, `missing UI section: ${start}`);
  return html.slice(from, to);
}
const keyDeclaration = html.match(/^    const modelTestKey = .+;$/m);
assert.ok(keyDeclaration, 'missing model test cache key');
const code = [
  keyDeclaration[0],
  section('    function renderModelRows()', '    function syncModelTestCredentials()'),
  section('    async function testMappedModel(', '    function candidateChanges('),
].join('\n');

// A model test result belongs to one model/credential/reasoning input triple, so any
// change to the mapping revision must hide the previous result instead of reusing it.
function page(reply = async () => ({ ok: true, provider: 'deepseek' })) {
  const state = { models: [{ id: 'flash', upstream_id: 'opencode-go/flash' }], modelBusy: false };
  const nodes = new Map();
  function node(id) {
    if (!nodes.has(id)) nodes.set(id, { value: '', innerHTML: '' });
    return nodes.get(id);
  }
  node('modelTestCredential').value = 'fixture-credential';
  const context = vm.createContext({
    state, $: node, modelTestResults: new Map(), reasoningMappingRevision: 0,
    reasoningMappingBusy: false, performance,
    shown: value => value, esc: value => String(value), buttonIcon: () => '',
    setButtonLabel() {},
    modelTestHTML: result => result ? result.pending ? 'pending' : result.provider : '',
    api: reply,
  });
  vm.runInContext(code, context);
  return {
    state, node,
    run: () => context.testMappedModel({ ...state.models[0] }),
    setMapping(mapping) {
      state.models = [{ ...state.models[0], reasoning_mapping: mapping }];
      context.renderModelRows();
    },
    setRevision(revision) {
      context.reasoningMappingRevision = revision;
      context.renderModelRows();
    },
    assertProvider(provider) {
      const rows = node('modelList').innerHTML;
      if (provider) assert.ok(rows.includes(provider), `expected ${provider} in ${rows}`);
      else assert.ok(!rows.includes('deepseek'), `stale result stayed visible: ${rows}`);
    },
  };
}

test('a saved reasoning mapping hides results produced before it', async () => {
  for (const [before, after] of [[undefined, { enabled: true }], [{ enabled: true }, { enabled: false }]]) {
    const view = page();
    view.setMapping(before);
    await view.run();
    view.assertProvider('deepseek');
    view.setMapping(after);
    view.assertProvider('');
  }
});

test('a mapping revision bump hides results produced before the save', async () => {
  const view = page();
  await view.run();
  view.assertProvider('deepseek');
  view.setRevision(1);
  view.assertProvider('');
});

test('a late response from the previous mapping stays hidden until a new test runs', async () => {
  let finishOld;
  let calls = 0;
  const view = page(() => ++calls === 1
    ? new Promise(resolve => { finishOld = resolve; })
    : Promise.resolve({ ok: true, provider: 'kimi' }));
  const oldTest = view.run();
  view.assertProvider('pending');
  view.setRevision(1);
  view.assertProvider('');
  finishOld({ ok: true, provider: 'deepseek' });
  await oldTest;
  view.assertProvider('');
  await view.run();
  assert.equal(calls, 2);
  view.assertProvider('kimi');
  assert.ok(!view.node('modelList').innerHTML.includes('deepseek'));
});
