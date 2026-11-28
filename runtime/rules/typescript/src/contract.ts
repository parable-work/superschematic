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
  /** The to-one relation whose key the field holds (D103); absent for a field that holds none. */
  readonly relation?: RelationSpec;
  /** The field's JSON Schema, which a host gives for a field a behavior hands a model: the output a deriver asks for a field Derived derives (D73). */
  readonly schema?: unknown;
}

/** A to-one relation an own field holds the key of (superschematic_rules::RelationSpec): the relation field's name and the type it points at. */
export interface RelationSpec {
  readonly name: string;
  readonly type: string;
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
  /** The subject the caller acts for (D77), which Provenance records (D73). */
  readonly actingFor?: string;
  /** What the caller is (D156): user, party, agent or extension; absent reads as user. */
  readonly kind?: string;
  /** The kind of the subject it acts for; absent reads as user. */
  readonly actingForKind?: string;
  /** The Agent whose run the caller is (D158, D200). */
  readonly agent?: string;
  /** How the host verified the caller for this call (D200), which an Approvals requirement's proof reads. */
  readonly assurance?: { readonly signedInAt?: number; readonly userVerified?: boolean };
  /** The channel kind the call came over (D157, #606), which Comments records as a message's via; absent for a call over the API. */
  readonly channel?: string;
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
  /** An operation of a use on the table as a whole (D104), one its declaration's scope schema gives: no row, so no key, and no preconditions; it reads rows through the read across a table and writes them only through its calls on each. */
  | { readonly kind: 'tableOperation'; readonly useId: string; readonly operation: string; readonly params?: unknown }
  | { readonly kind: 'deadline'; readonly key: string; readonly useId: string; readonly name: string; readonly at: number; readonly payload?: unknown }
  /** A write of a row the table's change queue hands the core (D103): the row's key and the change. */
  | { readonly kind: 'change'; readonly key: string; readonly change: RowChange }
  /** A read of a row (D104's step 4): the fields each use computes through reads, by use id, as the change set's result; a field that is the reader's (Comments' unread) is the caller's. */
  | { readonly kind: 'read'; readonly key: string }
  /**
   * A caller's signal about a row (D77's primitive 2): the use whose
   * behavior declares it, its name and its data. The core holds it to the
   * declaration (one its operations alone send is forbidden), to 4096
   * bytes of compact JSON and to the behavior's check of the data, and
   * writes nothing: the change set's signals hold the one sent. The access
   * policy is the host's to ask, read or write as the signal's access says,
   * with the operation signal:<name>.
   */
  | { readonly kind: 'signal'; readonly key: string; readonly useId: string; readonly name: string; readonly data?: unknown }
  /**
   * An operation of the namespace as a whole (D77's primitive 3), by its
   * behavior and its declared name: it runs on no type and no row, over the
   * types that compose the behavior and that the caller may read, which the
   * host answers (ReadRequest composing), keyed by the caller. The spec it
   * runs on is not read.
   */
  | { readonly kind: 'namespaceOperation'; readonly behavior: string; readonly operation: string; readonly params?: unknown };

/** What a write did to a row (superschematic_rules::WriteKind). */
export type WriteKind = 'insert' | 'update' | 'delete';

/** A row's fields as a write found or left them: own fields by name, owned fields by use id and declared name, a field with no value absent (superschematic_rules::RowImage). */
export interface RowImage {
  readonly own?: Readonly<Record<string, unknown>>;
  readonly owned?: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
}

/** A write of a row as a table's change trigger wrote it (superschematic_rules::RowChange): what it did, when (epoch milliseconds, the database's clock), and the row before it (none for an insert) and after it (none for a delete). */
export interface RowChange {
  readonly write: WriteKind;
  readonly at: number;
  readonly before?: RowImage;
  readonly after?: RowImage;
}

/** One command (superschematic_rules::Command): who calls, when (epoch milliseconds), what, and the host's id for the request, the same for every run of the command. */
export interface Command {
  readonly caller: Caller;
  readonly now: number;
  readonly action: Action;
  readonly request?: string;
  /**
   * The caller's preconditions (D16, D104): each use's entry by its use id
   * (an implemented use's id is its behavior's name, { Lease: { token: 3 } }),
   * which the core checks against the use's behavior before it reads the
   * row, and hands that use's guard. Only an update, a delete and an
   * operation present them; absent or empty for none.
   */
  readonly preconditions?: Readonly<Record<string, unknown>>;
}

/** What the core asks the host to read (superschematic_rules::ReadRequest), tagged by kind. Open. */
export type ReadRequest =
  | { readonly kind: 'row'; readonly type: string; readonly key: string }
  | { readonly kind: 'hasRows'; readonly type: string }
  | { readonly kind: 'spec'; readonly type: string }
  /** The rows of one sub-table of one use on one row (Answer subRows), each by its key, in key order by code point. */
  | { readonly kind: 'subRows'; readonly type: string; readonly key: string; readonly useId: string; readonly table: string }
  /** The rows of a type whose own field holds a key (Answer linked), in the host's key order, at most limit (the core asks for its LINKED_PAGE, 500), after the key after when given; a soft-deletable table's live rows alone (D103). */
  | { readonly kind: 'linked'; readonly type: string; readonly field: string; readonly key: string; readonly after?: string; readonly limit: number }
  /** The rows of a type whose use's owned field holds a value (Answer linked), in the host's key order, at most limit, after the key after when given, the value compared as the column holds it; a soft-deletable table's live rows alone, and none of a type the host does not have (D104). */
  | { readonly kind: 'holding'; readonly type: string; readonly useId: string; readonly field: string; readonly value: unknown; readonly after?: string; readonly limit: number }
  /**
   * The live rows of a type (Answer linked, the read across a table, D104):
   * each own field equal names holding its value (a null: the field holds
   * none), each row with the rows of the sub-tables tables names, in the
   * host's key order, at most limit (the core asks for its LINKED_PAGE,
   * 500), after the key after when given; a soft-deletable table's live rows
   * alone, and none of a type the host does not have.
   */
  | {
      readonly kind: 'rows';
      readonly type: string;
      readonly equal?: Readonly<Record<string, unknown>>;
      readonly tables?: readonly TableRef[];
      readonly after?: string;
      readonly limit: number;
    }
  /** The row of a key of a use's shared table (Answer sharedRow), a table its behavior keeps for the type's table as a whole rather than per row (D103's step 4: Numbering's counts), with its version. */
  | { readonly kind: 'sharedRow'; readonly type: string; readonly useId: string; readonly table: string; readonly key: string }
  /** The live versions of the types that compose a behavior and that the command's caller may read (Answer specs, D77's primitive 3), in name order: what an operation of the namespace runs over. The host asks its access policy of each type and leaves out one the caller may not read. */
  | { readonly kind: 'composing'; readonly behavior: string };

/** One sub-table of a use, as a read across a table names it (superschematic_rules::TableRef, D104): the use's id and the sub-table's declared name. */
export interface TableRef {
  readonly useId: string;
  readonly table: string;
}

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

/** A row a linked, holding or table read answers, with its key (superschematic_rules::KeyedRow). */
export interface KeyedRow {
  readonly key: string;
  readonly row: Row;
  /** With a read across a table (D104), the rows of each sub-table it names, by use id and then the table's declared name, each table's in key order, an empty list for a row that has none; absent for a linked or a holding read. */
  readonly tables?: Readonly<Record<string, Readonly<Record<string, readonly SubRow[]>>>>;
}

/** The rows a linked read asks for, and whether more follow (superschematic_rules::LinkedRows). */
export interface LinkedRows {
  readonly rows: readonly KeyedRow[];
  readonly more: boolean;
}

/** One row of a use's shared table as the host read it (superschematic_rules::SharedRow, D103's step 4): its key, its values by the declared field's name, the key's field left out and a NULL column absent, and the version its next write is held to. */
export interface SharedRow {
  readonly key: string;
  readonly values?: Readonly<Record<string, unknown>>;
  readonly version: number;
}

/** The host's answer to one request, of the variant it asks for. */
export type Answer =
  | { readonly row: Row | null }
  | { readonly hasRows: boolean }
  | { readonly spec: TypeSpec | null }
  | { readonly subRows: readonly SubRow[] }
  | { readonly linked: LinkedRows }
  | { readonly sharedRow: SharedRow | null }
  /** The types a composing read asks for (D77's primitive 3), in name order. */
  | { readonly specs: readonly TypeSpec[] };

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
  | { readonly kind: 'delete'; readonly type: string; readonly key: string; readonly expectedVersion: number }
  /** A row a change command found gone (D103): no write of it; its deadlines go, and its sub-table rows when no row of the key is left at all. */
  | { readonly kind: 'gone'; readonly type: string; readonly key: string };

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

/** What happens to a row of a shared table (D103's step 4), tagged by kind. Open. */
export type SharedRowChange =
  | { readonly kind: 'insert'; readonly values: Readonly<Record<string, unknown>> }
  | { readonly kind: 'update'; readonly expectedVersion: number; readonly values: Readonly<Record<string, unknown>> };

/** One write of a row of a use's shared table (superschematic_rules::SharedRowWrite): an insert of a key the table does not hold, or an update held to the version the host read it at, each of which the host holds to one row. */
export interface SharedRowWrite {
  readonly type: string;
  readonly useId: string;
  readonly table: string;
  readonly key: string;
  readonly change: SharedRowChange;
}

/**
 * An event a behavior emits about a row (Cx::emit, D104), which the host
 * writes to the event queue of its row's table in the change set's
 * transaction: a kind the use's declaration does not list is the core's
 * defect.
 */
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

/**
 * A signal a command sends about a row (superschematic_rules::Signal, D77's
 * primitive 2): the row, the use whose behavior declares it, its name and
 * its data, at most 4096 bytes of compact JSON. The host stores none: it
 * publishes each to the event streams open on its row once the change
 * set's transaction commits, as the engine's stream message (event: signal,
 * no id), { namespace, schema, instanceId, behavior, name, data, from, at },
 * from the caller's subject and at the command's time, and drops them when
 * it rolls back.
 */
export interface Signal {
  readonly row: RowRef;
  readonly useId: string;
  readonly name: string;
  readonly data: unknown;
}

/** Everything one command changes, and what it returns (superschematic_rules::ChangeSet). */
export interface ChangeSet {
  readonly rows: readonly RowWrite[];
  readonly subRows: readonly SubRowWrite[];
  /** The rows of the uses' shared tables it writes (D103's step 4); absent for none. */
  readonly sharedRows?: readonly SharedRowWrite[];
  readonly events: readonly OutboxEvent[];
  readonly deadlines: readonly DeadlineChange[];
  /** The signals it sends (D77's primitive 2), published after the commit and stored nowhere; absent for none. */
  readonly signals?: readonly Signal[];
  /** An operation's result; null for the rest. */
  readonly result: unknown;
}

/** A signal a behavior declares (D77's primitive 2), as its declaration file has it: its name, the JSON Schema of its data, and who sends it: a caller who may read the row, one who may write it, or only the behavior's own operations. */
export interface DeclaredSignal {
  readonly name: string;
  readonly description?: string;
  readonly paramsSchema?: unknown;
  readonly access: 'read' | 'write' | 'operation';
}

/** A field a behavior declares, as its declaration file has it. */
export interface DeclaredField {
  readonly name: string;
  readonly description?: string;
  /** The IR type of the column it takes on a table (D103); a date-time is epoch milliseconds in the core. */
  readonly type?: string;
  readonly volatile?: boolean;
  readonly perReader?: boolean;
  /** True for a value field the behavior does not set when a row is created (Reconcile's observed, D104): on a table its column takes NULL. Only a declaration's value field says it. */
  readonly optional?: boolean;
}

/** A sub-table a behavior declares, as its declaration file has it: its name, the field whose value keys a row within its row, and its typed fields, the key among them. One whose scope is schema is kept for the table as a whole, its rows shared by every row of the type (D103's step 4). */
export interface DeclaredTable {
  readonly name: string;
  readonly description?: string;
  readonly key: string;
  readonly scope?: 'instance' | 'schema';
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
  /** True for a writing operation of a row that writes beside it (D77's primitive 1, Comments'): its use's sub-tables and shared tables alone, so the row's version stays and no other use's guard is asked. */
  readonly beside?: boolean;
  readonly [member: string]: unknown;
}

/**
 * An event a behavior declares it emits about a row (D104), as its
 * declaration file has it: its kind, which names it in its table's event
 * queue and names the Workers method of the API that takes it, what it
 * says, and the JSON Schema of its payload, an object schema.
 */
export interface DeclaredEvent {
  readonly kind: string;
  readonly description?: string;
  readonly payloadSchema?: unknown;
}

/**
 * A contract between behaviors a declaration names (D103, D104): its name,
 * one the behaviors' core defines (Capacity), whether the behavior answers
 * its queries (implements) or asks them of other uses (asks), and what it
 * means here. The core resolves a type's implementers by these, never by a
 * behavior's name.
 */
export interface DeclaredContract {
  readonly name: string;
  readonly role: 'implements' | 'asks';
  readonly description?: string;
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
  /** Whether the behavior hears its row's writes, which a table that composes it keeps in a change queue its trigger writes (D103). */
  readonly hearsChanges?: boolean;
  /** The JSON Schema of the entry a caller presents the behavior as a command's precondition (D16, D104); absent for a behavior that takes none. */
  readonly preconditionSchema?: unknown;
  readonly operations?: readonly DeclaredOperation[];
  /** The events the behavior emits about a row (D104): a table that composes it keeps them in an event queue, which a behavior worker hands to the project's handler. Only a behavior the core alone implements declares any; absent for none. */
  readonly events?: readonly DeclaredEvent[];
  /** The contracts between behaviors it implements or asks (D104): Budget implements Capacity, which Pool asks; absent for none. */
  readonly contracts?: readonly DeclaredContract[];
  /** The signals it declares (D77's primitive 2): typing, viewing and draft of Comments; absent for none. */
  readonly signals?: readonly DeclaredSignal[];
  readonly vetoes?: readonly { readonly code: string; readonly description?: string }[];
  /** The rules of a declarative behavior (D103's step 5), in the vocabulary the core interprets: a behavior no Rust implements, which a host hands the core with declare. Absent for one the core implements. */
  readonly rules?: Record<string, unknown>;
  readonly [member: string]: unknown;
}

/**
 * A recurrence the core dates (D152), the core's expand operation's input:
 * an anchor (`calendar`, an RFC 5545 RRULE from a wall start; `afterDone`,
 * `every` after the last completion; `relative`, `offset` from a date and,
 * with `every`, from each of its anniversaries), the zone its wall times
 * are read in (UTC when absent), the adjustment of a due date that falls on
 * a weekend day or a holiday, the lead times (ISO 8601 spans: P14D, -P60D,
 * PT12H), and the window [from, to) of due instants in epoch milliseconds.
 */
export interface ExpandInput {
  readonly anchor: 'calendar' | 'afterDone' | 'relative';
  readonly timeZone?: string;
  /** The RRULE, with or without its `RRULE:` name: FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=31. */
  readonly rule?: string;
  /** A wall date or date-time: the rule's start (calendar), the first due when nothing is done and the time of day (afterDone). */
  readonly start?: string;
  readonly every?: string;
  /** When the last occurrence was done, in epoch milliseconds (afterDone). */
  readonly lastDone?: number;
  /** A wall date or date-time, or epoch milliseconds (relative). */
  readonly date?: string | number;
  readonly offset?: string;
  readonly adjust?: 'none' | 'before' | 'after';
  /** The days a due date moves off; Saturday and Sunday when absent. */
  readonly weekendDays?: readonly string[];
  /** Wall dates a due date moves off: the holiday calendar's rows. */
  readonly holidays?: readonly string[];
  readonly opensBefore?: string;
  readonly remindBefore?: string;
  readonly overdueAfter?: string;
  readonly from?: number;
  readonly to?: number;
  /** At most this many occurrences, 1 to 5000; 1000 when absent. */
  readonly limit?: number;
}

/** One dated occurrence of a recurrence (D152). */
export interface ExpandedOccurrence {
  /** Its nominal wall date-time, before the adjustment: 2026-10-31T09:00. */
  readonly key: string;
  /** The wall date-time it falls due at, after the adjustment. */
  readonly due: string;
  readonly dueAt: number;
  readonly opensAt?: number;
  readonly remindAt?: number;
  readonly overdueAt: number;
  /** Why the adjustment moved it. */
  readonly shifted?: 'weekend' | 'holiday';
  /** An afterDone occurrence past the first, dated as if each before it were done on its due date. */
  readonly projected?: true;
}

/** What the core's expand operation answers (D152). */
export interface Expansion {
  readonly occurrences: readonly ExpandedOccurrence[];
  /** Whether more occurrences lie in the window past the limit. */
  readonly more: boolean;
}
