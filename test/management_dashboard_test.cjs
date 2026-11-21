// Run with: node --test test/management_dashboard_test.cjs
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '../assets/management-custom.html'), 'utf8');
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((match) => match[1]);
const script = scripts[1].replace(/\/\/ -+ boot[\s\S]*?\}\)\(\);\s*$/, `
globalThis.dashboard = { S, canLiveQuota, parseClaudeUsage, parseCodexUsage, parseAntigravityUsage,
  quotaInfo, credHTML, fetchLiveQuota, refreshNewQuotas, refreshAllQuotas,
  stubRender() { renderCreds = () => {}; renderKpis = () => {}; } };
})();`);
const future = () => new Date(Date.now() + 7 * 86400e3).toISOString();
const antigravity = (remaining = 0.75) => ({ models: {
  'gemini-4-hydrogen-low': { displayName: 'Gemini 4 Hydrogen (Low)', quotaInfo: { remainingFraction: remaining, resetTime: future() } },
} });
const claude = (used = 17) => ({ limits: [
  { kind: 'session', group: 'session', percent: 3, resets_at: future(), scope: null },
  { kind: 'weekly_all', group: 'weekly', percent: 40, resets_at: future(), scope: null },
  { kind: 'weekly_scoped', group: 'weekly', percent: used, resets_at: future(), scope: { model: { display_name: 'Fable' } } },
] });
const account = { provider: 'antigravity', auth_index: 'test', project_id: 'test-project', name: 'test.json', email: 'test@example.invalid', status: 'active' };

function load() {
  const nodes = new Map();
  const node = () => ({ dataset: {}, style: {}, classList: { add() {}, remove() {}, toggle() {} }, addEventListener() {}, querySelectorAll() { return []; } });
  const storage = { getItem() { return null; }, setItem() {}, removeItem() {} };
  const requests = [];
  let response = { status_code: 200, body: JSON.stringify(antigravity()) };
  const context = vm.createContext({
    URL, Intl, Date, window: {}, localStorage: storage, sessionStorage: storage,
    matchMedia() { return { matches: false, addEventListener() {} }; },
    document: { documentElement: node(), body: node(), addEventListener() {}, querySelectorAll() { return []; }, getElementById(id) { if (!nodes.has(id)) nodes.set(id, node()); return nodes.get(id); } },
    setInterval() {}, clearInterval() {}, setTimeout() {}, clearTimeout() {},
    location: { origin: 'http://localhost:8317', protocol: 'http:' },
    async fetch(url, options) {
      requests.push({ url, body: JSON.parse(options.body) });
      return { status: 200, ok: true, headers: { get(name) { return name.toLowerCase() === 'content-type' ? 'application/json' : null; } }, async json() { return response; } };
    },
  });
  vm.runInContext(script, context);
  return { ...context.dashboard, requests, respond(value) { response = value; } };
}

test('both inline scripts are valid JavaScript', () => {
  assert.equal(scripts.length, 2);
  for (const source of scripts) new vm.Script(source);
});

test('Claude structured usage includes a zero-percent Fable limit without duplicate legacy windows', () => {
  const d = load();
  const parsed = d.parseClaudeUsage({ ...claude(0), seven_day: { utilization: 20, resets_at: future() } });
  assert.deepEqual(Array.from(parsed.windows, (window) => window.label), ['5-hour', 'Weekly', 'Weekly · Fable']);
  assert.equal(parsed.windows[1].used, 40);
  assert.equal(parsed.windows[2].used, 0);
  assert.equal(d.parseClaudeUsage({ seven_day_fable: { utilization: 12, resets_at: future() } }).windows[0].label, 'Weekly · Fable');
});

test('new ordinary traffic does not discard or re-date the last live Fable reading', () => {
  const d = load();
  const at = Date.now() - 120000;
  const file = { ...account, provider: 'claude', quota: { observed_at: new Date(at + 60000).toISOString(), signals: {
    'Anthropic-Ratelimit-Unified-7d-Utilization': '0.5', 'Anthropic-Ratelimit-Unified-7d-Reset': future(),
  } } };
  d.S.live.test = { ...d.parseClaudeUsage(claude()), at };
  const combined = d.quotaInfo(file);
  assert.equal(combined.windows.find((window) => window.label === 'Weekly').used, 50);
  const fable = combined.windows.find((window) => window.label === 'Weekly · Fable');
  assert.equal(fable.used, 17);
  assert.equal(fable.observedAt, at);
});

