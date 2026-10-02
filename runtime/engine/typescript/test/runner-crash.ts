// runner.test.ts runs this in a child process: it opens an engine on the
// file the test names, with a reaction that writes a note and then ends the
// process before its transaction commits, as a crash would.
import { allowAll, openEngine, type DriverName } from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { ledger, mark, probe, runnerPrincipal } from './runner-fixtures.ts';

const [path, driver] = process.argv.slice(2);
const engine = openEngine({
  path,
  driver: driver as DriverName,
  policy: allowAll,
  metaSchema: openMetaSchema(),
  behaviors: [ledger],
  runner: { principal: runnerPrincipal },
});
probe.react = (context, event) => {
  mark(context, event);
  process.exit(7);
};
engine.runner.runDue();
process.exit(1);
