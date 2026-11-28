// The waking drainer of relay.capnp (#600): the HTTP runtime's queue waker,
// installed from the Worker's bindings as every drain.ts installs it, wakes
// each queue QUEUE_WAKES names through its WAKE_ binding, a service binding
// of the other drainer's relay. The test then checks, through RECORDED,
// that each wake reached the Workflow as one instance named for the second
// it is due in, that a wake the run already covers sent none, and, through
// RELAY, that the relay refuses an id the Workflow holds as the Workflow
// does, refuses a wake that names no run, and answers any request with 404.

import { queueWakesSettled, wakeAfterEnqueue } from '../../../../../runtime/http/typescript/src/queue-wake.ts';
import { installQueueWakes } from '../../../../../runtime/http/typescript/src/workers.ts';

interface Env {
  readonly QUEUE_WAKES: string;
  readonly RELAY: { fetch(request: Request): Promise<Response>; create(options: unknown): Promise<unknown> };
  readonly RECORDED: { recorded(): Promise<{ id: string; dueAt: number }[]> };
  readonly [name: string]: unknown;
}

export default {
  async test(_controller: unknown, env: Env): Promise<void> {
    const failures: string[] = [];
    const warnings: string[] = [];
    console.warn = (message: unknown) => warnings.push(String(message));
    const expect = (what: string, got: unknown, want: unknown) => {
      if (JSON.stringify(got) !== JSON.stringify(want)) failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    };

    installQueueWakes(env as never);
    const queues = Object.keys(JSON.parse(env.QUEUE_WAKES) as Record<string, unknown>).sort();
    const bindings = [...new Set(Object.values(JSON.parse(env.QUEUE_WAKES) as Record<string, { binding: string }[]>).flat().map(w => w.binding))].sort();
    const dueAt = Date.now() + 20_000;
    for (const queue of queues) wakeAfterEnqueue(queue, dueAt);
    // Covered by the run each binding was woken for: nothing more is sent.
    for (const queue of queues) wakeAfterEnqueue(queue, dueAt + 1_000);
    await queueWakesSettled();
    expect('the warnings of the wakes', warnings, []);
    const id = `due-${Math.ceil(dueAt / 1000)}`;
    // Every binding is the one relay here, so the Workflow holds one
    // instance, the first wake's, and refused the others' as a duplicate.
    expect('the instances the wakes created', await env.RECORDED.recorded(), [{ id, dueAt }]);
    expect('the bindings the wakes went through', bindings.length > 0, true);

    let refused = '';
    try {
      await env.RELAY.create({ id, params: { dueAt } });
    } catch (error) {
      refused = error instanceof Error ? error.message : String(error);
    }
    expect('a second create of one id', /already_exists/.test(refused), true);
    let invalid = '';
    try {
      await env.RELAY.create({ params: {} });
    } catch (error) {
      invalid = error instanceof Error ? error.message : String(error);
    }
    expect('a create with no run id', invalid, 'a wake names a run id and the time it is due');
    const response = await env.RELAY.fetch(new Request('https://relay/'));
    expect('a request of the relay', response.status, 404);

    if (failures.length > 0) throw new Error(failures.join('\n'));
    console.log(`a drainer's wakes through the relay: ${queues.length} queues, one instance, a duplicate refused`);
  },
};
