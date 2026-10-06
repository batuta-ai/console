import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const guard = resolve('scripts/verify-beta.mjs');
function verify(tag, version = '0.1.0-beta.1') {
  const root = mkdtempSync(join(tmpdir(), 'console-beta-test-'));
  try {
    writeFileSync(join(root, '.release-please-manifest.json'), JSON.stringify({ '.': version }));
    return spawnSync(process.execPath, [guard, tag], { cwd: root, encoding: 'utf8' });
  } finally { rmSync(root, { recursive: true, force: true }); }
}
test('a matching independent beta is accepted', () => {
  const result = verify('v0.1.0-beta.1');
  assert.equal(result.status, 0, result.stderr);
});
test('stable, malformed, foreign, seed and mismatched tags fail closed', () => {
  for (const tag of ['v0.1.0', 'v1.0.0', 'v1.1.0-beta.49', '0.1.0-beta.1', 'v0.1.0-beta.01', 'v0.1.0-beta.2', 'v0.1.0-beta.1\n', 'v0.1.0-beta.1;echo bad']) {
    assert.notEqual(verify(tag).status, 0, tag);
  }
  assert.notEqual(verify('v0.1.0-beta.0', '0.1.0-beta.0').status, 0);
  assert.notEqual(verify('v0.1.0-beta.1', '0.1.0').status, 0);
  assert.notEqual(verify('v0.1.0-beta.1\n', '0.1.0-beta.1\n').status, 0);
});

const assetCheck = resolve('scripts/check-assets.sh');
function git(root, ...args) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  return result;
}
test('asset check rejects modified, removed and new embedded files', () => {
  const root = mkdtempSync(join(tmpdir(), 'console-assets-test-'));
  try {
    git(root, 'init', '-q');
    mkdirSync(join(root, 'internal/webui/dist'), { recursive: true });
    const file = join(root, 'internal/webui/dist/index.html');
    writeFileSync(file, 'original');
    git(root, 'add', '.');
    git(root, '-c', 'user.name=Contract Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'fixture');
    const check = () => spawnSync('sh', [assetCheck], { cwd: root, encoding: 'utf8' });
    assert.equal(check().status, 0);
    writeFileSync(file, 'drift');
    assert.notEqual(check().status, 0);
    git(root, 'add', '.');
    assert.notEqual(check().status, 0, 'staging must not hide drift');
    git(root, 'reset', '--hard', 'HEAD');
    writeFileSync(join(root, 'internal/webui/dist/new.js'), 'untracked');
    assert.notEqual(check().status, 0);
    rmSync(join(root, 'internal/webui/dist/new.js'));
    rmSync(file);
    assert.notEqual(check().status, 0);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('package command rejects invalid modes and stable release tags before building', () => {
  const script = resolve('scripts/package-release.sh');
  for (const args of [['--publish'], ['--release', 'v1.0.0'], ['--release', 'v0.1.0'], ['--release', 'v0.1.0-beta.0']]) {
    const result = spawnSync('sh', [script, ...args], { encoding: 'utf8' });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /Usage:|Release requires/);
  }
});

test('release configuration rejects schema errors and stable policy changes', () => {
  const script = resolve('scripts/check-release-config.mjs');
  const config = JSON.parse(readFileSync('release-please-config.json', 'utf8'));
  for (const mutate of [
    value => { value.unrecognized = true; },
    value => { value.packages['.'].prerelease = 'true'; },
    value => { value.packages['.'].versioning = 'default'; },
    value => { value.packages['.']['include-component-in-tag'] = true; },
  ]) {
    const root = mkdtempSync(join(tmpdir(), 'console-schema-test-'));
    try {
      const changed = structuredClone(config);
      mutate(changed);
      writeFileSync(join(root, 'release-please-config.json'), JSON.stringify(changed));
      const result = spawnSync(process.execPath, [script], { cwd: root, encoding: 'utf8' });
      assert.notEqual(result.status, 0);
      assert.match(result.stderr, /AssertionError/);
    } finally { rmSync(root, { recursive: true, force: true }); }
  }
});

test('upload entry point rejects a stable tag before any network request', () => {
  const result = spawnSync(process.execPath, [resolve('scripts/upload-release.mjs'), 'v1.0.0'], { encoding: 'utf8' });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Release requires/);
});
