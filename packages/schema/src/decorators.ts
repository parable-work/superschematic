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
