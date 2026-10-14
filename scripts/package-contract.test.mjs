import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import test from 'node:test';

const { CONSOLE_PACKAGE_DIST: directory, CONSOLE_VERSION: version, CONSOLE_REVISION: revision } = process.env;
assert.ok(directory, 'CONSOLE_PACKAGE_DIST must identify the real GoReleaser packages');
assert.match(version ?? '', /^[a-zA-Z0-9][a-zA-Z0-9.-]*$/, 'CONSOLE_VERSION is required');
assert.match(revision ?? '', /^[a-f0-9]{40}$/, 'CONSOLE_REVISION must be the full source revision');
const source = resolve(directory);
const verifier = resolve('scripts/verify-packages.mjs');
const archive = target => `console_${version}_${target}`;
const archives = [
  'darwin_amd64.tar.gz', 'darwin_arm64.tar.gz',
  'linux_amd64.tar.gz', 'linux_arm64.tar.gz', 'windows_amd64.zip',
].map(archive);
const windows = archive('windows_amd64.zip');
const digest = file => createHash('sha256').update(readFileSync(file)).digest('hex');

function verify(root) {
  const result = spawnSync(process.execPath, [verifier, root, version, revision], { encoding: 'utf8', timeout: 30_000, killSignal: 'SIGKILL' });
  assert.ifError(result.error);
  assert.equal(result.signal, null, result.stderr);
  return result;
}

function extract(root, name, destination) {
  mkdirSync(destination);
  if (name.endsWith('.zip')) execFileSync('unzip', ['-q', join(root, name), '-d', destination]);
  else execFileSync('tar', ['-xzf', join(root, name), '-C', destination]);
}

function repack(root, name, contents) {
  rmSync(join(root, name));
  if (name.endsWith('.zip')) execFileSync('zip', ['-q', join(root, name), ...readdirSync(contents)], { cwd: contents });
  else execFileSync('tar', ['-czf', join(root, name), '-C', contents, ...readdirSync(contents)]);
}

function checksums(root) {
  writeFileSync(join(root, 'checksums.txt'), archives.map(name => `${digest(join(root, name))}  ${name}\n`).join(''));
}

function rejected(result, target, reason) {
  assert.equal(result.status, 1, result.stdout + result.stderr);
  assert.match(result.stderr, /AssertionError \[ERR_ASSERTION\]/);
  assert.match(result.stderr, reason);
  assert.deepEqual(result.stdout.trim().split('\n'), archives.slice(0, archives.indexOf(target))
    .map(name => `${name}: checksum, contents and both embedded build records verified`));
}

test('real GoReleaser package contract', async t => {
  const files = [...archives, 'checksums.txt'];
  const before = files.map(name => digest(join(source, name)));
  async function fixture(name, run) {
    await t.test(name, () => {
      const root = mkdtempSync(join(tmpdir(), 'console-package-contract-'));
      try {
        for (const file of files) copyFileSync(join(source, file), join(root, file));
        run(root);
      } finally { rmSync(root, { recursive: true, force: true }); }
    });
  }

  try {
    await fixture('accepts all five unmodified archives and ten real build records', root => {
      const result = verify(root);
      assert.equal(result.status, 0, result.stdout + result.stderr);
      assert.equal(result.stdout.trim().split('\n').length, 5);
    });

    await fixture('rejects a real Linux binary renamed console.exe in the Windows ZIP', root => {
      const contents = join(root, 'windows');
      const donor = join(root, 'linux');
      extract(root, windows, contents);
      extract(root, archive('linux_amd64.tar.gz'), donor);
      copyFileSync(join(donor, 'console'), join(contents, 'console.exe'));
      repack(root, windows, contents);
      checksums(root);
      rejected(verify(root), windows, /Binary GOOS must match its archive/);
    });

    await fixture('rejects a real Linux ARM64 binary in the Linux AMD64 archive', root => {
      const target = archive('linux_amd64.tar.gz');
      const contents = join(root, 'amd64');
      const donor = join(root, 'arm64');
      extract(root, target, contents);
      extract(root, archive('linux_arm64.tar.gz'), donor);
      copyFileSync(join(donor, 'console'), join(contents, 'console'));
      repack(root, target, contents);
      checksums(root);
      rejected(verify(root), target, /Binary GOARCH must match its archive/);
    });

    await fixture('rejects a ZIP missing console.exe at the member inventory check', root => {
      const contents = join(root, 'windows');
      extract(root, windows, contents);
      rmSync(join(contents, 'console.exe'));
      repack(root, windows, contents);
      checksums(root);
      rejected(verify(root), windows, /Expected values to be strictly deep-equal:[\s\S]*-\s+'console\.exe'/);
    });

    await fixture('rejects a one-byte change to the exact MIT notice after extraction', root => {
      const contents = join(root, 'windows');
      extract(root, windows, contents);
      const notice = join(contents, 'THIRD_PARTY_NOTICES');
      const bytes = readFileSync(notice);
      bytes[0] ^= 1;
      writeFileSync(notice, bytes);
      repack(root, windows, contents);
      checksums(root);
      rejected(verify(root), windows, /Expected values to be strictly deep-equal:[\s\S]*Buffer\(\d+\) \[Uint8Array\]/);
    });

    await fixture('rejects a wrong SHA256 digest for an otherwise unchanged Windows ZIP', root => {
      const actual = digest(join(root, windows));
      const wrong = `${actual[0] === '0' ? '1' : '0'}${actual.slice(1)}`;
      const file = join(root, 'checksums.txt');
      writeFileSync(file, readFileSync(file, 'utf8').replace(actual, wrong));
      const result = verify(root);
      rejected(result, windows, /Expected values to be strictly equal:/);
      assert.ok(result.stderr.includes(`actual: '${actual}'`), result.stderr);
      assert.ok(result.stderr.includes(`expected: '${wrong}'`), result.stderr);
    });
  } finally {
    assert.deepEqual(files.map(name => digest(join(source, name))), before, 'Tests must preserve the real distribution');
  }
});
