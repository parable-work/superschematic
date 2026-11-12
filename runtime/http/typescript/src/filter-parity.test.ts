import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { HttpProblem, problemBody, readFilters, type FilterFields, type FilterOperator, type QueryFilter } from './index';

/*
The filter parity vectors (D106), which the Go runtime writes (`go test
./filterparse -run TestWriteParityVectors -update` in runtime/http/go) and
the Go, TypeScript and Rust runtimes each run through their filter reader.
The file's comment states the harness.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/filter_parity.json', import.meta.url));

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
    readonly spec: FilterFields;
    readonly query: string;
    readonly filters?: readonly QueryFilter[];
    readonly problem?: Problem;
  }[];
  readonly queries: readonly {
    readonly name: string;
    readonly filters: readonly QueryFilter[];
    readonly pairs: readonly (readonly [string, string])[];
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

describe('filter parity vectors', () => {
  test('every section has vectors with distinct names', () => {
    for (const section of [corpus.reads, corpus.queries]) {
      expect(section.length).toBeGreaterThan(0);
      expect(new Set(section.map(vector => vector.name)).size).toBe(section.length);
    }
    // Refusals and reads both run.
    expect(corpus.reads.some(vector => vector.problem)).toBe(true);
    expect(corpus.reads.some(vector => vector.filters)).toBe(true);
  });

  for (const vector of corpus.reads) {
    test(`reads: ${vector.name}`, () => {
      // The query as the router's URL parses it.
      const query = new URLSearchParams(vector.query);
      if (vector.problem) {
        // Compared as JSON values, as the harness says.
        expect(JSON.parse(JSON.stringify(problemOf(() => readFilters(query, vector.spec))))).toEqual(vector.problem);
        return;
      }
      // A vector that names no filters holds none.
      expect(readFilters(query, vector.spec)).toEqual([...(vector.filters ?? [])]);
    });
  }

  for (const vector of corpus.queries) {
    test(`queries: ${vector.name}`, () => {
      // The pairs a writer sends read back, under a spec that names their
      // fields, as the filters they were written from.
      const fields: { name: string; operators: FilterOperator[] }[] = [];
      for (const filter of vector.filters) {
        let field = fields.find(candidate => candidate.name === filter.field);
        if (!field) fields.push((field = { name: filter.field, operators: [] }));
        field.operators.push(filter.operator);
      }
      const query = new URLSearchParams();
      for (const [name, value] of vector.pairs) query.append(name, value);
      // The reader answers them in the spec's order; the vector holds them
      // in the writer's.
      expect(readFilters(query, { fields }).sort(byKey)).toEqual([...vector.filters].sort(byKey));
    });
  }
});

/** Orders filters by their field and operator. */
function byKey(a: QueryFilter, b: QueryFilter): number {
  const x = `${a.field} ${a.operator}`;
  const y = `${b.field} ${b.operator}`;
  return x < y ? -1 : x > y ? 1 : 0;
}
