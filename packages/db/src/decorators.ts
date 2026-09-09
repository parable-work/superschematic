const noopPropertyDecorator: PropertyDecorator = () => {};
const noopClassDecorator: ClassDecorator = () => {};

export interface VersionedPruneReference {
  /** Referencing table name (snake_case, as in DDL). */
  readonly table: string;
  /** Referencing table's column holding the versioned row key. */
  readonly keyColumn: string;
  /** Referencing table's column holding the pinned _version. */
  readonly versionColumn: string;
}

export interface VersionedOptions {
  readonly retentionDays?: number;
  readonly partitionBy?: "month";
  /**
   * Keep history rows whose (key, _version) pair is referenced by these tables
   * out of the generated prune function, so externally pinned row images
   * survive retention. Requires retentionDays.
   *
   * Takes one reference or a list of them: a versioned table can have several
   * independent readers of its historical rows, and each needs its own
   * exclusion. Declaring a second reference never displaces the first.
   *
   * Verification is syntactic only. It checks that this implies retentionDays,
   * that every identifier is snake_case (they are interpolated into DDL), and
   * that no reference is repeated. It cannot check the dangerous direction: a
   * table that declares retentionDays and has as-of readers but does NOT
   * declare the exclusion that protects them prunes their pinned images with
   * no diagnostic. Nothing in superschematic knows who reads a table's history, so that
   * one stays on the schema author and on repo-local tests.
   */
  readonly pruneKeepReferencedBy?: VersionedPruneReference | readonly VersionedPruneReference[];
}

export interface IndexOptions {
  readonly unique?: boolean;
  readonly name?: string;
}

export function index<T>(
  _keys: readonly (keyof T)[],
  _uniqueOrOptions?: boolean | IndexOptions,
): ClassDecorator {
  return noopClassDecorator;
}

export function versioned<TFunction extends Function>(target: TFunction): void;
export function versioned(_options?: VersionedOptions): ClassDecorator;
export function versioned<TFunction extends Function>(_arg?: VersionedOptions | TFunction): ClassDecorator | void {
  if (typeof _arg === "function") {
    return;
  }
  return noopClassDecorator;
}

export const key: PropertyDecorator = noopPropertyDecorator;
export const unique: PropertyDecorator = noopPropertyDecorator;
export const searchField: PropertyDecorator = noopPropertyDecorator;
export const jsonField: PropertyDecorator = noopPropertyDecorator;

/**
 * Marks a field that @source projections are expected to carry. A projection
 * omitting the field draws a verification warning instead of the usual
 * silent removal.
 */
export const sourceMustProject: PropertyDecorator = noopPropertyDecorator;
