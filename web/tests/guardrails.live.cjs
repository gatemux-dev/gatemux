// Run only by the Go fixture with an isolated disposable schema. Real APIs.
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
    await page.goto(`${fixture.base}/guardrails`);
    // Known teams and aliases load into a picker; choosing one loads its policies.
    await page.locator('select#guard-subject').waitFor();
    await page.getByLabel('Team', { exact: true }).selectOption(fixture.team);
    await page.getByLabel('Rule name', { exact: true }).fill('pilot-text');
    await page.getByLabel('Literal terms (one per line)', { exact: true }).fill('secret\nprivate');
    await page.getByRole('button', { name: 'Add rule to draft', exact: true }).click();
    const save = async () => {
      await page.getByRole('button', { name: 'Save policies', exact: true }).click();
      const response = page.waitForResponse(r => r.request().method() === 'PUT' && r.url().includes('/admin/guardrails/'));
      await page.getByRole('dialog').getByRole('button', { name: 'Save policies', exact: true }).click();
      assert.equal((await response).status(), 200);
      await page.getByRole('status').getByText(/Policies saved and audited/).waitFor();
    };
    await save();
    await page.getByRole('button', { name: 'Edit pilot-text', exact: true }).click();
    await page.getByLabel('Action', { exact: true }).selectOption('redact');
    await page.getByRole('button', { name: 'Update draft rule', exact: true }).click();
    await save();
    await page.reload();
    await page.getByRole('button', { name: 'Edit', exact: true }).click();
    await page.getByText('pilot-text', { exact: true }).first().waitFor();
    await page.getByRole('button', { name: 'Remove pilot-text', exact: true }).click();
    await save();
    // Same workflow supports model assignment, with no global/team override.
    await page.getByLabel('Scope', { exact: true }).selectOption('alias');
    await page.getByLabel('Model alias', { exact: true }).selectOption('allowed-model');
    await page.getByLabel('Rule name', { exact: true }).fill('model-terms');
    await page.getByLabel('Action', { exact: true }).selectOption('flag');
    await page.getByLabel('Inspection phase', { exact: true }).selectOption('post');
    await page.getByLabel('Literal terms (one per line)', { exact: true }).fill('internal');
    await page.getByRole('button', { name: 'Add rule to draft', exact: true }).click();
    await save();
    await page.setViewportSize({ width: 390, height: 844 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'guardrail mobile overflow');
    await page.screenshot({ path: '/tmp/gatemux-guardrails-mobile.png', fullPage: true });
    assert.deepEqual(errors, []);
    console.log('PASS: real master login, guardrail create/edit/delete, team and model assignment, persisted reload, confirmation, mobile bounds.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
