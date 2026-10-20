import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  HttpProblem,
  OperationResult,
  SORTED_CURSOR_DETAIL,
  bindCursor,
  bindPage,
  decodeCursor,
  encodeCursor,
  lastPage,
  morePage,
  pageFromRows,
  parseSort,
  problemBody,
  readPage,
  sortText,
  sortValues,
  unbindCursor,
  type PageRequest,
  type PageSort,
  type PageSpec,
} from './index';

/*
The page parity vectors (D60), which the Go runtime writes (`go test
./paging -run TestWriteParityVectors -update` in runtime/http/go) and the
Go, TypeScript and Rust runtimes each run through their page reader and
builder. The file's comment states the harness.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/page_parity.json', import.meta.url));

interface Problem {
  readonly status: number;
  readonly code: string;
  readonly detail: string;
  readonly details: unknown;
}

interface Corpus {
  readonly comment: string;
  readonly reads: readonly {
    readonly name: string;
    readonly spec: PageSpec;
    readonly query: string;
    readonly request?: { readonly cursor: string | null; readonly limit: number; readonly sort?: string };
    readonly problem?: Problem;
  }[];
  readonly cursors: readonly { readonly name: string; readonly keyJSON: string; readonly cursor: string }[];
  readonly refused: readonly { readonly name: string; readonly cursor: string; readonly problem: Problem }[];
  readonly unencodable: readonly { readonly name: string; readonly keyJSON: string }[];
  readonly fromRows: readonly {
    readonly name: string;
    readonly rows: unknown[];
    readonly limit: number;
    readonly page: { readonly items: unknown[]; readonly next: string | null };
  }[];
  readonly bindings: readonly { readonly name: string; readonly sort: string; readonly cursor: string; readonly bound: string }[];
  readonly refusedBindings: readonly { readonly name: string; readonly sort: string; readonly cursor: string; readonly problem: Problem }[];
  readonly unbindable: readonly { readonly name: string; readonly sort: string; readonly cursor: string }[];
}

const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as Corpus;

/** A vector's sort, which the corpus writes as its text. */
function sortOf(text: string): PageSort {
  const sort = parseSort(text);
  if (sort === undefined) throw new Error(`${text} is no sort`);
  return sort;
}

/** The vector's view of a refusal: the problem the router writes for it. */
function problemOf(run: () => unknown): Problem {
  try {
    run();
  } catch (err) {
    expect(err).toBeInstanceOf(HttpProblem);
    const body = problemBody(err as HttpProblem, 'req-1');
    return { status: body.status, code: body.code!, detail: body.detail, details: body.details };
  }
  throw new Error('expected a refusal');
}

