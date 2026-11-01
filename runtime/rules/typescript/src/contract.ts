// The JSON contract of the pure core (D103), as superschematic-rules
// (runtime/engine/rust/rules) reads and writes it: every value that crosses
// the binding, in serde's shape for each Rust type (camelCase members, an
// enum of variants with data as an object keyed by its variant, an enum
// tagged by `kind` where the Rust type says so). The core's README
// (runtime/engine/rust/rules/README.md) is its prose.
//
// The enums the README calls open (Action, ReadRequest, RowWrite,
// SubRowChange, DeadlineChange) gain variants as behaviors need them, so a
// host matches each with a fallback that refuses the command as internal
// rather than assume the union is closed.

/** Where a use is anchored: the whole type, or one field by its name. */
export type Anchor = 'type' | { readonly field: string };

/** One behavior on a type: superschematic_rules::UseSpec. */
export interface UseSpec {
  /** The behavior's declared name (Workflow). */
  readonly behavior: string;
  /** The config as the schema gives it; absent when it gives none, which the core checks as {}. JSON null is a config given. */
  readonly config?: unknown;
  /** The anchor; the whole type when absent. */
  readonly anchor?: Anchor;
}

/** One of a type's own fields (superschematic_rules::FieldSpec): what a config that names a field, a Timers field start, is held to. */
export interface FieldSpec {
  readonly name: string;
  /** Its IR type's name (string, Temporal.DateTime). */
  readonly type: string;
  /** Whether its values are instants: epoch milliseconds from a table's adapter, or the ISO 8601 text an engine schema's date-time holds. */
  readonly dateTime?: boolean;
}

/** A type the core runs: its name, its uses in list order, and its own fields, none when the host gives none (superschematic_rules::TypeSpec). */
export interface TypeSpec {
  readonly name: string;
  readonly uses?: readonly UseSpec[];
  readonly fields?: readonly FieldSpec[];
}

/** Who makes the call: a subject and the permissions it holds; standsIn for a service standing in for an end user, which holds none (D37). */
export interface Caller {
  readonly subject: string;
  readonly permissions?: readonly string[];
  readonly standsIn?: boolean;
}

/** What a command does (superschematic_rules::Action), tagged by kind. Open: a host matches it with a fallback. */
export type Action =
  | { readonly kind: 'define' }
  | { readonly kind: 'publish'; readonly from?: TypeSpec | null }
  | {
      readonly kind: 'create';
      /** The key the host will write; absent or null leaves it to the host's layout. */
      readonly id?: string | null;
      /** Own fields, the author's. */
      readonly own?: Readonly<Record<string, unknown>>;
      /** Each use's create parameters, by use id. */
      readonly params?: Readonly<Record<string, unknown>>;
    }
  | { readonly kind: 'update'; readonly key: string; readonly patch: Readonly<Record<string, unknown>> }
  | { readonly kind: 'delete'; readonly key: string }
  | { readonly kind: 'operation'; readonly key: string; readonly useId: string; readonly operation: string; readonly params?: unknown }
  | { readonly kind: 'deadline'; readonly key: string; readonly useId: string; readonly name: string; readonly at: number; readonly payload?: unknown };

/** One command (superschematic_rules::Command): who calls, when (epoch milliseconds), what, and the host's id for the request, the same for every run of the command. */
export interface Command {
  readonly caller: Caller;
  readonly now: number;
  readonly action: Action;
  readonly request?: string;
}

/** What the core asks the host to read (superschematic_rules::ReadRequest), tagged by kind. Open. */
export type ReadRequest =
  | { readonly kind: 'row'; readonly type: string; readonly key: string }
  | { readonly kind: 'hasRows'; readonly type: string }
  | { readonly kind: 'spec'; readonly type: string }
  /** The rows of one sub-table of one use on one row (Answer subRows), each by its key, in key order by code point. */
  | { readonly kind: 'subRows'; readonly type: string; readonly key: string; readonly useId: string; readonly table: string };

/** One row as the host read it: its own fields, each use's owned fields by use id then declared name, and its version. */
export interface Row {
  readonly own?: Readonly<Record<string, unknown>>;
  readonly owned?: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
  readonly version: number;
}

/** One sub-table row as the host read it (superschematic_rules::SubRow): its key, and its values by the declared field's name, the key's field left out and a NULL column absent. */
export interface SubRow {
  readonly key: string;
  readonly values?: Readonly<Record<string, unknown>>;
}

/** The host's answer to one request, of the variant it asks for. */
export type Answer = { readonly row: Row | null } | { readonly hasRows: boolean } | { readonly spec: TypeSpec | null } | { readonly subRows: readonly SubRow[] };

/** A key the host locks before it answers the reads a rule across rows decides on. */
export type LockKey = string;

/** One answered request, and how many locks the host held when it made the read. */
export interface Answered {
  readonly request: ReadRequest;
  readonly answer: Answer;
  readonly held?: number;
}

/** What the host has answered so far for one command: each request with its answer, and the locks it holds in the order it took them. */
export interface Answers {
  readonly reads?: readonly Answered[];
  readonly locks?: readonly LockKey[];
}

