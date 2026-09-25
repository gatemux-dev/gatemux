import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';

function compose(extra = {}, legacy = false) {
  const env = { ...process.env };
  for (const k of ['GATEMUX_ADMIN_KEY', 'AIPORT_ADMIN_KEY', 'GATEMUX_PORT', 'AIPORT_PORT', 'GATEMUX_LEGACY_PG_VOLUME']) delete env[k];
  Object.assign(env, extra);
  const args = ['compose', '-p', 'rename-check', '-f', 'deploy/docker/docker-compose.yml'];
  if (legacy) args.push('-f', 'deploy/docker/legacy-compose.yaml');
  args.push('config', '--format', 'json');
  return JSON.parse(execFileSync('docker', args, { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }));
}

test('canonical module, command, image and chart agree', () => {
  assert.match(readFileSync('go.mod', 'utf8'), /^module github\.com\/gatemux-dev\/gatemux\n/);
  assert.match(readFileSync('Dockerfile', 'utf8'), /\.\/cmd\/gatemux\b/);
  assert.match(readFileSync('Dockerfile', 'utf8'), /ENTRYPOINT \["\/gatemux"\]/);
  assert.match(readFileSync('deploy/helm/gatemux/values.yaml', 'utf8'), /ghcr\.io\/gatemux-dev\/gatemux/);
});

test('fresh Compose uses GateMux and retains loopback bindings with legacy env fallback', () => {
  assert.throws(() => compose());
  const old = compose({ AIPORT_ADMIN_KEY: 'fixture-only', AIPORT_PORT: '44001' });
  assert.equal(old.services.gatemux.environment.GATEMUX_ADMIN_KEY, 'fixture-only');
  assert.equal(old.services.gatemux.ports[0].published, '44001');
  const current = compose({ GATEMUX_ADMIN_KEY: 'fixture-current', AIPORT_ADMIN_KEY: 'fixture-old', GATEMUX_PORT: '44002', AIPORT_PORT: '44001' });
  assert.equal(current.services.gatemux.environment.GATEMUX_ADMIN_KEY, 'fixture-current');
  assert.equal(current.services.gatemux.ports[0].published, '44002');
  assert.equal(current.services.postgres.environment.POSTGRES_DB, 'gatemux');
  assert.equal(current.volumes.gatemux_pg_data.name, 'rename-check_gatemux_pg_data');
  for (const service of Object.values(current.services)) for (const port of service.ports || []) assert.equal(port.host_ip, '127.0.0.1');
});

test('upgrade Compose requires an explicit external existing volume and keeps database identity', () => {
  assert.throws(() => compose({ GATEMUX_ADMIN_KEY: 'fixture-only' }, true));
  const config = compose({ GATEMUX_ADMIN_KEY: 'fixture-only', GATEMUX_LEGACY_PG_VOLUME: 'existing_fixture_pg_data' }, true);
  assert.equal(config.volumes.gatemux_pg_data.name, 'existing_fixture_pg_data');
  assert.equal(config.volumes.gatemux_pg_data.external, true);
  assert.equal(config.services.postgres.environment.POSTGRES_DB, 'aiport');
  assert.equal(config.services.postgres.environment.POSTGRES_USER, 'aiport');
  assert.equal(config.services.gatemux.volumes.length, 1);
  assert.match(config.services.gatemux.volumes[0].source, /legacy-config\.yaml$/);
  assert.equal(config.services.gatemux.volumes[0].target, '/etc/gatemux/config.yaml');
});
