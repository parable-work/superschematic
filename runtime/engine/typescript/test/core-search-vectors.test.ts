// Search's vectors: an outside embedder pulls the instances whose text has
// no vector (staleEmbeddings) and settles their vectors (settleEmbeddings),
// which skips an instance whose text moved since and refuses a vector the
// config's dimensions or a 32-bit float cannot hold, with no event; a
// search by vector ranks by cosine and one by a query and a vector fuses
// the two rankings by reciprocal rank; similar leaves the instance out,
// and ranks a draft's text and vector the same way; a
// search across schemas skips one the caller may not read; a version that
// changes the model, the dimensions or the fields makes every vector
// stale; and the HTTP route and the MCP tool of the search across schemas.
import assert from 'node:assert/strict';
import type { Server as NodeServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { afterEach, describe, test } from 'node:test';

import { serve as serveNode } from '@hono/node-server';
import { Client, StreamableHTTPClientTransport, type CallToolResult } from '@modelcontextprotocol/client';
import type { Authenticator } from '@superschematic/http-runtime';
import { Hono } from 'hono';

import {
  EngineError,
  OperationParamsError,
  RRF_K,
  type AccessPolicy,
  type Engine,
  type EngineOptions,
  type Principal,
  type SchemaSearchHit,
  type SearchHit,
} from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { MCP_PATH, engineMcp } from '../dist/mcp/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

const embedder: Principal = { subject: 'embedder', permissions: ['notes.embed'] };
const bob: Principal = { subject: 'bob', permissions: [] };

type Behaviors = Array<{ name: string; config?: unknown }>;

const VECTORS = { dimensions: 3, model: 'test-3d', permission: 'notes.embed' };
const SEARCH = { name: 'Search', config: { fields: ['title', 'body'], weights: { title: 3 }, vectors: VECTORS } };

// A schema of notes: a title, a body, and an author Search does not index.
function notes(behaviors: Behaviors = [SEARCH], name = 'Note'): Record<string, unknown> {
  const document = schemaDocument(name, [
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'body', typeRef: { name: 'string' } },
    { name: 'author', typeRef: { name: 'Contact.Email' } },
  ]) as { types: Record<string, Record<string, unknown>> };
  document.types[name].behaviors = behaviors;
  return document;
}

function publish(engine: Engine, document: Record<string, unknown>): number {
  engine.schemas.define(alice, document);
  return engine.schemas.publish(alice, document.name as string).version;
}

interface Page<T = SearchHit> {
  items: T[];
  next: string | null;
}

interface Stale {
  model: string;
  dimensions: number;
  items: Array<{ id: string; text: string; sourceHash: string }>;
  next: string | null;
}

interface Settled {
  settled: number;
  skipped: Array<{ id: string; reason: string }>;
}

function call(engine: Engine, operation: string, params: Record<string, unknown>, principal: Principal = alice, schema = 'Note'): unknown {
  return engine.instances.invokeSchema(principal, schema, operation, params);
}

function stale(engine: Engine, params: Record<string, unknown> = {}, schema = 'Note'): Stale {
  return call(engine, 'staleEmbeddings', params, alice, schema) as Stale;
}

function settle(engine: Engine, items: Array<{ id: string; sourceHash: string; vector: number[] }>, principal = embedder): Settled {
  return call(engine, 'settleEmbeddings', { items }, principal) as Settled;
}

// settleAll settles a vector for every stale instance the map names.
function settleAll(engine: Engine, vectors: Record<string, number[]>, schema = 'Note'): Settled {
  const items = stale(engine, { limit: 100 }, schema).items.filter((item) => vectors[item.id] !== undefined);
  return engine.instances.invokeSchema(embedder, schema, 'settleEmbeddings', {
    items: items.map((item) => ({ id: item.id, sourceHash: item.sourceHash, vector: vectors[item.id] })),
  }) as Settled;
}

function search(engine: Engine, params: Record<string, unknown>, principal: Principal = alice): Page {
  return call(engine, 'search', params, principal) as Page;
}

function ids(page: Page<{ id: string }>): string[] {
  return page.items.map((hit) => hit.id);
}

function lastCursor(engine: Engine): number {
  const events = engine.events.read(alice, { limit: 500 }).events;
  return events[events.length - 1]?.cursor ?? 0;
}

// rowsOf reads Search's own record of each instance's vector.
function rowsOf(engine: Engine): Array<{ id: string; hashed: boolean; embedded: boolean; settledBy: string | null }> {
  return engine.storage
    .all("SELECT id, source_hash, vector, settled_by FROM bhv_search__rows WHERE schema = 'Note' ORDER BY row")
    .map((row) => ({ id: String(row.id), hashed: row.source_hash !== null, embedded: row.vector !== null, settledBy: row.settled_by as string | null }));
}

// pulledAs is what a settle presents of a pulled instance: its id and hash.
const pulledAs = ({ id, sourceHash }: { id: string; sourceHash: string }) => ({ id, sourceHash });

