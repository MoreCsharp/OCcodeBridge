const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const root = path.join(__dirname, '../internal/bridge/ui');
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8');
const scheduling = fs.readFileSync(path.join(root, 'scheduling.js'), 'utf8');

const cardStart = html.indexOf('    const usageLabels = {');
const cardEnd = html.indexOf('    function updateUsageRow(', cardStart);
assert.ok(cardStart >= 0 && cardEnd > cardStart, 'usage card functions must precede updateUsageRow');

function card(usage) {
  const item = { id: 'one', label: '测试账号', enabled: true, vendor: usage?.vendor };
  const context = vm.createContext({
    credentialView: { usage: new Map([[item.id, usage]]), pending: new Map(), queue: new Map() },
    esc: value => String(value ?? ''),
    shown: (value, fallback = '') => (value === '' || value === null || value === undefined ? fallback : String(value)),
  });
  vm.runInContext(html.slice(cardStart, cardEnd) + `
    globalThis.card = { meters: usageHTML, status: usageStatusHTML, plan: usagePlanHTML, money: usageMoney };
  `, context);
  return { item, ...context.card };
}

test('CommandCode GOAT shows plan credits (not dollars), dollar extra credits and its three windows', () => {
  const view = card({
    id: 'one', vendor: 'commandcode', status: 'ok',
    updated_at: new Date().toISOString(),
    balance: 12.5, balance_currency: 'USD',
    plan: { name: 'GOAT', current_period_end: '2030-01-31T00:00:00Z' },
    limits: [
      { type: 'five_hour', percent_used: 95, used: 9.5, cap: 10, unit: 'credit', resets_at: '2030-01-02T00:00:00Z' },
      { type: 'weekly', percent_used: 40, used: 20, cap: 50, unit: 'credit', resets_at: '2030-01-08T00:00:00Z' },
      { type: 'monthly', used: 3, cap: 4, unit: 'credit' },
    ],
  });
  const markup = view.meters(view.item);
  assert.match(markup, /5 小时/);
  assert.match(markup, /每周/);
  assert.match(markup, /每月/);
  assert.doesNotMatch(markup, /每天/, 'there is no daily plan window');
  assert.match(markup, /9\.5 \/ 10 积分/);
  assert.match(markup, /3 \/ 4 积分/);
  assert.doesNotMatch(markup, /\$9\.5|\$10\.00|\$3\.00/, 'plan credits are never shown as dollars');
  assert.match(markup, /积分按模型折算/, 'the tooltip explains that a credit is worth a different amount per model');
  assert.match(markup, /额外额度 <b>\$12\.50<\/b>（超窗口时会被自动扣用）/);
  assert.match(markup, /usage-meter bad/, 'a 95% window is highlighted as risky');
  // The period end is shown in the browser's local time zone, so the expectation
  // is built the same way instead of assuming the machine runs at UTC+8.
  const end = new Date('2030-01-31T00:00:00Z');
  const pad = (n) => String(n).padStart(2, '0');
  const local = `${pad(end.getMonth() + 1)}/${pad(end.getDate())} ${pad(end.getHours())}:${pad(end.getMinutes())}`;
  assert.equal(view.plan(view.item), `CommandCode · GOAT · 本期至 ${local}`);
});

test('missing values stay unknown and only reported windows get a bar', () => {
  const view = card({
    id: 'one', vendor: 'opencode-go', status: 'ok', updated_at: new Date().toISOString(),
    plan: { name: 'Go' },
    limits: [{ type: 'five_hour' }, { type: 'weekly', percent_used: 0, used: 0, cap: 12, unit: 'usd', status: 'derived', resets_at: '2030-01-07T00:00:00Z' }],
  });
  const markup = view.meters(view.item);
  assert.doesNotMatch(markup, /每月|每天/, 'an unreported window gets no placeholder row');
  assert.match(markup, /\$0\.00 \/ \$12\.00/, 'dollar windows keep the dollar format');
  const fiveHour = markup.slice(markup.indexOf('5 小时'), markup.indexOf('每周'));
  assert.match(fiveHour, /—/);
  assert.match(fiveHour, /重置时间 —/);
  assert.doesNotMatch(fiveHour, /role="progressbar"/, 'a window without usage must not claim 0%');
  assert.match(markup, /按规则推算/);
  assert.doesNotMatch(markup, /额外额度/, 'only CommandCode reports a spendable balance');
});

test('a failed query keeps the last sample and says so', () => {
  const view = card({
    id: 'one', vendor: 'commandcode', status: 'unavailable', error: '上游拒绝查询，请检查 API Key 或账号权限',
    checked_at: new Date().toISOString(),
    updated_at: '2020-01-01T00:00:00Z',
    limits: [{ type: 'weekly', percent_used: 12, used: 6, cap: 50, unit: 'usd' }],
  });
  const status = view.status(view.item);
  assert.match(status, /查询失败/);
  assert.match(status, /上次数据/);
  assert.match(status, /上游拒绝查询/);
  assert.match(view.meters(view.item), /\$6\.00 \/ \$50\.00/);
});

