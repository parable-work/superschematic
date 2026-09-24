const noopPropertyDecorator: PropertyDecorator = () => {};
const noopClassOrPropertyDecorator: ClassDecorator &
  PropertyDecorator = () => {};

export const internalMetadata: PropertyDecorator = noopPropertyDecorator;
export const jsonField: ClassDecorator & PropertyDecorator =
  noopClassOrPropertyDecorator;
// Make the generated Rust serde deserializer reject a payload key the type
// does not declare instead of dropping it (#[serde(deny_unknown_fields)]).
// Opt-in per type: strict decoding suits a closed wire contract, where a
// misspelled key silently disappearing is the failure, and breaks a payload
// that has to survive a producer newer than its consumer.
export const denyUnknownFields: ClassDecorator = () => {};
export const uiHidden: PropertyDecorator = noopPropertyDecorator;
export function source(_target: unknown): ClassDecorator {
  return () => {};
}
export const virtual: PropertyDecorator = noopPropertyDecorator;

// Wire encodings for @temporalFormat: the epoch members of
// IncrementalTimeFormatEnum. ISO text is the default and is never declared.
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
