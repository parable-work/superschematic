# search-across-tables

A team's workspace whose projects, tasks and pages are found by one search
(D155): one ranked list across tables, each caller's access checked inside
the query, a task's conversation and files found with it, and embeddings
that re-rank when a model is configured.

| Path | What it is |
|---|---|
| `schemas/services/workspace-db` | DB, Postgres: `Project`, `Task` and `Page` are `@searchable`, each with its title, its body and its scope (the project); `Task` keeps a conversation (`Comments`) and files (`@attachments`), and its access code is `Encrypted<T>`, which the index never holds; `Page` is an `@audience` table a person may keep to themselves; `SearchIndexing` is the queue a write that changes the indexed text enqueues on, whose fields the build fills |
| `server/server.ts` | the search route and MCP tool over the generated repositories, and the `SearchIndexing` handler a worker runs, with embeddings from a model provider that embeds, or none |

## What it shows

- One search ranks tasks, pages and projects together with Postgres full
  text: a word in a title ranks above one in a body, which ranks above one
  in a conversation or a file.
- A page its owner keeps to themselves is in no one else's results, and
  the total and the pages are the same as if it did not exist. Making a
  page private takes effect at once: a trigger copies the page's audience
  into its index row in the same transaction.
- A changed title is found by its new words once the queue's worker has
  run: on Cloudflare Workers, at the next Cron firing, within a minute.
- `scope` keeps a search to one project.
- An agent run, or a party with a grant, calls the MCP tool through its
  grant's token and finds only the rows its grant reaches.

## Where it runs

`TestSearchAcrossTablesExampleLoads` (`internal/loader/search`) loads the
schema. The same scenario, under the fixture name `fixture-search-db`, runs
end to end against Postgres in `TestSearchTablesEndToEnd`
(`internal/generator/tsgen`): two users and a party with a grant search the
same text across six tables, and each gets exactly the rows its list reads
show.
