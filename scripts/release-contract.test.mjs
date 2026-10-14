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

const packageCheck = resolve('scripts/verify-packages.mjs');
const packageVersion = '0.1.0-beta.1-snapshot.contract';
const packageArchives = [
  'darwin_amd64.tar.gz', 'darwin_arm64.tar.gz',
  'linux_amd64.tar.gz', 'linux_arm64.tar.gz', 'windows_amd64.zip',
].map(target => `console_${packageVersion}_${target}`);
function invalidPackageInventory(mutate) {
  const root = mkdtempSync(join(tmpdir(), 'console-package-inventory-test-'));
  try {
    for (const name of packageArchives) writeFileSync(join(root, name), '');
    const lines = packageArchives.map(name => `${'0'.repeat(64)}  ${name}`);
    mutate(root, lines);
    writeFileSync(join(root, 'checksums.txt'), `${lines.join('\n')}\n`);
    return spawnSync(process.execPath, [packageCheck, root, packageVersion, 'a'.repeat(40)], { encoding: 'utf8' });
  } finally { rmSync(root, { recursive: true, force: true }); }
}
test('package inventory rejects missing targets and extra archives before reading their contents', () => {
  for (const missing of packageArchives) {
    const result = invalidPackageInventory(root => rmSync(join(root, missing)));
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /Archive inventory must match the five release targets/);
  }
  for (const extra of ['console_extra.tar.gz', 'console_extra.zip', `console_${packageVersion}_windows_arm64.zip`]) {
    const result = invalidPackageInventory(root => writeFileSync(join(root, extra), ''));
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /Archive inventory must match the five release targets/);
  }
});
test('checksum inventory rejects missing, duplicate, extra and malformed records before extraction', () => {
  for (const mutate of [
    (_root, lines) => lines.pop(),
    (_root, lines) => lines.push(lines[0]),
    (_root, lines) => lines.push(`${'0'.repeat(64)}  extra.zip`),
    (_root, lines) => { lines[4] = `${'0'.repeat(64)}  wrong.zip`; },
    (_root, lines) => { lines[4] = `not-sha256  ${packageArchives[4]}`; },
    (_root, lines) => { lines[4] += ' extra-field'; },
  ]) {
    const result = invalidPackageInventory(mutate);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /Checksum (inventory|record)/);
  }
});

// Upload wiring is tested offline; real archive validation has its own package contract.
function uploadFixture(extra) {
  const root = mkdtempSync(join(tmpdir(), 'console-upload-test-'));
  const source = join(root, 'source');
  const requests = join(root, 'requests.jsonl');
  const validated = join(root, 'validated.json');
  const tag = 'v0.1.0-beta.1';
  const names = [
    'checksums.txt',
    `console_${tag}_darwin_amd64.tar.gz`, `console_${tag}_darwin_arm64.tar.gz`,
    `console_${tag}_linux_amd64.tar.gz`, `console_${tag}_linux_arm64.tar.gz`,
    `console_${tag}_windows_amd64.zip`,
  ];
  try {
    mkdirSync(join(source, 'scripts'), { recursive: true });
    mkdirSync(join(source, 'dist'));
    for (const name of ['upload-release.mjs', 'verify-beta.mjs']) {
      writeFileSync(join(source, 'scripts', name), readFileSync(resolve('scripts', name)));
    }
    writeFileSync(join(source, '.release-please-manifest.json'), JSON.stringify({ '.': '0.1.0-beta.1' }));
    for (const name of [...names, ...(extra ? [extra] : [])]) writeFileSync(join(source, 'dist', name), `fixture:${name}`);
    writeFileSync(join(source, 'scripts/verify-packages.mjs'), `
      import { writeFileSync } from 'node:fs';
      writeFileSync(${JSON.stringify(validated)}, JSON.stringify(process.argv.slice(2)));
    `);
    const preload = join(root, 'offline-fetch.mjs');
    writeFileSync(preload, `
      import assert from 'node:assert/strict';
      import { appendFileSync } from 'node:fs';
      globalThis.fetch = async (url, options) => {
        assert.equal(options.redirect, 'error');
        assert.ok(options.signal instanceof AbortSignal);
        assert.equal(options.headers.Authorization, 'Bearer contract-token');
        let result;
        if (options.method === 'POST') {
          const target = new URL(url);
          assert.equal(target.origin + target.pathname, 'https://uploads.github.com/repos/contract/console/releases/17/assets');
          const name = target.searchParams.get('name');
          appendFileSync(${JSON.stringify(requests)}, JSON.stringify({ name, type: options.headers['Content-Type'], body: options.body.toString() }) + '\\n');
          result = { name, size: options.body.length };
        } else {
          assert.equal(url, 'https://api.github.com/repos/contract/console/releases/tags/${tag}');
          appendFileSync(${JSON.stringify(requests)}, JSON.stringify({ release: true }) + '\\n');
          result = { tag_name: '${tag}', prerelease: true, draft: false, id: 17 };
        }
        return { ok: true, status: 200, json: async () => result };
      };
    `);
    git(source, 'init', '-q');
    git(source, 'add', '.');
    git(source, '-c', 'user.name=Contract Test', '-c', 'user.email=test@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixture');
    git(source, '-c', 'tag.gpgsign=false', 'tag', tag);
    const revision = git(source, 'rev-parse', 'HEAD').stdout.trim();
    const result = spawnSync(process.execPath, ['--import', preload, 'scripts/upload-release.mjs', tag], {
      cwd: source, encoding: 'utf8', timeout: 30_000, killSignal: 'SIGKILL',
      env: { ...process.env, GITHUB_REPOSITORY: 'contract/console', GITHUB_TOKEN: 'contract-token' },
    });
    assert.ifError(result.error);
    assert.equal(result.signal, null, result.stderr);
    assert.deepEqual(JSON.parse(readFileSync(validated, 'utf8')), ['dist', tag, revision]);
    const calls = readFileSync(requests, 'utf8').trim().split('\n').map(line => JSON.parse(line));
    return { result, calls, names };
  } finally { rmSync(root, { recursive: true, force: true }); }
}

test('upload wires six verified assets with platform media types and exact bodies offline', () => {
  const { result, calls, names } = uploadFixture();
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.deepEqual(calls, [
    { release: true },
    ...names.map(name => ({ name, type: name === 'checksums.txt' ? 'text/plain' : name.endsWith('.zip') ? 'application/zip' : 'application/gzip', body: `fixture:${name}` })),
  ]);
});

test('upload rejects a seventh archive before sending any asset offline', () => {
  const { result, calls } = uploadFixture('stray.zip');
  assert.equal(result.status, 1, result.stdout + result.stderr);
  assert.match(result.stderr, /AssertionError \[ERR_ASSERTION\]/);
  assert.deepEqual(calls, [{ release: true }]);
});
