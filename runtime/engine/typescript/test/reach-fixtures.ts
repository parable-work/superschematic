// Behaviors the cross-instance tests register: a reader that reads and
// invokes other instances and schema-level operations, and a holder that
// records references to them, hears of their changes and has schema-level
// operations.
import { EngineError, defineBehavior, type BehaviorDeclaration, type FrozenJSON } from '../dist/index.js';

const noParams = { type: 'object', additionalProperties: false } as const;
const target = {
  schema: { type: 'string', minLength: 1 },
  id: { type: 'string', minLength: 1 },
} as const;
const call = {
  ...target,
  operation: { type: 'string', minLength: 1 },
  params: { type: 'object' },
} as const;
const schemaCall = {
  schema: target.schema,
  operation: { type: 'string', minLength: 1 },
  params: { type: 'object' },
} as const;

export const readerDeclaration: BehaviorDeclaration = {
  name: 'test.Reader',
  description: 'Reads other instances and invokes their operations.',
  configSchema: { type: 'object', additionalProperties: false, properties: { partnerSchema: { type: 'string' } } },
  fields: [
    { name: 'pokes', description: 'How many times poke ran on the instance.' },
    { name: 'echo', description: "The partner's title, read when the instance names one." },
  ],
  operations: [
    {
      name: 'peek',
      paramsSchema: {
        type: 'object',
        additionalProperties: false,
        required: ['schema', 'id'],
        properties: { ...target, fields: { type: 'array', items: { type: 'string' } } },
      },
      resultSchema: true,
    },
    {
      name: 'peekMany',
      paramsSchema: {
        type: 'object',
        additionalProperties: false,
        required: ['schema', 'ids'],
        properties: { schema: target.schema, ids: { type: 'array', items: { type: 'string' } }, fields: { type: 'array', items: { type: 'string' } } },
      },
      resultSchema: { type: 'object' },
    },
    {
      name: 'configOf',
      paramsSchema: {
        type: 'object',
        additionalProperties: false,
        required: ['schema', 'behavior'],
        properties: { schema: target.schema, behavior: { type: 'string' } },
      },
      resultSchema: true,
    },
    {
      name: 'canRead',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema'], properties: { schema: target.schema } },
      resultSchema: { type: 'boolean' },
    },
    {
      name: 'poke',
      description: 'Invokes an operation of another instance, then fails when asked to.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id', 'operation'], properties: { ...call, fail: { type: 'boolean' } } },
      resultSchema: true,
      writes: true,
    },
    {
      name: 'pokeCatch',
      description: 'Invokes an operation of another instance and returns the name of what it threw.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id', 'operation'], properties: call },
      resultSchema: true,
      writes: true,
    },
    {
      name: 'pokeRead',
      description: 'Invokes an operation of another instance from a read-only operation.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id', 'operation'], properties: call },
      resultSchema: true,
    },
    {
      name: 'peekSchema',
      description: 'Invokes a schema-level operation from a read-only operation.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'operation'], properties: schemaCall },
      resultSchema: true,
    },
    {
      name: 'pokeSchema',
      description: 'Counts a poke, then invokes a schema-level operation.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'operation'], properties: schemaCall },
      resultSchema: true,
      writes: true,
    },
    {
      name: 'guarded',
      description: 'Its guard invokes an operation of another instance before it runs.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id', 'operation'], properties: call },
      resultSchema: true,
      writes: true,
    },
    {
      name: 'failAfterWrite',
      description: 'Counts a poke, then throws.',
      paramsSchema: noParams,
      resultSchema: true,
      writes: true,
    },
  ],
};

