// Exact JSON: a parser and a writer that keep every number as the text it
// was written with, so an integer wider than a double, or a numeric with
// more digits than a double holds, survives a round trip. JSON.parse reads
// every number as a double.
//
// A parsed object is a Map, which keeps its members in the order they were
// written, whatever their names; a repeated member keeps its last value, as
// Go's decoder does.

/** A JSON number, kept as the text it was written with. */
export class JsonNumber {
  constructor(readonly text: string) {}

  toString(): string {
    return this.text;
  }
}

/** A JSON value with its numbers kept exact. */
export type JsonValue = null | boolean | string | JsonNumber | JsonValue[] | JsonObject;

/** A JSON object, its members in the order they were written. */
export type JsonObject = Map<string, JsonValue>;

/** Text that is not one JSON value. */
export class JsonSyntaxError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "JsonSyntaxError";
  }
}

const numberPattern = /-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/y;

/**
 * Parses one JSON value, with only whitespace around it. Numbers stay
 * JsonNumber, objects are Maps. A lone surrogate escape reads as U+FFFD, as
 * Go's decoder reads it.
 */
export function parseJson(text: string): JsonValue {
  const parser = new Parser(text);
  parser.space();
  const value = parser.value();
  parser.space();
  if (parser.at < text.length) {
    throw new JsonSyntaxError(`more than one JSON value (at offset ${parser.at})`);
  }
  return value;
}

class Parser {
  at = 0;

  constructor(readonly text: string) {}

  fail(what: string): never {
    if (this.at >= this.text.length) {
      throw new JsonSyntaxError(`unexpected end of JSON input (${what})`);
    }
    throw new JsonSyntaxError(`invalid JSON at offset ${this.at}: ${what}`);
  }

  space(): void {
    const text = this.text;
    while (this.at < text.length) {
      const c = text.charCodeAt(this.at);
      if (c !== 0x20 && c !== 0x09 && c !== 0x0a && c !== 0x0d) {
        return;
      }
      this.at++;
    }
  }

  value(): JsonValue {
    const c = this.text[this.at];
    switch (c) {
      case "{":
        return this.object();
      case "[":
        return this.array();
      case '"':
        return this.string();
      case "t":
        return this.literal("true", true);
      case "f":
        return this.literal("false", false);
      case "n":
        return this.literal("null", null);
    }
    numberPattern.lastIndex = this.at;
    const match = numberPattern.exec(this.text);
    if (match === null) {
      this.fail("expected a value");
    }
    this.at += match[0].length;
    return new JsonNumber(match[0]);
  }

  literal<T>(word: string, value: T): T {
    if (!this.text.startsWith(word, this.at)) {
      this.fail(`expected ${word}`);
    }
    this.at += word.length;
    return value;
  }

  object(): JsonObject {
    const out: JsonObject = new Map();
    this.at++;
    this.space();
    if (this.text[this.at] === "}") {
      this.at++;
      return out;
    }
    for (;;) {
      this.space();
      if (this.text[this.at] !== '"') {
        this.fail("expected a member name");
      }
      const name = this.string();
      this.space();
      if (this.text[this.at] !== ":") {
        this.fail("expected ':'");
      }
      this.at++;
      this.space();
      out.set(name, this.value());
      this.space();
      const c = this.text[this.at];
      this.at++;
      if (c === "}") {
        return out;
      }
      if (c !== ",") {
        this.at--;
        this.fail("expected ',' or '}'");
      }
    }
  }

  array(): JsonValue[] {
    const out: JsonValue[] = [];
    this.at++;
    this.space();
    if (this.text[this.at] === "]") {
      this.at++;
      return out;
    }
    for (;;) {
      this.space();
      out.push(this.value());
      this.space();
      const c = this.text[this.at];
      this.at++;
      if (c === "]") {
        return out;
      }
      if (c !== ",") {
        this.at--;
        this.fail("expected ',' or ']'");
      }
    }
  }

  string(): string {
    const text = this.text;
    this.at++;
    let out = "";
    let start = this.at;
    for (;;) {
      if (this.at >= text.length) {
        this.fail("unterminated string");
      }
      const c = text.charCodeAt(this.at);
      if (c === 0x22) {
        out += text.slice(start, this.at);
        this.at++;
        return out;
      }
      if (c < 0x20) {
        this.fail("control character in string");
      }
      if (c !== 0x5c) {
        this.at++;
        continue;
      }
      out += text.slice(start, this.at);
      this.at++;
      const e = text[this.at];
      this.at++;
      switch (e) {
        case '"':
          out += '"';
          break;
        case "\\":
          out += "\\";
          break;
        case "/":
          out += "/";
          break;
        case "b":
          out += "\b";
          break;
        case "f":
          out += "\f";
          break;
        case "n":
          out += "\n";
          break;
        case "r":
          out += "\r";
          break;
        case "t":
          out += "\t";
          break;
        case "u": {
          const unit = this.hex4();
          if (unit >= 0xd800 && unit < 0xdc00) {
            // A high surrogate pairs with a following low surrogate escape;
            // alone it is U+FFFD, and what follows is read on its own.
            if (text[this.at] === "\\" && text[this.at + 1] === "u") {
              const save = this.at;
              this.at += 2;
              const low = this.hex4();
              if (low >= 0xdc00 && low < 0xe000) {
                out += String.fromCharCode(unit, low);
                break;
              }
              this.at = save;
            }
            out += "\ufffd";
          } else if (unit >= 0xdc00 && unit < 0xe000) {
            out += "\ufffd";
          } else {
            out += String.fromCharCode(unit);
          }
          break;
        }
        default:
          this.at--;
          this.fail("invalid escape");
      }
      start = this.at;
    }
  }

