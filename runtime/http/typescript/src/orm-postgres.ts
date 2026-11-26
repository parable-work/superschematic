import pg from 'pg';
import { parseFieldKeys } from './fieldcrypt.js';
import { ormOfRuntime, type Orm, type OrmDefinition, type OrmOptions, type OrmPool, type OrmRuntime } from './orm.js';
import { postgresTypeParsers, withTypeParsers } from './pgcodec.js';
import { parseArray } from './pgtext.js';

/*
The generated repositories on Postgres (D79): ormOf runs a types package's
./orm definitions over a pg Pool with the types each statement reads with
and the field keys read by ./fieldcrypt. The runtime itself (./orm) imports
neither pg nor node:crypto, so ./d1 runs the same repositories over a D1
binding in a Worker (D182).
*/

const INTERVAL = 1186;
const INTERVAL_ARRAY = 1187;
const POINT = 600;
const POINT_ARRAY = 1017;

/**
 * The types each statement of a repository reads with: the pool's
 * (postgresTypeParsers), and an INTERVAL and a POINT as their text, which
 * the codec reads, whatever the pool was opened with.
 */
const ormTypes = withTypeParsers(pg.types, {
  ...postgresTypeParsers,
  [INTERVAL]: value => value,
  [INTERVAL_ARRAY]: parseArray,
  [POINT]: value => value,
  [POINT_ARRAY]: parseArray,
});

/** What the repositories run with on Postgres. */
export const POSTGRES_ORM_RUNTIME: OrmRuntime = { types: ormTypes, parseFieldKeys };

/**
 * The repositories of a database's tables over a pg pool, as its types
 * package's ./orm entry describes them, acting as the user options names,
 * or as the user it is when a string, or as no one for null; with no user,
 * as the caller of the operation being run (D87). createOrm there calls it
 * with the definitions and types the result. The field keys are read once,
 * and a malformed secret throws FieldEncryptionError here.
 */
export function ormOf<R>(pool: OrmPool, orm: OrmDefinition, options?: string | null | OrmOptions): Orm<R> {
  if (orm.dialect !== undefined && orm.dialect !== 'postgres') {
    throw new Error(`orm: ormOf runs a definition of Postgres over a pg pool, and this one is ${orm.dialect}'s; open it with sqliteOrm (@superschematic/http-runtime/d1)`);
  }
  return ormOfRuntime<R>(pool, orm, POSTGRES_ORM_RUNTIME, options);
}
