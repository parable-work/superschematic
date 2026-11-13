import type { Wrapped } from "./wrappers";

const noopPropertyDecorator: PropertyDecorator = () => {};
const noopClassOrPropertyDecorator: ClassDecorator &
  PropertyDecorator = () => {};

/** Marks the field as dispatch metadata, such as a union member's discriminator, rather than user-facing data. */
export const internalMetadata: PropertyDecorator = noopPropertyDecorator;
export const jsonField: ClassDecorator & PropertyDecorator =
  noopClassOrPropertyDecorator;
// Make the generated Rust serde deserializer reject a payload key the type
// does not declare instead of dropping it (#[serde(deny_unknown_fields)]).
// Opt-in per type: strict decoding suits a closed wire contract, where a
// misspelled key silently disappearing is the failure, and breaks a payload
// that has to survive a producer newer than its consumer.
export const denyUnknownFields: ClassDecorator = () => {};
// Make every generated decoder of the type (Go, TypeScript, Python, Rust)
// reject a key the type does not declare and a required field that is absent
// or null. Applies to this object only: a nested object type opts in on its
// own.
export const strictJSON: ClassDecorator = () => {};
// Make every generated decoder of the type (Go, TypeScript, Python, Rust),
// and a server's decoding of it as an operation's input, accept a key the
// type does not declare and drop it, so the implementation sees only the
// declared fields, which are checked as before. For a payload a producer
// newer than its consumer sends, such as a provider's webhook; an @webhook
// operation's input decodes this way unless its type is @strictJSON (D87).
// A type is never both this and @strictJSON or @denyUnknownFields.
export const allowUnknownFields: ClassDecorator = () => {};
/** A UI schema renderer hides the field. */
export const uiHidden: PropertyDecorator = noopPropertyDecorator;

// Field presentation. None of these changes the wire format or validation.
// @superschematic/api also exports docs, for operations; import one of the
// two under another name when a file uses both.

/** Short human-facing label for the field: JSON Schema `title`. */
export function docs(_config: { readonly title: string }): PropertyDecorator {
  return noopPropertyDecorator;
}

/** Longer-form Markdown explaining what the field is for: `x-purpose`. */
export function purpose(_markdown: string): PropertyDecorator {
  return noopPropertyDecorator;
}

/**
 * Name of the glyph a UI shows for the field: `x-icon`. The core accepts any
 * name; a distribution may restrict it to an icon set.
 */
export function icon(_name: string): PropertyDecorator {
  return noopPropertyDecorator;
}

/**
 * The control a schema form draws for a field, in place of the one it picks
 * from the field's type and `Validate` limits: `uiControl` in the IR.
 *
 * - `slider`, `number-field`: a number. A slider needs a finite min and max.
 * - `switch`, `segmented`, `radio`, `select`, `combobox`: an enum. A switch
 *   needs two values that read as on and off.
 * - `checkboxes`, `combobox`: an array of an enum.
 * - `input`, `textarea`: a string.
 * A form ignores a control that does not fit the field and picks its own.
 */
export type UIControl =
  | 'slider'
  | 'number-field'
  | 'switch'
  | 'segmented'
  | 'radio'
  | 'select'
  | 'combobox'
  | 'checkboxes'
  | 'input'
  | 'textarea';

/** The control a schema form draws for the field, in place of the one it picks (UIControl). */
export function uiControl(_control: UIControl): PropertyDecorator {
  return noopPropertyDecorator;
}

/** The class is a view of a table: its fields are checked against the table's. */
export function source(_target: unknown): ClassDecorator {
  return () => {};
}
/** A field of a `@source` view with no column behind it, which the implementation fills in. */
export const virtual: PropertyDecorator = noopPropertyDecorator;
/**
 * A field of a `@source` view that carries the plaintext of its table's
 * `Encrypted<T>` field (D85). A view leaves such a field out unless it says
 * so: whatever the view holds, a response sends.
 */
export const decrypted: PropertyDecorator = noopPropertyDecorator;

/**
 * The states of a field: the enum that types it, for a field typed `E`,
 * `Nullable<E>` or `Default<E, V>`. A wrapper's enum is read through its
 * `Wrapped<T>` base. A field of any text or of no text has no states.
 */
export type StateOf<F> = (NonNullable<F> extends Wrapped<infer T> ? T : NonNullable<F>) extends infer E
  ? [E] extends [string]
    ? string extends E
      ? never
      : E
    : never
  : never;

