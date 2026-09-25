// Read-only against preview: writes are intercepted and never reach its DB.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');

(async () => {
  const base = process.env.GATEMUX_SMOKE_URL || 'http://localhost:4001';
  const key = process.env.GATEMUX_SMOKE_ADMIN_KEY;
  assert(key, 'GATEMUX_SMOKE_ADMIN_KEY required');
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1050 } });
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    await page.goto(base);
    await page.getByRole('button', { name: 'Emergency / break-glass admin key' }).click();
    await page.getByLabel('Admin master key', { exact: false }).fill(key);
    await page.getByRole('button', { name: 'Sign in as admin' }).click();
    await page.getByRole('heading', { name: 'Overview', exact: true }).waitFor();
    let policy = { idle_timeout: '45s', first_event_timeout: '60s', write_timeout: '10s', keepalive_interval: '15s' };
    let patch;
    const deployment = () => ({ name: 'stream-fixture', provider_type: 'openai_compatible', upstream_model: 'test', credential_ref: '', enabled: true, has_credential: true, streaming: policy, capabilities: { chat: true, stream_chat: true, embeddings: false } });
    await page.route('**/admin/deployments**', async route => {
      if (route.request().method() === 'PATCH') {
        patch = route.request().postDataJSON();
        policy = patch.streaming;
        return route.fulfill({ json: deployment() });
      }
      if (route.request().method() !== 'GET') throw new Error('Unexpected deployment mutation');
      return route.fulfill({ json: [deployment()], headers: { 'X-Total-Count': '1' } });
    });
    await page.goto(`${base}/models?tab=deployments`);
    await page.getByRole('row').filter({ hasText: 'stream-fixture' }).getByRole('button', { name: 'Edit' }).click();
    let dialog = page.getByRole('dialog');
    assert.equal(await dialog.getByLabel('Upstream idle timeout', { exact: true }).inputValue(), '45s');
    await dialog.getByLabel('Upstream idle timeout', { exact: true }).fill(' 250ms ');
    await dialog.getByLabel('SSE keepalive interval', { exact: true }).fill('');
    await dialog.getByRole('button', { name: 'Save changes', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    assert.equal(patch.streaming.idle_timeout, '250ms');
    assert.equal(patch.streaming.keepalive_interval, undefined);
    await page.getByRole('row').filter({ hasText: 'stream-fixture' }).getByRole('button', { name: 'Edit' }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByRole('checkbox', { name: 'Override gateway defaults for this deployment' }).uncheck();
    await dialog.getByRole('button', { name: 'Save changes', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    assert.equal(patch.streaming, null);
    for (const width of [390, 1440]) {
      await page.setViewportSize({ width, height: 844 });
      await page.getByRole('button', { name: 'Add deployment', exact: true }).click();
      dialog = page.getByRole('dialog');
      await dialog.getByRole('button', { name: /^Advanced/ }).click();
      await dialog.getByRole('checkbox', { name: 'Override gateway defaults for this deployment' }).check();
      await dialog.getByLabel('First event / byte timeout', { exact: true }).fill('30s');
      await dialog.getByLabel('SSE keepalive interval', { exact: true }).fill('10s');
      const box = await dialog.getByLabel('SSE keepalive interval', { exact: true }).boundingBox();
      assert(box && box.x >= 0 && box.x + box.width <= width, 'policy input overflows');
      await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    }
    assert.deepEqual(errors, []);
    console.log('PASS: deployment streaming edit, normalized durations, inheritance clear, mobile/desktop inputs; all writes intercepted');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
