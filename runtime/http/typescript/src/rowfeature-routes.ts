import type { Bucket } from './bucket.js';
import type { RequestContext } from './operation.js';
import { HttpProblem, internal, notFound } from './problem.js';
import { uuidValue } from './uuid.js';

/*
The handlers of a generated router's row feature operations (#723, D154):
the routes the loader adds for an API's @rowFeatureOperations sets, which
run a table's references, backlinks and files through the database's
references and attachments (orm.references, orm.attachments in its
orm.ts), with no implementation method. The route runs every step an
implemented route runs (the service step, the end-user step and the
permission check of the operation's own permissions among them); the
handler then runs it through the repositories as the route's principal
(orm.as), so each read and write keeps to what the caller may read (D156)
and records who called, whichever copy of the runtime the repositories
were opened with: a reference to a row the caller cannot read is left out entirely,
a row the caller cannot read is the 404 of one that is not there, and only
the owner of a row whose audience is not everyone hears how many rows it
cannot see point at it.

A route that names one of the row's files reads it first: a file of
another row, or one that is not there, is the 404 of one that is not
there, so a caller who may read one row cannot reach another's files by
their ids. A file's upload and download sign a URL of the bucket
RouterOptions.attachmentsBucket holds, the one the set names.

A refusal of the declaration's rules (the runtime's RowFeatureError) is
the 422 problem of its code and details: table_not_allowed, too_many,
invalid, media_type, too_large, row_full, not_pending, expired,
not_uploaded, mismatch, not_ready and the rest; and the repositories'
NotFoundError the 404. Any other error is the server's own.

It imports nothing from pg or the repositories, so the main entry stays
free of them: it takes the repositories as a value, by the shape of what
it calls, and recognizes a refusal by its name, whichever copy of the
runtime threw it.
*/

/** The operations a @rowFeatureOperations set serves (ir.RowFeatureOperationNames). */
export type RowFeatureOperationName =
  | 'references'
  | 'addReference'
  | 'removeReference'
  | 'backlinks'
  | 'attachments'
  | 'beginUpload'
  | 'completeUpload'
  | 'download'
  | 'fileAttachment'
  | 'removeAttachment';

/** A row feature operation as the generated router carries it: the table, its key's path parameter and the operation. */
export interface RowFeatureOperation {
  readonly type: string;
  readonly key: string;
  readonly operation: RowFeatureOperationName;
}

/** A route's request as a row feature handler reads it. */
export interface RowFeatureRequest {
  readonly path: Readonly<Record<string, unknown>>;
  /** The body, as the operation's input type's parser read it. */
  readonly input?: unknown;
}

interface RowRefShape {
  readonly table: string;
  readonly key: string;
}

interface FileShape {
  readonly table: string;
  readonly key: string;
}

/** The members of the repositories the handlers call, by their shape. */
interface RowFeatureOrm {
  readonly references?: {
    add(input: { from: RowRefShape; to: RowRefShape; role?: string }): Promise<unknown>;
    remove(input: { from: RowRefShape; to: RowRefShape; role?: string }): Promise<number>;
    of(row: RowRefShape): Promise<unknown>;
    referencedBy(row: RowRefShape): Promise<unknown>;
  };
  readonly attachments?: {
    begin(input: Record<string, unknown>, options: { bucket: Bucket }): Promise<unknown>;
    complete(file: { table: string; id: string }, options: { bucket: Bucket }): Promise<unknown>;
    list(row: RowRefShape): Promise<unknown>;
    get(file: { table: string; id: string }): Promise<FileShape>;
    download(file: { table: string; id: string }, options: { bucket: Bucket }): Promise<unknown>;
    detach(file: { table: string; id: string }): Promise<void>;
    file(input: Record<string, unknown>): Promise<unknown>;
  };
}

type Values = Readonly<Record<string, unknown>>;

/** The members of the route's body, an object the input type's parser read; nulls stand for absent members. */
function bodyOf(request: RowFeatureRequest): Values {
  const input = request.input;
  if (input === null || typeof input !== 'object' || Array.isArray(input)) return {};
  return Object.fromEntries(Object.entries(input as Values).filter(([, value]) => value !== null && value !== undefined));
}

/** The row's key as the path holds it, in its wire form. */
function keyOf(operation: RowFeatureOperation, request: RowFeatureRequest): string {
  const key = request.path[operation.key];
  return typeof key === 'bigint' ? key.toString() : String(key ?? '');
}