/** The machine of the field `K` of `T`: every state is a member of the enum that types the field. */
export interface StateMachineConfigFor<T, K extends keyof T> {
  /** The field the machine governs, one an enum types. */
  readonly field: K;
  /**
   * The states a state may move to. A state with no entry moves nowhere.
   * `null` in a list returns the field to null, on a `Nullable` field with
   * a `fromNull`.
   */
  readonly transitions: {
    readonly [S in StateOf<T[K]>]?: readonly (StateOf<T[K]> | null)[];
  };
  /** The states no update leaves. */
  readonly terminal: readonly StateOf<T[K]>[];
  /**
   * The states a null field may take, on a `Nullable` field. Without it an
   * update cannot set a null field.
   */
  readonly fromNull?: readonly StateOf<T[K]>[];
}

/**
 * The options of `@stateMachine<T>`: the machine of one field of `T`, told
 * apart by `field`, so the one type argument holds every state to that
 * field's enum.
 */
export type StateMachineConfig<T> = {
  [K in keyof T]-?: StateMachineConfigFor<T, K>;
}[keyof T];

/**
 * Declares the moves an enum field of the class may make (D117). A class
 * takes one per field:
 *
 * ```ts
 * @stateMachine<Ticket>({
 *   field: "state",
 *   transitions: {
 *     [TicketState.Open]: [TicketState.Resolved, TicketState.Closed],
 *     [TicketState.Resolved]: [TicketState.Open, TicketState.Closed],
 *   },
 *   terminal: [TicketState.Closed],
 * })
 * export abstract class Ticket {
 *   state: Default<TicketState, TicketState.Open>;
 * }
 * ```
 *
 * The build checks the graph: no transition leaves a terminal state, and
 * every state can be reached from the field's `Default`, or from null when
 * it has none and the machine has a `fromNull`. A write that keeps the
 * field's value is no move, and an insert is not held. On a table of a DB
 * service the database refuses every other move, whatever writes the row;
 * the generated types carry the same moves for a caller to ask.
 */
export function stateMachine<T>(_config: StateMachineConfig<T>): ClassDecorator {
  return () => {};
}

/**
 * The fields of `T` a fence may be: those typed by a number that is never
 * null, as `Generic.Int64` or `Default<Generic.Int64, 0>`. A text field, a
 * `Nullable` one and an optional one are none.
 */
export type FenceFieldOf<T> = {
  [K in keyof T]-?: [T[K]] extends [number] ? K : never;
}[keyof T];

/** The options of `@fenced<T>`: the fence of `T`, and the fields it covers. */
export interface FencedConfig<T> {
  /** The fence: an integer field of the class that is never null, as `version`. */
  readonly field: FenceFieldOf<T>;
  /**
   * The fields whose change the fence counts. Without it the fence counts
   * every update of the row. A class with several fences gives each its
   * own, and no field is in two lists. A relation is covered as the key it
   * stores.
   */
  readonly covers?: readonly [keyof T, ...(keyof T)[]];
}

/**
 * Names a counter of a DB table that every write it covers advances by one
 * (D118), so a writer that compares the counter in its WHERE clause knows
 * no other covered write came between its read and its write:
 *
 * ```ts
 * @fenced<Job>({ field: "version", covers: ["label", "priority"] })
 * @fenced<Job>({ field: "executionVersion", covers: ["claimedBy", "progress"] })
 * export abstract class Job {
 *   label: string;
 *   priority: Default<Generic.Int64, 0>;
 *   version: Default<Generic.Int64, 0>;
 *   claimedBy: Nullable<string>;
 *   progress: Default<Generic.Int64, 0>;
 *   executionVersion: Default<Generic.Int64, 0>;
 * }
 * ```
 *
 * Without `covers`, every update of a row sets the fence to the stored
 * value plus one. With it, an update that changes a covered field does,
 * and any other update keeps the fence or adds one. The database refuses
 * a write that does not, whatever writes the row, and the generated
 * repositories advance the fence themselves. An insert is not fenced.
 *
 * The build checks that the fence is a required integer column no
 * `@stateMachine` governs, and that each covered field stores a column
 * one fence alone covers.
 */
export function fenced<T>(_config: FencedConfig<T>): ClassDecorator {
  return () => {};
}

// Wire encodings for @temporalFormat: the epoch units. ISO text is the
// default and is never declared.
export type TemporalWireFormat =
  | 'unix'
  | 'unix_millis'
  | 'unix_micros'
  | 'unix_nanos';

// Declares the wire encoding of a Temporal.DateTime field whose source sends
// a bare epoch count instead of ISO text. The unit is a fact
// about the source API and is never guessed from the digit count. Serializes
// as the `temporalFormat` IR field (`x-temporal-format` in the legacy
// JSON-Schema wire form). Only valid on Temporal.DateTime fields.
export function temporalFormat(_format: TemporalWireFormat): PropertyDecorator {
  return noopPropertyDecorator;
}
