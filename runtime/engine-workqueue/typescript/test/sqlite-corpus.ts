// The engine file the TypeScript engine wrote, as data the engine opens
// (D58): runtime/engine/testdata/sqlite/typescript.sql, with every core
// and work-queue behavior in use, frozen at the cutover as the last file
// that engine wrote, and the reads expected.json holds of it once the
// engine has migrated it. writeCorpus runs the script that wrote it, on the
// engine today, whose Comments posts beside the instance (D77) where the
// TypeScript engine's commented, so the file it writes reads as
// written.json holds; dumpDatabase writes a file as SQL text, one
// statement per line, loadDatabase loads that text into an empty file, and
// readCorpus makes the reads.
// runtime/engine/testdata/sqlite/README.md gives the format and the
// script.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { allowAll, openEngine, type Engine, type EngineOptions, type Principal, type SqliteConnection } from '@superschematic/engine';

import { workQueueBehaviors } from '../dist/index.js';
import { openConnection } from '../../../engine/typescript/dist/native/sqlite.js';

type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };

/** The directory the corpus lives in. */
export const SQLITE_TESTDATA = fileURLToPath(new URL('../../../engine/testdata/sqlite/', import.meta.url));

/** When the script starts: 2026-10-09T09:00:00Z. */
export const START = Date.UTC(2026, 9, 9, 9);

/** When the script ends, and the clock the reads run at: 2027-01-01T00:00:00Z. */
export const END = Date.UTC(2027, 0, 1);

/** The principals the script acts as, by name. */
export const PRINCIPALS: Record<string, Principal> = {
  alice: { subject: 'alice', permissions: [] },
  wes: { subject: 'wes', permissions: [] },
  rae: { subject: 'rae', permissions: ['documents.review'] },
  pat: { subject: 'pat', permissions: ['documents'] },
  cleo: { subject: 'cleo', permissions: ['expenses.approve'] },
  ann: { subject: 'ann', permissions: ['expenses.approve'] },
  bob: { subject: 'bob', permissions: ['expenses.approve'] },
  pia: { subject: 'pia', permissions: ['expenses.pay'] },
  lee: { subject: 'lee', permissions: ['requests.timers'] },
  emma: { subject: 'emma', permissions: ['notes.embed'] },
  wren: { subject: 'wren', permissions: [] },
  otto: { subject: 'otto', permissions: [] },
  lena: { subject: 'lena', permissions: ['jobs.assign'] },
  runner: { subject: 'runner', permissions: ['jobs.override'] },
};

/** The options the script opens the engine with, but its path, ids and clock. */
export const OPTIONS = {
  policy: 'allowAll',
  namespaces: { names: ['tree', 'billing'] },
  runner: { principal: 'runner' },
  values: { thresholdBytes: 1024 },
} as const;

