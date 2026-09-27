/**
 * JSON with its number literals kept, and the canonical form the Go IR
 * persists extension and document data in (`ir.CanonicalJSON`): compact,
 * object keys sorted by their UTF-8 bytes, arrays in order, number literals
 * as written, strings escaped as Go's encoding/json escapes them.
 *
 * `JSON.parse` turns `1.0` into `1` and rounds an integer past 2^53, while
 * Go keeps the literal. The parse here reads each number's source text
 * through the reviver's context (JSON.parse source text access, in Node.js
 * 21 and later and in Bun), so the JavaScript runtime's own parser checks
 * the syntax and no literal is lost.
 */

/** A JSON number, kept as the literal it was written as. */
export class JSONNumber {
  constructor(readonly literal: string) {}
}

/** An object keeps its keys in a Map, so no key reaches a prototype. */
export type JSONObject = Map<string, JSONNode>;

export type JSONNode = null | boolean | string | JSONNumber | JSONNode[] | JSONObject;

type ReviverContext = { source?: string } | undefined;

let sourceTextAccess: boolean | undefined;

function hasSourceTextAccess(): boolean {
  if (sourceTextAccess === undefined) {
    let seen: string | undefined;
    JSON.parse('1.0', (_key: string, value: unknown, context?: ReviverContext) => {
      seen = context?.source;
      return value;
    });
    sourceTextAccess = seen === '1.0';
  }
  return sourceTextAccess;
}

const loneSurrogate = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g;

/**
 * wellFormed replaces each lone surrogate with U+FFFD, as Go's decoder
 * does for a `\uXXXX` escape that is half a pair and as encoding the text
 * as UTF-8 does for a raw one.
 */
export function wellFormed(s: string): string {
  return s.replace(loneSurrogate, '\uFFFD');
}

/**
 * parseJSON reads JSON text into a JSONNode. It throws the runtime's
 * SyntaxError on invalid JSON, and an Error when the runtime's JSON.parse
 * does not expose number source text.
 */
export function parseJSON(text: string): JSONNode {
  if (!hasSourceTextAccess()) {
    throw new Error(
      'this JavaScript runtime does not expose number source text to JSON.parse revivers (JSON.parse source text access, Node.js 21 or later)'
    );
  }
  return JSON.parse(wellFormed(text), (_key: string, value: unknown, context?: ReviverContext) => {
    if (typeof value === 'number') {
      return new JSONNumber((context as { source: string }).source);
    }
    if (typeof value === 'string') {
      return wellFormed(value);
    }
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      const object: JSONObject = new Map();
      for (const key of Object.keys(value)) {
        object.set(wellFormed(key), (value as Record<string, JSONNode>)[key]);
      }
      return object;
    }
    return value;
  }) as JSONNode;
}

/** isJSONObject reports whether node is a JSON object. */
export function isJSONObject(node: JSONNode | undefined): node is JSONObject {
  return node instanceof Map;
}

/**
 * toPlain converts a JSONNode to plain JavaScript values: numbers become
 * JavaScript numbers, and objects plain objects whose keys are own data
 * properties, `__proto__` included.
 */
export function toPlain(node: JSONNode): unknown {
  if (node instanceof JSONNumber) {
    return Number(node.literal);
  }
  if (Array.isArray(node)) {
    return node.map(toPlain);
  }
  if (node instanceof Map) {
    return Object.fromEntries([...node].map(([key, value]) => [key, toPlain(value)]));
  }
  return node;
}

/** writeCanonical writes node as ir.CanonicalJSON writes a value. */
export function writeCanonical(node: JSONNode): string {
  const out: string[] = [];
  write(node, out);
  return out.join('');
}

/**
 * canonicalJSON rewrites JSON text in the form ir.CanonicalJSON writes it,
 * which is how the IR persists extension and document data. It throws on
 * invalid JSON.
 */
export function canonicalJSON(text: string): string {
  return writeCanonical(parseJSON(text));
}

function write(node: JSONNode, out: string[]): void {
  if (node === null) {
    out.push('null');
  } else if (node === true || node === false) {
    out.push(node ? 'true' : 'false');
  } else if (typeof node === 'string') {
    out.push(quote(node));
  } else if (node instanceof JSONNumber) {
    out.push(node.literal);
  } else if (Array.isArray(node)) {
    out.push('[');
    node.forEach((item, i) => {
      if (i > 0) {
        out.push(',');
      }
      write(item, out);
    });
    out.push(']');
  } else {
    out.push('{');
    [...node.keys()].sort(compareUTF8).forEach((key, i) => {
      if (i > 0) {
        out.push(',');
      }
      out.push(quote(key), ':');
      write(node.get(key) as JSONNode, out);
    });
    out.push('}');
  }
}

const shortEscapes: Record<number, string> = {
  0x08: '\\b',
  0x09: '\\t',
  0x0a: '\\n',
  0x0c: '\\f',
  0x0d: '\\r',
  0x22: '\\"',
  0x5c: '\\\\',
};

/**
 * quote writes a JSON string as Go's encoder does with HTML escaping on:
 * `"` and `\` escaped, \b \f \n \r \t short, every other control character
 * and <, >, & as \u00XX, U+2028 and U+2029 as \u2028 and \u2029, and
 * everything else, DEL and non-ASCII included, as is.
 */
function quote(s: string): string {
  let out = '"';
  let start = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c >= 0x20 && c !== 0x22 && c !== 0x5c && c !== 0x3c && c !== 0x3e && c !== 0x26 && c !== 0x2028 && c !== 0x2029) {
      continue;
    }
    out += s.slice(start, i) + (shortEscapes[c] ?? '\\u' + c.toString(16).padStart(4, '0'));
    start = i + 1;
  }
  return out + s.slice(start) + '"';
}

function isSurrogate(unit: number): boolean {
  return unit >= 0xd800 && unit <= 0xdfff;
}

/**
 * compareUTF8 orders strings by their UTF-8 bytes, which is code point
 * order and Go's order for map keys. UTF-16 code unit order differs where a
 * surrogate (a code point past U+FFFF) meets U+E000 to U+FFFF.
 */
export function compareUTF8(a: string, b: string): number {
  const n = Math.min(a.length, b.length);
  for (let i = 0; i < n; i++) {
    const x = a.charCodeAt(i);
    const y = b.charCodeAt(i);
    if (x === y) {
      continue;
    }
    if (isSurrogate(x) !== isSurrogate(y)) {
      return isSurrogate(x) ? 1 : -1;
    }
    return x - y;
  }
  return a.length - b.length;
}

/**
 * formatFloat64 writes a float64 as Go's encoder does: the shortest
 * decimal that reads back, in exponent form below 1e-6 and from 1e21,
 * which is also ECMAScript's Number to string, except that Go keeps the
 * sign of negative zero.
 */
export function formatFloat64(value: number): string {
  return Object.is(value, -0) ? '-0' : String(value);
}
