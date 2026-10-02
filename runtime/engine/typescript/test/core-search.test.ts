// Search, the core's full-text search: the index kept in the transaction
// of each create, update, delete and operation that changes an indexed
// field, ranking by the weights, paging, plain words and the opt-in FTS5
// syntax, answers only for a caller who may read the schema and only from
// the caller's namespace, the config's rules, and the index rebuilt or
// dropped when a version changes the fields or adds or removes Search.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  EngineError,
  OperationParamsError,
  SchemaDocumentError,
  defineBehavior,
  type AccessPolicy,
  type Engine,
  type EngineOptions,
  type Principal,
  type SearchHit,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob', permissions: [] };
const reviewer: Principal = { subject: 'rae', permissions: ['notes.review'] };

type Behaviors = Array<{ name: string; config?: unknown }>;

const SEARCH = { name: 'Search', config: { fields: ['title', 'body'], weights: { title: 3 } } };

// A Note with fields of every kind Search's config is checked against.
function notes(behaviors: Behaviors = [SEARCH]): Record<string, unknown> {
  const document = schemaDocument(
    'Note',
    [
      { name: 'title', typeRef: { name: 'string' }, required: true },
      { name: 'body', typeRef: { name: 'string' } },
      { name: 'author', typeRef: { name: 'Contact.Email' } },
      { name: 'rating', typeRef: { name: 'Generic.Int64' } },
      { name: 'mood', typeRef: { name: 'Mood' } },
      { name: 'tags', typeRef: { name: 'string', isArray: true } },
      { name: 'meta', typeRef: { name: 'Meta' } },
    ],
    {
      enums: { Mood: { name: 'Mood', values: [{ name: 'CALM' }, { name: 'LOUD' }] } },
      types: { Meta: { name: 'Meta', role: 'EmbeddedStruct', fields: [{ name: 'source', typeRef: { name: 'string' } }] } },
    }
  ) as { types: Record<string, Record<string, unknown>> };
  document.types.Note.behaviors = behaviors;
  return document;
}

function publish(engine: Engine, document: Record<string, unknown>, namespace?: string): number {
  engine.schemas.define(alice, document, { namespace });
  return engine.schemas.publish(alice, document.name as string, { namespace }).version;
}

interface Page {
  items: SearchHit[];
  next: string | null;
}

function search(engine: Engine, params: Record<string, unknown>, options: { principal?: Principal; namespace?: string } = {}): Page {
  return engine.instances.invokeSchema(options.principal ?? alice, 'Note', 'search', params, { namespace: options.namespace }) as Page;
}

// ids searches with plain words and returns the matching ids in rank order.
function ids(engine: Engine, query: string, namespace?: string): string[] {
  return search(engine, { query, limit: 500 }, { namespace }).items.map((hit) => hit.id);
}

// rowsOf counts the index rows a namespace holds for Note.
function rowsOf(engine: Engine, namespace = 'default'): number {
  return Number(engine.storage.get("SELECT count(*) AS n FROM bhv_search__rows WHERE namespace = ? AND schema = 'Note'", [namespace])?.n);
}