/** Whether two keys name one row: equal, or one UUID in either of its forms. */
function sameKey(a: string, b: string): boolean {
  if (a === b) return true;
  const x = uuidValue(a);
  return x !== undefined && x === uuidValue(b);
}

/** A row reference of the body's member, a RowRef. */
function rowRefOf(value: unknown): RowRefShape {
  const ref = (value ?? {}) as Values;
  return { table: String(ref.table ?? ''), key: String(ref.key ?? '') };
}

/**
 * Runs a row feature operation of the table's row the path names, through
 * the repositories in orm, within the route's caller, and answers what the
 * references or the attachments answer. bucket is the bucket a file's
 * upload and download sign a URL of; an operation that reads one is the
 * generator's error without it.
 */
export async function rowFeatureOperation(
  orm: object,
  operation: RowFeatureOperation,
  ctx: RequestContext,
  request: RowFeatureRequest,
  bucket?: Bucket
): Promise<unknown> {
  // The repositories as the route's principal, explicitly, so the reads
  // keep to the caller and the writes record them whichever copy of the
  // runtime the repositories were opened with, as a table operation's do
  // (orm.as): a principal whose subject is no UUID reads as no one.
  const acting = ctx.principal && uuidValue(ctx.principal.subject) !== undefined ? ctx.principal : null;
  const repositories = (orm as { as(principal: unknown): RowFeatureOrm }).as(acting);
  const row: RowRefShape = { table: operation.type, key: keyOf(operation, request) };
  const body = bodyOf(request);
  const references = () => {
    if (!repositories.references) throw new Error(`row feature operation: the repositories hold no references, which ${operation.type}'s ${operation.operation} runs through`);
    return repositories.references;
  };
  const attachments = () => {
    if (!repositories.attachments) throw new Error(`row feature operation: the repositories hold no attachments, which ${operation.type}'s ${operation.operation} runs through`);
    return repositories.attachments;
  };
  const files = () => {
    if (!bucket) throw new Error(`row feature operation: ${operation.type}'s ${operation.operation} signs a URL of a bucket, and the router holds none (RouterOptions.attachmentsBucket)`);
    return bucket;
  };
  /** The file the path names, which must be one of the row's. */
  const file = async (): Promise<{ table: string; id: string }> => {
    const ref = { table: operation.type, id: String(request.path.file ?? '') };
    const found = await attachments().get(ref);
    if (!sameKey(found.key, row.key) || found.table !== operation.type) throw notFound(`${operation.type} ${row.key} holds no file ${ref.id}`);
    return ref;
  };
  try {
    switch (operation.operation) {
      case 'references':
        return await references().of(row);
      case 'addReference':
        return await references().add({ from: row, to: rowRefOf(body.to), ...(typeof body.role === 'string' ? { role: body.role } : {}) });
      case 'removeReference':
        return { removed: await references().remove({ from: row, to: rowRefOf(body.to), ...(typeof body.role === 'string' ? { role: body.role } : {}) }) };
      case 'backlinks':
        return await references().referencedBy(row);
      case 'attachments':
        return await attachments().list(row);
      case 'beginUpload':
        return await attachments().begin({ ...body, to: row }, { bucket: files() });
      case 'completeUpload':
        return await attachments().complete(await file(), { bucket: files() });
      case 'download':
        return await attachments().download(await file(), { bucket: files() });
      case 'fileAttachment':
        return await attachments().file({ ...(await file()), to: rowRefOf(body.to), ...(body.move === true ? { move: true } : {}), ...(typeof body.role === 'string' ? { role: body.role } : {}) });
      case 'removeAttachment':
        await attachments().detach(await file());
        return true;
    }
  } catch (error) {
    throw problemOf(operation, error);
  }
  throw internal(`row feature operation: ${String(operation.operation)} is no operation of a table's references and files`);
}

/** The problem an error of the references or the attachments is. */
function problemOf(operation: RowFeatureOperation, error: unknown): unknown {
  if (error instanceof HttpProblem) return error;
  if (error === null || typeof error !== 'object') return error;
  const { name, code, message, details } = error as { name?: unknown; code?: unknown; message?: unknown; details?: unknown };
  if (name === 'NotFoundError') return notFound(typeof message === 'string' && message !== '' ? message : `${operation.type} not found`, { cause: error });
  if (name === 'RowFeatureError' && typeof code === 'string') {
    return new HttpProblem(422, typeof message === 'string' ? message : code, {
      code,
      ...(details !== null && typeof details === 'object' && Object.keys(details).length > 0 ? { details } : {}),
      cause: error,
    });
  }
  return error;
}
