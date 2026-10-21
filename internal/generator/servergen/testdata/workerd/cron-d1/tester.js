// The tester of cron-d1.capnp: it fires the job Worker's scheduled handler
// through its service binding, as the Worker's Cron Trigger fires it,
// twice: the first run prunes the stand-in's two empty notes through the
// d1 binding, and the second finds none.

const CRON = '0 3 * * *';

export default {
  async test(_controller, env) {
    for (let run = 1; run <= 2; run++) {
      await env.PRUNE.scheduled({ cron: CRON, scheduledTime: Date.UTC(2026, 9, 20, 3) });
    }
    console.log('the D1 job under workerd: two runs done');
  },
};
