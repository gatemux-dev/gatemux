// Uses live login and UI assets; all customer/team policy writes are intercepted.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');

(async () => {
  const base = process.env.GATEMUX_SMOKE_URL || 'http://localhost:4001';
  const key = process.env.GATEMUX_SMOKE_ADMIN_KEY;
  assert(key, 'GATEMUX_SMOKE_ADMIN_KEY required');
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    await page.goto(base);
    await page.getByRole('button', { name: 'Emergency / break-glass admin key' }).click();
    await page.getByLabel('Admin master key', { exact: false }).fill(key);
    await page.getByRole('button', { name: 'Sign in as admin' }).click();
    await page.getByRole('heading', { name: 'Overview', exact: true }).waitFor();
    const teams = await page.request.get(`${base}/admin/teams?limit=1`, { headers: { Authorization: `Bearer ${key}` } });
    assert.equal(teams.status(), 200);
    const team = (await teams.json())[0];
    assert(team, 'preview needs an existing team');
    let customer = { id: 998, external_id: 'policy-fixture', name: 'Example customer', usd_limit_cents: 100, period: 'month', rpm: 10, tpm: 4000, max_parallel_requests: 3 };
    let mode = 'optional', saved, fail = false;
    const teamPath = `/admin/teams/${encodeURIComponent(team.slug)}`;
    await page.route('**/admin/**', route => {
      const request = route.request(), url = new URL(request.url());
      if (url.pathname === `${teamPath}/customer-policy`) {
        assert.equal(request.method(), 'PATCH');
        mode = request.postDataJSON().customer_registration;
        return route.fulfill({ json: { ...team, customer_registration: mode } });
      }
      if (url.pathname === teamPath) {
        assert.equal(request.method(), 'GET');
        return route.fulfill({ json: { ...team, customer_registration: mode } });
      }
      if (url.pathname.startsWith(`${teamPath}/customers`)) {
        if (fail) return route.fulfill({ status: 503, json: { error: { message: 'Fixture unavailable' } } });
        if (request.method() === 'PATCH') {
          saved = request.postDataJSON(); customer = { ...customer, ...saved };
          return route.fulfill({ json: customer });
        }
        assert.equal(request.method(), 'GET');
        if (url.pathname.endsWith('/budget')) return route.fulfill({ json: { used_cents: 12, limit_cents: 100, period: 'month' } });
        return route.fulfill({ json: [customer], headers: { 'X-Total-Count': '1' } });
      }
      if (url.pathname === '/admin/spend' && url.searchParams.has('customer')) {
        assert.equal(url.searchParams.get('team'), team.slug);
        assert.equal(url.searchParams.get('customer'), customer.external_id);
        return route.fulfill({ json: { total: { cost_cents: 9 }, by_team: [], by_user: [], by_model: [], daily: [] } });
      }
      assert.equal(request.method(), 'GET', 'unexpected preview write blocked');
      return route.continue();
    });
    await page.goto(`${base}/teams/${encodeURIComponent(team.slug)}?tab=customers`);
    await page.getByLabel('Customer registration policy', { exact: true }).selectOption('required');
    await page.getByRole('button', { name: 'Save registration policy', exact: true }).click();
    await page.getByText('Customer registration policy updated', { exact: true }).waitFor();
    assert.equal(mode, 'required');
    await page.getByRole('button', { name: 'Budget & rates', exact: true }).click();
    await page.getByText(/Budget used or reserved this month: 12¢/).waitFor();
    assert.equal(await page.getByLabel('Customer RPM', { exact: true }).inputValue(), '10');
    await page.getByLabel('Budget (cents)', { exact: true }).fill('0');
    await page.getByLabel('Customer RPM', { exact: true }).fill('');
    await page.getByRole('button', { name: 'Save customer policy', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'hidden' });
    assert.equal(saved.usd_limit_cents, 0); assert.equal(saved.rpm, null); assert.equal(saved.tpm, 4000);
    await page.getByRole('button', { name: 'Budget & rates', exact: true }).click();
    await page.getByLabel('Budget (cents)', { exact: true }).fill('');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByLabel('Customer TPM', { exact: true }).scrollIntoViewIfNeeded();
    const box = await page.getByLabel('Customer TPM', { exact: true }).boundingBox();
    assert(box && box.x >= 0 && box.x + box.width <= 390, 'mobile form overflows');
    if (process.env.GATEMUX_CUSTOMER_SCREENSHOT) {
      while (await page.getByRole('button', { name: 'Dismiss notification' }).count()) await page.getByRole('button', { name: 'Dismiss notification' }).first().click();
      await page.screenshot({ path: process.env.GATEMUX_CUSTOMER_SCREENSHOT, animations: 'disabled' });
    }
    await page.getByRole('button', { name: 'Save customer policy', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'hidden' });
    assert.equal(saved.usd_limit_cents, null);
    fail = true; await page.reload();
    await page.getByText('Customers unavailable.', { exact: true }).waitFor();
    fail = false;
    await page.getByRole('button', { name: 'Retry', exact: true }).click();
    await page.getByText('policy-fixture', { exact: true }).waitFor();
    assert.deepEqual(errors, []);
    console.log('PASS: customer registration, budgets, null/zero rates, scoped spend, mobile form and retry; preview writes intercepted');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
