import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  HttpProblem,
  decodeCursor,
  encodeCursor,
  lastPage,
  morePage,
  pageFromRows,
  problemBody,
  readPage,
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
    readonly request?: { readonly cursor: string | null; readonly limit: number };
    readonly problem?: Problem;
  }[];
  readonly cursors: readonly { readonly name: string; readonly keyJSON: string; readonly cursor: string }[];
  readonly refused: readonly { readonly name: string; readonly cursor: string; readonly problem: Problem }[];
  readonly fromRows: readonly {
    readonly name: string;
    readonly rows: unknown[];
    readonly limit: number;
    readonly page: { readonly items: unknown[]; readonly next: string | null };
  }[];
}

const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as Corpus;

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
    for (const section of [corpus.reads, corpus.cursors, corpus.refused, corpus.fromRows]) {
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
          const { cursor, limit } = vector.request!;
          expect(readPage(query, vector.spec)).toEqual(cursor === null ? { limit } : { cursor, limit });
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

  for (const vector of corpus.fromRows) {
    test(`fromRows: ${vector.name}`, () => {
      expect(pageFromRows(vector.rows, vector.limit, row => row)).toEqual(vector.page);
    });
  }
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
});
