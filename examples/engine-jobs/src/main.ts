// Runs the jobs server, and the engine's runner beside it, until it is
// interrupted:
//
//   node src/main.ts        (Node.js 24)
//   bun src/main.ts
//
// JOBS_DB names the SQLite file (jobs.db by default) and PORT the port
// (8788 by default).
import { jobsApp, listen, openJobs } from './server.ts';

const path = process.env.JOBS_DB ?? 'jobs.db';
const port = Number(process.env.PORT ?? 8788);

const engine = openJobs(path);
// The runner expires lapsed leases, misses silent workers and settles
// batches: it runs what is due now, then wakes on each commit and on a
// timer when a sweep comes due.
engine.runner.start();
const server = await listen(jobsApp(engine), port);
console.log(`engine-jobs: ${server.url}/api, database ${path}`);

for (const signal of ['SIGINT', 'SIGTERM'] as const) {
  process.once(signal, async () => {
    await server.close();
    engine.close();
    process.exit(0);
  });
}
