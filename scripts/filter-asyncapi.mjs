#!/usr/bin/env node
/**
 * filter-asyncapi.mjs
 *
 * Filters an AsyncAPI spec to only include channels/operations whose tags
 * contain a given service name. Supports AsyncAPI v2 and v3.
 *
 * Usage:
 *   node scripts/filter-asyncapi.mjs --input <file> --output <file> [--service <name>]
 */

import { readFileSync, writeFileSync } from 'node:fs';
import yaml from 'js-yaml';

// ---------------------------------------------------------------------------
// Args
// ---------------------------------------------------------------------------
const args = process.argv.slice(2);
const get = (flag) => {
  const i = args.indexOf(flag);
  return i !== -1 ? args[i + 1] : null;
};

const inputPath = get('--input');
const outputPath = get('--output');
const service = get('--service') ?? 'management-api';

if (inputPath && outputPath) {
  run();
} else {
  console.error(
    'Usage: filter-asyncapi.mjs --input <file> --output <file> [--service <name>]',
  );
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function hasTag(obj, tag) {
  if (!Array.isArray(obj?.tags)) return false;
  return obj.tags.some((t) => (typeof t === 'string' ? t : t?.name) === tag);
}

function collectRefs(obj, refs = new Set()) {
  if (!obj || typeof obj !== 'object') return refs;
  if (Array.isArray(obj)) {
    for (const item of obj) collectRefs(item, refs);
  } else {
    for (const [key, value] of Object.entries(obj)) {
      if (key === '$ref' && typeof value === 'string') refs.add(value);
      else collectRefs(value, refs);
    }
  }
  return refs;
}

/**
 * Transitively expand a set of $refs by following each one that points into
 * spec.components and collecting any nested $refs inside the resolved object.
 * Repeats until no new refs are discovered (fixpoint).
 */
function collectRefsTransitive(spec, initialRefs) {
  const allRefs = new Set(initialRefs);
  const queue = [...initialRefs];

  while (queue.length > 0) {
    const ref = queue.pop();
    if (!ref.startsWith('#/')) continue;

    const parts = ref.slice(2).split('/');
    const obj = parts.reduce((acc, part) => acc?.[part], spec);
    if (!obj) continue;

    for (const nested of collectRefs(obj)) {
      if (!allRefs.has(nested)) {
        allRefs.add(nested);
        queue.push(nested);
      }
    }
  }

  return allRefs;
}

/**
 * Prune a components section to only entries whose canonical $ref
 * appears in the provided Set of kept refs.
 */
function pruneComponents(components, keptRefs) {
  if (!components) return components;
  const pruned = {};
  for (const [section, entries] of Object.entries(components)) {
    pruned[section] = {};
    for (const [name, value] of Object.entries(entries)) {
      if (keptRefs.has(`#/components/${section}/${name}`)) {
        pruned[section][name] = value;
      }
    }
  }
  return pruned;
}

// ---------------------------------------------------------------------------
// Filter — AsyncAPI v3
// ---------------------------------------------------------------------------
function filterV3(spec, tag) {
  const keptOps = {};
  const keptChannelNames = new Set();

  for (const [opId, op] of Object.entries(spec.operations ?? {})) {
    const chRef = op.channel?.$ref;
    const chName = chRef?.replace(/^#\/channels\//, '');
    const ch = chName ? spec.channels?.[chName] : undefined;

    if (!hasTag(op, tag) && !hasTag(ch, tag)) continue;

    keptOps[opId] = op;
    if (chName) keptChannelNames.add(chName);
  }

  const keptChannels = {};
  for (const chName of keptChannelNames) {
    if (spec.channels?.[chName]) keptChannels[chName] = spec.channels[chName];
  }

  const keptRefs = collectRefsTransitive(spec, collectRefs({ operations: keptOps, channels: keptChannels }));
  const components = pruneComponents(spec.components, keptRefs);

  return { ...spec, operations: keptOps, channels: keptChannels, components };
}

// ---------------------------------------------------------------------------
// Filter — AsyncAPI v2
// ---------------------------------------------------------------------------
function filterV2(spec, tag) {
  const keptChannels = {};

  for (const [chName, ch] of Object.entries(spec.channels ?? {})) {
    const ops = [ch.subscribe, ch.publish].filter(Boolean);
    const relevant = hasTag(ch, tag) || ops.some((op) => hasTag(op, tag));
    if (relevant) keptChannels[chName] = ch;
  }

  const keptRefs = collectRefsTransitive(spec, collectRefs(keptChannels));
  const components = pruneComponents(spec.components, keptRefs);

  return { ...spec, channels: keptChannels, components };
}

// ---------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------
function run() {
  const raw = readFileSync(inputPath, 'utf8');
  const spec = yaml.load(raw);
  const isV3 = String(spec.asyncapi ?? '').startsWith('3.');

  const filtered = isV3 ? filterV3(spec, service) : filterV2(spec, service);

  writeFileSync(outputPath, yaml.dump(filtered, { lineWidth: -1, noRefs: true }), 'utf8');

  const channelCount = Object.keys(filtered.channels ?? {}).length;
  const opInfo = isV3 ? `, ${Object.keys(filtered.operations ?? {}).length} operation(s)` : '';
  console.log(`  Filtered: ${channelCount} channel(s)${opInfo} kept for service '${service}'`);
}