// A behavior listed after Search whose afterChange fails for a title of
// Boom, after Search's has run: the write rolls back, the index with it.
const tripwire = defineBehavior({
  declaration: { name: 'test.Tripwire' },
  afterChange(context) {
    if (context.data.title === 'Boom') {
      throw new Error('tripped');
    }
  },
});

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, ...options });
  }

  // n1 and n2 mention a walnut desk, n1 in its title.
  function desks(options: Partial<EngineOptions> = {}, behaviors: Behaviors = [SEARCH]): Engine {
    const engine = open(options);
    publish(engine, notes(behaviors));
    engine.instances.create(alice, 'Note', { title: 'Walnut desk', body: 'A desk of solid wood.' }, { id: 'n1' });
    engine.instances.create(alice, 'Note', { title: 'Oak chair', body: 'Pairs with the walnut desk.' }, { id: 'n2' });
    return engine;
  }

  describe(`Search (${driver})`, () => {
    test('a create, an update and a delete keep the index in their own transaction', () => {
      const engine = desks({ metaSchema: openMetaSchema(), behaviors: [tripwire] }, [SEARCH, { name: 'test.Tripwire' }]);
      assert.deepEqual(search(engine, { query: 'walnut' }), {
        items: [
          { id: 'n1', rank: 1, field: 'title', snippet: [{ text: 'Walnut', match: true }, { text: ' desk', match: false }] },
          {
            id: 'n2',
            rank: 2,
            field: 'body',
            snippet: [
              { text: 'Pairs with the ', match: false },
              { text: 'walnut', match: true },
              { text: ' desk.', match: false },
            ],
          },
        ],
        next: null,
      });

      engine.instances.update(alice, 'Note', 'n1', { title: 'Pine desk', body: 'Light and cheap.' });
      assert.deepEqual(ids(engine, 'walnut'), ['n2']);
      assert.deepEqual(ids(engine, 'pine'), ['n1']);
      // A field Search does not index changes nothing it holds.
      engine.instances.update(alice, 'Note', 'n1', { author: 'ann@example.com', rating: 4 });
      assert.deepEqual([ids(engine, 'pine'), ids(engine, 'ann')], [['n1'], []]);
      // A field cleared leaves the others searchable.
      engine.instances.update(alice, 'Note', 'n1', { body: null });
      assert.deepEqual([ids(engine, 'cheap'), ids(engine, 'pine')], [[], ['n1']]);

      // A write that fails after Search's afterChange leaves the index as it was.
      assert.throws(() => engine.instances.create(alice, 'Note', { title: 'Boom' }, { id: 'n3' }), /tripped/);
      assert.throws(() => engine.instances.update(alice, 'Note', 'n1', { title: 'Boom' }), /tripped/);
      assert.deepEqual([ids(engine, 'boom'), ids(engine, 'pine'), rowsOf(engine)], [[], ['n1'], 2]);

      assert.equal(engine.instances.delete(alice, 'Note', 'n2'), true);
      assert.deepEqual([ids(engine, 'walnut'), ids(engine, 'desk'), rowsOf(engine)], [[], ['n1'], 1]);
    });

    test('an operation that changes the own fields with update() reindexes them', () => {
      const engine = open();
      publish(engine, notes([SEARCH, { name: 'Revisions', config: { review: { permission: 'notes.review' } } }]));
      engine.instances.create(alice, 'Note', { title: 'Draft plan' }, { id: 'n1' });
      const proposal = engine.instances.invoke(alice, 'Note', 'n1', 'propose', { patch: { title: 'Final plan' } }) as { id: number };
      assert.deepEqual([ids(engine, 'final'), ids(engine, 'draft')], [[], ['n1']], 'a proposal changes nothing a search sees');
      engine.instances.invoke(reviewer, 'Note', 'n1', 'approve', { proposal: proposal.id });
      assert.deepEqual([ids(engine, 'final'), ids(engine, 'draft')], [['n1'], []]);
    });

    test('ranks by bm25 with the weights, then by when an instance was indexed; a change of weights alone applies at once', () => {
      const engine = open();
      publish(engine, notes());
      engine.instances.create(alice, 'Note', { title: 'Other', body: 'A fox ran by.' }, { id: 'body' });
      engine.instances.create(alice, 'Note', { title: 'A fox', body: 'Nothing else.' }, { id: 'title' });
      engine.instances.create(alice, 'Note', { title: 'Fox fox fox', body: 'Nothing else.' }, { id: 'often' });
      assert.deepEqual(ids(engine, 'fox'), ['often', 'title', 'body']);
      // The body now weighs more than the title. The index stays as it was.
      const rows = engine.storage.all('SELECT row, id FROM bhv_search__rows ORDER BY row');
      assert.equal(publish(engine, notes([{ name: 'Search', config: { fields: ['title', 'body'], weights: { body: 20 } } }])), 2);
      assert.deepEqual(ids(engine, 'fox'), ['body', 'often', 'title']);
      assert.deepEqual(engine.storage.all('SELECT row, id FROM bhv_search__rows ORDER BY row'), rows);

      // Equal matches keep the order they were first indexed in, an update included.
      engine.instances.create(alice, 'Note', { title: 'Twin lamp' }, { id: 't1' });
      engine.instances.create(alice, 'Note', { title: 'Twin lamp' }, { id: 't2' });
      engine.instances.update(alice, 'Note', 't1', { title: 'Twin lamp ' });
      assert.deepEqual(ids(engine, 'lamp'), ['t1', 't2']);
    });

    test("the match drives the query: a prefix search's cost follows its matches, not the schema's instances", () => {
      // With the schema's rows as the outer loop, which Node.js 24's
      // SQLite picks for a plain JOIN, the match runs once per instance:
      // these five searches take seconds instead of milliseconds.
      const engine = open();
      publish(engine, notes());
      const words = ['alpha', 'bravo', 'charlie', 'delta', 'echo'];
      engine.storage.transaction(() => {
        for (let n = 0; n < 1500; n += 1) {
          const body = Array.from({ length: 20 }, (_, k) => `${words[(n + k) % 5]}${(n * 7 + k) % 300}`).join(' ');
          engine.instances.create(alice, 'Note', { title: `lamp ${n}`, body }, { id: `n${n}` });
        }
      });
      const started = performance.now();
      for (const word of words) {
        assert.equal(search(engine, { query: `${word}*`, syntax: 'fts5', limit: 10 }).items.length, 10);
      }
      const elapsed = performance.now() - started;
      assert.ok(elapsed < 750, `five prefix searches took ${elapsed.toFixed(0)} ms`);
    });

    test('pages through the ranking with limit and cursor, rank continuing across pages', () => {
      const engine = open();
      publish(engine, notes());
      for (let n = 1; n <= 5; n += 1) {
        engine.instances.create(alice, 'Note', { title: `lamp ${'lamp '.repeat(5 - n)}`.trim() }, { id: `n${n}` });
      }
      const all = search(engine, { query: 'lamp' }).items.map((hit) => [hit.id, hit.rank]);
      assert.deepEqual(all, [['n1', 1], ['n2', 2], ['n3', 3], ['n4', 4], ['n5', 5]]);
      const pages: Array<Array<[string, number]>> = [];
      let cursor: string | undefined;
      do {
        const page = search(engine, { query: 'lamp', limit: 2, ...(cursor === undefined ? {} : { cursor }) });
        pages.push(page.items.map((hit) => [hit.id, hit.rank]));
        cursor = page.next ?? undefined;
      } while (cursor !== undefined);
      assert.deepEqual(pages, [[['n1', 1], ['n2', 2]], [['n3', 3], ['n4', 4]], [['n5', 5]]]);

      assert.deepEqual(thrown(() => search(engine, { query: 'lamp', cursor: 'nope' }), OperationParamsError).issues, [
        { path: '/cursor', message: 'is not a cursor this operation returned' },
      ]);
      for (const limit of [0, 501]) {
        assert.equal(thrown(() => search(engine, { query: 'lamp', limit }), OperationParamsError).code, 'invalid_argument');
      }
      assert.equal(thrown(() => search(engine, { query: '' }), OperationParamsError).code, 'invalid_argument');
    });

    test('a query is plain words: quotes, operators and FTS5 syntax in it are text', () => {
      const engine = open();
      publish(engine, notes());
      engine.instances.create(alice, 'Note', { title: 'C++ tips', body: 'Use "const" and NOT macros: a OR b, near(x) * star. Caf\u00e9 cr\u00e8me.' }, { id: 'n1' });
      engine.instances.create(alice, 'Note', { title: 'Plain', body: 'Nothing to see.' }, { id: 'n2' });
      // Each word must be in the instance, operators included.
      assert.deepEqual(ids(engine, 'macros NOT'), ['n1']);
      assert.deepEqual(ids(engine, 'a OR b'), ['n1']);
      assert.deepEqual(ids(engine, 'nothing OR macros'), []);
      assert.deepEqual(ids(engine, 'NEAR(x'), ['n1']);
      assert.deepEqual(ids(engine, '"const"'), ['n1']);
      assert.deepEqual(ids(engine, '"const'), ['n1']);
      // A column filter is a word, and so is a prefix.
      assert.deepEqual(ids(engine, 'body: macros'), []);
      assert.deepEqual(ids(engine, 'tip*'), []);
      assert.deepEqual(ids(engine, 'tips*'), ['n1']);
      // Diacritics and case fold.
      assert.deepEqual(ids(engine, 'CAFE creme'), ['n1']);
      // What holds no word matches nothing, without an error.
      for (const query of ['"', '(', ')', '*', '-', ' ', '\u0000', '{}', ':']) {
        assert.deepEqual(search(engine, { query }), { items: [], next: null }, JSON.stringify(query));
      }
      // A dangling operator is a word too; one of no letters counts for
      // nothing, and one FTS5 splits is a phrase of its parts.
      assert.deepEqual(ids(engine, 'macros AND'), ['n1']);
      assert.deepEqual(ids(engine, 'macros -'), ['n1']);
      assert.deepEqual(ids(engine, 'a-or-b'), ['n1']);
      assert.deepEqual(ids(engine, 'b-or-a'), []);
    });

    test('syntax fts5 takes an FTS5 expression, and refuses one it cannot parse or a column filter', () => {
      const engine = desks();
      const fts5 = (query: string) => search(engine, { query, syntax: 'fts5' }).items.map((hit) => hit.id);
      assert.deepEqual(fts5('wood OR chair'), ['n2', 'n1']);
      assert.deepEqual(fts5('"walnut desk"'), ['n1', 'n2']);
      assert.deepEqual(fts5('"solid wood" OR "oak chair"'), ['n2', 'n1']);
      assert.deepEqual(fts5('wal*'), ['n1', 'n2']);
      assert.deepEqual(fts5('desk NOT oak'), ['n1']);
      assert.deepEqual(fts5('NEAR(pairs desk, 3)'), ['n2']);
      assert.deepEqual(fts5('"title: walnut"'), [], 'a colon in a string is text');

      for (const query of ['walnut AND', '*', 'walnut)', '"walnut', '-wood', 'NEAR(walnut desk, x)', `${'('.repeat(200)}walnut${')'.repeat(200)}`]) {
        const error = thrown(() => fts5(query), OperationParamsError);
        assert.equal(error.code, 'invalid_argument', query);
        assert.equal(error.issues[0].path, '/query', query);
        assert.match(error.issues[0].message, /^is not an FTS5 expression this index takes: /, query);
      }
      for (const query of ['title: walnut', '{title body}: walnut', '- title : oak']) {
        assert.deepEqual(thrown(() => fts5(query), OperationParamsError).issues, [
          { path: '/query', message: 'holds a column filter, which search does not take: a query searches every indexed field' },
        ]);
      }
      assert.equal(thrown(() => search(engine, { query: 'walnut', syntax: 'regex' }), OperationParamsError).code, 'invalid_argument');
    });

    test("it answers only a caller who may read the schema, and only from the caller's namespace", () => {
      const policy: AccessPolicy = ({ principal, action, namespace, operation }) =>
        principal.subject === 'alice' ||
        (principal.subject === 'bob' && namespace === 'east' && action === 'read') ||
        (principal.subject === 'carl' && operation === 'search');
      const engine = open({ policy, namespaces: { names: ['east', 'west'] } });
      for (const namespace of ['east', 'west']) {
        publish(engine, notes(), namespace);
      }
      engine.instances.create(alice, 'Note', { title: 'Walnut desk' }, { id: 'n1', namespace: 'east' });
      engine.instances.create(alice, 'Note', { title: 'Walnut chair' }, { id: 'n1', namespace: 'west' });
      assert.deepEqual(search(engine, { query: 'walnut' }, { principal: bob, namespace: 'east' }).items, [
        { id: 'n1', rank: 1, field: 'title', snippet: [{ text: 'Walnut', match: true }, { text: ' desk', match: false }] },
      ]);
      assert.deepEqual(ids(engine, 'chair', 'east'), []);
      assert.deepEqual(ids(engine, 'chair', 'west'), ['n1']);

      // bob may not read west's notes; carl may call search, but may not read a note.
      for (const [principal, namespace, message] of [
        [bob, 'west', 'bob may not call search (read) on Note in namespace west'],
        [{ subject: 'carl', permissions: [] }, 'east', 'carl may not read Note in namespace east'],
      ] as const) {
        const error = thrown(() => search(engine, { query: 'walnut' }, { principal, namespace }), EngineError);
        assert.deepEqual([error.code, error.message], ['forbidden', message]);
      }
    });

    test("a schema of the shared namespace keeps each namespace's index to its own instances", () => {
      const engine = open({ namespaces: { names: ['shared', 'east', 'west'], shared: 'shared' } });
      publish(engine, notes([]), 'shared');
      engine.instances.create(alice, 'Note', { title: 'Walnut desk' }, { id: 'n1', namespace: 'east' });
      engine.instances.create(alice, 'Note', { title: 'Walnut chair' }, { id: 'n1', namespace: 'west' });
      engine.instances.create(alice, 'Note', { title: 'Walnut stool' }, { id: 's1', namespace: 'shared' });
      // Search added in the shared namespace indexes every namespace's instances, each in its own.
      assert.equal(publish(engine, notes(), 'shared'), 2);
      assert.deepEqual(
        ['shared', 'east', 'west'].map((namespace) => [ids(engine, 'walnut', namespace), rowsOf(engine, namespace)]),
        [[['s1'], 1], [['n1'], 1], [['n1'], 1]]
      );
      assert.deepEqual([ids(engine, 'chair', 'east'), ids(engine, 'chair', 'west')], [[], ['n1']]);
      engine.instances.create(alice, 'Note', { title: 'Walnut shelf' }, { id: 'n2', namespace: 'east' });
      assert.deepEqual(ids(engine, 'walnut', 'east'), ['n1', 'n2']);
    });

    test('a schema without Search has no search operation', () => {
      const engine = open();
      publish(engine, notes([]));
      const error = thrown(() => search(engine, { query: 'walnut' }), EngineError);
      assert.deepEqual(
        [error.code, error.message],
        ['not_found', "schema Note has no schema-level operation search (its behaviors' schema-level operations: none)"]
      );
    });

    test("its config names the type's own text fields: strings and string scalars", () => {
      const engine = open();
      const refusal = (config: unknown) =>
        thrown(() => engine.schemas.define(alice, notes([{ name: 'Search', config }])), SchemaDocumentError).issues.map((issue) => issue.message);
      assert.deepEqual(refusal({ fields: ['title', 'subject'] }), ['type Note: behavior Search config: fields: Note has no field subject']);
      for (const field of ['rating', 'mood', 'tags', 'meta']) {
        assert.deepEqual(refusal({ fields: [field] }), [`type Note: behavior Search config: fields: Note.${field} is not a string, or a scalar whose values are strings`]);
      }
      assert.deepEqual(refusal({ fields: ['title'], weights: { body: 2 } }), [
        'type Note: behavior Search config: weights: body is not an indexed field (fields: title)',
      ]);
      // The configSchema holds the rest: at least one field and at most 16, each once, weights above 0.
      for (const config of [{ fields: [] }, { fields: ['title', 'title'] }, { fields: ['title'], weights: { title: 0 } }, {}]) {
        thrown(() => engine.schemas.define(alice, notes([{ name: 'Search', config }])), SchemaDocumentError);
      }
      // A string scalar is text.
      assert.equal(publish(engine, notes([{ name: 'Search', config: { fields: ['author'] } }])), 1);
      engine.instances.create(alice, 'Note', { title: 'x', author: 'ann@example.com' }, { id: 'n1' });
      assert.deepEqual(search(engine, { query: 'ann' }).items, [
        { id: 'n1', rank: 1, field: 'author', snippet: [{ text: 'ann', match: true }, { text: '@example.com', match: false }] },
      ]);
    });

    test('a version that changes the indexed fields rebuilds the index of the instances that exist', () => {
      const engine = open();
      publish(engine, notes([{ name: 'Search', config: { fields: ['title'] } }]));
      engine.instances.create(alice, 'Note', { title: 'Walnut desk', body: 'Made of oak.' }, { id: 'n1' });
      assert.deepEqual(ids(engine, 'oak'), []);
      assert.equal(publish(engine, notes([{ name: 'Search', config: { fields: ['title', 'body'] } }])), 2);
      assert.deepEqual(ids(engine, 'oak'), ['n1']);
      assert.equal(publish(engine, notes([{ name: 'Search', config: { fields: ['body'] } }])), 3);
      assert.deepEqual(ids(engine, 'walnut'), []);
      assert.deepEqual(search(engine, { query: 'oak' }).items, [
        { id: 'n1', rank: 1, field: 'body', snippet: [{ text: 'Made of ', match: false }, { text: 'oak', match: true }, { text: '.', match: false }] },
      ]);
      assert.equal(rowsOf(engine), 1);
    });

    test('Search can be added to and removed from a schema with instances', () => {
      const engine = open();
      publish(engine, notes([]));
      engine.instances.create(alice, 'Note', { title: 'Walnut desk' }, { id: 'n1' });
      engine.instances.create(alice, 'Note', { title: 'Walnut chair' }, { id: 'n2' });
      assert.equal(publish(engine, notes()), 2);
      assert.deepEqual(ids(engine, 'walnut'), ['n1', 'n2']);

      // Removed: the index goes, and writes keep none.
      assert.equal(publish(engine, notes([])), 3);
      assert.equal(rowsOf(engine), 0);
      assert.equal(thrown(() => search(engine, { query: 'walnut' }), EngineError).code, 'not_found');
      engine.instances.create(alice, 'Note', { title: 'Walnut stool' }, { id: 'n3' });
      engine.instances.update(alice, 'Note', 'n1', { title: 'Pine desk' });
      assert.equal(rowsOf(engine), 0);

      // Added again: every instance is indexed as it is now.
      assert.equal(publish(engine, notes()), 4);
      assert.deepEqual([ids(engine, 'walnut'), ids(engine, 'pine'), rowsOf(engine)], [['n2', 'n3'], ['n1'], 3]);
    });
  });
}
