import { describe, expect, test } from 'bun:test';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { ftsQuery, postgresQuery, searchPlan, type SearchPlan } from './search-query';

/*
The query syntax of search across tables (#740), which the Go runtime's
crosstable and the Rust runtime's orm::search_tables read too: the plan a
query reads as, the Postgres query and the FTS5 match it renders. This test
computes each plan's wants from its q and holds
runtime/http/testdata/search_tables_parity.json to them;
SEARCH_TABLES_PARITY_WRITE=1 writes them instead. The file's corpus and
searches, the titles each database ranks for a query, are written by hand:
the generated repositories' search tests run them in each runtime, on
Postgres and on SQLite.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/search_tables_parity.json', import.meta.url));

interface PlanCase {
  name: string;
  q: string;
  plan?: SearchPlan;
  postgres?: { query: string; args: unknown[] };
  fts5?: string | null;
}

interface ParityFile {
  comment: string;
  plans: PlanCase[];
  [section: string]: unknown;
}

function wants(q: string): Required<Omit<PlanCase, 'name' | 'q'>> {
  const plan = searchPlan(q);
  const args: unknown[] = [];
  const query = postgresQuery(plan, "'simple'::regconfig", value => {
    args.push(value);
    return `$${args.length}`;
  });
  return { plan, postgres: { query, args }, fts5: ftsQuery(plan) };
}

const file = JSON.parse(readFileSync(CORPUS, 'utf8')) as ParityFile;

if (process.env.SEARCH_TABLES_PARITY_WRITE === '1') {
  for (const c of file.plans) Object.assign(c, wants(c.q));
  const text = JSON.stringify(file, null, 2).replace(/[\u0080-￿]/gu, c => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`);
  writeFileSync(CORPUS, `${text}\n`);
}

describe('search tables parity vectors', () => {
  test('every plan case has a distinct name', () => {
    expect(file.plans.length).toBeGreaterThan(0);
    expect(new Set(file.plans.map(c => c.name)).size).toBe(file.plans.length);
  });
  for (const c of file.plans) {
    test(`plans: ${c.name}`, () => {
      const got = wants(c.q);
      expect(got.plan).toEqual(c.plan!);
      expect(got.postgres).toEqual(c.postgres!);
      expect(got.fts5).toEqual(c.fts5 ?? null);
    });
  }
});