const rrf = (...places: number[]) => places.reduce((score, place) => score + 1 / (RRF_K + place), 0);

afterEach(cleanup);

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, clock: () => 1_790_000_000_000, ...options });
  }

  function desks(options: Partial<EngineOptions> = {}): Engine {
    const engine = open(options);
    publish(engine, notes());
    engine.instances.create(alice, 'Note', { title: 'Walnut desk', body: 'A desk of solid wood.' }, { id: 'n1' });
    engine.instances.create(alice, 'Note', { title: 'Oak chair' }, { id: 'n2' });
    engine.instances.create(alice, 'Note', { title: '  ', body: '\n' }, { id: 'blank' });
    return engine;
  }

  describe(`Search's vectors (${driver})`, () => {
    test('an embedder pulls the text of each instance without a vector and its hash, then settles the vectors, with no event', () => {
      const engine = desks();
      const pulled = stale(engine);
      assert.deepEqual(
        { ...pulled, items: pulled.items.map(({ id, text }) => ({ id, text })) },
        {
          model: 'test-3d',
          dimensions: 3,
          items: [
            { id: 'n1', text: 'Walnut desk\n\nA desk of solid wood.' },
            { id: 'n2', text: 'Oak chair' },
          ],
          next: null,
        },
        'text that holds nothing wants no vector'
      );
      for (const item of pulled.items) {
        assert.match(item.sourceHash, /^[0-9a-f]{64}$/);
      }
      const first = stale(engine, { limit: 1 });
      assert.deepEqual(ids(first), ['n1']);
      assert.deepEqual(ids(stale(engine, { limit: 1, cursor: first.next as string })), ['n2']);

      const cursor = lastCursor(engine);
      const seq = engine.instances.get(alice, 'Note', 'n1')?.seq;
      assert.deepEqual(settleAll(engine, { n1: [1, 0, 0], n2: [0, 2, 0] }), { settled: 2, skipped: [] });
      assert.deepEqual(stale(engine).items, []);
      assert.equal(lastCursor(engine), cursor, 'a settle appends no event');
      assert.equal(engine.instances.get(alice, 'Note', 'n1')?.seq, seq, "and moves no instance's sequence");
      assert.deepEqual(engine.instances.get(alice, 'Note', 'n1')?.data, { title: 'Walnut desk', body: 'A desk of solid wood.' });
      assert.deepEqual(rowsOf(engine), [
        { id: 'n1', hashed: true, embedded: true, settledBy: 'embedder' },
        { id: 'n2', hashed: true, embedded: true, settledBy: 'embedder' },
        { id: 'blank', hashed: false, embedded: false, settledBy: null },
      ]);
      // A vector is stored at unit length: n2's ranks with similarity 1.
      assert.deepEqual(search(engine, { vector: [0, 1, 0] }).items[0], { id: 'n2', rank: 1, score: rrf(1), vector: { rank: 1, similarity: 1 } });
    });

    test('a settle refuses a wrong dimension, a value a 32-bit float cannot hold, all zeros and an id twice, as a whole', () => {
      const engine = desks();
      const [n1, n2] = stale(engine).items;
      const refused = (items: Array<{ id: string; sourceHash: string; vector: number[] }>) =>
        thrown(() => settle(engine, items), OperationParamsError).issues;
      assert.deepEqual(refused([{ ...pulledAs(n2), vector: [0, 1, 0] }, { ...pulledAs(n1), vector: [1, 0] }]), [
        { path: '/items/1/vector', message: "holds 2 numbers, and the config's vectors hold 3" },
      ]);
      assert.deepEqual(refused([{ ...pulledAs(n1), vector: [1, 1e39, 0] }]), [{ path: '/items/0/vector', message: 'holds 1e+39 at 1, which a 32-bit float cannot hold' }]);
      assert.deepEqual(refused([{ ...pulledAs(n1), vector: [0, 0, 0] }]), [{ path: '/items/0/vector', message: 'is all zeros, which has no direction to compare' }]);
      assert.deepEqual(refused([{ ...pulledAs(n1), vector: [1, 0, 0] }, { ...pulledAs(n1), vector: [0, 1, 0] }]), [
        { path: '/items/1/id', message: 'settles n1 a second time in one call' },
      ]);
      assert.deepEqual(refused([{ ...pulledAs(n1), vector: [Number.NaN, 0, 0] }]), [{ path: '/items/0/vector/0', message: 'NaN is not a JSON number' }]);
      assert.equal(thrown(() => settle(engine, []), OperationParamsError).code, 'invalid_argument');
      assert.deepEqual(ids(stale(engine)), ['n1', 'n2'], 'a refused batch stores none of its vectors');

      // It needs the permission the config names, and read on the schema.
      const forbidden = thrown(() => settle(engine, [{ ...pulledAs(n1), vector: [1, 0, 0] }], alice), EngineError);
      assert.deepEqual(
        [forbidden.code, forbidden.message],
        ['forbidden', 'alice may not settle the vectors of Note in namespace default: it needs notes.embed']
      );
    });

    test('a settle skips an instance whose text moved since it was pulled, and one that is gone', () => {
      const engine = desks();
      const [n1, n2] = stale(engine).items;
      engine.instances.update(alice, 'Note', 'n1', { title: 'Pine desk' });
      engine.instances.delete(alice, 'Note', 'n2');
      assert.deepEqual(settle(engine, [{ ...pulledAs(n1), vector: [1, 0, 0] }, { ...pulledAs(n2), vector: [0, 1, 0] }]), {
        settled: 0,
        skipped: [
          { id: 'n1', reason: 'moved' },
          { id: 'n2', reason: 'not_found' },
        ],
      });
      const [moved] = stale(engine).items;
      assert.deepEqual([moved.id, moved.text, moved.sourceHash === n1.sourceHash], ['n1', 'Pine desk\n\nA desk of solid wood.', false]);
      assert.deepEqual(settle(engine, [{ ...pulledAs(moved), vector: [1, 0, 0] }]), { settled: 1, skipped: [] });

      // A field Search does not index leaves the vector as it is; the text
      // back as it was pulled takes the old hash again.
      engine.instances.update(alice, 'Note', 'n1', { author: 'ann@example.com' });
      assert.deepEqual(stale(engine).items, []);
      engine.instances.update(alice, 'Note', 'n1', { title: 'Walnut desk' });
      assert.deepEqual(stale(engine).items, [n1]);
      // Text that comes to hold nothing wants no vector, and has none.
      engine.instances.update(alice, 'Note', 'n1', { title: ' ', body: null });
      assert.deepEqual([stale(engine).items, rowsOf(engine).find((row) => row.id === 'n1')], [[], { id: 'n1', hashed: false, embedded: false, settledBy: null }]);
      assert.deepEqual(settle(engine, [{ ...pulledAs(n1), vector: [1, 0, 0] }]).skipped, [{ id: 'n1', reason: 'moved' }]);
    });

    test('a vector ranks by cosine similarity; a query and a vector fuse their rankings by reciprocal rank, each hit saying how it matched', () => {
      const engine = open();
      publish(engine, notes([{ name: 'Search', config: { fields: ['title'], vectors: VECTORS } }]));
      for (const [id, title] of [
        ['a', 'walnut desk'],
        ['b', 'walnut chair'],
        ['c', 'oak table'],
        ['d', 'walnut walnut walnut lamp'],
        ['e', 'walnut stool'],
      ]) {
        engine.instances.create(alice, 'Note', { title }, { id });
      }
      // e is not embedded yet: it matches by its text alone.
      settleAll(engine, { a: [1, 0, 0], b: [0, 1, 0], c: [0.8, 0.6, 0], d: [-1, 0, 0] });

      const byVector = search(engine, { vector: [2, 0, 0], model: 'test-3d' });
      assert.deepEqual(ids(byVector), ['a', 'c', 'b', 'd']);
      assert.deepEqual(byVector.items[0], { id: 'a', rank: 1, score: rrf(1), vector: { rank: 1, similarity: 1 } });
      assert.ok(Math.abs((byVector.items[1].vector?.similarity ?? 0) - 0.8) < 1e-6);
      assert.deepEqual(byVector.items.slice(2), [
        { id: 'b', rank: 3, score: rrf(3), vector: { rank: 3, similarity: 0 } },
        { id: 'd', rank: 4, score: rrf(4), vector: { rank: 4, similarity: -1 } },
      ]);

      // By text alone, as before: d, then a, b and e in the order they were indexed.
      assert.deepEqual(search(engine, { query: 'walnut' }).items.slice(0, 2), [
        { id: 'd', rank: 1, field: 'title', snippet: [{ text: 'walnut', match: true }, { text: ' ', match: false }, { text: 'walnut', match: true }, { text: ' ', match: false }, { text: 'walnut', match: true }, { text: ' lamp', match: false }] },
        { id: 'a', rank: 2, field: 'title', snippet: [{ text: 'walnut', match: true }, { text: ' desk', match: false }] },
      ]);

      // Fused: a is second by text and first by vector, so it passes d.
      const fused = search(engine, { query: 'walnut', vector: [1, 0, 0] });
      assert.deepEqual(ids(fused), ['a', 'd', 'b', 'c', 'e']);
      assert.deepEqual(fused.items[0], {
        id: 'a',
        rank: 1,
        score: rrf(2, 1),
        field: 'title',
        snippet: [{ text: 'walnut', match: true }, { text: ' desk', match: false }],
        text: { rank: 2 },
        vector: { rank: 1, similarity: 1 },
      });
      assert.deepEqual(
        fused.items.map((hit) => [hit.id, hit.score, hit.text?.rank, hit.vector?.rank]),
        [
          ['a', rrf(2, 1), 2, 1],
          ['d', rrf(1, 4), 1, 4],
          ['b', rrf(3, 3), 3, 3],
          ['c', rrf(2), undefined, 2],
          ['e', rrf(4), 4, undefined],
        ]
      );
      assert.equal(fused.items[3].snippet, undefined, 'a hit by vector alone has no snippet');

      // Pages go on through the fused ranking.
      const first = search(engine, { query: 'walnut', vector: [1, 0, 0], limit: 3 });
      assert.deepEqual([ids(first), first.next !== null], [['a', 'd', 'b'], true]);
      const second = search(engine, { query: 'walnut', vector: [1, 0, 0], limit: 3, cursor: first.next as string });
      assert.deepEqual([second.items.map((hit) => [hit.id, hit.rank]), second.next], [[['c', 4], ['e', 5]], null]);
    });

    test('a vector the config does not take is invalid_argument, and so is one on a schema without vectors', () => {
      const engine = desks();
      const issues = (params: Record<string, unknown>) => thrown(() => search(engine, params), OperationParamsError).issues;
      assert.deepEqual(issues({ vector: [1, 0] }), [{ path: '/vector', message: "holds 2 numbers, and the config's vectors hold 3" }]);
      assert.deepEqual(issues({ vector: [1, 0, 0], model: 'other' }), [{ path: '/model', message: 'is "other", and Note\'s vectors come from "test-3d"' }]);
      assert.deepEqual(issues({ vector: [0, 0, 0] }), [{ path: '/vector', message: 'is all zeros, which has no direction to compare' }]);
      for (const params of [{}, { model: 'test-3d', query: 'walnut' }, { vector: [] }]) {
        assert.equal(thrown(() => search(engine, params), OperationParamsError).code, 'invalid_argument', JSON.stringify(params));
      }

      const plain = open();
      publish(plain, notes([{ name: 'Search', config: { fields: ['title'] } }]));
      const none = "Note's Search keeps no vectors: its config has none";
      assert.deepEqual(thrown(() => search(plain, { vector: [1, 0, 0] }), OperationParamsError).issues, [{ path: '/vector', message: none }]);
      for (const operation of ['staleEmbeddings', 'settleEmbeddings']) {
        const params = operation === 'staleEmbeddings' ? {} : { items: [{ id: 'n1', sourceHash: 'x', vector: [1, 0, 0] }] };
        assert.deepEqual(thrown(() => plain.instances.invokeSchema(embedder, 'Note', operation, params), OperationParamsError).issues, [
          { path: '', message: none },
        ]);
      }
    });

    test('similar lists the nearest instances and leaves the instance out: by its text, then fused with its vector once settled', () => {
      const engine = open();
      publish(engine, notes());
      engine.instances.create(alice, 'Note', { title: 'Walnut desk', body: 'Solid walnut with brass legs.' }, { id: 'n1' });
      engine.instances.create(alice, 'Note', { title: 'Walnut chair', body: 'Pairs with the desk.' }, { id: 'n2' });
      engine.instances.create(alice, 'Note', { title: 'Oak shelf', body: 'Brass brackets.' }, { id: 'n3' });
      engine.instances.create(alice, 'Note', { title: 'Pine', body: 'Nothing alike.' }, { id: 'n4' });
      const similar = (id: string) => call(engine, 'similar', { id }) as Page & { embedded: boolean };

      const byText = similar('n1');
      assert.deepEqual([ids(byText), byText.embedded, byText.next], [['n2', 'n3'], false, null]);
      assert.deepEqual(Object.keys(byText.items[0]), ['id', 'rank', 'field', 'snippet']);

      settleAll(engine, { n1: [1, 0, 0], n2: [0.9, 0.1, 0], n3: [0, 1, 0], n4: [0.95, 0, 0.05] });
      const fused = similar('n1');
      assert.equal(fused.embedded, true);
      assert.deepEqual(
        fused.items.map((hit) => [hit.id, hit.score, hit.text?.rank, hit.vector?.rank]),
        [
          ['n2', rrf(1, 2), 1, 2],
          ['n3', rrf(2, 3), 2, 3],
          ['n4', rrf(1), undefined, 1],
        ],
        'n4 shares no word with n1 and is nearest by vector'
      );

      // An instance without a vector is ranked by its text alone, and one
      // whose words match nothing has no neighbours.
      engine.instances.update(alice, 'Note', 'n4', { body: 'Nothing comparable.' });
      assert.deepEqual(similar('n4'), { items: [], next: null, embedded: false });
      const missing = thrown(() => similar('n9'), EngineError);
      assert.deepEqual([missing.code, missing.message], ['not_found', 'Note n9 does not exist in namespace default']);
    });

    test("similar takes a draft's text in place of an id, with an optional vector, and ranks as similar of an instance does, with nothing left out", () => {
      const engine = open();
      publish(engine, notes());
      engine.instances.create(alice, 'Note', { title: 'Walnut desk', body: 'Solid walnut with brass legs.' }, { id: 'n1' });
      engine.instances.create(alice, 'Note', { title: 'Walnut chair', body: 'Pairs with the desk.' }, { id: 'n2' });
      engine.instances.create(alice, 'Note', { title: 'Oak shelf', body: 'Brass brackets.' }, { id: 'n3' });
      engine.instances.create(alice, 'Note', { title: 'Pine', body: 'Nothing alike.' }, { id: 'n4' });
      const similar = (params: Record<string, unknown>) => call(engine, 'similar', params) as Page & { embedded: boolean };
      // The text n1 holds ranks every instance but n1 as similar of n1 does, and n1 itself first.
      const draft = 'Walnut desk\n\nSolid walnut with brass legs.';
      const byText = similar({ text: draft });
      assert.deepEqual([ids(byText), byText.embedded], [['n1', 'n2', 'n3'], false]);
      assert.deepEqual(byText.items.slice(1), (similar({ id: 'n1' }).items as SearchHit[]).map((hit, index) => ({ ...hit, rank: index + 2 })));
      // With a vector, the two rankings fuse, as they do for a settled instance.
      settleAll(engine, { n1: [1, 0, 0], n2: [0.9, 0.1, 0], n3: [0, 1, 0], n4: [0.95, 0, 0.05] });
      const fused = similar({ text: draft, vector: [1, 0, 0], model: 'test-3d' });
      assert.equal(fused.embedded, true);
      assert.deepEqual(
        fused.items.map((hit) => [hit.id, hit.score, hit.text?.rank, hit.vector?.rank]),
        [
          ['n1', rrf(1, 1), 1, 1],
          ['n2', rrf(2, 3), 2, 3],
          ['n3', rrf(3, 4), 3, 4],
          ['n4', rrf(2), undefined, 2],
        ]
      );
      // A vector alone ranks when the text holds no word to search for.
      assert.deepEqual(ids(similar({ text: 'a b', vector: [0, 1, 0] })), ['n3', 'n2', 'n1', 'n4']);
      assert.deepEqual(similar({ text: 'a b' }), { items: [], next: null, embedded: false });
      // A page at a time.
      const first = similar({ text: draft, limit: 2 });
      assert.deepEqual([ids(first), ids(similar({ text: draft, limit: 2, cursor: first.next as string }))], [['n1', 'n2'], ['n3']]);
      // Its parameters: one of id and text, a vector only with text, the config's model and dimensions.
      const issues = (params: Record<string, unknown>) => thrown(() => similar(params), OperationParamsError).issues;
      assert.equal(issues({ id: 'n1', text: draft })[0].path, '');
      assert.equal(issues({})[0].path, '');
      assert.ok(issues({ id: 'n1', vector: [1, 0, 0] }).length > 0);
      assert.deepEqual(issues({ text: draft, vector: [1, 0] }), [{ path: '/vector', message: "holds 2 numbers, and the config's vectors hold 3" }]);
      assert.deepEqual(issues({ text: draft, vector: [1, 0, 0], model: 'other' }), [{ path: '/model', message: 'is "other", and Note\'s vectors come from "test-3d"' }]);
      const plain = open();
      publish(plain, notes([{ name: 'Search', config: { fields: ['title'] } }]));
      assert.deepEqual(thrown(() => plain.instances.invokeSchema(alice, 'Note', 'similar', { text: 'walnut', vector: [1, 0, 0] }), OperationParamsError).issues, [
        { path: '/vector', message: "Note's Search keeps no vectors: its config has none" },
      ]);
      // A caller who may not read the schema is refused, as for every search.
      const closed = open({ policy: ((request) => request.principal.subject === 'alice' || request.action !== 'read' || request.operation !== undefined) as AccessPolicy });
      publish(closed, notes());
      assert.equal(thrown(() => closed.instances.invokeSchema(bob, 'Note', 'similar', { text: draft }), EngineError).code, 'forbidden');
    });

    test('a version that changes the model, the dimensions or the fields makes every vector stale; removing vectors forgets them', () => {
      const engine = desks();
      const vectors = { n1: [1, 0, 0], n2: [0, 1, 0] };
      settleAll(engine, vectors);
      const pulled = () => stale(engine).items.map((item) => item.id);
      const config = (change: Record<string, unknown>) => notes([{ name: 'Search', config: { ...SEARCH.config, ...change } }]);

      // Weights and the permission change nothing a vector is computed from.
      publish(engine, config({ weights: { body: 2 } }));
      publish(engine, config({ vectors: { ...VECTORS, permission: 'notes.write' } }));
      assert.deepEqual(pulled(), []);

      // A new model: every vector goes stale under a new hash, and a settle
      // under the old one is skipped.
      const before = stale(engine, {}).items;
      const old = engine.storage.all("SELECT id, source_hash FROM bhv_search__rows WHERE schema = 'Note' AND id = 'n1'")[0];
      publish(engine, config({ vectors: { ...VECTORS, model: 'test-3d-v2' } }));
      assert.deepEqual([before, pulled()], [[], ['n1', 'n2']]);
      assert.equal(stale(engine).model, 'test-3d-v2');
      assert.deepEqual(search(engine, { vector: [1, 0, 0] }).items, [], 'no vector ranks until it is settled again');
      assert.deepEqual(
        settle(engine, [{ id: 'n1', sourceHash: String(old.source_hash), vector: [1, 0, 0] }]).skipped,
        [{ id: 'n1', reason: 'moved' }]
      );
      settleAll(engine, vectors);

      // New dimensions: stale again, and a vector of the old ones is refused.
      publish(engine, config({ vectors: { ...VECTORS, model: 'test-3d-v2', dimensions: 2 } }));
      assert.deepEqual(pulled(), ['n1', 'n2']);
      assert.equal(thrown(() => settleAll(engine, vectors), OperationParamsError).code, 'invalid_argument');
      settleAll(engine, { n1: [1, 0], n2: [0, 1] });

      // Removed: the hashes and vectors go; added back, every instance is stale.
      publish(engine, notes([{ name: 'Search', config: { fields: ['title', 'body'] } }]));
      assert.deepEqual(rowsOf(engine).filter((row) => row.hashed || row.embedded), []);
      publish(engine, config({}));
      assert.deepEqual(pulled(), ['n1', 'n2']);
      settleAll(engine, vectors);

      // New fields rebuild the index, with every vector stale.
      publish(engine, config({ fields: ['title'], weights: {} }));
      assert.deepEqual(stale(engine).items.map((item) => [item.id, item.text]), [
        ['n1', 'Walnut desk'],
        ['n2', 'Oak chair'],
      ]);

      // Search removed and added: the index and its vectors start again.
      publish(engine, notes([]));
      assert.deepEqual(rowsOf(engine), []);
      publish(engine, config({}));
      assert.deepEqual(pulled(), ['n1', 'n2']);
    });

    test("each namespace's vectors are its own", () => {
      const engine = open({ namespaces: { names: ['shared', 'east', 'west'], shared: 'shared' } });
      engine.schemas.define(alice, notes(), { namespace: 'shared' });
      engine.schemas.publish(alice, 'Note', { namespace: 'shared' });
      for (const namespace of ['east', 'west']) {
        engine.instances.create(alice, 'Note', { title: `Walnut ${namespace}` }, { id: 'n1', namespace });
      }
      const pull = (namespace: string) => engine.instances.invokeSchema(alice, 'Note', 'staleEmbeddings', {}, { namespace }) as Stale;
      const [east] = pull('east').items;
      assert.deepEqual(
        engine.instances.invokeSchema(embedder, 'Note', 'settleEmbeddings', { items: [{ ...pulledAs(east), vector: [1, 0, 0] }] }, { namespace: 'west' }),
        { settled: 0, skipped: [{ id: 'n1', reason: 'moved' }] },
        "west's n1 has text of its own"
      );
      engine.instances.invokeSchema(embedder, 'Note', 'settleEmbeddings', { items: [{ ...pulledAs(east), vector: [1, 0, 0] }] }, { namespace: 'east' });
      assert.deepEqual([pull('east').items, ids(pull('west'))], [[], ['n1']]);
      const west = engine.instances.invokeSchema(alice, 'Note', 'search', { vector: [1, 0, 0] }, { namespace: 'west' }) as Page;
      assert.deepEqual(west.items, []);
    });
  });

  describe(`search across schemas (${driver})`, () => {
    // Note keeps vectors of test-3d, Memo of other-3d, Task none; Secret is
    // bob's to not read, and Plain does not compose Search.
    function shelves(policy?: AccessPolicy): Engine {
      const engine = open(policy ? { policy } : {});
      publish(engine, notes([{ name: 'Search', config: { fields: ['title'], vectors: VECTORS } }]));
      publish(engine, notes([{ name: 'Search', config: { fields: ['title'], vectors: { ...VECTORS, model: 'other-3d' } } }], 'Memo'));
      publish(engine, notes([{ name: 'Search', config: { fields: ['title'] } }], 'Task'));
      publish(engine, notes([{ name: 'Search', config: { fields: ['title'] } }], 'Secret'));
      publish(engine, notes([], 'Plain'));
      const create = (schema: string, id: string, title: string) => engine.instances.create(alice, schema, { title }, { id });
      create('Note', 'n1', 'Walnut desk');
      create('Note', 'n2', 'Oak chair');
      create('Memo', 'm1', 'Walnut memo');
      create('Task', 't1', 'Buy walnut oil');
      create('Task', 't2', 'Sand the walnut desk');
      create('Secret', 's1', 'Walnut secret');
      create('Plain', 'p1', 'Walnut plain');
      settleAll(engine, { n1: [1, 0, 0], n2: [0, 1, 0] });
      settleAll(engine, { m1: [1, 0, 0] }, 'Memo');
      return engine;
    }

    const notSecret: AccessPolicy = ({ principal, schema }) => principal.subject === 'alice' || principal.subject === 'embedder' || schema !== 'Secret';

    function across(engine: Engine, params: Record<string, unknown>, principal: Principal = bob): Page<SchemaSearchHit> {
      return engine.search(principal, params) as Page<SchemaSearchHit>;
    }

    const places = (page: Page<SchemaSearchHit>) => page.items.map((hit) => `${hit.schema}/${hit.id}`);

    test("fuses each readable schema's ranking, a schema's best tying another's, and skips one the caller may not read", () => {
      const engine = shelves(notSecret);
      const byText = across(engine, { query: 'walnut' });
      assert.deepEqual(places(byText), ['Memo/m1', 'Note/n1', 'Task/t1', 'Task/t2']);
      assert.deepEqual(byText.items[0], {
        schema: 'Memo',
        id: 'm1',
        rank: 1,
        score: rrf(1),
        field: 'title',
        snippet: [{ text: 'Walnut', match: true }, { text: ' memo', match: false }],
        text: { rank: 1 },
      });
      assert.deepEqual(places(across(engine, { query: 'walnut' }, alice)), ['Memo/m1', 'Note/n1', 'Secret/s1', 'Task/t1', 'Task/t2']);

      // A vector ranks the schemas of its model; the query alone the others.
      const fused = across(engine, { query: 'walnut', vector: [1, 0, 0], model: 'test-3d' });
      assert.deepEqual(
        fused.items.map((hit) => [`${hit.schema}/${hit.id}`, hit.rank, hit.score, hit.text?.rank, hit.vector?.rank]),
        [
          ['Note/n1', 1, rrf(1, 1), 1, 1],
          ['Memo/m1', 2, rrf(1), 1, undefined],
          ['Task/t1', 3, rrf(1), 1, undefined],
          ['Note/n2', 4, rrf(2), undefined, 2],
          ['Task/t2', 5, rrf(2), 2, undefined],
        ]
      );
      assert.deepEqual(places(across(engine, { vector: [1, 0, 0], model: 'other-3d' })), ['Memo/m1']);
      assert.deepEqual(across(engine, { vector: [1, 0, 0], model: 'none' }), { items: [], next: null });

      // Pages go on through the merged ranking.
      const first = across(engine, { query: 'walnut', limit: 3 });
      assert.deepEqual(places(across(engine, { query: 'walnut', limit: 3, cursor: first.next as string })), ['Task/t2']);
    });

    test('skips a schema whose search the policy refuses, and refuses parameters it does not take', () => {
      const noTaskSearch: AccessPolicy = (request) => notSecret(request) && !(request.schema === 'Task' && request.operation === 'search');
      const engine = shelves(noTaskSearch);
      assert.deepEqual(places(across(engine, { query: 'walnut' })), ['Memo/m1', 'Note/n1']);
      for (const params of [{}, { vector: [1, 0, 0] }, { query: 'walnut', model: 'test-3d' }, { query: 'walnut', schema: 'Note' }, { query: 'walnut', limit: 0 }]) {
        assert.equal(thrown(() => across(engine, params), OperationParamsError).code, 'invalid_argument', JSON.stringify(params));
      }
      const fts5 = thrown(() => across(engine, { query: 'walnut AND', syntax: 'fts5' }), OperationParamsError);
      assert.equal(fts5.issues[0].path, '/query');
      assert.equal(thrown(() => engine.search(bob, { query: 'walnut' }, { namespace: 'nowhere' }), EngineError).code, 'unknown_namespace');
    });
  });
}