test('an empty sample does not render a zero bar', () => {
  const view = card({ id: 'one', vendor: 'commandcode', status: 'ok', updated_at: new Date().toISOString(), limits: [] });
  const markup = view.meters(view.item);
  assert.doesNotMatch(markup, /role="progressbar"/);
  assert.doesNotMatch(markup, /usage-meter /, 'no placeholder windows are drawn');
  assert.match(view.status(view.item), /上游未返回用量窗口/);
});

test('amounts with an unknown unit carry no currency sign', () => {
  const view = card({
    id: 'one', vendor: 'commandcode', status: 'ok', updated_at: new Date().toISOString(),
    limits: [{ type: 'five_hour', percent_used: 50, used: 1, cap: 2 }],
  });
  const markup = view.meters(view.item);
  assert.match(markup, />1 \/ 2</);
  assert.doesNotMatch(markup, /\$/);
});

function schedulingView(account, item = { id: 'one', label: '测试账号', enabled: true, quota_paused: true }) {
  const nodes = new Map();
  const toggleLabel = {};
  const toggle = { setAttribute() {}, closest: () => toggleLabel };
  nodes.set('.credential-enabled', toggle);
  for (const selector of ['.credential-quota-recovery', '.credential-schedule-badges', '.credential-schedule-detail', '.credential-toggle-label', '.reset-cooldown', '.recheck-credential']) {
    nodes.set(selector, { hidden: true, disabled: false });
  }
  const row = { dataset: { id: item.id }, querySelector: selector => nodes.get(selector) };
  const list = { children: [row] };
  const calls = [];
  const toasts = [];
  const context = vm.createContext({
    esc: value => String(value ?? ''),
    authGate: { verified: true, paused: null },
    credentialView: { items: [item], usage: new Map(), pending: new Set(), queue: new Map() },
    $: id => (id === 'credentialList' ? list : {}),
    syncCredentialCollapse() {},
    syncModelTestCredentials() {},
    syncClearAffinityControls() {},
    toast: message => toasts.push(message),
    api: async (url, options) => {
      calls.push({ url, options });
      if (url === '/scheduling') return { accounts: [{ ...account }] };
      assert.equal(url, '/credentials/recheck');
      return { cleared: true, status: 'ok' };
    },
  });
  vm.runInContext(scheduling.slice(0, scheduling.indexOf("$('credentialList').addEventListener('change'")) + `
    globalThis.view = {
      badges: credentialScheduleBadges, recovery: credentialQuotaRecoveryHTML,
      update: updateCredentialScheduling, recheck: recheckCredential,
      setAccounts: rows => { credentialScheduling.accounts = new Map(rows.map(a => [a.id, a])); }
    };
  `, context);
  context.view.setAccounts([account]);
  return { context, item, row, nodes, calls, toasts, ...context.view };
}

test('a plan without API access is a distinct badge with a recheck action, not a timed pause', () => {
  const view = schedulingView({ id: 'one', enabled: true, quota_blocked: true, plan_unsupported: true, quota_reason: '套餐不支持 API 访问：请升级套餐' }, { id: 'one', label: '测试账号', enabled: true, quota_paused: true, plan_unsupported: true });
  const badges = view.badges(view.item);
  assert.match(badges, /套餐不支持 API/);
  assert.doesNotMatch(badges, /额度耗尽/);
  const recovery = view.recovery(view.item);
  assert.match(recovery, /等待重新检测/);
  assert.doesNotMatch(recovery, /恢复时间待确认/);
  view.update();
  assert.equal(view.nodes.get('.recheck-credential').hidden, false);
});

test('a window pause keeps its reset wording and hides the recheck action', () => {
  const view = schedulingView({ id: 'one', enabled: true, quota_blocked: true, retry_at: '2030-01-02T00:00:00Z' });
  assert.match(view.badges(view.item), /额度耗尽/);
  assert.doesNotMatch(view.badges(view.item), /套餐不支持 API/);
  assert.match(view.recovery(view.item), /后恢复/);
  view.update();
  assert.equal(view.nodes.get('.recheck-credential').hidden, true);
});

test('recheck calls the endpoint once and requests a fresh usage sample', async () => {
  const view = schedulingView({ id: 'one', enabled: true, quota_blocked: true, plan_unsupported: true });
  await view.recheck(view.item.id);
  const posts = view.calls.filter(call => call.url === '/credentials/recheck');
  assert.equal(posts.length, 1);
  assert.equal(posts[0].options.body.id, view.item.id);
  assert.ok(view.context.credentialView.queue.get(view.item.id), 'a cleared state must refresh the card');
  assert.match(view.toasts.join(' '), /已清除「套餐不支持 API」状态/);
  assert.equal(view.context.credentialView.pending.has(view.item.id), false);
});
