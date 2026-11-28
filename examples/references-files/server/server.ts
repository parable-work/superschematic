/*
What the issue tracker adds to the routes issues-api's generated router
serves (#723, D154). The router serves an issue's conversation
(@behaviorOperations) and the references, backlinks and files of issues
and notes (@rowFeatureOperations) through issues-db's repositories, each
as its caller, so every read keeps to what the caller may see (D156): ask
for an upload (POST /api/issue/{id}/files), PUT the bytes straight to the
bucket through the URL it answers, complete it (POST
/api/issue/{id}/files/{file}/complete), post a message whose attachment
block names the file (POST /api/issue/{id}/comments/post), file it onto a
note (POST /api/issue/{id}/files/{file}/file), and read the note's
backlinks (GET /api/note/{id}/backlinks). The generated SDK has a method
for each, and the MCP endpoint a tool for the reads and the add of a
reference.

The app gives the router its caller, the repositories and the bucket, and
runs the FileReleased queue's worker, which deletes an object once no file
names it. The imports name the packages `superschematic build-all` writes
for issues-api and issues-db.
*/
import pg from 'pg';
import { Hono } from 'hono';
import type { Bucket, Principal, RequestContext } from '@superschematic/http-runtime';
import { buildRouter, type Implementations } from '@schemas/issues-api-api';
import { createOrm } from '@schemas/issues-db-types/orm';

/** Who calls: the app's own session reader, which answers a user's id or none. */
export type Caller = (request: Request) => Promise<string | undefined>;

/** The API's routes, over pool and the bucket that holds the files. */
export function issuesServer(pool: pg.Pool, bucket: Bucket, caller: Caller): Hono {
  const authenticate = Object.assign(
    async (ctx: RequestContext): Promise<Principal | null> => {
      const subject = await caller(ctx.raw);
      return subject ? { subject, permissions: [] } : null;
    },
    { readsAuthorization: true }
  );
  // Every operation of issues-api is generated: no implementation method.
  const implementations: Implementations = {};
  const app = new Hono();
  app.route('/', buildRouter(implementations, { authenticate, orm: createOrm(pool), attachmentsBucket: bucket }));
  return app;
}

/**
 * The FileReleased queue's handler, which a worker over issues-db runs:
 * deletes the object once no file of any table names it.
 */
export async function releaseFile(pool: pg.Pool, bucket: Bucket, message: { objectKey: string }): Promise<void> {
  await createOrm(pool).asSystem().attachments.release(message, { bucket });
}
