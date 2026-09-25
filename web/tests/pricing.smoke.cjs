// Read-only against the preview: every pricing write is intercepted.
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
    let price = { provider_type: 'openai', upstream_model: 'tier-fixture', input_per_million_cents: 100, output_per_million_cents: 200, cache_read_per_million_cents: 0, cache_write_per_million_cents: 125, cache_write_1h_per_million_cents: 200, reasoning_per_million_cents: 400, effective_at: '2026-09-07T00:00:00Z' };
    let saved;
    await page.route('**/admin/pricing**', route => {
      if (route.request().method() === 'POST') {
        saved = route.request().postDataJSON();
        price = { ...price, ...saved };
        return route.fulfill({ status: 201, json: price });
      }
      assert.equal(route.request().method(), 'GET');
      return route.fulfill({ json: [price], headers: { 'X-Total-Count': '1' } });
    });
    await page.goto(`${base}/models?tab=pricing`);
    await page.getByRole('button', { name: 'Edit pricing openai/tier-fixture', exact: true }).click();
    assert.equal(await page.getByLabel('Cache read ¢/M', { exact: true }).inputValue(), '0');
    assert.equal(await page.getByLabel('Cache write / 1h ¢/M', { exact: true }).inputValue(), '200');
    await page.getByLabel('Cache read ¢/M', { exact: true }).fill('');
    await page.getByLabel('Cache write / 1h ¢/M', { exact: true }).fill('0');
    await page.getByRole('button', { name: 'Save pricing', exact: true }).click();
    await page.getByText('Pricing saved', { exact: true }).waitFor();
    // The editor is a modal now and closes on save.
    await page.getByRole('dialog').waitFor({ state: 'hidden' });
    assert.equal(saved.cache_read_per_million_cents, null);
    assert.equal(saved.cache_write_1h_per_million_cents, 0);
    assert.equal(saved.cache_write_per_million_cents, 125);
    assert.equal(saved.reasoning_per_million_cents, 400);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('button', { name: 'Edit pricing openai/tier-fixture', exact: true }).click();
    await page.getByLabel('Reasoning ¢/M', { exact: true }).scrollIntoViewIfNeeded();
    const box = await page.getByLabel('Reasoning ¢/M', { exact: true }).boundingBox();
    assert(box && box.x >= 0 && box.x + box.width <= 390, 'tier input overflows mobile viewport');
    assert.deepEqual(errors, []);
    console.log('PASS: tier edit, explicit zero versus inheritance, preserved rates and mobile form; writes intercepted');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
