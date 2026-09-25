// Deterministic tests of the actual TypeScript HTTP client; no emitted files.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const ts = require('typescript');

function authFixture(session = {}, local = {}) {
  const storage = values => ({
    getItem: k => values[k] ?? null,
    setItem: (k, v) => { values[k] = v; },
    removeItem: k => { delete values[k]; },
  });
  const source = fs.readFileSync(path.join(__dirname, '../src/auth.ts'), 'utf8');
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const exports = {};
  vm.runInNewContext(compiled, {
    exports,
    window: { sessionStorage: storage(session), localStorage: storage(local), dispatchEvent() {} },
    CustomEvent: class {},
  });
  return { auth: exports, session, local };
}

test('rename moves the old master key once into session storage and logout removes it', () => {
  const { auth, session, local } = authFixture({ 'aiport.masterKey': 'fixture-old' });
  assert.equal(auth.getMasterKey(), 'fixture-old');
  assert.deepEqual(session, { 'gatemux.masterKey': 'fixture-old' });
  assert.deepEqual(local, {});
  auth.clearSession();
  assert.deepEqual(session, {});
});

test('rename retains a newer master key and clears old persistent credentials', () => {
  const { auth, session, local } = authFixture(
    { 'gatemux.masterKey': 'fixture-new', 'aiport.masterKey': 'fixture-old' },
    { 'aiport.adminKey': 'fixture-older', 'aiport.token': 'fixture-token', 'aiport.user': '{}' },
  );
  assert.equal(auth.getMasterKey(), 'fixture-new');
  assert.deepEqual(session, { 'gatemux.masterKey': 'fixture-new' });
  assert.deepEqual(local, {});
});

test('rename does not promote a legacy account token into a master key', () => {
  const { auth, local, session } = authFixture({}, { 'aiport.token': 'account-token', 'aiport.user': '{"role":"member"}' });
  assert.equal(auth.loadPrincipalSync(), null);
  assert.deepEqual(local, {});
  assert.deepEqual(session, {});
});

function clientFixture(key = null) {
  const state = { key, cleared: 0, requests: [], respond: async () => Response.json({}) };
  const source = fs.readFileSync(path.join(__dirname, '../src/api/client.ts'), 'utf8');
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const exports = {};
  vm.runInNewContext(compiled, {
    exports, Headers, URLSearchParams,
    require(name) {
      assert.equal(name, '../auth');
      return { getMasterKey: () => state.key, clearSession: () => { state.key = null; state.cleared++; } };
    },
    fetch: async (url, init) => { state.requests.push({ url, init }); return state.respond(); },
  });
  return { state, api: exports.api };
}

test('master-key validation omits cookies and does not persist a rejected key', async () => {
  const { state, api } = clientFixture();
  state.respond = async () => Response.json({ error: { message: 'invalid or expired session' } }, { status: 401 });
  await assert.rejects(api.verifyMasterKey('invalid-fixture'), error => error.status === 401);
  assert.equal(state.key, null);
  assert.equal(state.cleared, 0);
  assert.equal(state.requests[0].init.credentials, 'omit');
  assert.equal(state.requests[0].init.headers.get('Authorization'), 'Bearer invalid-fixture');
});

test('object and paginated master-key requests omit account cookies', async () => {
  const { state, api } = clientFixture('fixture-master');
  state.respond = async () => Response.json({ is_master_key: true });
  await api.whoami();
  state.respond = async () => Response.json([], { headers: { 'X-Total-Count': '0' } });
  await api.listTeams();
  for (const { init } of state.requests) {
    assert.equal(init.credentials, 'omit');
    assert.equal(init.headers.get('Authorization'), 'Bearer fixture-master');
  }
});

test('ordinary account requests retain HttpOnly cookie authentication', async () => {
  const { state, api } = clientFixture();
  state.respond = async () => Response.json({ user: { id: 1 } });
  await api.whoami();
  state.respond = async () => Response.json([]);
  await api.listTeams();
  for (const { init } of state.requests) {
    assert.equal(init.credentials, 'same-origin');
    assert.equal(init.headers.has('Authorization'), false);
  }
});

for (const operation of ['whoami', 'listTeams']) {
  test(`${operation}: late cookie rejection cannot clear a newer master-key login`, async () => {
    const { state, api } = clientFixture();
    let finish;
    state.respond = () => new Promise(resolve => { finish = resolve; });
    const pending = api[operation]();
    state.key = 'new-fixture-master';
    finish(Response.json({ error: { message: 'invalid session' } }, { status: 401 }));
    await assert.rejects(pending, error => error.status === 401);
    assert.equal(state.key, 'new-fixture-master');
    assert.equal(state.cleared, 0);
  });

  test(`${operation}: rejected current master key clears login with a key-specific error`, async () => {
    const { state, api } = clientFixture('rejected-fixture');
    state.respond = async () => Response.json({}, { status: 401 });
    await assert.rejects(api[operation](), /Admin master key was rejected/);
    assert.equal(state.key, null);
    assert.equal(state.cleared, 1);
  });
}
