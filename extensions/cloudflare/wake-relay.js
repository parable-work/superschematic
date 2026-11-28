// A drainer's wake relay on Cloudflare (#600; wakes.go says why): the
// create a queue waker makes of a wake's binding, over the service
// binding's RPC, handed to the woken drainer's Workflow (WORKFLOW).

import { WorkerEntrypoint } from 'cloudflare:workers';

export default class WakeRelay extends WorkerEntrypoint {
  async create(options) {
    const id = options?.id;
    const dueAt = options?.params?.dueAt;
    if (typeof id !== 'string' || id === '' || typeof dueAt !== 'number' || !Number.isFinite(dueAt)) {
      throw new TypeError('a wake names a run id and the time it is due');
    }
    const workflow = this.env.WORKFLOW;
    if (typeof workflow?.create !== 'function') throw new Error('the relay holds no WORKFLOW binding');
    await workflow.create({ id, params: { dueAt } });
  }

  async fetch() {
    return new Response('not found', { status: 404 });
  }
}
