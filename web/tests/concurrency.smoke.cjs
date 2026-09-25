// Run against a disposable GateMux database with GATEMUX_SMOKE_URL and
// GATEMUX_SMOKE_ADMIN_KEY. PLAYWRIGHT_MODULE can point to an existing install.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');

(async () => {
  const base = process.env.GATEMUX_SMOKE_URL || 'http://127.0.0.1:4015';
  const key = process.env.GATEMUX_SMOKE_ADMIN_KEY;
  assert(key, 'GATEMUX_SMOKE_ADMIN_KEY is required');
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const team = `smoke-${Date.now()}`;
    const created = await page.request.post(`${base}/admin/teams`, {
      headers: { Authorization: `Bearer ${key}` }, data: { slug: team, name: 'Concurrency smoke' },
    });
    assert.equal(created.status(), 201, await created.text());
    await page.goto(base);
    await page.getByRole('button', { name: 'Emergency / break-glass admin key' }).click();
    await page.getByLabel('Admin master key', { exact: false }).fill(key);
    await page.getByRole('button', { name: 'Sign in as admin' }).click();
    await page.getByRole('link', { name: 'Models', exact: true }).click();
    await page.getByRole('tab', { name: 'Concurrency', exact: true }).click();
    const form = page.locator('form').filter({ hasText: 'Set a concurrency limit' });
    await form.locator('select').selectOption('model');
    await form.locator('input').nth(0).fill(team);
    await form.locator('input').nth(1).fill('3');
    await form.getByRole('button', { name: 'Save limit' }).click();
    const modelRow = page.getByRole('row').filter({ hasText: team });
    await modelRow.waitFor();
    assert.match(await modelRow.innerText(), /3/);
    await modelRow.getByRole('button', { name: 'Edit', exact: true }).click();
    await page.getByRole('dialog').locator('input[type=number]').fill('');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await modelRow.getByText('unlimited', { exact: true }).waitFor();
    await page.goto(`${base}/teams/${team}?tab=customers`);
    const customerForm = page.locator('form').filter({ hasText: 'Register a customer' });
    await customerForm.locator('input').nth(0).fill('customer-smoke');
    await customerForm.locator('input').nth(1).fill('Customer smoke');
    await customerForm.locator('input').nth(2).fill('2');
    await customerForm.getByRole('button', { name: 'Create customer' }).click();
    const customerRow = page.getByRole('row').filter({ hasText: 'customer-smoke' });
    await customerRow.waitFor();
    assert.match(await customerRow.innerText(), /2/);
    await customerRow.getByRole('button', { name: 'Concurrency', exact: true }).click();
    await page.getByRole('dialog').locator('input[type=number]').fill('4');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await customerRow.getByRole('cell', { name: '4', exact: true }).waitFor();
    const response = await page.request.get(`${base}/admin/teams/${team}/customers`, { headers: { Authorization: `Bearer ${key}` } });
    assert.equal((await response.json())[0].max_parallel_requests, 4);
    assert.deepEqual(errors, [], 'browser page errors');
    console.log('PASS: admin login, model cap create/clear, customer create/edit, persisted API values, no page errors');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
