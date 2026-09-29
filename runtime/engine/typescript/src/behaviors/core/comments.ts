/*
Comments, the core's comments on an instance (D16). comment adds one, as
a reply to one of the instance's comments or not; listComments reads them
a page at a time in the order they were added, and goes through no write:
it is read-only, so the access policy is asked for read. Each comment has
a number on its instance, 1, 2, 3, ..., which a reply names; numbers are
per instance, so a comment's id says nothing about any other instance.
Comments live in the behavior's own table, and commentCount in a column
of its own, so a list read costs no count query. Deleting the instance
deletes its comments.

configChange: Comments takes no config. It can be added to a schema that
has instances, which start with none, and cannot be removed from one:
their comments would stay behind with nothing to delete them.
*/

import { OperationParamsError } from '../../errors.js';
import type { Row } from '../../storage/driver.js';
import { defineBehavior, type InstanceView } from '../behavior.js';
import declaration from './declarations/Comments.behavior.json' with { type: 'json' };
import { page, pageRequest } from './paging.js';

/** One comment, as comment and listComments return it. */
export interface CommentRecord {
  readonly id: number;
  readonly replyTo?: number;
  readonly body: string;
  readonly createdBy: string;
  readonly createdAt: number;
}

const COLUMNS = 'comment, reply_to, body, created_by, created_at';

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

function record(row: Row): CommentRecord {
  const comment = {
    id: Number(row.comment),
    ...(row.reply_to === null ? {} : { replyTo: Number(row.reply_to) }),
    body: String(row.body),
    createdBy: String(row.created_by),
    createdAt: Number(row.created_at),
  };
  return comment;
}

export const comments = defineBehavior({
  declaration,

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the comments its instances have would stay behind with nothing to delete them';
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'comments',
      columns: { count: { type: 'integer', notNull: true, default: 0 } },
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('comments')} (
          namespace  TEXT    NOT NULL,
          schema     TEXT    NOT NULL,
          id         TEXT    NOT NULL,
          comment    INTEGER NOT NULL,
          reply_to   INTEGER,
          body       TEXT    NOT NULL,
          created_by TEXT    NOT NULL,
          created_at INTEGER NOT NULL,
          PRIMARY KEY (namespace, schema, id, comment)
        ) STRICT`);
      },
    },
  ],

  operations: {
    comment(context, params) {
      const table = context.sql.table('comments');
      const replyTo = params.replyTo as number | undefined;
      if (
        replyTo !== undefined &&
        !context.sql.get(`SELECT 1 AS found FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND comment = ?`, [...key(context), replyTo])
      ) {
        throw new OperationParamsError('Comments', 'comment', [{ path: '/replyTo', message: `${context.schema} ${context.id} has no comment ${replyTo}` }]);
      }
      const last = context.sql.get(`SELECT MAX(comment) AS last FROM ${table} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
      const id = Number(last?.last ?? 0) + 1;
      context.sql.run(`INSERT INTO ${table} (namespace, schema, id, ${COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, [
        ...key(context),
        id,
        replyTo ?? null,
        params.body as string,
        context.principal.subject,
        context.now,
      ]);
      context.columns.set({ count: Number(context.columns.get().count) + 1 });
      return record({ comment: id, reply_to: replyTo ?? null, body: params.body as string, created_by: context.principal.subject, created_at: context.now });
    },

    listComments(context, params) {
      const { limit, after } = pageRequest('Comments', 'listComments', params);
      const rows = context.sql.all(
        `SELECT ${COLUMNS} FROM ${context.sql.table('comments')}
         WHERE namespace = ? AND schema = ? AND id = ? AND comment > ?
         ORDER BY comment LIMIT ?`,
        [...key(context), after, limit + 1]
      );
      return page(rows.map(record), limit, (comment) => comment.id);
    },
  },

  fields: {
    commentCount: (view) => Number(view.columns.get().count),
  },

  afterChange(context, change) {
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('comments')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
    }
  },
});