/** What one run returns (superschematic_rules::Step). */
export type Step = { readonly read: readonly ReadRequest[] } | { readonly lock: readonly LockKey[] } | { readonly done: Outcome };

/** A command's outcome: a refusal, or a change set the host applies. */
export type Outcome = { readonly refused: Refusal } | { readonly changed: ChangeSet };

/** What the core refuses for (superschematic_rules::Code). */
export type Code =
  | 'invalid_schema'
  | 'incompatible_change'
  | 'not_found'
  | 'invalid_argument'
  | 'forbidden'
  | 'invalid_instance'
  | 'conflict'
  | 'vetoed'
  | 'busy'
  | 'behavior_error'
  | 'internal';

/** A refused command: the code, its HTTP status, the engine's class for it, a message, and the class's members (superschematic_rules::Refusal). */
export interface Refusal {
  readonly code: Code;
  readonly status: number;
  readonly class: string;
  readonly message: string;
  readonly details?: Readonly<Record<string, unknown>>;
}

/** A row a change set names; the key null only for the row a create without an id makes. */
export interface RowRef {
  readonly type: string;
  readonly key?: string | null;
}

/** The write of one row (superschematic_rules::RowWrite), tagged by kind. Open. */
export type RowWrite =
  | {
      readonly kind: 'create';
      readonly type: string;
      readonly key: string | null;
      readonly own: Readonly<Record<string, unknown>>;
      readonly owned: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
    }
  | {
      readonly kind: 'update';
      readonly type: string;
      readonly key: string;
      readonly expectedVersion: number;
      readonly own: Readonly<Record<string, unknown>>;
      readonly owned: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
    }
  | { readonly kind: 'delete'; readonly type: string; readonly key: string; readonly expectedVersion: number };

/** What happens to a sub-table row, tagged by kind. Open. */
export type SubRowChange =
  | { readonly kind: 'insert'; readonly values: Readonly<Record<string, unknown>> }
  | { readonly kind: 'update'; readonly values: Readonly<Record<string, unknown>> }
  | { readonly kind: 'delete' };

/** One write of a sub-table row. */
export interface SubRowWrite {
  readonly row: RowRef;
  readonly useId: string;
  readonly table: string;
  readonly key: string;
  readonly change: SubRowChange;
}

/** An event a behavior emits, which the host writes to the outbox in the change set's transaction. */
export interface OutboxEvent {
  readonly row: RowRef;
  readonly kind: string;
  readonly useId: string;
  readonly payload: unknown;
}

/** A deadline of a row a use arms or clears, tagged by kind. Open. */
export type DeadlineChange =
  | { readonly kind: 'arm'; readonly row: RowRef; readonly useId: string; readonly name: string; readonly at: number; readonly payload: unknown }
  | { readonly kind: 'clear'; readonly row: RowRef; readonly useId: string; readonly name: string };

/** Everything one command changes, and what it returns (superschematic_rules::ChangeSet). */
export interface ChangeSet {
  readonly rows: readonly RowWrite[];
  readonly subRows: readonly SubRowWrite[];
  readonly events: readonly OutboxEvent[];
  readonly deadlines: readonly DeadlineChange[];
  /** An operation's result; null for the rest. */
  readonly result: unknown;
}

/** A field a behavior declares, as its declaration file has it. */
export interface DeclaredField {
  readonly name: string;
  readonly description?: string;
  /** The IR type of the column it takes on a table (D103); a date-time is epoch milliseconds in the core. */
  readonly type?: string;
  readonly volatile?: boolean;
  readonly perReader?: boolean;
}

/** A sub-table a behavior declares, as its declaration file has it: its name, the field whose value keys a row within its row, and its typed fields, the key among them. */
export interface DeclaredTable {
  readonly name: string;
  readonly description?: string;
  readonly key: string;
  readonly fields: readonly DeclaredField[];
}

/** An operation a behavior declares, as its declaration file has it. */
export interface DeclaredOperation {
  readonly name: string;
  readonly description?: string;
  readonly paramsSchema: unknown;
  readonly resultSchema: unknown;
  readonly writes?: boolean;
  readonly scope?: 'instance' | 'schema' | 'namespace';
  readonly [member: string]: unknown;
}

/**
 * A registered behavior's declaration file, as the compiler registers it
 * (internal/registry/behaviors) and the core reads it in place: the shape
 * of registry.BehaviorDeclaration.
 */
export interface BehaviorDeclaration {
  readonly name: string;
  readonly description?: string;
  readonly configSchema?: unknown;
  readonly createParamsSchema?: unknown;
  readonly requires?: readonly string[];
  readonly conflicts?: readonly string[];
  readonly fields?: readonly DeclaredField[];
  /** The field that holds the value when a type carries the behavior on a field (D103). */
  readonly value?: string;
  /** The sub-tables the behavior keeps per row (D103): each a child table of the row, deleted with it, which only its operations write. */
  readonly tables?: readonly DeclaredTable[];
  /** Whether the behavior arms deadlines, which a table that composes it keeps in a deadline queue (D103). */
  readonly armsDeadlines?: boolean;
  readonly operations?: readonly DeclaredOperation[];
  readonly vetoes?: readonly { readonly code: string; readonly description?: string }[];
  readonly [member: string]: unknown;
}
