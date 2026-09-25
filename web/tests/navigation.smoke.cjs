// Read-only browser checks against a running preview. No teams, keys, or
// policies are created. Role/edge-case checks use intercepted fixtures.
// PLAYWRIGHT_MODULE may point to an existing Playwright installation.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

(async () => {
  const base = process.env.GATEMUX_SMOKE_URL || 'http://localhost:4001';
  const key = process.env.GATEMUX_SMOKE_ADMIN_KEY;
  assert(key, 'GATEMUX_SMOKE_ADMIN_KEY is required');
  const output = process.env.GATEMUX_SCREENSHOT_DIR;
  if (output) fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1050 } });
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const capture = async name => { if (output) await page.screenshot({ path: path.join(output, `${name}.png`), fullPage: true }); };
    const primary = () => page.locator('nav[aria-label="Primary"]:visible');
    await page.goto(base);
    await page.getByRole('button', { name: 'Emergency / break-glass admin key' }).click();
    await page.getByLabel('Admin master key', { exact: false }).fill(key);
    await page.getByRole('button', { name: 'Sign in as admin' }).click();
    await page.getByRole('heading', { name: 'Overview', exact: true }).waitFor();
    await page.getByText('Connected', { exact: true }).first().waitFor();
    // The sidebar groups pages by job: an unlabeled observe block, then
    // Manage, Policies and System. Provider health and passthroughs are
    // tabs inside Settings.
    for (const group of ['Manage', 'Policies', 'System']) {
      assert.equal(await primary().getByRole('heading', { name: group, exact: true }).count(), 1);
    }
    assert.equal(await primary().getByRole('link').count(), 13, 'sidebar entries');
    await capture('overview-desktop');
    const destinations = [
      ['Logs', 'Request logs'], ['Usage & spend', 'Spend & usage'],
      ['Models', 'Models'], ['Teams', 'Teams'], ['Users', 'Users'],
      ['Guardrails', 'Guardrails'], ['Limits', 'Limits'], ['Alerts', 'Alerts'],
      ['Audit log', 'Audit log'],
      ['Settings', 'Gateway settings'], ['Overview', 'Overview'],
    ];
    for (const [link, title] of destinations) {
      await primary().getByRole('link', { name: link, exact: true }).click();
      await page.getByRole('heading', { name: title, exact: true, level: 1 }).waitFor();
      assert.equal(await primary().locator('[aria-current="page"]').count(), 1, `active route: ${link}`);
    }
    // System configuration sub-areas are a tab strip inside Settings; the
    // sidebar keeps Settings lit while inside any of them.
    await page.goto(`${base}/settings`);
    const settingsTabs = page.locator('nav[aria-label="Settings sections"]');
    const sections = [
      ['Providers', 'Provider health'], ['Passthroughs', 'Passthrough routes'],
      ['Runtime', 'Gateway settings'],
    ];
    for (const [tab, title] of sections) {
      await settingsTabs.getByRole('link', { name: tab, exact: true }).click();
      await page.getByRole('heading', { name: title, exact: true, level: 1 }).waitFor();
      const active = primary().locator('[aria-current="page"]');
      assert.equal(await active.count(), 1, `sidebar current inside ${tab}`);
      assert.equal((await active.textContent()).trim(), 'Settings', `Settings stays lit inside ${tab}`);
    }
    // Invitations are a Users tab: the Invite user action opens the invite
    // dialog there, and the old /invites address redirects to the tab.
    await page.goto(`${base}/users`);
    await page.getByRole('link', { name: 'Invite user', exact: true }).click();
    await page.getByRole('dialog').waitFor();
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click();
    assert.equal(await page.getByRole('tab', { name: 'Invitations', exact: true }).getAttribute('aria-selected'), 'true');
    assert.equal((await primary().locator('[aria-current="page"]').textContent()).trim(), 'Users');
    await page.goto(`${base}/invites`);
    await page.waitForURL(url => url.pathname === '/users' && url.searchParams.get('tab') === 'invites');
    await page.goto(`${base}/settings`);
    assert.equal(await page.getByRole('tablist').count(), 0, 'Settings must not contain unrelated tabs');
    await page.getByText('Instance configuration · read-only', { exact: true }).waitFor();
    await capture('settings-desktop');
    await page.goto(`${base}/settings?tab=guardrails`);
    await page.waitForURL('**/guardrails');
    await page.getByText('Literal-term policies (beta)', { exact: true }).waitFor();
    assert.equal(await page.getByRole('link', { name: 'Edit', exact: true }).count(), 0);
    await capture('guardrails-desktop');
    // Import is preview-only here: malformed/provider-less data must not enable
    // writes, and valid unmatched prices must leave the deployment list alone.
    await page.goto(`${base}/models?tab=pricing`);
    await page.getByRole('button', { name: 'Import prices', exact: true }).click();
    const priceDialog = page.getByRole('dialog');
    await priceDialog.getByText(/GateMux JSON price list/).waitFor();
    await priceDialog.locator('textarea').fill('{"fixture-unmatched-model":{"input_cost_per_token":0,"output_cost_per_token":0}}');
    await priceDialog.getByRole('alert').getByText(/requires a provider field/).waitFor();
    assert.equal(await priceDialog.getByRole('button', { name: 'Nothing to import' }).isDisabled(), true);
    await priceDialog.locator('textarea').fill('{"fixture-unmatched-model":{"provider":"openai","input_cost_per_token":0,"output_cost_per_token":0}}');
    await priceDialog.getByText('No deployment matches a model in this list.').waitFor();
    assert.equal(await priceDialog.getByRole('alert').count(), 0);
    assert.equal(await priceDialog.getByRole('button', { name: 'Nothing to import' }).isDisabled(), true);
    await priceDialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    await page.goto(`${base}/models?tab=deployments`);
    const deploymentTab = page.getByRole('tab', { name: /^Deployments/ });
    assert.equal(await deploymentTab.getAttribute('aria-selected'), 'true');
    await deploymentTab.press('ArrowRight');
    await page.waitForURL('**/models?tab=capabilities');
    await page.reload();
    assert.equal(await page.getByRole('tab', { name: 'Capabilities', exact: true }).getAttribute('aria-selected'), 'true');
    await page.goBack();
    await page.waitForURL('**/models?tab=deployments');
    await page.waitForFunction(() => document.querySelector('[role="tab"][aria-selected="true"]')?.textContent?.startsWith('Deployments'));
    assert.equal(await deploymentTab.getAttribute('aria-selected'), 'true');
    await page.goForward();
    assert.equal(await page.getByRole('tab', { name: 'Capabilities', exact: true }).getAttribute('aria-selected'), 'true');

    const teams = await page.request.get(`${base}/admin/teams?limit=1`, { headers: { Authorization: `Bearer ${key}` } });
    assert.equal(teams.status(), 200);
    const team = (await teams.json())[0];
    if (team) {
      await page.goto(`${base}/teams/${encodeURIComponent(team.slug)}`);
      await page.getByRole('heading', { name: / this team$/ }).waitFor();
      await page.getByRole('tab', { name: 'Budget & limits', exact: true }).click();
      await page.getByRole('heading', { name: 'Spending budget', exact: true }).waitFor();
      await page.reload();
      assert.equal(await page.getByRole('tab', { name: 'Budget & limits', exact: true }).getAttribute('aria-selected'), 'true');
      await capture('team-limits-desktop');
      await page.getByRole('button', { name: 'Edit budget', exact: true }).click();
      await page.getByRole('dialog').waitFor();
      await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click();
      await page.getByRole('tab', { name: 'Data privacy', exact: true }).click();
      await page.goBack();
      assert.equal(await page.getByRole('tab', { name: 'Budget & limits', exact: true }).getAttribute('aria-selected'), 'true');
    }

    await page.goto(`${base}/guardrails`);
    await page.locator('.desktop-sidebar').getByRole('button', { name: 'Toggle theme' }).click();
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark'
      && getComputedStyle(document.querySelector('.desktop-sidebar .navlink')).color === 'rgb(162, 169, 182)');
    await capture('guardrails-dark');
    await page.locator('.desktop-sidebar').getByRole('button', { name: 'Toggle theme' }).click();
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'light'
      && getComputedStyle(document.querySelector('.desktop-sidebar .navlink')).color === 'rgb(81, 91, 109)');
    for (const width of [390, 768, 1000]) {
      await page.setViewportSize({ width, height: 844 });
      await page.goto(`${base}/overview`);
      await page.getByRole('heading', { name: 'Overview', exact: true }).waitFor();
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), `overflow at ${width}px`);
      await page.getByRole('button', { name: 'Open menu', exact: true }).click();
      await page.getByRole('dialog', { name: 'Navigation menu', exact: true }).waitFor();
      assert.equal(await primary().getByRole('link', { name: 'Settings', exact: true }).count(), 1);
      if (width === 390) await capture('mobile-navigation');
      await page.keyboard.press('Escape');
      assert.equal(await page.getByRole('button', { name: 'Open menu' }).getAttribute('aria-expanded'), 'false');
      await page.getByRole('button', { name: 'Open menu' }).click();
      await primary().getByRole('link', { name: 'Settings', exact: true }).click();
      await page.getByRole('heading', { name: 'Gateway settings', level: 1, exact: true }).waitFor();
      assert.equal(await page.getByRole('dialog', { name: 'Navigation menu' }).count(), 0);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), `settings overflow at ${width}px`);
      if (width === 390) await capture('settings-mobile');
    }

    // A failed catalog must not display an endless loading skeleton.
    await page.route('**/admin/guardrails', route => route.fulfill({ status: 500, json: { error: { message: 'Fixture failure' } } }));
    await page.goto(`${base}/guardrails`);
    await page.getByText('Unable to load the guardrail catalog. Use Refresh to retry.').waitFor();
    await page.unroute('**/admin/guardrails');
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await page.getByText('Unable to load the guardrail catalog. Use Refresh to retry.').waitFor({ state: 'hidden' });

    // UI-only role fixtures: validate navigation without creating accounts or
    // impersonating a real user. Backend authorization is tested by Go suites.
    for (const role of ['manager', 'member']) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
      const user = { id: 999, email: 'navigation@example.test', name: 'Navigation fixture', role, is_admin: false, team_slug: 'nav-team' };
      await context.route('**/auth/me', route => route.fulfill({ json: { user, is_master_key: false } }));
      await context.route('**/me/sessions', route => route.fulfill({ json: [] }));
      await context.route('**/me/budget', route => route.fulfill({ json: { used_cents: 0, period: 'month' } }));
      await context.route('**/admin/**', route => {
        const pathname = new URL(route.request().url()).pathname;
        return route.fulfill({ json: pathname === '/admin/teams/nav-team'
          ? { id: 999, slug: 'nav-team', name: 'Navigation fixture', period: 'month' }
          : [] });
      });
      const rolePage = await context.newPage();
      rolePage.on('pageerror', error => errors.push(`${role}: ${error.message}`));
      await rolePage.goto(`${base}/guardrails`);
      await rolePage.waitForURL(role === 'manager' ? '**/teams/nav-team' : '**/account');
      const nav = rolePage.locator('nav[aria-label="Primary"]:visible');
      await nav.waitFor();
      assert.equal(await nav.getByRole('link', { name: 'Guardrails', exact: true }).count(), 0);
      assert.equal(await nav.getByRole('link', { name: 'Settings', exact: true }).count(), 0);
      assert.equal(await nav.getByRole('link', { name: role === 'manager' ? 'My team' : 'My keys', exact: true }).count(), 1);
      await context.close();
    }
    assert.deepEqual(errors, [], 'browser page errors');
    console.log('PASS: live admin routes, URL tabs/history/keyboard, team policy dialogs, guardrail error/retry, mobile 390/768/1000px, role fixtures, no page errors');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
