import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Duplex } from 'node:stream';
import pg from 'pg';
import type { Pool, PoolConfig } from 'pg';
import { withTypeParsers } from './pgcodec.js';
import type { CloudSqlConnection, DatabaseConnection } from './stackconfig.js';

/*
The database of a generated TypeScript server (D51, section 8.6 of
docs/stack-model.md): a pg Pool opened from the database field its sql edge
derives, which loadDatabase reads. This is the package's ./postgres entry,
so the main entry never imports pg. pg is an optional peer dependency, and
so is @google-cloud/cloud-sql-connector, which only a server some
environment places on Cloud SQL needs, as only such a Go server links the
Go connector (section 8.1).

A pool reads BIGINT, NUMERIC and DATE as the TypeScript types hold them
(D79): a number, refused past 2^53 - 1, the exact text, and the
YYYY-MM-DD text, through type parsers of its own (postgresTypeParsers),
never pg's global ones, so a pool opened elsewhere in the process is left
as it was. typeParsers: false keeps pg's: text for a BIGINT and a NUMERIC,
and a Date at local midnight for a DATE.

A connection string opens a pool with pg. A Cloud SQL connector
configuration opens one whose connections the connector dials, over its
own TLS, logging in as the IAM database user with no password: IAM database
authentication, on the instance's public IP, as the Go server's dialer
does. One connector serves every Cloud SQL pool of the process; ending the
last such pool closes it, which stops its certificate refreshes.

A pool connects when it is first used, so the server starts while its
database is not up and /readyz, which pings each pool, reports it. The Node
connector reads the instance's connection settings from the Cloud SQL Admin
API when the pool is opened, which the Go dialer leaves to the first dial.

A client that dials Postgres itself, as the behavior engine does from its
addon (D58), reaches Cloud SQL through cloudSqlSocket: the connector's
local proxy on a unix socket in a directory of its own, each connection
to it carried over a connection the connector dials, with IAM database
authentication, as the Go server's engine socket is. Its URL names the
directory as its host, the IAM user and the database, with no password and
no TLS of its own.
*/

/** What connectPostgres and cloudSqlSocket use of a Cloud SQL connector. */
export interface CloudSqlConnector {
  getOptions(options: { readonly instanceConnectionName?: string; readonly ipType?: unknown; readonly authType?: unknown }): Promise<{ stream: () => Duplex }>;
  /** Listens on a unix socket at listenOptions.path and carries each connection to the instance; close stops it. cloudSqlSocket's. */
  startLocalProxy?(options: {
    readonly instanceConnectionName?: string;
    readonly ipType?: unknown;
    readonly authType?: unknown;
    readonly listenOptions: { readonly path: string | undefined };
  }): Promise<void>;
  close(): void;
}

/** What connectPostgres uses of @google-cloud/cloud-sql-connector. */
export interface CloudSqlConnectorModule {
  readonly Connector: new () => CloudSqlConnector;
  readonly IpAddressTypes: { readonly PUBLIC: unknown };
  readonly AuthTypes: { readonly IAM: unknown };
}

// The connector this package is checked against fits what it uses of it:
// the build fails here when a release of the connector stops fitting.
type Fits<T extends CloudSqlConnectorModule> = T;
// eslint-disable-next-line @typescript-eslint/no-unused-vars
type _InstalledConnectorFits = Fits<typeof import('@google-cloud/cloud-sql-connector')>;

/** The pool settings connectPostgres takes beside the connection, which it sets itself. */
export type PostgresPoolSettings = Omit<PoolConfig, 'connectionString' | 'host' | 'port' | 'user' | 'password' | 'database' | 'stream' | 'ssl' | 'types'>;

export interface ConnectPostgresOptions {
  /** Pool settings: max, idleTimeoutMillis and the like. */
  readonly pool?: PostgresPoolSettings;
  /**
   * Called with the error of a pooled connection that fails while idle, such
   * as one the database closes when it restarts; the pool drops it. Without
   * a listener pg would throw it on the process. console.error by default.
   */
  readonly onIdleError?: (error: Error) => void;
  /** Loads the Cloud SQL connector; a dynamic import of @google-cloud/cloud-sql-connector by default. For tests. */
  readonly loadConnector?: () => Promise<CloudSqlConnectorModule>;
  /**
   * Whether the pool reads BIGINT as a number, refused past 2^53 - 1,
   * NUMERIC as its exact text and DATE as its YYYY-MM-DD text
   * (postgresTypeParsers, D79); true unless false, which keeps pg's own
   * parsers. Either way pg's global types are left alone.
   */
  readonly typeParsers?: boolean;
}

/** The package a Cloud SQL configuration needs. */
export const CLOUD_SQL_CONNECTOR_PACKAGE = '@google-cloud/cloud-sql-connector';