// seeded is the ids the script draws, the engine's and the version
// graph's: the n-th, from 1, is the version-4 UUID made of the first 16
// bytes of SHA-256("superschematic-engine-sqlite/<n>").
function seeded(): () => string {
  let drawn = 0;
  return () => {
    drawn += 1;
    const bytes = createHash('sha256').update(`superschematic-engine-sqlite/${drawn}`).digest().subarray(0, 16);
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = bytes.toString('hex');
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20, 32)}`;
  };
}

/** openCorpusEngine opens an engine on path with the corpus's options, ids and a clock that reads clock.now. */
export function openCorpusEngine(path: string, clock: { now: number }, ids: () => string = seeded()): Engine {
  const options: EngineOptions = {
    path,
    policy: allowAll,
    behaviors: workQueueBehaviors,
    namespaces: { names: [...OPTIONS.namespaces.names] },
    runner: { principal: PRINCIPALS.runner },
    values: { thresholdBytes: OPTIONS.values.thresholdBytes },
    ids,
    clock: () => clock.now,
  };
  return openEngine(options);
}

function fixture(service: string, file: string): Record<string, unknown> {
  return JSON.parse(readFileSync(new URL(`../../../../internal/loader/testdata/services/${service}/src/${file}.schema.json`, import.meta.url), 'utf8')) as Record<
    string,
    unknown
  >;
}

// workQueueType reads one type of fixture-workqueue-json as a schema of its own.
function workQueueType(name: string, file: string): Record<string, unknown> {
  const type = fixture('fixture-workqueue-json', file) as { name: string };
  return { kind: 'General', name, types: { [type.name]: type } };
}

/**
 * writeCorpus runs the corpus's script on a file at path: the engine, the
 * core's behaviors and the work queue's, each in use. It draws every random id,
 * the version graph's too, from the seeded generator, and leaves the
 * engine closed.
 */
export function writeCorpus(path: string): void {
  const ids = seeded();
  const random = globalThis.crypto.randomUUID;
  globalThis.crypto.randomUUID = ids as typeof globalThis.crypto.randomUUID;
  const clock = { now: START };
  const engine = openCorpusEngine(path, clock, ids);
  try {
    script(engine, clock);
  } finally {
    engine.close();
    globalThis.crypto.randomUUID = random;
  }
}

function script(engine: Engine, clock: { now: number }): void {
  const p = PRINCIPALS;
  const tick = (ms = 1000): void => {
    clock.now += ms;
  };
  const publish = (document: Record<string, unknown>, namespace?: string): void => {
    engine.schemas.define(p.alice, document, { namespace });
    engine.schemas.publish(p.alice, document.name as string, { namespace });
  };
  const invoke = (who: Principal, schema: string, id: string, operation: string, params: Record<string, unknown> = {}, namespace?: string): unknown =>
    engine.instances.invoke(who, schema, id, operation, params, { namespace });

  // Every schema, in three namespaces: the default one, tree, where the
  // project tree of Reactions has its own projects, and billing, where the
  // numbered invoices have theirs.
  const documents = fixture('fixture-behaviors-json', 'document');
  const tasks = fixture('fixture-cross-instance-json', 'task');
  const projects = fixture('fixture-rollups-json', 'project');
  for (const document of [
    documents,
    tasks,
    projects,
    fixture('fixture-search-json', 'note'),
    fixture('fixture-variants-json', 'step'),
    fixture('fixture-branches-json', 'recipe'),
    fixture('fixture-staterules-json', 'invoice'),
    fixture('fixture-approvals-json', 'expense'),
    fixture('fixture-timers-json', 'request'),
    fixture('fixture-workqueue-json', 'job'),
    workQueueType('workers', 'worker'),
    workQueueType('steps', 'step'),
    workQueueType('batches', 'batch'),
  ]) {
    publish(document);
  }
  publish(fixture('fixture-reactions-json', 'project'), 'tree');
  for (const document of [documents, tasks, projects, fixture('fixture-numbering-json', 'invoice')]) {
    publish(document, 'billing');
  }
  engine.runner.runDue();

  // Workflow, Comments and Revisions: a document is commented on, revised
  // through a proposal and published.
  tick();
  engine.instances.create(p.wes, 'documents', { title: 'Launch plan' }, { id: 'doc-1' });
  invoke(p.wes, 'documents', 'doc-1', 'post', { body: 'First pass is up.' });
  invoke(p.wes, 'documents', 'doc-1', 'transition', { to: 'review' });
  invoke(p.wes, 'documents', 'doc-1', 'propose', { patch: { body: 'The plan, in full.' }, note: 'Adds the body.' });
  tick();
  invoke(p.rae, 'documents', 'doc-1', 'approve', { proposal: 1 });
  invoke(p.rae, 'documents', 'doc-1', 'post', { body: 'Approved the body.', thread: 1 });
  invoke(p.pat, 'documents', 'doc-1', 'transition', { to: 'published' });

  // Dependencies, Links and Rollups: tasks of a project wait on each
  // other and on a document, and the project rolls them up.
  tick();
  engine.instances.create(p.wes, 'documents', { title: 'Design' }, { id: 'design' });
  engine.instances.create(p.wes, 'projects', { title: 'Launch' }, { id: 'launch' });
  engine.instances.create(p.wes, 'tasks', { title: 'Plan' }, { id: 'plan', behaviors: { Links: { project: 'launch' } } });
  engine.instances.create(p.wes, 'tasks', { title: 'Build' }, { id: 'build', behaviors: { Links: { project: 'launch' } } });
  invoke(p.wes, 'tasks', 'build', 'addBlocker', { id: 'plan' });
  invoke(p.wes, 'tasks', 'build', 'addBlocker', { schema: 'documents', id: 'design' });
  invoke(p.wes, 'tasks', 'build', 'link', { name: 'spec', id: 'design' });
  invoke(p.wes, 'tasks', 'build', 'link', { name: 'parent', id: 'plan' });
  engine.instances.create(p.wes, 'tasks', { title: 'Test' }, { id: 'test', behaviors: { Links: { project: 'launch', parent: 'plan' }, Dependencies: { blockers: [{ id: 'plan' }] } } });
  tick();
  engine.instances.update(p.wes, 'documents', 'design', { body: 'Revised.' });
  invoke(p.wes, 'tasks', 'plan', 'transition', { to: 'doing' });
  invoke(p.wes, 'tasks', 'plan', 'transition', { to: 'done' });
  invoke(p.wes, 'tasks', 'test', 'transition', { to: 'dropped' });

  // Search, and the value store: three notes, one with a body past the
  // threshold, and a vector for one.
  tick();
  engine.instances.create(p.wes, 'notes', { title: 'Standup', body: 'The release slips a week.' }, { id: 'n1' });
  engine.instances.create(p.wes, 'notes', { title: 'Release plan', body: 'Dates and owners.' }, { id: 'n2' });
  engine.instances.create(p.wes, 'notes', { title: 'Minutes', body: `Release notes, at length. ${'The minutes run on. '.repeat(80)}` }, { id: 'n3' });
  const stale = engine.instances.invokeSchema(p.emma, 'notes', 'staleEmbeddings', { limit: 1 }) as { items: Array<{ id: string; sourceHash: string }> };
  engine.instances.invokeSchema(p.emma, 'notes', 'settleEmbeddings', {
    items: stale.items.map((item) => ({ id: item.id, sourceHash: item.sourceHash, vector: Array.from({ length: 384 }, (_, index) => (index % 7) / 8) })),
  });

  // Constants and Variants: a step keeps its kind, and its result has the kind's shape.
  tick();
  engine.instances.create(p.wes, 'Step', { title: 'Lint', kind: 'verify', result: { passed: true, checks: [{ name: 'eslint', ok: true }] } }, { id: 'lint' });
  engine.instances.update(p.wes, 'Step', 'lint', { result: { passed: false, checks: [{ name: 'eslint', ok: false }] } });

  // Branches: a recipe's draft merges into its primary line as a tagged
  // commit, which is released.
  tick();
  engine.instances.create(p.wes, 'Recipe', { title: 'Soup' }, { id: 'soup' });
  type Ref = { id: string; version: number };
  const recipe = <T>(operation: string, params: Record<string, unknown> = {}): T => invoke(p.wes, 'Recipe', 'soup', operation, params) as T;
  const [main] = recipe<{ items: Ref[] }>('refs').items;
  const draft = recipe<Ref>('branch', { fromRef: main.id, name: 'first' });
  const saved = recipe<{ ref: Ref; saved: { step: Array<{ entity_key: string }> } }>('save', {
    ref: draft.id,
    version: draft.version,
    edits: { step: { upsert: [{ instruction: 'Boil', position: 1 }] }, cover: { upsert: [{ photoUrl: 'soup.jpg' }] } },
  });
  const more = recipe<{ ref: Ref }>('save', {
    ref: draft.id,
    version: saved.ref.version,
    edits: { ingredient: { upsert: [{ stepKey: saved.saved.step[0].entity_key, quantity: '1 l' }] } },
  });
  tick();
  recipe('commit', { ref: draft.id, version: more.ref.version, message: 'First cut' });
  const merged = recipe<{ commit: { id: string } }>('merge', { source: draft.id, target: main.id, targetVersion: main.version, tag: true });
  recipe('releaseCommit', { commit: merged.commit.id, version: 0 });

  // StateRules: an invoice is sent once it holds what sent requires, and paid.
  tick();
  engine.instances.create(p.wes, 'invoices', { title: 'March', customer: 'Acme' }, { id: 'march' });
  engine.instances.update(p.wes, 'invoices', 'march', { amount: 1200, lines: ['Consulting'] });
  invoke(p.wes, 'invoices', 'march', 'transition', { to: 'sent' });
  invoke(p.wes, 'invoices', 'march', 'transition', { to: 'paid' });

  // Approvals: a claim waits for two approvers' sign-offs, then one to pay it.
  tick();
  engine.instances.create(p.cleo, 'expenses', { title: 'Train to the offsite', amount: 120 }, { id: 'trip' });
  invoke(p.cleo, 'expenses', 'trip', 'transition', { to: 'submitted' });
  invoke(p.ann, 'expenses', 'trip', 'signOff', { state: 'approved' });
  invoke(p.bob, 'expenses', 'trip', 'signOff', { state: 'approved' });
  invoke(p.cleo, 'expenses', 'trip', 'transition', { to: 'approved' });
  tick();
  invoke(p.pia, 'expenses', 'trip', 'signOff', { state: 'paid' });

  // Timers: a request open four hours is escalated on the runner; a
  // waiting one's lapse is snoozed.
  tick();
  const opened = clock.now;
  engine.instances.create(p.wes, 'requests', { title: 'Printer jams', visitAt: new Date(opened + 3 * 86_400_000).toISOString() }, { id: 'jam' });
  engine.instances.create(p.wes, 'requests', { title: 'Toner' }, { id: 'toner' });
  invoke(p.wes, 'requests', 'toner', 'transition', { to: 'waiting' });
  invoke(p.lee, 'requests', 'toner', 'snoozeTimer', { deadline: 'lapse', dueAt: opened + 8 * 86_400_000 });
  clock.now = opened + 5 * 3_600_000;
  engine.runner.runDue();

  // Reactions, in tree: a project starts its parent and the last to
  // finish finishes it, on the runner.
  tick();
  for (const id of ['launch', 'design', 'build']) {
    engine.instances.create(p.wes, 'projects', { title: id }, { id, namespace: 'tree' });
  }
  for (const id of ['design', 'build']) {
    invoke(p.wes, 'projects', id, 'link', { name: 'parent', id: 'launch' }, 'tree');
  }
  invoke(p.wes, 'projects', 'design', 'transition', { to: 'doing' }, 'tree');
  engine.runner.runDue();
  invoke(p.wes, 'projects', 'design', 'transition', { to: 'done' }, 'tree');
  invoke(p.wes, 'projects', 'build', 'transition', { to: 'doing' }, 'tree');
  invoke(p.wes, 'projects', 'build', 'transition', { to: 'done' }, 'tree');
  engine.runner.runDue();

  // The work queue: jobs claimed in order, held, budgeted and retried, a
  // job whose time runs out put back; a worker that stops beating missed;
  // a batch that stamps its steps.
  tick();
  const job = (id: string, data: Record<string, unknown>): void => {
    engine.instances.create(p.alice, 'jobs', { title: id, ...data }, { id });
  };
  job('index', { topic: 'search', priority: 5, timeLimitMs: 45000 });
  job('reindex', { topic: 'search', priority: 9 });
  job('digest', { topic: 'mail', priority: 7 });
  job('mine', { topic: 'search', priority: 10 });
  invoke(p.lena, 'jobs', 'mine', 'assign', { to: 'otto' });
  engine.instances.create(p.alice, 'workers', { subject: 'wren', name: 'Wren' }, { id: 'w1' });
  const claimNext = (who: Principal, topic: string): void => {
    engine.instances.invokeSchema(who, 'jobs', 'claimNext', { match: { topic } });
  };
  invoke(p.wren, 'workers', 'w1', 'beat');
  claimNext(p.wren, 'search');
  claimNext(p.wren, 'search');
  claimNext(p.otto, 'search');
  invoke(p.wren, 'jobs', 'index', 'recordUsage', { meter: 'cpuSeconds', amount: 100 });
  invoke(p.wren, 'jobs', 'index', 'recordAttempt', { failure: 'timeout' });
  tick(20000);
  const fenced = { preconditions: { Lease: { token: 1 } } };
  engine.instances.invoke(p.wren, 'jobs', 'reindex', 'heartbeat', {}, fenced);
  engine.instances.invoke(p.wren, 'jobs', 'index', 'heartbeat', {}, fenced);
  invoke(p.wren, 'jobs', 'reindex', 'transition', { to: 'done' });
  engine.instances.invoke(p.wren, 'jobs', 'reindex', 'release', {}, fenced);
  tick(25000);
  engine.runner.runDue();
  engine.instances.create(p.alice, 'batches', { title: 'Search the archive', topic: 'search' }, { id: 'b1' });
  engine.instances.create(p.alice, 'batches', { title: 'Copy the archive', topic: 'copy' }, { id: 'b2' });

  // A namespace made while the engine runs, and archived.
  tick();
  engine.namespaces.create(p.alice, 'scratch');
  engine.namespaces.archive(p.alice, 'scratch');

  // Numbering, in billing: invoices numbered in their project's count,
  // which starts again with the year.
  clock.now = Date.UTC(2026, 11, 31);
  for (const id of ['launch', 'site']) {
    engine.instances.create(p.wes, 'projects', { title: id }, { id, namespace: 'billing' });
  }
  const bill = (id: string, project: string): void => {
    engine.instances.create(p.wes, 'invoices', { title: id, amount: 100 }, { id, namespace: 'billing', behaviors: { Links: { project } } });
  };
  bill('i1', 'launch');
  bill('i2', 'launch');
  bill('i3', 'site');
  clock.now = END;
  bill('i4', 'launch');
}

/** One read: the principal, the call and its arguments, and what it returned. */
export interface Read {
  principal?: string;
  call: string;
  args: JSONValue[];
  expect: JSONValue;
  dialect?: 'sqlite';
}

/**
 * readCorpus makes the reads expected.json holds, on an engine opened on
 * the corpus's file at END: what a second implementation that opens the
 * file must answer the same.
 */
export function readCorpus(engine: Engine): Read[] {
  const reads: Read[] = [];
  const json = (value: unknown): JSONValue => (value === undefined ? null : (JSON.parse(JSON.stringify(value)) as JSONValue));
  const read = (principal: string | undefined, call: string, args: unknown[], invoke: () => unknown, dialect?: 'sqlite'): unknown => {
    const result = invoke();
    reads.push({ ...(principal ? { principal } : {}), call, args: args.map(json), expect: json(result), ...(dialect ? { dialect } : {}) });
    return result;
  };
  const alice = PRINCIPALS.alice;
  read('alice', 'namespaces.list', [], () => engine.namespaces.list(alice));
  for (const namespace of ['default', 'tree', 'billing', 'scratch']) {
    const target = { namespace };
    const schemas = read('alice', 'schemas.list', [target], () => engine.schemas.list(alice, target)) as Array<{ name: string }>;
    for (const { name } of schemas) {
      read('alice', 'schemas.live', [name, target], () => engine.schemas.live(alice, name, target));
      read('alice', 'instances.list', [name, { namespace, limit: 500 }], () => engine.instances.list(alice, name, { namespace, limit: 500 }));
    }
    let after = 0;
    for (;;) {
      const page = read('alice', 'events.read', [{ namespace, after, limit: 500 }], () => engine.events.read(alice, { namespace, after, limit: 500 })) as {
        next: number;
        more: boolean;
      };
      if (!page.more) {
        break;
      }
      after = page.next;
    }
  }
  const operation = (schema: string, id: string, name: string, params: Record<string, unknown> = {}, namespace = 'default'): void => {
    read('alice', 'instances.invoke', [schema, id, name, params, { namespace }], () => engine.instances.invoke(alice, schema, id, name, params, { namespace }));
  };
  operation('documents', 'doc-1', 'listMessages');
  operation('documents', 'doc-1', 'listMessages', { thread: 1 });
  operation('documents', 'doc-1', 'listRevisions');
  operation('documents', 'doc-1', 'listProposals');
  operation('tasks', 'build', 'listBlockers');
  operation('tasks', 'plan', 'listDependents');
  operation('expenses', 'trip', 'listSignOffs');
  operation('Recipe', 'soup', 'refs');
  operation('Recipe', 'soup', 'releases');
  operation('Recipe', 'soup', 'released');
  operation('jobs', 'index', 'checkReserve', { meter: 'cpuSeconds', amount: 600 });
  read('alice', 'instances.invokeSchema', ['notes', 'search', { query: 'release' }], () => engine.instances.invokeSchema(alice, 'notes', 'search', { query: 'release' }), 'sqlite');
  read('alice', 'instances.invokeSchema', ['notes', 'staleEmbeddings', { limit: 10 }], () => engine.instances.invokeSchema(alice, 'notes', 'staleEmbeddings', { limit: 10 }));
  read('alice', 'instances.invokeSchema', ['jobs', 'countClaimable', { match: { topic: 'search' } }], () =>
    engine.instances.invokeSchema(alice, 'jobs', 'countClaimable', { match: { topic: 'search' } })
  );
  const n3 = read('alice', 'instances.get', ['notes', 'n3', { valueRefs: true }], () => engine.instances.get(alice, 'notes', 'n3', { valueRefs: true })) as {
    data: { body: { $value: string } };
  };
  read('alice', 'values.get', [n3.data.body.$value], () => engine.values.get(alice, n3.data.body.$value));
  read(undefined, 'runner.status', [], () => engine.runner.status());
  return reads;
}

/**
 * renderReads writes expected.json, the reads of typescript.sql, or with
 * written written.json, the reads of the file the script writes today:
 * its members on lines of their own, and each read on one.
 */
export function renderReads(reads: readonly Read[], written = false): string {
  const principals = Object.fromEntries(Object.entries(PRINCIPALS).map(([name, principal]) => [name, principal]));
  const head = {
    name: written ? 'written' : 'typescript',
    description: written
      ? 'What runtime/engine-workqueue/typescript/test/sqlite-corpus.ts reads from the file its script writes on the engine today, opened with the core and work-queue behaviors, at the clock below.'
      : 'What runtime/engine-workqueue/typescript/test/sqlite-corpus.ts reads from typescript.sql loaded into a file and opened with the core and work-queue behaviors, at the clock below.',
    open: OPTIONS,
    clock: END,
    principals,
  };
  const lines = ['{'];
  for (const [key, value] of Object.entries(head)) {
    lines.push(`  ${JSON.stringify(key)}: ${JSON.stringify(value)},`);
  }
  lines.push('  "reads": [');
  reads.forEach((step, index) => lines.push(`    ${JSON.stringify(step)}${index < reads.length - 1 ? ',' : ''}`));
  lines.push('  ]', '}');
  return `${lines.join('\n')}\n`;
}

// The suffixes of the tables FTS5 keeps a virtual table's index in, which
// the dump leaves out: it inserts the virtual table's rows through it.
const SHADOW_SUFFIXES = ['_data', '_idx', '_content', '_docsize', '_config'];

/**
 * dumpDatabase writes the file at path as SQL text, one statement per line:
 * its schema in the order SQLite keeps it, every row, sqlite_sequence, then
 * its triggers. README.md gives the rules.
 */
export function dumpDatabase(path: string): string {
  const db = openConnection(path);
  try {
    return dump(db);
  } finally {
    db.close();
  }
}

function dump(db: SqliteConnection): string {
  const entries = db.all(`SELECT type, name, tbl_name AS owner, sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY rowid`) as Array<{
    type: string;
    name: string;
    owner: string;
    sql: string;
  }>;
  const virtual = new Set(entries.filter((entry) => entry.type === 'table' && /^CREATE VIRTUAL TABLE/iu.test(entry.sql)).map((entry) => entry.name));
  const shadow = (name: string): boolean => [...virtual].some((table) => SHADOW_SUFFIXES.some((suffix) => name === `${table}${suffix}`));
  const lines: string[] = [];
  const schema = entries.filter((entry) => entry.type !== 'trigger' && entry.name !== 'sqlite_sequence' && !shadow(entry.owner));
  for (const entry of schema) {
    lines.push(`${oneLine(entry.sql)};`);
  }
  for (const entry of schema.filter((candidate) => candidate.type === 'table')) {
    lines.push(...rows(db, entry.name, entry.sql));
  }
  const sequences = db.all(`SELECT name, seq FROM sqlite_sequence ORDER BY name`) as Array<{ name: string; seq: number }>;
  if (entries.some((entry) => entry.name === 'sqlite_sequence')) {
    lines.push('DELETE FROM sqlite_sequence;');
    for (const { name, seq } of sequences) {
      lines.push(`INSERT INTO sqlite_sequence (name, seq) VALUES (${literal(name)}, ${seq});`);
    }
  }
  for (const entry of entries.filter((candidate) => candidate.type === 'trigger')) {
    lines.push(`${oneLine(entry.sql)};`);
  }
  return `${lines.join('\n')}\n`;
}

// oneLine joins a schema statement's lines, as SQLite keeps the text a
// migration gave, with single spaces, outside of quoted text, leaving out
// a `--` comment, which would run to the end of the one line.
function oneLine(sql: string): string {
  let out = '';
  let quote: string | undefined;
  for (let index = 0; index < sql.length; index += 1) {
    const char = sql[index];
    if (quote === undefined && char === '-' && sql[index + 1] === '-') {
      const end = sql.indexOf('\n', index);
      index = end < 0 ? sql.length : end - 1;
      continue;
    }
    if (quote !== undefined) {
      out += char;
      if (char === quote) {
        quote = undefined;
      }
      continue;
    }
    if (char === "'" || char === '"' || char === '`') {
      quote = char;
      out += char;
      continue;
    }
    if (/\s/u.test(char)) {
      if (!out.endsWith(' ') && !out.endsWith('(')) {
        out += ' ';
      }
      continue;
    }
    if (char === ')' && out.endsWith(' ')) {
      out = out.slice(0, -1);
    }
    out += char;
  }
  return out.trim();
}

