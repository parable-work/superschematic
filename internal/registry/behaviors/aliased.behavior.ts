// Aliased's class (D103): the build derives aliased.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { declaresBehavior, fromCreate, subTable, valueField, type JsonSchema, type RowKey } from "@superschematic/db";

interface AliasedConfig {
  /**
   * How two names are read as one: lower, the default, compares them in lower
   * case with the white space at their ends dropped and each run of it within
   * one space; slug compares only their letters and digits, in lower case,
   * each run of anything else one hyphen.
   */
  normalize?: "lower" | "slug";
}

interface RenameParams {
  /** The new name. */
  name: JsonSchema<string, { minLength: 1; maxLength: 256 }>;
}

interface RenameResult {
  /** The row's name. */
  name: string;
  /** The old name, kept as an alias; absent when the rename kept none. */
  alias?: string;
  /**
   * True when the row had the name already, so nothing was written; absent
   * otherwise.
   */
  unchanged?: boolean;
}

interface AddAliasParams {
  /** The alias to add. */
  alias: JsonSchema<string, { minLength: 1; maxLength: 256 }>;
}

interface AddAliasResult {
  /** The alias, as given. */
  alias: string;
  /**
   * True when the row held the alias already, so nothing was written; absent
   * otherwise.
   */
  unchanged?: boolean;
}

interface RemoveAliasParams {
  /** The alias to remove, in any form that normalizes to it. */
  alias: JsonSchema<string, { minLength: 1; maxLength: 256 }>;
}

interface RemoveAliasResult {
  /**
   * The alias removed, as it was given when added; the one asked for when the
   * row held none.
   */
  alias: string;
  /**
   * True when the row held no such alias, so nothing was written; absent
   * otherwise.
   */
  unchanged?: boolean;
}

interface ResolveParams {
  /** The name or alias to find. */
  name: JsonSchema<string, { minLength: 1; maxLength: 256 }>;
}

interface ResolveResult {
  /**
   * The row that holds the name; null when none does, and to a caller held to
   * per-record access who may not read the row that does (D156).
   */
  row: {
    /** The row's key, in its wire form. */
    key: string;
    /** The row's name. */
    name: string;
    /**
     * True when the name found is one of the row's aliases, false when it is
     * its name.
     */
    alias: boolean;
  } | null;
}

/** The codes Aliased's refusals carry. */
enum AliasedVeto {
  /**
   * A create, a rename or an addAlias of a name another row holds, as its
   * name or an alias, in its normalized form; details give the name and its
   * normalized form, and nothing of the row that holds it, which the caller
   * may not be allowed to see (D156): resolve answers it to one who may.
   */
  name_taken,
  /**
   * removeAlias of the row's name, which is no alias: a rename gives the row
   * another name first; details give the name.
   */
  is_name,
}

/**
 * A name on each row with the aliases it was also known by, unique across the
 * table: no two rows hold one name, as a name or an alias, in its normalized
 * form. normalize lower, the default, reads a name with its case and its runs
 * of white space ignored; slug reads only its letters and digits, in lower
 * case. A create gives the name. rename keeps the old name as an alias;
 * addAlias and removeAlias keep the others; resolve, on the table, finds the
 * row a name or an alias names, to a caller who may read it. A name another
 * row holds is refused, the veto name_taken, which names nothing of that row.
 * A delete frees the row's names, and a
 * removed alias frees its own. The behaviors' core alone implements it, on a
 * table; no engine type composes it.
 */
@declaresBehavior({ vetoes: AliasedVeto })
export abstract class Aliased<C extends AliasedConfig> {
  /**
   * The row's name, as a create or a rename gave it: no other row holds it,
   * as a name or an alias, in its normalized form.
   */
  @valueField @fromCreate name: string;

  /**
   * Gives the row a new name, keeping the old one as an alias. A name the row
   * holds as an alias becomes its name; one only its case or spacing changes
   * leaves the aliases as they are. A name another row holds is refused, the
   * veto name_taken. The name the row has is answered unchanged.
   * @writes
   */
  abstract rename(params: RenameParams): RenameResult;

  /**
   * Adds an alias the row is also known by. A name another row holds is
   * refused, the veto name_taken; one the row holds already, as its name or
   * an alias, is answered unchanged.
   * @writes
   */
  abstract addAlias(params: AddAliasParams): AddAliasResult;

  /**
   * Removes one of the row's aliases, freeing it for any row. The row's name
   * is refused, the veto is_name: a rename gives the row another first. An
   * alias the row does not hold is answered unchanged.
   * @writes
   */
  abstract removeAlias(params: RemoveAliasParams): RemoveAliasResult;

  /**
   * Finds the row whose name or alias a name names, in its normalized form: a
   * read of the table as a whole that writes nothing. A row the caller may not
   * read is none (D156).
   * @scope schema
   */
  abstract resolve(params: ResolveParams): ResolveResult;

  /**
   * The row's aliases, one per normalized form: a name it was renamed from,
   * or one addAlias gave it. A removeAlias deletes its row, and a merge moves
   * the row merged away's to the row that stays.
   */
  @subTable({ key: "normalized" })
  static aliases = class {
    /** The alias in its normalized form, which keys it within its row. */
    normalized: string;
    /** The alias as it was given. */
    alias: string;
  };

  /**
   * On a table, one row per name a row holds, as its name or an alias, kept
   * for the table as a whole and keyed by its normalized form, so the
   * database holds each unique: a create or a rename that takes a name
   * another row holds finds its row here. A removed alias and a deleted row's
   * names delete theirs.
   */
  @subTable({ key: "normalized", shared: true })
  static names = class {
    /** The name in its normalized form. */
    normalized: string;
    /** The key of the row that holds the name, as its name or an alias. */
    holder: RowKey;
  };
}
