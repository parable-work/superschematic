/*
The routes of an issue tracker's files and links (D154), written by hand
over issues-db's repositories and a bucket: the generated router serves no
references or attachments yet. Each route runs within its caller, as a
generated router's operations do, so every read keeps to what the caller may
see (D156).

A browser posts a file on an issue's thread in three steps: it asks for an
upload (POST .../files), PUTs the bytes straight to the bucket through the
URL it gets back, and completes the upload (POST .../files/:id/complete),
which checks the bytes against the size and SHA-256 it declared. It then
posts a message whose block names the file. Filing the file onto a note is
one call, with no second upload, and the note then lists the issue among its
backlinks.

The imports name the package `superschematic build-all` writes for
issues-db.
*/
import pg from 'pg';
import { Hono } from 'hono';
import type { Bucket } from '@superschematic/http-runtime';
import { withCaller } from '@superschematic/http-runtime/hono';
import { NotFoundError, RowFeatureError, attachmentBlock } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@schemas/issues-db-types/orm';

/** Who calls: the app's own session reader, which answers a user's id or none. */
export type Caller = (request: Request) => Promise<string | undefined>;

/** The routes, over pool and the bucket that holds the files. */
export function issuesServer(pool: pg.Pool, bucket: Bucket, caller: Caller): Hono {
  const orm = createOrm(pool);
  const app = new Hono();

  // Each request runs as its caller, so the repositories keep its reads to
  // the rows it may see, and its writes record it.
  app.use('*', async (c, next) => {
    const subject = await caller(c.req.raw);
    await withCaller(subject ? { subject } : null, next);
  });

  // Ask for an upload: the file's name, type, size and SHA-256.
  app.post('/:table/:key/files', async c => {
    const { name, mediaType, size, sha256 } = await c.req.json();
    const { attachment, upload } = await orm.attachments.begin({ to: { table: c.req.param('table'), key: c.req.param('key') }, name, mediaType, size, sha256 }, { bucket });
    return c.json({ file: attachment, upload }, 201);
  });

  // Complete it once the bytes are in the bucket.
  app.post('/:table/:key/files/:id/complete', async c => {
    const file = await orm.attachments.complete({ table: c.req.param('table'), id: c.req.param('id') }, { bucket });
    return c.json(file);
  });

  // Post a file on an issue's thread: a message whose block names it.
  app.post('/issues/:key/messages', async c => {
    const { body, file } = await c.req.json();
    const blocks = file ? [attachmentBlock(await orm.attachments.get({ table: 'Issue', id: file }))] : [];
    const subject = (await caller(c.req.raw)) ?? '';
    const message = await orm.withBehaviorCaller({ subject, permissions: [] }).issue.comments.post(c.req.param('key'), { body, blocks } as never);
    return c.json(message, 201);
  });

  // File it onto another row: no second upload.
  app.post('/:table/:key/files/:id/file', async c => {
    const { to, move } = await c.req.json();
    const filed = await orm.attachments.file({ table: c.req.param('table'), id: c.req.param('id'), to, move });
    return c.json(filed, 201);
  });

  // A row's files, and a download link that asks the file's preview on its
  // first view.
  app.get('/:table/:key/files', async c => c.json(await orm.attachments.list({ table: c.req.param('table'), key: c.req.param('key') })));
  app.get('/:table/:key/files/:id', async c => c.json(await orm.attachments.download({ table: c.req.param('table'), id: c.req.param('id') }, { bucket })));

  // The rows that point at a row: what the caller may see, and for the
  // owner of a private note, how many others there are.
  app.get('/:table/:key/backlinks', async c => c.json(await orm.references.referencedBy({ table: c.req.param('table'), key: c.req.param('key') })));
  app.post('/:table/:key/references', async c => {
    const { to, role } = await c.req.json();
    return c.json(await orm.references.add({ from: { table: c.req.param('table'), key: c.req.param('key') }, to, role }), 201);
  });

  app.onError((error, c) => {
    if (error instanceof NotFoundError) return c.json({ code: 'not_found' }, 404);
    if (error instanceof RowFeatureError) return c.json({ code: error.code, detail: error.message, ...error.details }, 422);
    throw error;
  });
  return app;
}

/**
 * The FileReleased queue's handler, which a worker over issues-db runs:
 * deletes the object once no file of any table names it.
 */
export async function releaseFile(pool: pg.Pool, bucket: Bucket, message: { objectKey: string }): Promise<void> {
  await createOrm(pool).asSystem().attachments.release(message, { bucket });
}