// rows writes a table's rows as INSERT statements, in rowid order, the
// rowid first when no column holds it; a WITHOUT ROWID table's by its
// primary key; a virtual table's through it, with their rowids.
function rows(db: SqliteConnection, table: string, sql: string): string[] {
  const info = db.all(`SELECT name, type, pk FROM pragma_table_info(?) ORDER BY cid`, [table]) as Array<{ name: string; type: string; pk: number }>;
  const columns = info.map((column) => column.name);
  const withoutRowid = /\)\s*(?:STRICT\s*,\s*)?WITHOUT\s+ROWID|WITHOUT\s+ROWID\s*(?:,\s*STRICT)?\s*$/iu.test(sql);
  const keys = info.filter((column) => column.pk > 0).sort((a, b) => a.pk - b.pk);
  const aliased = !withoutRowid && keys.length === 1 && keys[0].type.toUpperCase() === 'INTEGER';
  const withRowid = !withoutRowid && !aliased;
  const selected = [...(withRowid ? ['rowid'] : []), ...columns];
  const parts = selected.map((column, index) => {
    const name = column === 'rowid' ? 'rowid' : ident(column);
    return `typeof(${name}) AS t${index}, quote(${name}) AS q${index}, CASE typeof(${name}) WHEN 'real' THEN printf('%.17g', ${name}) WHEN 'text' THEN hex(${name}) END AS x${index}`;
  });
  const order = withoutRowid ? keys.map((column) => ident(column.name)).join(', ') : 'rowid';
  const found = db.all(`SELECT ${parts.join(', ')} FROM ${ident(table)} ORDER BY ${order}`) as Array<Record<string, string | number | null>>;
  const head = `INSERT INTO ${ident(table)} (${selected.map((column) => (column === 'rowid' ? 'rowid' : column)).join(', ')}) VALUES (`;
  return found.map((row) => `${head}${selected.map((_, index) => value(row[`t${index}`] as string, row[`q${index}`] as string, row[`x${index}`] as string | null)).join(', ')});`);
}

