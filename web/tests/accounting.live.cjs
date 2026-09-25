// Real API/UI reads against the Go fixture's private disposable schema.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fixture = JSON.parse(require('node:fs').readFileSync(0, 'utf8'));
(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } });
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(fixture.base);
    await page.getByRole('button', { name: 'Emergency / break-glass admin key', exact: true }).click();
    await page.getByLabel('Admin master key', { exact: false }).fill(fixture.masterKey);
    await page.getByRole('button', { name: 'Sign in as admin', exact: true }).click();
    await page.getByRole('heading', { name: 'Overview', exact: true }).waitFor();
    await page.goto(`${fixture.base}/usage`);
    for (const [state, value] of [['unknown', 'Unknown'], ['unpriced', 'Unpriced'], ['estimated', '$1.2300'], ['priced', '$0.0000'], ['not_billable', '$0.0000']]) {
      const row = page.getByRole('row').filter({ has: page.getByRole('cell', { name: `evidence-${state}`, exact: true }) });
      await row.waitFor();
      assert((await row.innerText()).includes(value), `missing ${state} cost evidence`);
      if (state === 'unknown' || state === 'unpriced') assert(!(await row.innerText()).includes('$0.'), 'unknown cost displayed as free');
      await row.click();
      const drawer = page.getByRole('dialog');
      const detail = drawer.locator('section').filter({ has: page.getByText('Accounting evidence', { exact: true }) });
      await detail.getByText(state, { exact: true }).waitFor();
      if (['unknown', 'unpriced', 'estimated'].includes(state)) await detail.getByText(/not a verified provider invoice amount/).waitFor();
      await page.keyboard.press('Escape');
      await drawer.waitFor({ state: 'hidden' });
    }
    const csvResponse = await page.request.get(`${fixture.base}/admin/export/usage.csv?team=${fixture.team}`, { headers: { Authorization: `Bearer ${fixture.masterKey}` } });
    assert.equal(csvResponse.status(), 200);
    const csv = await csvResponse.text();
    assert(csv.split('\n')[0].includes('accounting_state'));
    for (const state of ['estimated', 'unknown', 'unpriced']) assert(csv.includes(`,${state}`));
    await page.goto(`${fixture.base}/spend`);
    await page.getByText(/Recorded totals include conservative estimates/).waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'accounting mobile overflow');
    await page.screenshot({ path: '/tmp/gatemux-accounting-mobile.png', fullPage: true });
    assert.deepEqual(errors, []);
    console.log('PASS: real usage evidence states, no false free-cost labels, detail warnings, CSV evidence, spend caveat and mobile layout.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
