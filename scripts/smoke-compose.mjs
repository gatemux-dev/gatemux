// Fresh project, tmpfs dependencies, loopback ephemeral gateway port, synthetic
// upstream only. No preview volumes, provider credentials or paid requests.
import { spawn, spawnSync, execFileSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { resolve, dirname } from 'node:path';
import assert from 'node:assert/strict';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const project = `gatemux-smoke-${randomBytes(6).toString('hex')}`;
const master = randomBytes(32).toString('hex');
const env = { ...process.env, GATEMUX_ADMIN_KEY: master };
const args = ['compose', '--project-name', project, '--env-file', '/dev/null', '-f', 'deploy/smoke/docker-compose.yml'];
const run = (rest, timeout = 600000) => new Promise((done, reject) => {
  const child = spawn('docker', [...args, ...rest], { cwd: root, env, stdio: 'inherit', timeout });
  child.once('error', reject);
  child.once('exit', code => code === 0 ? done() : reject(Error(`Compose ${rest[0]} failed (${code})`)));
});
let cleaning;
const cleanup = () => cleaning ||= run(['down', '--timeout', '45', '--remove-orphans'], 90000);
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => {
  cleanup().then(() => process.exit(1), () => process.exit(1));
});
try {
  console.log(`Starting isolated project ${project}`);
  await run(['up', '-d', '--build']);
  // A one-off container publishes no ports and must fail in config validation,
  // before opening the store or starting the listener. Never print its output.
  const noKey = spawnSync('docker', [...args, 'run', '--rm', '--no-deps', '-T',
    '-e', 'GATEMUX_ADMIN_KEY=', '-e', 'AIPORT_ADMIN_KEY=', 'gateway'], {
    cwd: root, env, encoding: 'utf8', timeout: 30000,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  assert.equal(noKey.error, undefined, 'missing-key startup check did not finish');
  assert.equal(noKey.status, 1, 'gateway must exit with code 1 without an admin key');
  assert(noKey.stderr.includes('serve: load config: env var "GATEMUX_ADMIN_KEY" (admin.master_key_env) is empty'),
    'gateway must reject the missing admin key before store/listener startup');
  console.log('PASS: gateway refuses to start without an admin key');
  const address = execFileSync('docker', [...args, 'port', 'gateway', '4000'], { cwd: root, env, encoding: 'utf8' }).trim();
  assert.match(address, /^127\.0\.0\.1:\d+$/);
  const base = `http://${address}`;
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { ready = (await fetch(`${base}/readyz`, { signal: AbortSignal.timeout(2000) })).ok; } catch {}
    if (ready) break;
    await new Promise(done => setTimeout(done, 1000));
  }
  assert(ready, 'gateway did not become ready');
  const request = (path, { token, body, method } = {}) => fetch(base + path, {
    method: method || (body ? 'POST' : 'GET'),
    headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(10000),
  });
  assert.equal((await request('/healthz')).status, 200);
  const html = await (await request('/')).text();
  const script = html.match(/src="(\/assets\/[^" ]+\.js)"/);
  assert(script, 'embedded admin app missing');
  assert.equal((await request(script[1])).status, 200);
  assert.equal((await request('/docs')).status, 200);
  assert.equal((await request('/openapi/v1.json')).status, 200);
  assert.equal((await request('/admin/teams')).status, 401);
  assert.equal((await request('/auth/me', { token: master })).status, 200);
  const team = await request('/admin/teams', { token: master, body: { slug: 'smoke', name: 'Smoke' } });
  assert.equal(team.status, 201);
  const key = await request('/admin/teams/smoke/keys', { token: master, body: { name: 'smoke' } });
  assert.equal(key.status, 201);
  const credential = (await key.json()).key;
  assert.equal(typeof credential, 'string');
  assert.equal((await request('/v1/models', { token: credential })).status, 200);
  const chat = await request('/v1/chat/completions', { token: credential, body: { model: 'smoke-model', messages: [{ role: 'user', content: 'synthetic smoke request' }] } });
  assert.equal(chat.status, 200);
  assert((await chat.json()).choices?.length > 0, 'mock inference response missing');
  console.log('PASS: readiness, embedded UI assets, API docs, admin auth, team/key issuance and mock inference');
  if (process.env.GATEMUX_SMOKE_BROWSER === '1') {
    for (const name of ['master-key.smoke.cjs', 'navigation.smoke.cjs', 'docs.smoke.cjs']) {
      await new Promise((done, reject) => {
        const child = spawn(process.execPath, [resolve(root, 'web/tests', name)], {
          cwd: root, stdio: 'inherit', timeout: 120000,
          env: { ...process.env, GATEMUX_SMOKE_URL: base, GATEMUX_SMOKE_ADMIN_KEY: master },
        });
        child.once('error', reject);
        child.once('exit', code => code === 0 ? done() : reject(Error(`Browser ${name} failed (${code})`)));
      });
    }
  }
} catch (error) {
  // Do not print response bodies, environment, raw tokens or container logs.
  console.error(error.message);
  process.exitCode = 1;
} finally {
  try { await cleanup(); console.log('Owned smoke stack removed; no persistent data created.'); }
  catch { console.error(`Cleanup failed: inspect owned project ${project}`); process.exitCode = 1; }
}
