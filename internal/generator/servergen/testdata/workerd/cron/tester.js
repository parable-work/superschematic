// The tester of cron.capnp: it fires each job Worker's scheduled handler
// through its service binding, as the Worker's Cron Trigger fires it, and
// checks how each run ends: ok's first try succeeds; flaky's first fails
// and its second, the retry, succeeds; broken's two fail, so the run
// throws, as Cloudflare records a failed run; and hung's two each outlive
// the job's two-second timeout, so the run throws too. Then it runs the
// job's Workflow as Cloudflare's engine runs an instance (D64, amended):
// ok's for the bundle its Worker runs completes, its tries within one step
// that retries nothing, the payload as an object or as JSON; one for
// another bundle fails before the step; and hung's fails with the error its
// last try threw, its timeout.

import { RpcTarget } from 'cloudflare:workers';

const CRON = '0 4 * * *';

/** The bundle the Workers' SUPERSCHEMATIC_BUNDLE binding names. */
const BUNDLE = 'b7b7b7';

/** A Workflow's step, as the engine hands it to run: each do runs its callback once. */
class Step extends RpcTarget {
  calls = [];
  async do(name, config, callback) {
    this.calls.push({ name, config });
    return await callback();
  }
}

/** An instance's event. */
function event(payload) {
  return { payload, instanceId: 'instance-1', timestamp: new Date(Date.UTC(2026, 9, 21)) };
}

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
    for (const payload of [{ bundle: BUNDLE }, JSON.stringify({ bundle: BUNDLE })]) {
      const step = new Step();
      try {
        const result = await env.OK_RUN.run(event(payload), step);
        const calls = JSON.stringify(step.calls);
        if (result !== 'done' || calls !== '[{"name":"run","config":{"retries":{"limit":0,"delay":0},"timeout":64000}}]') {
          failures.push(`ok's Workflow returned ${result} with steps ${calls}`);
        }
      } catch (error) {
        failures.push(`ok's Workflow failed for ${typeof payload}: ${error}`);
      }
    }
    const other = new Step();
    try {
      await env.OK_RUN.run(event({ bundle: 'c0ffee' }), other);
      failures.push("ok's Workflow ran another bundle");
    } catch (error) {
      const want = 'job ts-reports-compile-reports runs bundle b7b7b7, but the last deploy rolled out c0ffee';
      if (!String(error).includes(want) || other.calls.length > 0) failures.push(`ok's Workflow for another bundle failed with ${error} after ${other.calls.length} steps, want ${want} before any`);
    }
    try {
      await env.HUNG_RUN.run(event({ bundle: BUNDLE }), new Step());
      failures.push("hung's Workflow succeeded");
    } catch (error) {
      const want = "job ts-reports-compile-reports failed on each of its 2 tries: timed out after 2s, the job's timeout";
      if (!String(error).includes(want)) failures.push(`hung's Workflow failed with ${error}, want ${want}`);
    }
    if (failures.length > 0) throw new Error(`the job under workerd:\n${failures.join('\n')}`);
    console.log('the job under workerd: ok, flaky after a retry, broken and hung failed; its Workflow ran ok, refused another bundle and failed hung');
  },
};
