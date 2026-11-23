// A server written by hand on development mode (D117): Bun.serve mounts
// the library's routes, the engine's paths under /namespaces/default, over
// a SQLite file, and runs the deadlines each second as they fall due; the
// change queue runs after each request.
//
//   bun run build
//   bun examples/serve.ts                 # dev.db, http://127.0.0.1:4500
//   DEV_DB=:memory: PORT=0 bun examples/serve.ts
//
//   curl -X POST localhost:4500/namespaces/default/schemas \
//     -H 'content-type: application/json' \
//     -d '{"name": "Note", "fields": [{"name": "title", "typeRef": {"name": "string"}}]}'
//
// A request's caller is the subject X-Superschematic-Subject names, or
// developer. Anyone who reaches the server is whoever they say, so it
// listens on 127.0.0.1.
import { devCallers } from '@superschematic/rules-handler';
import { openDev } from '@superschematic/rules-handler/sqlite';

const dev = await openDev(process.env.DEV_DB ?? 'dev.db', {
  authenticate: devCallers({ subject: 'developer', permissions: [] }),
});

const server = Bun.serve({
  hostname: '127.0.0.1',
  port: Number(process.env.PORT ?? 4500),
  fetch: request => dev.http(request),
});

const runner = setInterval(() => dev.runDue(), 1000);
process.on('SIGTERM', () => {
  clearInterval(runner);
  void server.stop().then(() => process.exit(0));
});

console.log(`serving ${new URL('namespaces/default', server.url).href}`);
