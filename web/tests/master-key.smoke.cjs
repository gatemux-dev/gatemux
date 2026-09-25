// Real master-key sign-in with a stale HttpOnly cookie. Read-only API requests;
// no account creation, data-plane calls, route interception or secret output.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');

(async () => {
  const base = process.env.GATEMUX_SMOKE_URL || 'http://localhost:4001';
  const key = process.env.GATEMUX_SMOKE_ADMIN_KEY;
  assert(key, 'GATEMUX_SMOKE_ADMIN_KEY is required');
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'gatemux_session', value: 'sess-expired-smoke-fixture', url: base, httpOnly: true, sameSite: 'Strict' }]);
    // The backend's existing cookie-priority rule remains intact.
    const mixed = await context.request.get(`${base}/auth/me`, { headers: { Authorization: `Bearer ${key}` } });
    assert.equal(mixed.status(), 401, 'stale cookie reproduces ambiguous-credential failure');
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors = [];
    const checks = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', request => {
      if (!request.headers().authorization) return;
      checks.push(request.allHeaders().then(headers => ({ path: new URL(request.url()).pathname, cookieSent: !!headers.cookie })));
    });
    await page.goto(base);
    await page.getByRole('button', { name: 'Emergency / break-glass admin key', exact: true }).click();
    await page.getByLabel('Admin master key', { exact: false }).fill('invalid-smoke-master');
    await page.getByRole('button', { name: 'Sign in as admin', exact: true }).click();
    await page.getByRole('alert').getByText(/Invalid admin master key/).waitFor();
    assert.equal(await page.evaluate(() => sessionStorage.getItem('gatemux.masterKey')), null, 'failed validation must not persist a key');
    assert((await context.cookies()).some(cookie => cookie.name === 'gatemux_session'), 'do not delete unrelated account cookies');
    await page.getByLabel('Admin master key', { exact: false }).fill(`  ${key}  `);
    await page.getByRole('button', { name: 'Sign in as admin', exact: true }).click();
    await page.getByRole('heading', { name: 'Overview', exact: true }).waitFor();
    await page.getByText('Connected', { exact: true }).first().waitFor();
    const teamLoaded = page.waitForResponse(response => new URL(response.url()).pathname === '/admin/teams' && response.status() === 200);
    await page.goto(`${base}/teams`);
    await page.getByRole('heading', { name: 'Teams', exact: true, level: 1 }).waitFor();
    await teamLoaded;
    const reloaded = page.waitForResponse(response => new URL(response.url()).pathname === '/admin/teams' && response.status() === 200);
    await page.reload();
    await reloaded;
    await page.getByRole('heading', { name: 'Teams', exact: true, level: 1 }).waitFor();
    const requests = await Promise.all(checks);
    for (const request of requests) assert.equal(request.cookieSent, false, `master request sent an account cookie: ${request.path}`);
    assert(requests.some(request => request.path === '/auth/me'));
    assert(requests.some(request => request.path === '/admin/info'));
    assert(requests.some(request => request.path === '/admin/teams'));
    assert.deepEqual(errors, [], 'browser errors');
    const legacy = await browser.newContext();
    await legacy.addCookies([{ name: 'aiport_session', value: 'sess-expired-legacy-fixture', url: base, httpOnly: true, sameSite: 'Strict' }]);
    assert.equal((await legacy.request.get(`${base}/auth/me`, { headers: { Authorization: `Bearer ${key}` } })).status(), 401, 'legacy cookie still precedes bearer');
    await legacy.addInitScript(master => {
      sessionStorage.setItem('aiport.masterKey', master);
      localStorage.setItem('aiport.theme', 'dark');
    }, key);
    const migrated = await legacy.newPage();
    migrated.on('pageerror', error => errors.push(error.message));
    await migrated.goto(base);
    await migrated.getByRole('heading', { name: 'Overview', exact: true }).waitFor();
    assert.equal(await migrated.title(), 'GateMux');
    assert.equal(await migrated.evaluate(() => !!sessionStorage.getItem('gatemux.masterKey') && !sessionStorage.getItem('aiport.masterKey')), true);
    assert.equal(await migrated.evaluate(() => localStorage.getItem('gatemux.theme')), 'dark');
    assert.equal(await migrated.evaluate(() => localStorage.getItem('aiport.theme')), null);
    assert.equal(await migrated.locator('html').getAttribute('data-theme'), 'dark');
    assert.deepEqual(errors, [], 'legacy migration browser errors');
    await legacy.close();
    console.log('PASS: stale-cookie reproduction, clear invalid-key error, validated-only storage, master login/object/list requests without cookies, reload, preserved cookie-priority backend.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
