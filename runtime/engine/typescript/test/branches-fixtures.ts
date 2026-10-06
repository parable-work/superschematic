// What the Branches tests share: a Recipe schema whose instances are graph
// roots, with steps ordered by position, ingredients under a step and at
// most one cover; an engine on a file the test can read itself; and the
// calls a test makes on an instance.
import {
  allowAll,
  openDriver,
  openEngine,
  type DriverName,
  type Engine,
  type EngineOptions,
  type Principal,
  type Row,
  type SqlValue,
} from '../dist/index.js';
import { alice, clone, freshPath, schemaDocument, track } from './helpers.ts';

/** The Branches config of the Recipe document. */
export const recipeConfig = {
  kinds: {
    step: { type: 'Step', order: 'position', units: { timings: 'keyed' } },
    ingredient: { type: 'Ingredient', parent: { key: 'stepKey', of: 'step' } },
    cover: { type: 'Cover', singleton: true },
  },
};

export interface RecipeDocument {
  name: string;
  types: Record<string, { name: string; role: string; behaviors?: Array<{ name: string; config?: unknown }>; fields: Array<Record<string, unknown>> }>;
  [member: string]: unknown;
}

/**
 * recipeDocument is a schema, Recipe unless name says otherwise, whose
 * instance type composes Branches with config, beside the kinds' types;
 * with config null it composes nothing.
 */
export function recipeDocument(config: unknown = recipeConfig, name = 'Recipe'): RecipeDocument {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }], {
    enums: { Unit: { name: 'Unit', values: [{ name: 'GRAM', serializedAs: 'g' }, { name: 'CUP', serializedAs: 'cup' }] } },
    types: {
      Step: {
        name: 'Step',
        role: 'EmbeddedStruct',
        fields: [
          { name: 'instruction', typeRef: { name: 'string' }, required: true },
          { name: 'position', typeRef: { name: 'Int' }, required: true },
          { name: 'timings', typeRef: { name: 'Generic.JSON' } },
        ],
      },
      Ingredient: {
        name: 'Ingredient',
        role: 'EmbeddedStruct',
        fields: [
          { name: 'stepKey', typeRef: { name: 'Identity.UUID' }, required: true },
          { name: 'quantity', typeRef: { name: 'string' }, required: true },
          { name: 'unit', typeRef: { name: 'Unit' } },
        ],
      },
      Cover: { name: 'Cover', role: 'EmbeddedStruct', fields: [{ name: 'photoUrl', typeRef: { name: 'string' }, required: true }] },
    },
  }) as unknown as RecipeDocument;
  if (config !== null) {
    document.types[name].behaviors = [{ name: 'Branches', config: clone(config) }];
  }
  return document;
}

/** publish defines and publishes a document as alice. */
export function publish(engine: Engine, document: RecipeDocument): void {
  engine.schemas.define(alice, document as unknown as Record<string, unknown>);
  engine.schemas.publish(alice, document.name);
}

/** A Branches engine on a file of its own, and a reader of that file. */
export interface Opened {
  readonly engine: Engine;
  /** Runs a read on the engine's file through a connection of its own. */
  all(sql: string, params?: readonly SqlValue[]): Row[];
}

/** openBranches opens an engine on a fresh file, with a reader of the file beside it; the test cleans both up. */
export function openBranches(driver: DriverName, options: Partial<EngineOptions> = {}): Opened {
  const path = freshPath();
  const engine = track(openEngine({ path, policy: allowAll, driver, ...options }));
  let reader: ReturnType<typeof openDriver> | undefined;
  return {
    engine,
    all(sql, params = []) {
      if (reader === undefined) {
        reader = track(openDriver(path, driver));
      }
      return reader.all(sql, params);
    },
  };
}

/** A ref as Branches returns it. */
export interface Ref {
  id: string;
  name: string;
  parent: string | null;
  base: string | null;
  head: string | null;
  sealed: boolean;
  discarded: boolean;
  version: number;
  createdAt: string;
  createdBy: string;
  updatedAt: string;
  updatedBy: string;
}

/** A commit as Branches returns it. */
export interface Commit {
  id: string;
  ref: string;
  parent: string | null;
  message: string;
  sequence: number | null;
  contentHash: string;
  createdAt: string;
  createdBy: string;
  snapshot: boolean;
}

export type Tree = Record<string, Array<Record<string, unknown>>>;

/** The calls a test makes on one instance of a schema, as a principal. */
export class Calls {
  readonly engine: Engine;
  readonly id: string;
  readonly principal: Principal;
  readonly schema: string;

  constructor(engine: Engine, id: string, principal: Principal = alice, schema = 'Recipe') {
    this.engine = engine;
    this.id = id;
    this.principal = principal;
    this.schema = schema;
  }

  as(principal: Principal): Calls {
    return new Calls(this.engine, this.id, principal, this.schema);
  }

  invoke<T>(operation: string, params: Record<string, unknown> = {}): T {
    return this.engine.instances.invoke(this.principal, this.schema, this.id, operation, params) as T;
  }

  refs(): Ref[] {
    return this.invoke<{ items: Ref[] }>('refs').items;
  }

  main(): Ref {
    return this.refs().find((ref) => ref.parent === null) as Ref;
  }

  branch(name: string, from: Ref = this.main()): Ref {
    return this.invoke<Ref>('branch', { fromRef: from.id, name });
  }

  save(ref: Ref, edits: Record<string, unknown>): { ref: Ref; saved: Tree } {
    return this.invoke('save', { ref: ref.id, version: ref.version, edits });
  }

  commit(ref: Ref, options: { message?: string; tag?: boolean } = {}): { ref: Ref; commit: Commit | null } {
    return this.invoke('commit', { ref: ref.id, version: ref.version, ...options });
  }

  merge(source: Ref, target: Ref, options: Record<string, unknown> = {}): { ref: Ref; commit: Commit | null; conflicts: Array<Record<string, unknown>> } {
    return this.invoke('merge', { source: source.id, target: target.id, targetVersion: target.version, ...options });
  }

  compose(ref: Ref): { tree: Tree; contentHash: string; findings: unknown[] } {
    return this.invoke('compose', { ref: ref.id });
  }

  /** change writes rows on a new draft of the primary line, commits it and merges it in, tagged; it returns the merge's commit. */
  change(name: string, edits: Record<string, unknown>): Commit {
    const draft = this.branch(name);
    const saved = this.save(draft, edits);
    const committed = this.commit(saved.ref);
    return this.merge(committed.ref, this.main(), { tag: true }).commit as Commit;
  }
}

/** contentOf is each row of a tree's kind with only the columns named. */
export function contentOf(tree: Tree, kind: string, columns: readonly string[]): Array<Record<string, unknown>> {
  return (tree[kind] ?? []).map((row) => Object.fromEntries(columns.map((column) => [column, row[column]])));
}
