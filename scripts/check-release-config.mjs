import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';

// Validate the pinned upstream schema without installing another dependency.
const schema = JSON.parse(readFileSync(process.argv[2] ?? new URL('./release-please-schema.json', import.meta.url), 'utf8'));
function validate(value, rule, path = '$') {
  if (rule === true) return;
  assert.notEqual(rule, false, `${path}: prohibited`);
  if (rule.$ref) {
    assert.ok(rule.$ref.startsWith('#/'), 'Only local schema references are supported');
    const referenced = rule.$ref.slice(2).split('/').reduce((part, key) => part[key], schema);
    validate(value, referenced, path);
  }
  for (const part of rule.allOf ?? []) validate(value, part, path);
  for (const keyword of ['anyOf', 'oneOf']) {
    if (!rule[keyword]) continue;
    const matches = rule[keyword].filter(part => {
      try { validate(value, part, path); return true; } catch { return false; }
    }).length;
    assert.ok(keyword === 'oneOf' ? matches === 1 : matches > 0, `${path}: ${keyword}`);
  }
  if (rule.type) {
    const types = Array.isArray(rule.type) ? rule.type : [rule.type];
    const type = Array.isArray(value) ? 'array' : value === null ? 'null' : typeof value;
    assert.ok(types.includes(type) || (types.includes('integer') && Number.isInteger(value)), `${path}: type ${rule.type}`);
  }
  if (rule.enum) assert.ok(rule.enum.includes(value), `${path}: enum`);
  if (rule.pattern && typeof value === 'string') assert.match(value, new RegExp(rule.pattern));
  if (Array.isArray(value) && rule.items) value.forEach((item, i) => validate(item, rule.items, `${path}[${i}]`));
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const key of rule.required ?? []) assert.ok(Object.hasOwn(value, key), `${path}.${key}: required`);
    for (const [key, item] of Object.entries(value)) {
      const rules = [];
      if (Object.hasOwn(rule.properties ?? {}, key)) rules.push(rule.properties[key]);
      for (const [pattern, part] of Object.entries(rule.patternProperties ?? {})) if (new RegExp(pattern).test(key)) rules.push(part);
      if (!rules.length && rule.additionalProperties !== undefined) rules.push(rule.additionalProperties);
      for (const part of rules) validate(item, part, `${path}.${key}`);
    }
  }
}
const config = JSON.parse(readFileSync('release-please-config.json', 'utf8'));
validate(config, schema);
const component = config.packages['.'];
assert.equal(component['release-type'], 'go');
assert.equal(component.versioning, 'prerelease');
assert.equal(component['prerelease-type'], 'beta');
assert.equal(component.prerelease, true);
assert.equal(component['include-component-in-tag'], false);
assert.equal(component['include-v-in-tag'], true);
assert.equal(component['initial-version'], '0.1.0-beta.1');
assert.ok(!component['version-file'], 'Do not rewrite runtime or Core versions');
const manifest = JSON.parse(readFileSync('.release-please-manifest.json', 'utf8'));
assert.deepEqual(Object.keys(manifest), ['.']);
assert.match(manifest['.'], /^0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-beta\.(0|[1-9][0-9]*)$/);
assert.match(config['bootstrap-sha'], /^[a-f0-9]{40}$/);
execFileSync('git', ['cat-file', '-e', `${config['bootstrap-sha']}^{commit}`]);
console.log('Pinned release-please schema and independent beta policy passed.');