describe('page parity vectors', () => {
  test('every section has vectors with distinct names', () => {
    for (const section of [
      corpus.reads,
      corpus.cursors,
      corpus.refused,
      corpus.unencodable,
      corpus.fromRows,
      corpus.bindings,
      corpus.refusedBindings,
      corpus.unbindable,
    ]) {
      expect(section.length).toBeGreaterThan(0);
      expect(new Set(section.map(vector => vector.name)).size).toBe(section.length);
    }
  });

  for (const vector of corpus.reads) {
    test(`reads: ${vector.name}`, () => {
      // Both shapes a router may hand the reader: URLSearchParams, and each
      // key's values as Hono's queries() gives them.
      const params = new URLSearchParams(vector.query);
      const values: Record<string, string[]> = {};
      for (const [name, value] of params) (values[name] ??= []).push(value);
      for (const query of [params, values]) {
        if (vector.problem) {
          expect(problemOf(() => readPage(query, vector.spec))).toEqual(vector.problem);
        } else {
          const { cursor, limit, sort } = vector.request!;
          const want: PageRequest = {
            ...(cursor === null ? {} : { cursor }),
            limit,
            ...(sort === undefined ? {} : { sort: sortOf(sort) }),
          };
          const got = readPage(query, vector.spec);
          expect(got).toEqual(want);
          expect(Object.keys(got).sort()).toEqual(Object.keys(want).sort());
        }
      }
    });
  }

  for (const vector of corpus.cursors) {
    test(`cursors: ${vector.name}`, () => {
      const key = JSON.parse(vector.keyJSON);
      expect(encodeCursor(key)).toBe(vector.cursor);
      expect(decodeCursor(vector.cursor)).toEqual(key);
    });
  }

  for (const vector of corpus.refused) {
    test(`refused: ${vector.name}`, () => {
      expect(problemOf(() => decodeCursor(vector.cursor))).toEqual(vector.problem);
    });
  }

  for (const vector of corpus.unencodable) {
    test(`unencodable: ${vector.name}`, () => {
      const key = JSON.parse(vector.keyJSON);
      expect(() => encodeCursor(key)).toThrow(RangeError);
      expect(() => pageFromRows(['first', key, 'after'], 2, row => row)).toThrow(RangeError);
    });
  }

  for (const vector of corpus.fromRows) {
    test(`fromRows: ${vector.name}`, () => {
      expect(pageFromRows(vector.rows, vector.limit, row => row)).toEqual(vector.page);
    });
  }

  for (const vector of corpus.bindings) {
    test(`bindings: ${vector.name}`, () => {
      const sort = sortOf(vector.sort);
      expect(bindCursor(vector.cursor, sort)).toBe(vector.bound);
      expect(unbindCursor(vector.bound, sort)).toBe(vector.cursor);
      expect(bindPage(morePage([1], vector.cursor), { limit: 1, sort })).toEqual(morePage([1], vector.bound));
    });
  }

  for (const vector of corpus.refusedBindings) {
    test(`refusedBindings: ${vector.name}`, () => {
      expect(problemOf(() => unbindCursor(vector.cursor, sortOf(vector.sort)))).toEqual(vector.problem);
    });
  }

  for (const vector of corpus.unbindable) {
    test(`unbindable: ${vector.name}`, () => {
      const sort = sortOf(vector.sort);
      expect(() => bindCursor(vector.cursor, sort)).toThrow(RangeError);
      expect(() => bindPage(morePage([1], vector.cursor), { limit: 1, sort })).toThrow(RangeError);
    });
  }
});

describe('sorted pages', () => {
  const spec: PageSpec = { defaultLimit: 2, maxLimit: 5, sort: { param: 'sort', fields: ['placedAt', 'totalCents'], default: 'placedAt' } };
  const descending: PageSort = { field: 'totalCents', desc: true };

  test('a sort reads and writes as its text', () => {
    expect(sortValues(spec.sort!)).toEqual(['placedAt', '-placedAt', 'totalCents', '-totalCents']);
    expect(sortText(descending)).toBe('-totalCents');
    expect(parseSort('-totalCents')).toEqual(descending);
    for (const text of ['', '-', '--placedAt', '+placedAt', 'placedAt,id', '2nd', 'placed At']) {
      expect(parseSort(text)).toBeUndefined();
    }
  });

  test('the router binds the next page to its sort and reads its own cursor back', () => {
    const rows = [
      { id: 'a', totalCents: 300 },
      { id: 'b', totalCents: 200 },
      { id: 'c', totalCents: 100 },
    ];
    const first = readPage(new URLSearchParams('sort=-totalCents'), spec);
    expect(first).toEqual({ limit: 2, sort: descending });
    const page = pageFromRows(rows, first.limit, row => ({ totalCents: row.totalCents, id: row.id }));
    const bound = bindPage(page, first);
    expect(bound.items).toEqual(page.items);
    expect(Buffer.from(bound.next!, 'base64url').toString()).toBe('{"sort":"-totalCents","key":{"totalCents":200,"id":"b"}}');
    // The implementation's page is not written over.
    expect(page.next).toBe(encodeCursor({ totalCents: 200, id: 'b' }));

    const second = readPage(new URLSearchParams(`sort=-totalCents&cursor=${bound.next}`), spec);
    expect(second).toEqual({ cursor: page.next!, limit: 2, sort: descending });
    expect(decodeCursor(second.cursor!)).toEqual({ totalCents: 200, id: 'b' });

    // Under another sort, the default among them, it is refused.
    for (const query of [`sort=totalCents&cursor=${bound.next}`, `cursor=${bound.next}`]) {
      expect(problemOf(() => readPage(new URLSearchParams(query), spec)).detail).toBe(SORTED_CURSOR_DETAIL);
    }
  });

  test('bindPage binds a Page, an OperationResult holding one, and passes the rest', () => {
    const request: PageRequest = { limit: 1, sort: descending };
    const page = morePage([1], encodeCursor(1));
    const next = bindCursor(page.next!, descending);
    expect(bindPage(page, request)).toEqual(morePage([1], next));
    const result = bindPage(new OperationResult(page, 200, { 'x-total': '1' }), request);
    expect(result).toBeInstanceOf(OperationResult);
    expect(result.data).toEqual(morePage([1], next));
    expect(result.headers).toEqual({ 'x-total': '1' });
    const response = new Response('{}');
    expect(bindPage(response, request)).toBe(response);
    expect(bindPage(lastPage([1]), request)).toEqual(lastPage([1]));
    expect(bindPage(page, { limit: 1 })).toBe(page);
    const empty = new OperationResult(lastPage([]), 200);
    expect(bindPage(empty, request)).toBe(empty);
    // A next no router reads back under its sort is the implementation's error.
    expect(() => bindPage(morePage([1], encodeCursor('x'.repeat(740))), request)).toThrow('past the 1024 a router reads');
  });

  test('a spec whose default is no sort is the generator’s error', () => {
    const broken: PageSpec = { defaultLimit: 2, maxLimit: 5, sort: { param: 'sort', fields: ['name'], default: 'na me' } };
    expect(() => readPage(new URLSearchParams(''), broken)).toThrow(TypeError);
  });
});