async function importConnector(): Promise<CloudSqlConnectorModule> {
  return (await import(CLOUD_SQL_CONNECTOR_PACKAGE)) as CloudSqlConnectorModule;
}

/** A loaded connector module and the connector it built. */
interface LoadedConnector {
  readonly module: CloudSqlConnectorModule;
  readonly connector: CloudSqlConnector;
}

/** The process's one Cloud SQL connector, and how many pools use it. */
interface ConnectorState {
  readonly connector: Promise<LoadedConnector>;
  pools: number;
}

let shared: ConnectorState | undefined;

function acquireConnector(load: () => Promise<CloudSqlConnectorModule>): ConnectorState {
  if (!shared) {
    const state: ConnectorState = {
      connector: load().then(
        module => ({ module, connector: new module.Connector() }),
        (err: unknown) => {
          // A connector that failed to load is not kept: the next pool tries again.
          if (shared === state) shared = undefined;
          throw new Error(
            `a Cloud SQL connection needs ${CLOUD_SQL_CONNECTOR_PACKAGE}, which could not be loaded; add it to the server's dependencies (${err instanceof Error ? err.message : String(err)})`,
            { cause: err }
          );
        }
      ),
      pools: 0,
    };
    shared = state;
  }
  shared.pools++;
  return shared;
}

function releaseConnector(state: ConnectorState): void {
  if (--state.pools > 0) return;
  if (shared === state) shared = undefined;
  state.connector.then(
    ({ connector }) => connector.close(),
    () => undefined
  );
}

/** A pool over the process's Cloud SQL connector, which ending the last such pool closes. */
class CloudSqlPool extends pg.Pool {
  readonly #state: ConnectorState;
  #released = false;

  constructor(config: PoolConfig, state: ConnectorState) {
    super(config);
    this.#state = state;
  }

  override end(): Promise<void>;
  override end(callback: () => void): void;
  override end(callback?: () => void): Promise<void> | void {
    const release = (): void => {
      if (this.#released) return;
      this.#released = true;
      releaseConnector(this.#state);
    };
    if (callback) {
      super.end(() => {
        release();
        callback();
      });
      return;
    }
    return super.end().finally(release);
  }
}

async function cloudSqlPool(db: CloudSqlConnection, settings: PoolConfig, load: () => Promise<CloudSqlConnectorModule>): Promise<Pool> {
  const state = acquireConnector(load);
  try {
    const { module, connector } = await state.connector;
    const driver = await connector.getOptions({
      instanceConnectionName: db.instance,
      ipType: module.IpAddressTypes.PUBLIC,
      authType: module.AuthTypes.IAM,
    });
    // The connector's connections are already encrypted, so pg adds no TLS
    // of its own, whatever PGSSLMODE says.
    return new CloudSqlPool({ ...settings, stream: driver.stream, user: db.user, database: db.database, ssl: false }, state);
  } catch (err) {
    releaseConnector(state);
    throw new Error(`Cloud SQL instance ${db.instance}: ${err instanceof Error ? err.message : String(err)}`, { cause: err });
  }
}

/**
 * Opens a pool over db: pg over a connection string, or the Cloud SQL
 * connector with IAM database authentication over a Cloud SQL connector
 * configuration. Rejects for a Cloud SQL configuration when the connector
 * is not installed or cannot read the instance's settings, and for a D1
 * binding, which only a Worker reaches (D64).
 */
export async function connectPostgres(db: DatabaseConnection, options: ConnectPostgresOptions = {}): Promise<Pool> {
  if ('d1' in db) {
    throw new Error(`D1 database ${db.d1.database} is no Postgres database: a Worker reaches it through its binding ${db.d1.binding} (D64)`);
  }
  const settings: PoolConfig = { ...(options.pool ?? {}) };
  if (options.typeParsers !== false) settings.types = withTypeParsers(pg.types);
  const pool = 'cloudSql' in db ? await cloudSqlPool(db.cloudSql, settings, options.loadConnector ?? importConnector) : new pg.Pool({ ...settings, connectionString: db.url });
  const onIdleError = options.onIdleError ?? ((error: Error) => console.error(`postgres: an idle connection failed: ${error.message}`));
  pool.on('error', onIdleError);
  return pool;
}

/** A unix socket that carries each connection to a Cloud SQL instance over the connector (cloudSqlSocket). */
export interface CloudSqlSocket {
  /** The directory that holds the socket, .s.PGSQL.5432, which a client names as its host. */
  readonly directory: string;
  /**
   * A libpq keyword/value connection string: the directory as its host, the
   * IAM database user and the database, with no password, since the
   * connector authenticates as the IAM user, and no TLS of its own, since
   * the connector's connections are encrypted.
   */
  readonly url: string;
  /** Stops the socket's connector, which ends its connections, and removes the directory. Closing twice does nothing. */
  close(): void;
}

/**
 * Serves the Cloud SQL database db to a client that dials Postgres itself
 * (the behavior engine, D58) on a unix socket of its own: the connector's
 * local proxy, in a directory of its own, carrying each connection over a
 * connection the connector dials on the instance's public IP with IAM
 * database authentication. A connector of its own serves it, so closing it
 * leaves every pool's. Rejects when the connector is not installed, cannot
 * read the instance's settings, or cannot listen.
 */
export async function cloudSqlSocket(db: CloudSqlConnection, options: Pick<ConnectPostgresOptions, 'loadConnector'> = {}): Promise<CloudSqlSocket> {
  let module: CloudSqlConnectorModule;
  try {
    module = await (options.loadConnector ?? importConnector)();
  } catch (err) {
    throw new Error(
      `a Cloud SQL connection needs ${CLOUD_SQL_CONNECTOR_PACKAGE}, which could not be loaded; add it to the server's dependencies (${err instanceof Error ? err.message : String(err)})`,
      { cause: err }
    );
  }
  const connector = new module.Connector();
  const directory = mkdtempSync(join(tmpdir(), 'cloudsql-'));
  let closed = false;
  const close = (): void => {
    if (closed) return;
    closed = true;
    connector.close();
    rmSync(directory, { recursive: true, force: true });
  };
  try {
    if (typeof connector.startLocalProxy !== 'function') {
      throw new Error(`the installed ${CLOUD_SQL_CONNECTOR_PACKAGE} has no startLocalProxy`);
    }
    await connector.startLocalProxy({
      instanceConnectionName: db.instance,
      ipType: module.IpAddressTypes.PUBLIC,
      authType: module.AuthTypes.IAM,
      listenOptions: { path: join(directory, '.s.PGSQL.5432') },
    });
  } catch (err) {
    close();
    throw new Error(`Cloud SQL instance ${db.instance}: ${err instanceof Error ? err.message : String(err)}`, { cause: err });
  }
  return {
    directory,
    url: `host=${keywordValue(directory)} port=5432 user=${keywordValue(db.user)} dbname=${keywordValue(db.database)} sslmode=disable`,
    close,
  };
}

/** Quotes a value of a libpq keyword/value connection string. */
function keywordValue(value: string): string {
  return `'${value.replaceAll('\\', '\\\\').replaceAll("'", "\\'")}'`;
}

/**
 * Resolves when the database answers `SELECT 1` within timeoutMs, and
 * rejects otherwise: what a server's /readyz asks of each pool.
 */
export async function ping(pool: Pick<Pool, 'query'>, timeoutMs: number): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`the database did not answer within ${timeoutMs} ms`)), timeoutMs);
  });
  try {
    await Promise.race([pool.query('SELECT 1'), timeout]);
  } finally {
    clearTimeout(timer);
  }
}