// The HTTP route and the MCP tool of the search across schemas, and the
// embedder's operations as routes and tools.
describe('search across schemas over HTTP and MCP', () => {
  const authenticate: Authenticator = async (ctx) =>
    ctx.bearerToken === 'embedder' ? embedder : ctx.bearerToken === 'alice' ? { subject: 'alice', permissions: [] } : null;

  function engineWithNotes(): Engine {
    const engine = openTestEngine();
    publish(engine, notes());
    engine.instances.create(alice, 'Note', { title: 'Walnut desk' }, { id: 'n1' });
    engine.instances.create(alice, 'Note', { title: 'Oak chair' }, { id: 'n2' });
    return engine;
  }

  async function post(app: Hono, path: string, body: unknown, token = 'alice', type = 'application/json'): Promise<{ status: number; body: any }> {
    const response = await app.request(path, {
      method: 'POST',
      headers: { authorization: `Bearer ${token}`, 'content-type': type },
      body: JSON.stringify(body),
    });
    return { status: response.status, body: await response.json() };
  }

  test('POST /namespaces/{namespace}/search answers the merged ranking; the embedder pulls and settles on the schema-level routes', async () => {
    const engine = engineWithNotes();
    const app = engineApp(engine, { authenticate });
    const pulled = await post(app, '/namespaces/default/schemas/Note/operations/staleEmbeddings', {}, 'embedder');
    assert.deepEqual([pulled.status, pulled.body.data.items.map((item: { id: string }) => item.id)], [200, ['n1', 'n2']]);
    const items = pulled.body.data.items.map((item: { id: string; sourceHash: string }, index: number) => ({
      id: item.id,
      sourceHash: item.sourceHash,
      vector: index === 0 ? [1, 0, 0] : [0, 1, 0],
    }));
    const settled = await post(app, '/namespaces/default/schemas/Note/operations/settleEmbeddings', { items }, 'embedder');
    assert.deepEqual([settled.status, settled.body.data], [200, { settled: 2, skipped: [] }]);
    assert.equal((await post(app, '/namespaces/default/schemas/Note/operations/settleEmbeddings', { items })).status, 403);

    const found = await post(app, '/namespaces/default/search', { query: 'walnut', vector: [1, 0, 0], model: 'test-3d' });
    assert.equal(found.status, 200);
    assert.deepEqual(
      found.body.data.items.map((hit: SchemaSearchHit) => [hit.schema, hit.id, hit.text?.rank, hit.vector?.rank]),
      [
        ['Note', 'n1', 1, 1],
        ['Note', 'n2', undefined, 2],
      ]
    );
    assert.deepEqual([(await post(app, '/namespaces/default/search', {})).status, (await post(app, '/namespaces/default/search', {})).body.code], [400, 'invalid_argument']);
    assert.equal((await post(app, '/namespaces/default/search', { query: 'walnut' }, 'alice', 'text/plain')).status, 415);
    assert.equal((await post(app, '/namespaces/nowhere/search', { query: 'walnut' })).status, 404);
  });

  test('the MCP tool search is listed where a schema composes Search, and runs the search across schemas', async () => {
    const engine = engineWithNotes();
    const bare = openTestEngine();
    assert.ok(!bare.tools.manifest(alice).tools.some((tool) => tool.name === 'engine.search'), 'no schema to search, no tool');

    const app = new Hono();
    app.route('/api', engineMcp(engine, { authenticate }));
    const server = await listen(app);
    const client = new Client({ name: 'engine-test', version: '1.0.0' }, { versionNegotiation: { mode: 'legacy' } });
    try {
      await client.connect(
        new StreamableHTTPClientTransport(new URL(`${server.url}/api${MCP_PATH.replace('{namespace}', 'default')}`), {
          requestInit: { headers: { authorization: 'Bearer alice' } },
        })
      );
      const tools = (await client.listTools()).tools;
      const names = tools.map((tool) => tool.name);
      for (const name of ['search', 'note_search', 'note_similar', 'note_stale_embeddings', 'note_settle_embeddings']) {
        assert.ok(names.includes(name), name);
      }
      const tool = tools.find((candidate) => candidate.name === 'search');
      assert.deepEqual(
        [tool?.annotations?.readOnlyHint, Object.keys(tool?.inputSchema.properties ?? {})],
        [true, ['cursor', 'limit', 'model', 'query', 'syntax', 'vector']]
      );
      const result = (await client.callTool({ name: 'search', arguments: { query: 'walnut' } })) as CallToolResult;
      assert.deepEqual(result.structuredContent, {
        items: [
          {
            schema: 'Note',
            id: 'n1',
            rank: 1,
            score: rrf(1),
            field: 'title',
            snippet: [{ text: 'Walnut', match: true }, { text: ' desk', match: false }],
            text: { rank: 1 },
          },
        ],
        next: null,
      });
      const refused = (await client.callTool({ name: 'search', arguments: { vector: [1, 0, 0] } })) as CallToolResult;
      assert.equal(refused.isError, true);
      assert.equal((refused.structuredContent as { code: string }).code, 'invalid_argument');
    } finally {
      await client.close();
      await server.close();
    }
  });
});

interface Listening {
  url: string;
  close(): Promise<void>;
}

interface BunRuntime {
  serve(options: { fetch: (request: Request) => Response | Promise<Response>; port: number; hostname: string }): { port: number; stop(force?: boolean): unknown };
}

// listen serves an app on a free port: Bun.serve on Bun, @hono/node-server elsewhere.
async function listen(app: Hono): Promise<Listening> {
  const bun = (globalThis as { Bun?: BunRuntime }).Bun;
  if (bun) {
    const server = bun.serve({ fetch: (request) => app.fetch(request), port: 0, hostname: '127.0.0.1' });
    return { url: `http://127.0.0.1:${server.port}`, close: async () => void server.stop(true) };
  }
  return new Promise((resolve) => {
    const server = serveNode({ fetch: app.fetch, port: 0, hostname: '127.0.0.1' }, (info: AddressInfo) => {
      resolve({
        url: `http://127.0.0.1:${info.port}`,
        close: () =>
          new Promise<void>((done) => {
            (server as NodeServer).closeAllConnections();
            server.close(() => done());
          }),
      });
    }) as NodeServer;
  });
}