describe('pages', () => {
  test('a page an implementation builds reads back as the key of its last item', () => {
    const rows = [
      { id: 'a', placedAt: '2026-10-09T14:30:00Z' },
      { id: 'b', placedAt: '2026-10-09T14:31:00Z' },
      { id: 'c', placedAt: '2026-10-09T14:32:00Z' },
    ];
    const page = pageFromRows(rows, 2, row => ({ placedAt: row.placedAt, id: row.id }));
    expect(page.items).toEqual(rows.slice(0, 2));
    expect(decodeCursor<{ placedAt: string; id: string }>(page.next!)).toEqual({ placedAt: '2026-10-09T14:31:00Z', id: 'b' });
    expect(pageFromRows(rows, 0, row => row.id)).toEqual(morePage([rows[0]!], encodeCursor('a')));
    expect(pageFromRows(rows, 3, row => row.id)).toEqual(lastPage(rows));
    // The page's items are its own list, not the rows the implementation read.
    expect(pageFromRows(rows, 3, row => row.id).items).not.toBe(rows);
  });

  test('a key that does not encode as JSON is the implementation’s error', () => {
    expect(() => encodeCursor(undefined)).toThrow(TypeError);
  });

  test('a key no router reads back throws at encode time', () => {
    expect(() => encodeCursor('x'.repeat(767))).toThrow('its cursor is 1026 characters, past the 1024 a router reads');
    expect(encodeCursor('x'.repeat(766))).toHaveLength(1024);
    expect(() => encodeCursor(Infinity)).toThrow(RangeError);
    expect(() => encodeCursor({ name: 'a\ud800' })).toThrow('a string of the key holds a lone surrogate');
    expect(() => encodeCursor({ ['\udc00']: 1 })).toThrow('a member name of the key holds a lone surrogate');
  });

  test('a forged cursor JSON reads as no other runtime does is refused', () => {
    const forged = (json: string) => Buffer.from(json).toString('base64url');
    // JSON.parse would read 1e400 as Infinity and keep a lone surrogate.
    for (const json of ['1e400', '[-1e999]', '"\\ud800"', '{"\\udfff":1}', 'null', '{ }']) {
      expect(() => decodeCursor(forged(json))).toThrow(HttpProblem);
    }
    expect(decodeCursor(forged('"\\ud83d\\ude00"'))).toBe('😀');
  });
});