// The queue client (D53): enqueue in the caller's transaction, and a
// worker's claims, over the definitions a DB's types package exports from
// its ./queues entry.
export { ClaimLostError, enqueue, parseArray, postgresQueue } from './queue.js';
export type { PostgresQueue, Queue, QueueClaim, QueueDefinition, QueueField, QueueQueryable, QueueQueryConfig, QueueStatements } from './queue.js';

// The value codec (D79): a UUID's two forms, a row read as the types hold
// it and a value bound as its column takes it, over the descriptors a DB's
// types package carries, and the type parsers each pool reads with.
export { uuidFromDb, uuidToDb } from './uuid.js';
export { PG_TYPES, columnKey, decodeRow, decodeValue, encodeParam, parseInt8, postgresTypeParsers, withTypeParsers } from './pgcodec.js';
export type { RowField, TypeParsers, ValueCodec } from './pgcodec.js';

// The generated repositories (D79): the tables a DB's types package
// describes in its ./orm entry, whose createOrm calls ormOf over a pool.
export { NoUserError, NotFoundError, ORM_MEMBERS, PageKeyError, fill, ormOf } from './orm.js';
export type {
  Actor,
  BoolFilter,
  Clauses,
  DateFilter,
  DateTimeFilter,
  DurationFilter,
  FindOptions,
  JsonFilter,
  NullFilter,
  NumberFilter,
  OrderBy,
  Orm,
  OrmDefinition,
  OrmMembers,
  OrmPool,
  OrmQueryConfig,
  OrmQueryResult,
  OrmQueryable,
  OrmTx,
  PageOptions,
  PageSortOf,
  PointFilter,
  ReferenceDefinition,
  ReferencedByFilter,
  Repository,
  RowPage,
  SoftDeleteRepository,
  StringFilter,
  TableColumn,
  TableDefinition,
  TableStatements,
  UniqueKeyDefinition,
  UuidFilter,
} from './orm.js';
