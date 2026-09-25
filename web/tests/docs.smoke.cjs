// Read-only preview checks; inference is intercepted and never reaches a provider.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
(async () => {
  const base = process.env.GATEMUX_SMOKE_URL || 'http://localhost:4001';
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [], remote = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('request', r => { if (new URL(r.url()).origin !== new URL(base).origin) remote.push(r.url()); });
    await page.goto(`${base}/docs`);
    await page.getByRole('button', { name: 'GET /v1/models', exact: true }).click();
    await page.getByRole('button', { name: 'Send request', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('#status').textContent.includes('401'));
    await page.getByLabel('Find an endpoint').fill('/v1/responses');
    await page.getByRole('button', { name: 'POST /v1/responses', exact: true }).click();
    let sent = 0;
    await page.route('**/v1/responses', route => {
      sent++;
      return route.fulfill({ contentType: 'text/event-stream', body: 'event: response.completed\ndata: {"type":"response.completed"}\n\n' });
    });
    await page.getByRole('button', { name: 'Send request', exact: true }).click();
    await page.getByRole('status').filter({ hasText: 'Confirm the possible data change' }).waitFor();
    assert.equal(sent, 0);
    await page.getByLabel('I understand this request').check();
    await page.getByLabel('Request path and query').fill('//example.invalid/v1/responses');
    await page.getByRole('button', { name: 'Send request', exact: true }).click();
    await page.getByRole('status').filter({ hasText: 'Enter a same-origin path' }).waitFor();
    assert.equal(sent, 0);
    await page.getByLabel('Request path and query').fill('/v1/responses');
    await page.getByRole('button', { name: 'Send request', exact: true }).click();
    await page.locator('#output').filter({ hasText: 'response.completed' }).waitFor();
    assert.equal(sent, 1);
    await page.setViewportSize({ width: 390, height: 844 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'mobile overflow');
    await page.reload();
    assert.equal(await page.getByLabel('Bearer key').inputValue(), '');
    assert.deepEqual(remote, []);
    assert.deepEqual(errors, []);
    console.log('PASS: offline explorer, schema loading, auth, mutation confirmation, same-origin protection, SSE display and mobile; inference intercepted');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
