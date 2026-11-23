// JSON text a host passes through as it is. A store keeps a row's fields
// as JSON text, and the handler reads them with the engine's JSON features
// (a number keeps its text, an object its members' order), so a store's
// answer carries the column's text into the module unparsed: a value wider
// than a double, or a member named like an integer, reaches the handler as
// the store holds it.

/** JSON text, spliced into a document as it is. */
export class RawJson {
  readonly text: string;

  constructor(text: string) {
    this.text = text;
  }

  toJSON(): never {
    throw new TypeError('rules-handler: a RawJson is spliced by encode, not by JSON.stringify');
  }
}

/** JSON text to splice into a document as it is. */
export function raw(text: string): RawJson {
  return new RawJson(text);
}

/** How a host reads and writes JSON: JSON.parse and JSON.stringify unless replaced. */
export interface Codec {
  parse(text: string): unknown;
  stringify(value: unknown): string;
}

export const JSON_CODEC: Codec = {
  parse: text => JSON.parse(text) as unknown,
  stringify: value => JSON.stringify(value),
};

function plain(value: object): boolean {
  const prototype = Object.getPrototypeOf(value) as unknown;
  return prototype === Object.prototype || prototype === null;
}

/**
 * A value as JSON text: a RawJson as its text, an array and a plain
 * object member by member (an undefined member left out, an undefined
 * item null), and anything else, a codec's own number type among them,
 * by the codec's stringify.
 */
export function encode(value: unknown, codec: Codec = JSON_CODEC): string {
  if (value instanceof RawJson) {
    return value.text;
  }
  if (Array.isArray(value)) {
    return `[${value.map(item => (item === undefined ? 'null' : encode(item, codec))).join(',')}]`;
  }
  if (value !== null && typeof value === 'object' && plain(value)) {
    const members: string[] = [];
    for (const [name, member] of Object.entries(value)) {
      if (member !== undefined) {
        members.push(`${JSON.stringify(name)}:${encode(member, codec)}`);
      }
    }
    return `{${members.join(',')}}`;
  }
  return codec.stringify(value);
}
