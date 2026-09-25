// Invoked only by TestManagerLiveBrowser, which owns an isolated DB schema.
// No route interception, auth fixtures, paid providers or writes to the preview.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const fixture = JSON.parse(fs.readFileSync(0, 'utf8'));

(async () => {
  const browser = await chromium.launch({ headless: true });
  const errors = [];
  const failed = [];
  const forbiddenLookups = [];
  let checking = true;
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    page.on('pageerror', error => errors.push(error.message));
    page.on('response', response => {
      const path = new URL(response.url()).pathname;
      if (checking && path.startsWith('/admin/') && response.status() >= 400) failed.push(`${response.status()} ${path}`);
    });
    page.on('request', request => {
      const path = new URL(request.url()).pathname;
      if (checking && (/^\/admin\/(users|aliases|pricing|projections)(\/|$)/.test(path) || /\/payload$|\/replay$|\/effective-policy$/.test(path))) forbiddenLookups.push(path);
    });
    const login = async (p, email) => {
      await p.goto(fixture.base);
      await p.getByLabel('Email', { exact: false }).fill(email);
      await p.getByLabel('Password', { exact: false }).fill(fixture.password);
      await p.getByRole('button', { name: 'Sign in', exact: true }).click();
    };
    await login(page, fixture.managerEmail);
    await page.getByRole('tablist', { name: 'Team sections', exact: true }).waitFor();
    const cookies = await context.cookies();
    assert(cookies.some(cookie => cookie.name === 'gatemux_session' && cookie.httpOnly), 'real HttpOnly session cookie');
    assert.equal(await page.evaluate(() => sessionStorage.getItem('gatemux.masterKey')), null);
    const tab = name => page.getByRole('tab', { name, exact: true });
    await tab('Members').click();
    await page.getByRole('cell', { name: fixture.memberEmail, exact: true }).waitFor();
    assert.equal(await page.getByRole('cell', { name: fixture.foreignEmail, exact: true }).count(), 0);
    await page.getByLabel('Results per page').selectOption('25');
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.getByRole('cell', { name: 'z-z@example.test', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Prev', exact: true }).click();
    await page.getByRole('cell', { name: fixture.memberEmail, exact: true }).waitFor();

    await page.getByRole('tab', { name: /^Virtual keys/ }).click();
    await page.getByRole('button', { name: 'Issue key', exact: true }).click();
    let dialog = page.getByRole('dialog');
    await dialog.getByLabel('Name', { exact: true }).fill('pilot-browser-key');
    // Owner is a server-searched picker scoped to the team's members.
    await dialog.getByLabel('Owner', { exact: true }).click();
    const memberSearch = page.waitForResponse(response => response.url().includes('/members?') && response.url().includes('q=member') && response.status() === 200);
    await dialog.getByRole('textbox', { name: 'Search', exact: true }).fill('member@example.test');
    await memberSearch;
    assert.equal(await dialog.getByRole('option', { name: new RegExp(fixture.foreignEmail) }).count(), 0);
    await dialog.getByRole('option', { name: new RegExp(fixture.memberEmail) }).click();
    // Models come from the team's allowed catalog only.
    const models = dialog.getByLabel('Allowed models', { exact: true });
    await models.fill('hidden-model');
    await dialog.getByText('No matches', { exact: true }).waitFor();
    await models.fill('allowed-model');
    await models.press('Enter');
    await dialog.getByRole('button', { name: 'Limits' }).click();
    await dialog.getByLabel('USD spend limit', { exact: true }).fill('1.25');
    await dialog.getByRole('button', { name: 'Create key', exact: true }).click();
    await page.getByRole('heading', { name: 'Save your key', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Done', exact: true }).click();
    await page.getByRole('cell', { name: 'pilot-browser-key', exact: true }).waitFor();
    await page.getByRole('cell', { name: /\$1\.25 \/ team period/ }).waitFor();
    await page.getByRole('button', { name: /^Actions for key/ }).first().click();
    await page.getByRole('menuitem', { name: 'Budget…', exact: true }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByText(/Used or reserved this month: \$0\.00/).waitFor();
    await dialog.getByLabel('USD spend limit', { exact: true }).fill('2.50');
    const savedBudget = page.waitForResponse(response => /\/keys\/\d+\/budget$/.test(response.url()) && response.request().method() === 'PATCH');
    await dialog.getByRole('button', { name: 'Save key budget', exact: true }).click();
    const budgetResponse = await savedBudget;
    assert.equal(budgetResponse.status(), 204);
    assert.deepEqual(budgetResponse.request().postDataJSON(), { usd_limit_cents: 250 });
    await page.getByRole('cell', { name: /\$2\.50 \/ team period/ }).waitFor();
    await page.getByRole('button', { name: 'Edit', exact: true }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByLabel('USD spend limit', { exact: true }).fill('3.75');
    await dialog.getByRole('button', { name: 'Save changes', exact: true }).click();
    await page.getByRole('cell', { name: /\$3\.75 \/ team period/ }).waitFor();
    await page.getByRole('button', { name: /^Actions for key/ }).first().click();
    await page.getByRole('menuitem', { name: 'Budget…', exact: true }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByLabel('USD spend limit', { exact: true }).fill('');
    await dialog.getByRole('button', { name: 'Save key budget', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    const budgetKeys = await (await page.request.get(`${fixture.base}/admin/teams/${fixture.team}/keys`)).json();
    const budgetKey = budgetKeys.find(key => key.name === 'pilot-browser-key');
    assert.equal(budgetKey.usd_limit_cents, undefined, 'clear key cap');
    assert.deepEqual(budgetKey.allowed_models, ['allowed-model'], 'budget edit preserves allowlist');
    assert.equal(budgetKey.user_id, fixture.memberID, 'budget edit preserves owner');
    // Row actions live in the key's ⋯ menu; managers never see the
    // administrator-only effective-policy view.
    await page.getByRole('button', { name: /^Actions for key/ }).first().click();
    assert.equal(await page.getByRole('menuitem', { name: 'Effective policy…', exact: true }).count(), 0);
    await page.getByRole('menuitem', { name: 'Rotate…', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Rotate', exact: true }).click();
    await page.getByRole('heading', { name: 'Save your key', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Done', exact: true }).click();
    await page.getByRole('button', { name: /^Actions for key/ }).first().click();
    await page.getByRole('menuitem', { name: 'Revoke…', exact: true }).click();
    const revoked = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/revoke'));
    await page.getByRole('dialog').getByRole('button', { name: 'Revoke', exact: true }).click();
    assert.ok((await revoked).ok(), 'revoke request succeeds');
    await page.getByRole('dialog').waitFor({ state: 'hidden' });
    const afterRevoke = await (await page.request.get(`${fixture.base}/admin/teams/${fixture.team}/keys`)).json();
    assert.equal(afterRevoke.filter(key => key.name === 'pilot-browser-key' && !key.revoked_at).length, 0, 'rotated and revoked');

    await tab('Customers').click();
    await page.getByLabel('Customer ID', { exact: true }).fill('pilot-customer');
    await page.getByLabel('Name', { exact: true }).fill('Pilot customer');
    await page.getByRole('button', { name: 'Create customer', exact: true }).click();
    await page.getByRole('cell', { name: 'pilot-customer', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Budget & rates', exact: true }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByText(/Budget used or reserved/).waitFor();
    await dialog.getByLabel('Budget (cents)', { exact: true }).fill('500');
    await dialog.getByLabel('Customer RPM', { exact: true }).fill('20');
    await dialog.getByRole('button', { name: 'Save customer policy', exact: true }).click();
    await page.getByRole('cell', { name: '$5.00 / month', exact: true }).waitFor();
    await page.getByLabel('Customer registration policy', { exact: true }).selectOption('required');
    const savePolicy = page.waitForResponse(response => response.url().endsWith('/customer-policy') && response.request().method() === 'PATCH');
    await page.getByRole('button', { name: 'Save registration policy', exact: true }).click();
    assert.equal((await savePolicy).status(), 200);

    await tab('Service accounts').click();
    await page.getByRole('cell', { name: 'pilot-ci', exact: true }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'Archive', exact: true }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'Issue key', exact: true }).count(), 0);
    await tab('Data privacy').click();
    await page.getByText('Managed by an administrator', { exact: true }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'Enable', exact: true }).count(), 0);

    await page.goto(`${fixture.base}/spend`);
    await page.getByRole('heading', { name: 'Spend & usage', exact: true }).waitFor();
    await page.getByRole('cell', { name: '$0.07', exact: true }).waitFor();
    assert.equal(await page.getByRole('cell', { name: 'hidden-model', exact: true }).count(), 0);
    assert.equal(await page.getByLabel('Team filter').isDisabled(), true);
    assert.equal(await page.getByRole('heading', { name: 'Pricing', exact: true }).count(), 0);
    await page.goto(`${fixture.base}/usage`);
    await page.getByRole('cell', { name: 'allowed-model', exact: true }).waitFor();
    await page.getByRole('cell', { name: 'allowed-model', exact: true }).click();
    await page.getByRole('dialog').waitFor();
    await page.getByText('Captured payloads and replay are available only to administrators.', { exact: true }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'Replay', exact: true }).count(), 0);
    await page.goto(`${fixture.base}/playground`);
    await page.waitForFunction(() => document.querySelector('#playground-models option')?.value === 'allowed-model');
    assert.deepEqual(await page.locator('#playground-models option').evaluateAll(options => options.map(option => option.value)), ['allowed-model']);
    await page.goto(`${fixture.base}/invites`);
    await page.getByRole('heading', { name: 'Invitations', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Send invite', exact: true }).click();
    await page.getByText(/Managers can only invite members into their own team/).waitFor();
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click();
    assert.deepEqual(failed, [], 'manager workflows must not call denied APIs');
    assert.deepEqual(forbiddenLookups, [], 'manager UI must not request administrator data');

    // Negative checks use the same actual browser session, not an admin token.
    checking = false;
    for (const path of ['/admin/users', '/admin/pricing', '/admin/aliases', `/admin/teams/${fixture.foreignTeam}/members`, `/admin/teams/${fixture.foreignTeam}/models`]) {
      assert.equal((await page.request.get(fixture.base + path)).status(), 403, `manager denial: ${path}`);
    }
    const keys = await (await page.request.get(`${fixture.base}/admin/teams/${fixture.team}/keys`)).json();
    const usage = await (await page.request.get(`${fixture.base}/admin/usage`)).json();
    assert.equal(usage.length, 1, 'real request metadata remains team-scoped');
    assert.equal(usage[0].has_payload, true, 'fixture must exercise captured-payload restrictions');
    for (const path of [`/admin/keys/${keys[0].id}/effective-policy`, `/admin/usage/${usage[0].id}/payload`, `/admin/spend?team=${fixture.foreignTeam}`]) {
      assert.equal((await page.request.get(fixture.base + path)).status(), 403, `manager denial: ${path}`);
    }
    assert.equal((await page.request.post(`${fixture.base}/admin/usage/${usage[0].id}/replay`)).status(), 403);
    assert.equal((await page.request.patch(`${fixture.base}/admin/teams/${fixture.team}/capture-payloads`, { data: { capture_payloads: true } })).status(), 403);
    await page.goto(`${fixture.base}/teams/${fixture.foreignTeam}`);
    await page.getByRole('heading', { name: 'Team unavailable', exact: true }).waitFor();
    assert.equal(await page.getByRole('tablist', { name: 'Team sections', exact: true }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'Retry', exact: true }).count(), 1);
    await page.goto(`${fixture.base}/teams/${fixture.team}?tab=members`);
    await page.getByRole('cell', { name: fixture.memberEmail, exact: true }).waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'mobile team page overflow');
    if (process.env.GATEMUX_MANAGER_SCREENSHOT) await page.screenshot({ path: process.env.GATEMUX_MANAGER_SCREENSHOT, fullPage: true, animations: 'disabled' });

    const memberContext = await browser.newContext();
    const memberPage = await memberContext.newPage();
    await login(memberPage, fixture.memberEmail);
    await memberPage.waitForURL('**/account');
    for (const suffix of ['members', 'models', 'keys', 'customers']) {
      assert.equal((await memberPage.request.get(`${fixture.base}/admin/teams/${fixture.team}/${suffix}`)).status(), 403, `member denial: ${suffix}`);
    }
    assert.equal((await memberPage.request.get(`${fixture.base}/admin/keys/${budgetKey.id}/budget`)).status(), 403);
    assert.equal((await memberPage.request.patch(`${fixture.base}/admin/keys/${budgetKey.id}/budget`, { data: { usd_limit_cents: null } })).status(), 403);
    await memberPage.goto(`${fixture.base}/playground`);
    await memberPage.getByLabel('Alias', { exact: false }).fill('allowed-model');
    assert.equal(await memberPage.locator('#playground-models option').count(), 0, 'members have manual alias entry, not an admin catalog');
    await memberContext.close();
    assert.deepEqual(errors, [], 'page errors');
    console.log('PASS: real manager/member logins, scoped directory/models, key issue/budget-create/edit/clear/rotate/revoke, customer writes, spend, request details, privacy/SA restrictions, playground, invites, role/tenant denials and mobile layout. No API interception or provider calls.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