// value is a column's value as a literal: SQLite's own quote() for an
// integer, a BLOB and NULL; a REAL with 17 significant digits; a text in
// single quotes, or, when it holds a line break or another control
// character, as CAST(X'<UTF-8>' AS TEXT), so every statement is one line.
function value(type: string, quoted: string, extra: string | null): string {
  if (type === 'real') {
    const text = String(extra);
    return /[.eEn]/u.test(text) ? text : `${text}.0`;
  }
  if (type === 'text' && /[\u0000-\u001f\u007f\u0085\u2028\u2029]/u.test(quoted)) {
    return `CAST(X'${String(extra)}' AS TEXT)`;
  }
  return quoted;
}

function literal(text: string): string {
  return `'${text.replace(/'/gu, "''")}'`;
}

function ident(name: string): string {
  return /^[A-Za-z_][A-Za-z0-9_]*$/u.test(name) ? name : `"${name.replace(/"/gu, '""')}"`;
}

/**
 * loadDatabase runs a dump's statements, one per line, in a new file at
 * path, with foreign keys off, since a row may name one inserted after it;
 * then it requires that no foreign key is broken.
 */
export function loadDatabase(text: string, path: string): void {
  const db = openConnection(path);
  try {
    db.exec('PRAGMA foreign_keys = OFF');
    db.exec('BEGIN');
    for (const line of text.split('\n')) {
      if (line !== '') {
        db.exec(line);
      }
    }
    db.exec('COMMIT');
    const broken = db.all('PRAGMA foreign_key_check');
    if (broken.length > 0) {
      throw new Error(`the dump breaks foreign keys: ${JSON.stringify(broken)}`);
    }
  } finally {
    db.close();
  }
}
