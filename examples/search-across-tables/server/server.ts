/*
Search across a workspace's tables (D155), wired by hand over
workspace-db's repositories: the search route, the search MCP tool, and
the handler the SearchIndexing queue's worker runs. The generated router
does not mount them yet.

Every read keeps to what its caller may read, inside the query: a page a
person keeps to themselves is in no one else's results or counts, and a
caller with a grant (a party, or an agent run's token) finds only the rows
its grant reaches. Embeddings are off unless the environment gives the
server a model provider that embeds.

The imports name the package `superschematic build-all` writes for
workspace-db.
*/
import pg from 'pg';
import type { Principal } from '@superschematic/http-runtime';
import type { ModelProvider } from '@superschematic/http-runtime/providers';
import { callSearchTool, indexSearch, searchEmbeddingsOf, searchRoutes, type SearchIndexing, type SearchOptions } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@schemas/workspace-db-types/orm';

/** Who calls: the app's authenticator, a session's user, a key's, or a grant's party or agent run. */
export type Authenticate = (request: Request) => Promise<Principal | null>;

/** Search's options: embeddings when the model provider embeds and EMBEDDING_MODEL names a model, off otherwise. */
export function searchOptions(model: ModelProvider | undefined, embeddingModel: string | undefined): SearchOptions {
  if (!model?.embed || !embeddingModel) return {};
  return { embeddings: searchEmbeddingsOf(model, embeddingModel) };
}

/** The search route, GET /api/search?q=&tables=&scope=&limit=&offset=, over pool. */
export function searchServer(pool: pg.Pool, authenticate: Authenticate, options: SearchOptions = {}): (request: Request) => Promise<Response | null> {
  const orm = createOrm(pool);
  return searchRoutes({ orm, authenticate, ...options });
}

/** The search MCP tool's call, as the principal the MCP endpoint resolved. */
export async function searchTool(pool: pg.Pool, principal: Principal, args: Record<string, unknown>, options: SearchOptions = {}) {
  return callSearchTool(createOrm(pool), principal, args, options);
}

/**
 * The SearchIndexing queue's handler: a @worker of the API runs it for each
 * message, on Workers at each Cron firing. A row's index row is rebuilt
 * from the row, its conversation and its files; a row that is gone loses
 * its index row.
 */
export function searchIndexingHandler(pool: pg.Pool, options: SearchOptions = {}): (message: SearchIndexing) => Promise<void> {
  const orm = createOrm(pool);
  return async message => {
    await indexSearch(orm, message, options);
  };
}
