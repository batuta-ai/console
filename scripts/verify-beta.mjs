import { readFileSync } from 'node:fs';

const tag = process.argv[2];
const manifest = JSON.parse(readFileSync('.release-please-manifest.json', 'utf8'));
const beta = /^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-beta\.(0|[1-9][0-9]*)$/;
if (!tag || !beta.test(tag) || tag !== `v${manifest['.']}` || tag === 'v0.1.0-beta.0') {
  console.error('Release requires an exact v0.x.y-beta.N manifest match; the unpublished bootstrap seed cannot be released.');
  process.exit(1);
}
console.log(`Verified Console beta ${tag}`);
