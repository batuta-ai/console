import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync } from 'node:fs';

const tag = process.argv[2];
execFileSync(process.execPath, ['scripts/verify-beta.mjs', tag ?? ''], { stdio: 'inherit' });
const revision = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
assert.equal(execFileSync('git', ['rev-parse', `${tag}^{commit}`], { encoding: 'utf8' }).trim(), revision);
assert.equal(execFileSync('git', ['status', '--porcelain', '--untracked-files=all'], { encoding: 'utf8' }), '');
execFileSync(process.execPath, ['scripts/verify-packages.mjs', 'dist', tag, revision], { stdio: 'inherit' });
const repository = process.env.GITHUB_REPOSITORY;
assert.match(repository ?? '', /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/);
assert.ok(process.env.GITHUB_TOKEN, 'GITHUB_TOKEN is required only for the authorized upload job');
const headers = {
  Authorization: `Bearer ${process.env.GITHUB_TOKEN}`,
  Accept: 'application/vnd.github+json',
  'X-GitHub-Api-Version': '2022-11-28',
};
async function request(url, options = {}) {
  const response = await fetch(url, { ...options, headers: { ...headers, ...options.headers }, redirect: 'error', signal: AbortSignal.timeout(120000) });
  assert.ok(response.ok, `GitHub release request failed: HTTP ${response.status}`);
  return response.json();
}
const release = await request(`https://api.github.com/repos/${repository}/releases/tags/${encodeURIComponent(tag)}`);
assert.equal(release.tag_name, tag);
assert.equal(release.prerelease, true);
assert.equal(release.draft, false);
assert.ok(Number.isSafeInteger(release.id) && release.id > 0);
const assets = readdirSync('dist').filter(name => name.endsWith('.tar.gz') || name === 'checksums.txt').sort();
assert.equal(assets.length, 5);
for (const name of assets) {
  const body = readFileSync(`dist/${name}`);
  const asset = await request(`https://uploads.github.com/repos/${repository}/releases/${release.id}/assets?name=${encodeURIComponent(name)}`, {
    method: 'POST', body,
    headers: { 'Content-Type': name.endsWith('.tar.gz') ? 'application/gzip' : 'text/plain' },
  });
  assert.equal(asset.name, name);
  assert.equal(asset.size, body.length);
  console.log(`Uploaded ${name}`);
}
