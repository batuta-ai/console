import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { execFileSync } from 'node:child_process';

function verifyPackages() {
  const [directory, version, revision] = process.argv.slice(2);
  assert.match(revision ?? '', /^[a-f0-9]{40}$/);
  const root = resolve(directory);
  assert.deepEqual(readFileSync('THIRD_PARTY_NOTICES'), readFileSync('internal/terminalengine/LICENSE'),
    'The archive notice must contain the exact full copied MIT license');
  const expected = ['darwin_amd64.tar.gz', 'darwin_arm64.tar.gz', 'linux_amd64.tar.gz', 'linux_arm64.tar.gz', 'windows_amd64.zip']
    .map(target => `console_${version}_${target}`).sort();
  assert.deepEqual(readdirSync(root).filter(name => name.endsWith('.tar.gz') || name.endsWith('.zip')).sort(), expected,
    'Archive inventory must match the five release targets');
  const records = readFileSync(join(root, 'checksums.txt'), 'utf8').trim().split('\n').map(line => {
    assert.match(line, /^[a-f0-9]{64}\s+\S+$/, 'Checksum record must contain a SHA256 digest and one filename');
    const [digest, name] = line.split(/\s+/);
    return [name, digest];
  });
  assert.deepEqual(records.map(([name]) => name).sort(), expected, 'Checksum inventory must match the five release targets exactly once');
  const checksums = new Map(records);
  for (const archive of expected) {
    assert.equal(createHash('sha256').update(readFileSync(join(root, archive))).digest('hex'), checksums.get(archive));
    const windows = archive.endsWith('_windows_amd64.zip');
    const [goos, goarch] = archive.slice(`console_${version}_`.length).split('.')[0].split('_');
    const binaries = windows ? ['console.exe', 'consoled.exe'] : ['console', 'consoled'];
    const members = (windows
      ? execFileSync('unzip', ['-Z1', join(root, archive)], { encoding: 'utf8' })
      : execFileSync('tar', ['-tzf', join(root, archive)], { encoding: 'utf8' })).trim().split('\n').sort();
    assert.deepEqual(members, ['LICENSE', 'README.md', 'THIRD_PARTY_NOTICES', ...binaries]);
    const extracted = mkdtempSync(join(tmpdir(), 'console-package-'));
    try {
      if (windows) execFileSync('unzip', ['-q', join(root, archive), '-d', extracted]);
      else execFileSync('tar', ['-xzf', join(root, archive), '-C', extracted]);
      for (const doc of ['LICENSE', 'README.md', 'THIRD_PARTY_NOTICES']) assert.deepEqual(readFileSync(join(extracted, doc)), readFileSync(doc));
      for (const binary of binaries) {
        const metadata = execFileSync('go', ['version', '-m', join(extracted, binary)], { encoding: 'utf8' });
        const buildInfo = JSON.parse(execFileSync('go', ['version', '-m', '-json', join(extracted, binary)], { encoding: 'utf8' }));
        assert.equal(buildInfo.GoVersion, 'go1.27.2');
        const settings = new Map(buildInfo.Settings.map(({ Key, Value }) => [Key, Value]));
        assert.equal(settings.get('GOOS'), goos, 'Binary GOOS must match its archive');
        assert.equal(settings.get('GOARCH'), goarch, 'Binary GOARCH must match its archive');
        assert.equal(settings.get('CGO_ENABLED'), '0');
        // The TUI may not link Core; never invent a dependency for diagnostics.
        if (binary === 'consoled' || binary === 'consoled.exe' || metadata.includes('github.com/batuta-ai/core')) {
          assert.match(metadata, /github\.com\/batuta-ai\/core\s+v1\.1\.0-beta\.55\s/);
        }
        assert.ok(metadata.includes(`ReleaseVersion=${version}`));
        assert.ok(metadata.includes(`ReleaseRevision=${revision}`));
        assert.ok(metadata.includes(`vcs.revision=${revision}`));
        assert.ok(!metadata.includes('\n\t=>'), 'module replacement is not release authority');
      }
    } finally { rmSync(extracted, { recursive: true, force: true }); }
    console.log(`${archive}: checksum, contents and both embedded build records verified`);
  }
}

// WORKAROUND: Node 26.10.0 can deadlock during uncaught-error shutdown while
// a Maglev compiler waits for main-thread GC (nodejs/node#64274).
try {
  verifyPackages();
} catch (error) {
  console.error(error);
  process.exitCode = 1;
}
