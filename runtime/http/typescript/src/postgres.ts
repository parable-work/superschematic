import type { Duplex } from 'node:stream';
import pg from 'pg';
import type { Pool, PoolConfig } from 'pg';
import type { CloudSqlConnection, Database } from './stackconfig.js';

/*
The database of a generated TypeScript server (D51, section 8.6 of
docs/stack-model.md): a pg Pool opened from the database field its sql edge
derives, which loadDatabase reads. This is the package's ./postgres entry,
so the main entry never imports pg. pg is an optional peer dependency, and
so is @google-cloud/cloud-sql-connector, which only a server some
environment places on Cloud SQL needs, as only such a Go server links the
Go connector (section 8.1).

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
*/

/** What connectPostgres uses of a Cloud SQL connector. */
export interface CloudSqlConnector {
  getOptions(options: { readonly instanceConnectionName?: string; readonly ipType?: unknown; readonly authType?: unknown }): Promise<{ stream: () => Duplex }>;
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
export type PostgresPoolSettings = Omit<PoolConfig, 'connectionString' | 'host' | 'port' | 'user' | 'password' | 'database' | 'stream' | 'ssl'>;

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

async function cloudSqlPool(db: CloudSqlConnection, settings: PostgresPoolSettings, load: () => Promise<CloudSqlConnectorModule>): Promise<Pool> {
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
 * is not installed or cannot read the instance's settings.
 */
export async function connectPostgres(db: Database, options: ConnectPostgresOptions = {}): Promise<Pool> {
  const settings = options.pool ?? {};
  const pool = 'cloudSql' in db ? await cloudSqlPool(db.cloudSql, settings, options.loadConnector ?? importConnector) : new pg.Pool({ ...settings, connectionString: db.url });
  const onIdleError = options.onIdleError ?? ((error: Error) => console.error(`postgres: an idle connection failed: ${error.message}`));
  pool.on('error', onIdleError);
  return pool;
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