  hex4(): number {
    const digits = this.text.slice(this.at, this.at + 4);
    if (!/^[0-9a-fA-F]{4}$/.test(digits)) {
      this.fail("invalid \\u escape");
    }
    this.at += 4;
    return parseInt(digits, 16);
  }
}

/**
 * Writes a JSON value compactly: members in their order, numbers as
 * written, strings as writeJsonString writes them.
 */
export function stringifyJson(value: JsonValue): string {
  const out: string[] = [];
  write(out, value);
  return out.join("");
}

function write(out: string[], value: JsonValue): void {
  if (value === null) {
    out.push("null");
  } else if (value === true) {
    out.push("true");
  } else if (value === false) {
    out.push("false");
  } else if (typeof value === "string") {
    out.push(writeJsonString(value));
  } else if (value instanceof JsonNumber) {
    out.push(value.text);
  } else if (Array.isArray(value)) {
    out.push("[");
    value.forEach((element, i) => {
      if (i > 0) {
        out.push(",");
      }
      write(out, element);
    });
    out.push("]");
  } else {
    out.push("{");
    let first = true;
    for (const [name, member] of value) {
      if (!first) {
        out.push(",");
      }
      first = false;
      out.push(writeJsonString(name), ":");
      write(out, member);
    }
    out.push("}");
  }
}

const hex = "0123456789abcdef";

/**
 * Writes a JSON string as the core's canonical JSON does: `"` and `\`
 * escaped, \b, \f, \n, \r and \t by name, every other control character as
 * \u00xx in lowercase hex, and everything else as it is. A lone surrogate,
 * which UTF-8 cannot carry, is written as U+FFFD, as Go writes it.
 */
export function writeJsonString(s: string): string {
  let out = '"';
  let start = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    let escaped: string | undefined;
    if (c < 0x20 || c === 0x22 || c === 0x5c) {
      switch (c) {
        case 0x22:
          escaped = '\\"';
          break;
        case 0x5c:
          escaped = "\\\\";
          break;
        case 0x08:
          escaped = "\\b";
          break;
        case 0x0c:
          escaped = "\\f";
          break;
        case 0x0a:
          escaped = "\\n";
          break;
        case 0x0d:
          escaped = "\\r";
          break;
        case 0x09:
          escaped = "\\t";
          break;
        default:
          escaped = "\\u00" + hex[c >> 4] + hex[c & 0xf];
      }
    } else if (c >= 0xd800 && c < 0xe000) {
      const next = s.charCodeAt(i + 1);
      if (c < 0xdc00 && next >= 0xdc00 && next < 0xe000) {
        i++;
        continue;
      }
      escaped = "\ufffd";
    }
    if (escaped !== undefined) {
      out += s.slice(start, i) + escaped;
      start = i + 1;
    }
  }
  return out + s.slice(start) + '"';
}

/**
 * Orders two strings by code point, as Go orders strings by their UTF-8
 * bytes. JavaScript's < orders by UTF-16 code unit, which puts a character
 * past U+FFFF before U+E000..U+FFFF.
 */
export function compareCodePoints(a: string, b: string): number {
  const n = Math.min(a.length, b.length);
  for (let i = 0; i < n; i++) {
    const x = a.charCodeAt(i);
    const y = b.charCodeAt(i);
    if (x === y) {
      continue;
    }
    const xs = x >= 0xd800 && x < 0xe000;
    const ys = y >= 0xd800 && y < 0xe000;
    if (xs !== ys) {
      return xs ? 1 : -1;
    }
    return x - y;
  }
  return a.length - b.length;
}

/** Whether a parsed value is a JSON object. */
export function isJsonObject(value: JsonValue | undefined): value is JsonObject {
  return value instanceof Map;
}

/**
 * Whether two JSON values are equal: numbers by their text, objects by
 * their members whatever their order.
 */
export function jsonEqual(a: JsonValue, b: JsonValue): boolean {
  if (a === b) {
    return true;
  }
  if (a instanceof JsonNumber) {
    return b instanceof JsonNumber && a.text === b.text;
  }
  if (Array.isArray(a)) {
    return Array.isArray(b) && a.length === b.length && a.every((element, i) => jsonEqual(element, b[i]!));
  }
  if (a instanceof Map) {
    if (!(b instanceof Map) || a.size !== b.size) {
      return false;
    }
    for (const [name, member] of a) {
      const other = b.get(name);
      if (other === undefined || !jsonEqual(member, other)) {
        return false;
      }
    }
    return true;
  }
  return false;
}