test('Antigravity reports used percentages, preserving zero remaining quota', () => {
  const d = load();
  assert.equal(d.parseAntigravityUsage(antigravity(1)).windows[0].used, 0);
  assert.equal(d.parseAntigravityUsage(antigravity(0)).windows[0].used, 100);
  assert.equal(d.parseAntigravityUsage(antigravity('0.5')).windows[0].used, 50);
  for (const invalid of [null, '', ' ', false, {}, NaN, Infinity, 'NaN', -0.1, 1.1]) {
    assert.equal(d.parseAntigravityUsage(antigravity(invalid)).windows[0].used, null);
  }
});

test('Antigravity rejects missing quota data and accepts snake-case metadata', () => {
  const d = load();
  for (const models of [null, [], {}, { missing: {} }]) assert.throws(() => d.parseAntigravityUsage({ models }), /Antigravity/);
  const parsed = d.parseAntigravityUsage({ models: { a: null, b: { quota_info: { remaining_fraction: 0.4, reset_time: future() } } } });
  assert.equal(parsed.windows.length, 1);
  assert.equal(parsed.windows[0].label, 'b');
  assert.equal(parsed.windows[0].used, 60);
});

test('one exhausted Antigravity model does not mark every model exhausted', () => {
  const d = load();
  const body = antigravity(0);
  body.models.healthy = { quotaInfo: { remainingFraction: 1 } };
  assert.equal(d.parseAntigravityUsage(body).status, 'allowed');
  assert.equal(d.parseAntigravityUsage(antigravity(0)).status, 'rejected');
});

test('model labels have escaped, focusable full-name/model-ID tooltips', () => {
  const d = load();
  d.S.files = [account];
  const body = antigravity();
  body.models['gemini-4-hydrogen-low'].displayName = '<img src=x onerror=alert(1)>';
  d.S.live.test = { ...d.parseAntigravityUsage(body), at: Date.now() };
  const card = d.credHTML(account);
  assert.doesNotMatch(card, /<img/);
  assert.match(card, /data-tip="&lt;img/);
  assert.match(card, /gemini-4-hydrogen-low/);
  assert.match(card, /model-quota/);
  assert.match(card, /tabindex="0"/);
  assert.match(card, /role="region"/);
  assert.match(card, /Google Antigravity/);
});

test('quota requests use the selected credential and exact project, without exposing its token', async () => {
  const d = load();
  await d.fetchLiveQuota(account);
  const request = d.requests[0].body;
  assert.equal(request.auth_index, account.auth_index);
  assert.equal(request.method, 'POST');
  assert.equal(request.url, 'https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels');
  assert.equal(request.header.Authorization, 'Bearer $TOKEN$');
  assert.deepEqual(JSON.parse(request.data), { project: account.project_id });
  d.respond({ status_code: 403, body: { error: { message: 'Account access denied' } } });
  await assert.rejects(d.fetchLiveQuota(account), /upstream HTTP 403: Account access denied/);
});

test('newly added accounts get one initial check, not a probe on every poll', async () => {
  const d = load();
  d.stubRender();
  d.S.files = [account];
  await d.refreshNewQuotas();
  await d.refreshNewQuotas();
  assert.equal(d.requests.length, 1);
  d.S.files.push({ ...account, auth_index: 'new-account', disabled: true });
  await d.refreshNewQuotas();
  assert.equal(d.requests.length, 1);
  d.S.files[1].disabled = false;
  await d.refreshNewQuotas();
  assert.equal(d.requests.length, 2);
  await d.refreshAllQuotas();
  assert.equal(d.requests.length, 4);
});

test('Codex quota parsing remains unchanged', () => {
  const d = load();
  assert.equal(d.parseCodexUsage({ rate_limit: { primary_window: { used_percent: 9, limit_window_seconds: 18000 } } }).windows[0].used, 9);
});
