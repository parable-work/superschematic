// Loads an engine document the enginedoc generator wrote into
// @superschematic/engine, as a server will (D58): it opens an engine over a
// new SQLite file with the document's meta-schema, tool options and
// namespace, defines every schema-file document, publishes each, then
// creates the instances it is given, and prints what it did as JSON.
//
// bun load.ts <engine package dir> <engine document> <instances>
//
// The engine package is runtime/engine/typescript, built (bun install
// --frozen-lockfile && bun run build); its dist/ resolves the packages it
// needs from its own node_modules. The instances file is a JSON list of
// { schema, id, data, behaviors? }, created in order, so an instance a
// link names comes first.
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

interface EngineDocument {
  version: number;
  namespace: string;
  tools: Record<string, unknown>;
  behaviors: { name: string; extension?: string; package?: string }[];
  schemas: { name: string }[];
  metaSchema: Record<string, unknown>;
}

interface Instance {
  schema: string;
  id: string;
  data: Record<string, unknown>;
  behaviors?: Record<string, unknown>;
}

const [packageDir, documentPath, instancesPath] = process.argv.slice(2);
if (!packageDir || !documentPath || !instancesPath) {
  throw new Error('usage: bun load.ts <engine package dir> <engine document> <instances>');
}
const engineModule = await import(pathToFileURL(join(packageDir, 'dist', 'index.js')).href);
const doc = JSON.parse(readFileSync(documentPath, 'utf8')) as EngineDocument;
const instances = JSON.parse(readFileSync(instancesPath, 'utf8')) as Instance[];

// Only the core engine's behaviors run without a registration of their own.
const others = doc.behaviors.filter((behavior) => behavior.package !== '@superschematic/engine');
if (others.length > 0) {
  throw new Error(`the document composes behaviors this harness does not register: ${others.map((b) => b.name).join(', ')}`);
}

const dir = mkdtempSync(join(tmpdir(), 'engine-document-'));
const engine = engineModule.openEngine({
  path: join(dir, 'engine.db'),
  policy: engineModule.allowAll,
  metaSchema: doc.metaSchema,
  tools: doc.tools,
  namespaces: { names: [doc.namespace] },
});
const system = { subject: 'system', permissions: [] };
const target = { namespace: doc.namespace };
try {
  // Every document is defined before any is published: a link names a
  // schema the document may define after it.
  for (const schema of doc.schemas) {
    engine.schemas.define(system, schema, target);
  }
  const published = doc.schemas.map((schema) => engine.schemas.publish(system, schema.name, target));
  const created = instances.map((instance) =>
    engine.instances.create(system, instance.schema, instance.data, {
      ...target,
      id: instance.id,
      behaviors: instance.behaviors,
    })
  );
  process.stdout.write(JSON.stringify({ published, created }));
} finally {
  engine.close();
  rmSync(dir, { recursive: true, force: true });
}