export const reader = defineBehavior<{ partnerSchema?: string }>({
  declaration: readerDeclaration,
  migrations: [{ version: 1, name: 'pokes', columns: { pokes: { type: 'integer', notNull: true, default: 0 } } }],
  guard(view, request) {
    if (request.kind === 'operation' && request.operation === 'guarded') {
      const { schema, id, operation, params } = request.params as { schema: string; id: string; operation: string; params?: FrozenJSON };
      view.instances.invoke(schema, id, operation, params);
    }
    return undefined;
  },
  operations: {
    peek(context, params) {
      const found = context.instances.get(params.schema as string, params.id as string, params.fields === undefined ? {} : { fields: params.fields as string[] });
      return found === undefined ? null : found.data;
    },
    peekMany(context, params) {
      const found = context.instances.getMany(
        params.schema as string,
        params.ids as string[],
        params.fields === undefined ? {} : { fields: params.fields as string[] }
      );
      return Object.fromEntries([...found].map(([id, record]) => [id, record.data]));
    },
    configOf(context, params) {
      return context.schemas.config(params.schema as string, params.behavior as string) ?? null;
    },
    canRead(context, params) {
      return context.schemas.readable(params.schema as string);
    },
    poke(context, params) {
      context.columns.set({ pokes: Number(context.columns.get().pokes) + 1 });
      const result = context.instances.invoke(params.schema as string, params.id as string, params.operation as string, params.params as FrozenJSON | undefined);
      if (params.fail === true) {
        throw new EngineError('invalid_argument', 'poke failed after its invoke, as asked');
      }
      return result;
    },
    pokeCatch(context, params) {
      context.columns.set({ pokes: Number(context.columns.get().pokes) + 1 });
      try {
        return { result: context.instances.invoke(params.schema as string, params.id as string, params.operation as string, params.params as FrozenJSON | undefined) };
      } catch (error) {
        return { threw: error instanceof Error ? error.name : String(error) };
      }
    },
    pokeRead(context, params) {
      return context.instances.invoke(params.schema as string, params.id as string, params.operation as string, params.params as FrozenJSON | undefined);
    },
    peekSchema(context, params) {
      return context.instances.invokeSchema(params.schema as string, params.operation as string, params.params as FrozenJSON | undefined);
    },
    pokeSchema(context, params) {
      context.columns.set({ pokes: Number(context.columns.get().pokes) + 1 });
      return context.instances.invokeSchema(params.schema as string, params.operation as string, params.params as FrozenJSON | undefined);
    },
    guarded() {
      return 'ran';
    },
    failAfterWrite(context) {
      context.columns.set({ pokes: Number(context.columns.get().pokes) + 1 });
      throw new EngineError('invalid_argument', 'failAfterWrite fails after its write');
    },
  },
  fields: {
    pokes: (view) => view.columns.get().pokes,
    echo: (view) => {
      const partner = view.data.partner;
      if (typeof partner !== 'string') {
        return undefined;
      }
      return view.instances.get(view.config.partnerSchema ?? view.schema, partner, { fields: ['echo'] })?.data.title ?? null;
    },
  },
});

export interface HolderConfig {
  /** The changes of a held instance its guard vetoes. */
  veto?: Array<'update' | 'delete' | 'operation'>;
  /** Leave the reference behind when a held instance is deleted. */
  leave?: boolean;
}

export const holderDeclaration: BehaviorDeclaration = {
  name: 'test.Holder',
  description: 'Holds references to other instances and notes what happens to them.',
  configSchema: {
    type: 'object',
    additionalProperties: false,
    properties: {
      veto: { type: 'array', items: { enum: ['update', 'delete', 'operation'] } },
      leave: { type: 'boolean' },
    },
  },
  fields: [{ name: 'held', description: 'What the instance holds: schema/id/key.' }],
  operations: [
    {
      name: 'hold',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id'], properties: { ...target, key: { type: 'string' } } },
      resultSchema: true,
      writes: true,
    },
    {
      name: 'release',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id'], properties: { ...target, key: { type: 'string' } } },
      resultSchema: { type: 'boolean' },
      writes: true,
    },
    {
      name: 'note',
      description: 'Notes what happened to a held instance.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id', 'kind'], properties: { ...target, kind: { type: 'string' } } },
      resultSchema: true,
      writes: true,
    },
    {
      name: 'notes',
      paramsSchema: noParams,
      resultSchema: { type: 'array', items: { type: 'string' } },
    },
    {
      name: 'holding',
      description: 'The references the engine holds for the instance.',
      paramsSchema: noParams,
      resultSchema: { type: 'array' },
    },
    {
      name: 'holders',
      description: 'The instances of the schema that hold one.',
      scope: 'schema',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id'], properties: target },
      resultSchema: { type: 'array', items: { type: 'string' } },
    },
    {
      name: 'releaseAll',
      description: 'Releases one held instance from every holder, through release.',
      scope: 'schema',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['schema', 'id'], properties: target },
      resultSchema: { type: 'integer' },
      writes: true,
    },
    {
      name: 'scribble',
      description: 'Tries to write its table without an instance.',
      scope: 'schema',
      paramsSchema: noParams,
      resultSchema: true,
      writes: true,
    },
  ],
};

