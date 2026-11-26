import pg from 'pg';
import { beginStatement, ormOn, type Orm, type OrmDefinition, type OrmDialect, type OrmOptions, type OrmPool } from './orm.js';
import { decodeRow, decodeValue, encodeParam, postgresTypeParsers, withTypeParsers } from './pgcodec.js';
import { parseArray } from './pgtext.js';
import { postgresRulesStore } from './rules.js';

/*
The generated repositories on Postgres (D79): ormOf, over a pg Pool, with
the dialect that reads each statement's rows through pg's types and the
Postgres value codec (./pgcodec). It is the one module of the repositories
that imports pg, so the repositories on SQLite and D1 (./orm-sqlite), which
run the same repository over another dialect, load no pg. ./postgres
exports it.
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

/** The repositories' dialect on Postgres (D79). */
export const POSTGRES_DIALECT: OrmDialect = {
  name: 'postgres',
  types: ormTypes,
  encodeParam,
  decodeRow,
  decodeValue,
  begin: options => beginStatement(options),
  rulesStore: queryable => postgresRulesStore({ query: config => queryable.query({ ...config, types: ormTypes }) }),
};

/**
 * The repositories of a database's tables over pool, as its types package's
 * ./orm entry describes them, acting as the user options names, or as the
 * user it is when a string, or as no one for null; with no user, as the
 * caller of the operation being run (D87). createOrm there calls it with
 * the definitions and types the result. The field keys are read once, and
 * a malformed secret throws FieldEncryptionError here.
 */
export function ormOf<R>(pool: OrmPool, orm: OrmDefinition, options?: string | null | OrmOptions): Orm<R> {
  return ormOn<R>(pool, orm, POSTGRES_DIALECT, options);
}
