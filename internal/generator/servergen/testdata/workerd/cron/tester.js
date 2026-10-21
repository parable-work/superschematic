// The tester of cron.capnp: it fires each job Worker's scheduled handler
// through its service binding, as the Worker's Cron Trigger fires it, and
// checks how each run ends: ok's first try succeeds; flaky's first fails
// and its second, the retry, succeeds; broken's two fail, so the run
// throws, as Cloudflare records a failed run; and hung's two each outlive
// the job's two-second timeout, so the run throws too.

const CRON = '0 4 * * *';

export default {
  async test(_controller, env) {
    const failures = [];
    for (const [name, worker] of [
      ['ok', env.OK],
      ['flaky', env.FLAKY],
    ]) {
      try {
        await worker.scheduled({ cron: CRON, scheduledTime: Date.UTC(2026, 9, 20, 4) });
      } catch (error) {
        failures.push(`${name}'s run failed: ${error}`);
      }
    }
    for (const [name, worker, want] of [
      ['broken', env.BROKEN, 'job ts-reports-compile-reports failed on each of its 2 tries: try 2 of CompileReports fails'],
      ['hung', env.HUNG, "job ts-reports-compile-reports failed on each of its 2 tries: timed out after 2s, the job's timeout"],
    ]) {
      try {
        await worker.scheduled({ cron: CRON });
        failures.push(`${name}'s run succeeded`);
      } catch (error) {
        if (!String(error).includes(want)) failures.push(`${name}'s run failed with ${error}, want ${want}`);
      }
    }
    if (failures.length > 0) throw new Error(`the job under workerd:\n${failures.join('\n')}`);
    console.log('the job under workerd: ok, flaky after a retry, broken and hung failed');
  },
};