function key(view: { namespace: string; schema: string; id: string }): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

export const holder = defineBehavior<HolderConfig>({
  declaration: holderDeclaration,
  migrations: [
    {
      version: 1,
      name: 'holds',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('holds')} (
          namespace TEXT NOT NULL, schema TEXT NOT NULL, id TEXT NOT NULL,
          target_schema TEXT NOT NULL, target_id TEXT NOT NULL, key TEXT NOT NULL,
          PRIMARY KEY (namespace, schema, id, target_schema, target_id, key)) STRICT`);
        sql.run(`CREATE TABLE ${sql.table('notes')} (
          namespace TEXT NOT NULL, schema TEXT NOT NULL, id TEXT NOT NULL, note TEXT NOT NULL) STRICT`);
      },
    },
  ],
  operations: {
    hold(context, params) {
      const k = (params.key as string | undefined) ?? '';
      context.references.add(params.schema as string, params.id as string, k);
      context.sql.run(`INSERT INTO ${context.sql.table('holds')} VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, [
        ...key(context),
        params.schema as string,
        params.id as string,
        k,
      ]);
      return null;
    },
    release(context, params) {
      const k = (params.key as string | undefined) ?? '';
      context.sql.run(`DELETE FROM ${context.sql.table('holds')} WHERE namespace = ? AND schema = ? AND id = ? AND target_schema = ? AND target_id = ? AND key = ?`, [
        ...key(context),
        params.schema as string,
        params.id as string,
        k,
      ]);
      return context.references.remove(params.schema as string, params.id as string, k);
    },
    note(context, params) {
      context.sql.run(`INSERT INTO ${context.sql.table('notes')} VALUES (?, ?, ?, ?)`, [
        ...key(context),
        `${params.kind as string} ${params.schema as string} ${params.id as string}`,
      ]);
      return null;
    },
    notes(context) {
      return context.sql
        .all(`SELECT note FROM ${context.sql.table('notes')} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY rowid`, key(context))
        .map((row) => String(row.note));
    },
    holding(context) {
      return context.references.list();
    },
  },
  schemaOperations: {
    holders(context, params) {
      return context.sql
        .all(`SELECT id FROM ${context.sql.table('holds')} WHERE namespace = ? AND schema = ? AND target_schema = ? AND target_id = ? ORDER BY id`, [
          context.namespace,
          context.schema,
          params.schema as string,
          params.id as string,
        ])
        .map((row) => String(row.id));
    },
    releaseAll(context, params) {
      const holders = context.sql.all(
        `SELECT id, key FROM ${context.sql.table('holds')} WHERE namespace = ? AND schema = ? AND target_schema = ? AND target_id = ? ORDER BY id`,
        [context.namespace, context.schema, params.schema as string, params.id as string]
      );
      for (const row of holders) {
        context.instances.invoke(context.schema, String(row.id), 'release', { schema: params.schema as string, id: params.id as string, key: String(row.key) });
      }
      return holders.length;
    },
    scribble(context) {
      (context.sql as unknown as { run(sql: string): unknown }).run(`DELETE FROM ${context.sql.table('notes')}`);
      return null;
    },
  },
  fields: {
    held: (view) => {
      const held = view.references.list().map((reference) => `${reference.schema}/${reference.id}/${reference.key}`);
      return held.length > 0 ? held : undefined;
    },
  },
  guardReference(view, reference, request) {
    if ((view.config.veto ?? []).some((kind) => kind === request.kind)) {
      return `${view.schema} ${view.id} holds it (${reference.key || 'no key'})`;
    }
    return undefined;
  },
  afterReferenceChange(context, reference, change) {
    // Its own write changed what it holds: noting it now would be a cycle.
    if (context.writing) {
      return;
    }
    context.instances.invoke(context.schema, context.id, 'note', { schema: reference.schema, id: reference.id, kind: change.kind });
    if (change.kind === 'delete' && context.config.leave !== true) {
      context.instances.invoke(context.schema, context.id, 'release', { schema: reference.schema, id: reference.id, key: reference.key });
    }
  },
});

export const reachBehaviors = [reader, holder];
