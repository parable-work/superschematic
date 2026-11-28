// The stand-in for a drainer's Workflow, which workerd does not run (D151):
// each instance created is recorded, and an id it holds already is refused
// as a Workflow refuses it.

import { WorkerEntrypoint } from 'cloudflare:workers';

const created = [];

export class Workflow extends WorkerEntrypoint {
  async create(options) {
    if (created.some(c => c.id === options.id)) throw new Error(`instance.already_exists: ${options.id}`);
    created.push({ id: options.id, dueAt: options.params?.dueAt });
    return { id: options.id };
  }

  async recorded() {
    return created.map(c => ({ ...c }));
  }
}

export default {
  async fetch() {
    return new Response('not found', { status: 404 });
  },
};
