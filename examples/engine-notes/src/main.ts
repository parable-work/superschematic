// Runs the notes server until it is interrupted:
//
//   node src/main.ts        (Node.js 24)
//   bun src/main.ts
//
// NOTES_DB names the SQLite file (notes.db by default) and PORT the port
// (8787 by default).
import { listen, notesApp, openNotes } from './server.ts';

const path = process.env.NOTES_DB ?? 'notes.db';
const port = Number(process.env.PORT ?? 8787);

const engine = openNotes(path);
const server = await listen(notesApp(engine), port);
console.log(`engine-notes: ${server.url}/api, database ${path}`);

for (const signal of ['SIGINT', 'SIGTERM'] as const) {
  process.once(signal, async () => {
    await server.close();
    engine.close();
    process.exit(0);
  });
}
