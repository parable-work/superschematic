// Parse-registry DateTime adapter must agree with the core canonical for every
// DateTime corpus vector. Pins the EDR-0076 fix that routes the adapter through
// the core (it was Luxon + Date.toISOString(), which forced `.000`/UTC and
// diverged from the core's sub-second-preserving, offset-keeping RFC3339Nano).
// Lived in superscalar's conformance runner until the runtime moved here.
const fs = require('node:fs');
const path = require('node:path');

const { scalarIdByCanonical } = require('superscalar/scalars');
const { napiBackend } = require('superscalar/backend');
const { createDefaultScalarParseRegistry } = require('../dist/runtime/parse/registry.js');

// Temporal.DateTime is a generic scalar, so its vectors are in the superscalar
// corpus (the Parable extension file has no DateTime entries). That file is not
// committed: `make -C utils/parable-scalars corpus` copies it out of the pinned
// superscalar checkout (make test-scalar-lib-ts runs the sync first).
const corpus = JSON.parse(
  fs.readFileSync(
    path.join(__dirname, '..', '..', '..', '..', 'parable-scalars', 'conformance', 'core-scalars.v2.json'),
    'utf8'
  )
);

let failures = 0;
const dtAdapter = createDefaultScalarParseRegistry().get('Temporal.DateTime');
const dtId = scalarIdByCanonical['Temporal.DateTime'];
const dtBackend = napiBackend();
let checked = 0;
for (const c of corpus.scalars['Temporal.DateTime'].accepted || []) {
  if (c.unresolved || c.normalized === undefined) continue;
  checked++;
  const [got, errs] = dtAdapter(c.input);
  const core = dtBackend.parse(dtId, c.input);
  if (errs.length > 0 || got !== core) {
    failures++;
    console.error(
      `registry DateTime adapter parse(${JSON.stringify(c.input)}) = ${JSON.stringify(got)} errs=${JSON.stringify(errs)}, core = ${JSON.stringify(core)}`
    );
  }
}
if (checked === 0) {
  failures++;
  console.error('no Temporal.DateTime accepted vectors found in the corpus');
}

if (failures > 0) {
  console.error(`parse-registry-datetime: ${failures} failure(s)`);
  process.exit(1);
}
console.log(`parse-registry-datetime: ${checked} DateTime vectors agree with the core`);
