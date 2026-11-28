// The searches of runtime/http/testdata/search_tables_parity.json (#740),
// which search.test.ts runs on Postgres and search_sqlite.test.ts on
// SQLite, as the Go and Rust runtimes' search tests do: the corpus is
// written into the search index as it stands, in a scope of its own, and
// each search answers the titles of its hits, in order, and its total, or
// null where the database refuses it.
import { readFileSync } from 'node:fs';
import type { Principal } from '@superschematic/http-runtime';
import type { SearchOptions, SearchRequest, SearchResult } from '@superschematic/http-runtime/postgres';

export type Dialect = 'postgres' | 'sqlite';

/** What a search answers: its hits' titles in order, and its total. */
export interface ParityAnswer {
  readonly titles: readonly string[];
  readonly total: number;
}

interface CorpusRow {
  readonly table: string;
  readonly key: string;
  readonly title: string;
  readonly body?: string;
  readonly comments?: string;
  readonly files?: string;
  readonly embedding?: readonly number[];
}

export interface ParitySearch {
  readonly name: string;
  readonly q: string;
  readonly tables?: readonly string[];
  readonly vector?: readonly number[];
  readonly limit?: number;
  readonly offset?: number;
  readonly postgres?: ParityAnswer | null;
  readonly sqlite?: ParityAnswer | null;
}

const file = JSON.parse(readFileSync(new URL('./search_tables_parity.json', import.meta.url), 'utf8')) as { corpus: CorpusRow[]; searches: ParitySearch[] };

/** The scope every corpus row is in, which keeps the searches to them. */
export const PARITY_SCOPE = 'search-parity';
/** The model the corpus's embeddings are of. */
const MODEL = 'parity-model';

export const paritySearches: readonly ParitySearch[] = file.searches;

/** Writes the corpus into the search index through sql, a statement on the database as the system. */
export async function writeParityCorpus(dialect: Dialect, sql: (text: string, values: unknown[]) => Promise<unknown>): Promise<void> {
  for (const row of file.corpus) {
    const values: unknown[] = [row.table, row.key, row.title, row.body ?? null, row.comments ?? null, row.files ?? null, PARITY_SCOPE];
    if (dialect === 'postgres') {
      values.push(row.embedding ?? null, row.embedding ? MODEL : null);
      await sql(
        `INSERT INTO search_index (source_table, source_key, title, body, comments, files, scope, embedding, embedding_model) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::float8[], $9)`,
        values
      );
    } else {
      await sql(`INSERT INTO search_index (source_table, source_key, title, body, comments, files, scope) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`, values);
    }
  }
}

/**
 * Writes the corpus into the environment's search index (#740) through
 * sql, a statement on the search database, as rows of database, each at
 * version 0, as a source's copy would hold them.
 */
export async function writeEnvironmentParityCorpus(database: string, sql: (text: string, values: unknown[]) => Promise<unknown>): Promise<void> {
  for (const row of file.corpus) {
    await sql(
      `INSERT INTO environment_search_index (database, source_table, source_key, title, body, comments, files, scope, embedding, embedding_model, version) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::float8[], $10, 0)`,
      [database, row.table, row.key, row.title, row.body ?? null, row.comments ?? null, row.files ?? null, PARITY_SCOPE, row.embedding ?? null, row.embedding ? MODEL : null]
    );
  }
}

/** Runs search through searchTables as principal: its answer, or null when it is refused. */
export async function parityAnswer(
  searchTables: (orm: object, principal: Principal | null, request: SearchRequest, options?: SearchOptions) => Promise<SearchResult>,
  orm: object,
  principal: Principal,
  search: ParitySearch
): Promise<ParityAnswer | null> {
  const request: SearchRequest = {
    q: search.q,
    scope: PARITY_SCOPE,
    ...(search.tables ? { tables: search.tables } : {}),
    ...(search.vector ? { vector: search.vector } : {}),
    ...(search.limit !== undefined ? { limit: search.limit } : {}),
    ...(search.offset !== undefined ? { offset: search.offset } : {}),
  };
  const options: SearchOptions = search.vector ? { embeddings: { model: MODEL, embed: async texts => texts.map(() => []) } } : {};
  try {
    const result = await searchTables(orm, principal, request, options);
    return { titles: result.items.map(hit => hit.title), total: result.total };
  } catch (error) {
    if (error instanceof TypeError) return null;
    throw error;
  }
}
