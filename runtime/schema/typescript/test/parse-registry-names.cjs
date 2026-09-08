// The default parse and normalize registries hold exactly the scalars whose
// generated catalog entry (builtin-scalars.generated.ts) marks a custom step.
// The walker consults a registry only for hasCustomParse / hasCustomNormalize
// scalars, so any flagged name missing here is a scalar the runtime would
// silently skip, and any unflagged name is dead weight the walker never reaches.
const { BUILTIN_SCALARS } = require('../dist/runtime/builtin-scalars.generated.js');
const {
  createDefaultScalarParseRegistry,
  createDefaultScalarNormalizeRegistry,
} = require('../dist/runtime/parse/registry.js');

// BUILTIN_SCALARS is keyed by the underscore form (Temporal_DateTime); the
// registries key by canonical name (Temporal.DateTime), which is how the
// registry code itself looks a catalog entry up.
function flagged(flag) {
  return Object.keys(BUILTIN_SCALARS)
    .filter((key) => BUILTIN_SCALARS[key][flag])
    .map((key) => key.replace(/_/g, '.'))
    .sort((a, b) => a.localeCompare(b));
}

let failures = 0;
function check(label, got, want) {
  if (want.length === 0) {
    failures++;
    console.error(`${label}: the catalog flags no scalar; the check proves nothing`);
  }
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failures++;
    console.error(`${label}: registry names ${JSON.stringify(got)} != catalog flags ${JSON.stringify(want)}`);
  }
}

check('parse', createDefaultScalarParseRegistry().names(), flagged('hasCustomParse'));
check('normalize', createDefaultScalarNormalizeRegistry().names(), flagged('hasCustomNormalize'));

if (failures > 0) {
  console.error(`parse-registry-names: ${failures} failure(s)`);
  process.exit(1);
}
console.log(
  `parse-registry-names: ${createDefaultScalarParseRegistry().names().length} parse, ${createDefaultScalarNormalizeRegistry().names().length} normalize names match the catalog flags`
);
