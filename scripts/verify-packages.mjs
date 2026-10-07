import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { execFileSync } from 'node:child_process';

const [directory, version, revision] = process.argv.slice(2);
assert.match(revision ?? '', /^[a-f0-9]{40}$/);
const root = resolve(directory);
assert.deepEqual(readFileSync('THIRD_PARTY_NOTICES'), readFileSync('internal/terminalengine/LICENSE'),
  'The archive notice must contain the exact full copied MIT license');
const expected = ['darwin_amd64', 'darwin_arm64', 'linux_amd64', 'linux_arm64']
  .map(target => `console_${version}_${target}.tar.gz`).sort();
assert.deepEqual(readdirSync(root).filter(name => name.endsWith('.tar.gz')).sort(), expected);
const checksums = new Map(readFileSync(join(root, 'checksums.txt'), 'utf8').trim().split('\n')
  .map(line => { const [digest, name] = line.split(/\s+/); return [name, digest]; }));
assert.equal(checksums.size, 4);
for (const archive of expected) {
  assert.equal(createHash('sha256').update(readFileSync(join(root, archive))).digest('hex'), checksums.get(archive));
  const members = execFileSync('tar', ['-tzf', join(root, archive)], { encoding: 'utf8' }).trim().split('\n').sort();
  assert.deepEqual(members, ['LICENSE', 'README.md', 'THIRD_PARTY_NOTICES', 'console', 'consoled']);
  const extracted = mkdtempSync(join(tmpdir(), 'console-package-'));
  try {
    execFileSync('tar', ['-xzf', join(root, archive), '-C', extracted]);
    for (const doc of ['LICENSE', 'README.md', 'THIRD_PARTY_NOTICES']) assert.deepEqual(readFileSync(join(extracted, doc)), readFileSync(doc));
    for (const binary of ['console', 'consoled']) {
      const metadata = execFileSync('go', ['version', '-m', join(extracted, binary)], { encoding: 'utf8' });
      assert.match(metadata, /go1\.27\.1/);
      // The TUI may not link Core; never invent a dependency for diagnostics.
      if (binary === 'consoled' || metadata.includes('github.com/batuta-ai/core')) {
        assert.match(metadata, /github\.com\/batuta-ai\/core\s+v1\.1\.0-beta\.49\s/);
      }
      assert.ok(metadata.includes(`ReleaseVersion=${version}`));
      assert.ok(metadata.includes(`ReleaseRevision=${revision}`));
      assert.ok(metadata.includes(`vcs.revision=${revision}`));
      assert.ok(!metadata.includes('\n\t=>'), 'module replacement is not release authority');
    }
  } finally { rmSync(extracted, { recursive: true, force: true }); }
  console.log(`${archive}: checksum, contents and both embedded build records verified`);
}
