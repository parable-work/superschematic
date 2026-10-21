// A Worker whose config is refused: it reads its database field at module
// scope, as a generated fetch.ts does, and its env sets a D1 binding's name
// and not its database. The read throws while workerd loads the script, as
// it would refuse a version's upload, naming the variable
// (scripts/workerd-test.mjs expects workerd to fail with the message).
import { env } from 'cloudflare:workers';
import { loadDatabase } from '../../../../dist/index.js';
import { stackEnvOf } from '../../../../dist/workers.js';

const database = loadDatabase('SHOP_DB_DATABASE', stackEnvOf(env));

export default {
  async fetch(): Promise<Response> {
    return Response.json(database);
  },
  async test(): Promise<void> {},
};
