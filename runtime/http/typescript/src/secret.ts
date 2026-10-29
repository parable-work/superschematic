/*
A secret a program holds (D85): an Encrypted<T> field the repositories
decrypt. Only reveal() returns it; JSON.stringify, util.inspect,
console.log and string conversion print SECRET_MASK, so logging a row
never prints a secret. An API's env config holds its Secret<T> fields in a
SecretValue of its own, alike.
*/

/** What a secret prints as everywhere except SecretValue.reveal(). */
export const SECRET_MASK = '[secret]';

const inspectCustom = Symbol.for('nodejs.util.inspect.custom');

/** A secret value, revealed only by reveal(). */
export class SecretValue<T> {
  readonly #value: T;

  constructor(value: T) {
    this.#value = value;
  }

  /** The secret itself, for the code that needs it. Never log the result. */
  reveal(): T {
    return this.#value;
  }

  toJSON(): string {
    return SECRET_MASK;
  }

  toString(): string {
    return SECRET_MASK;
  }

  [Symbol.toPrimitive](): string {
    return SECRET_MASK;
  }

  [inspectCustom](): string {
    return SECRET_MASK;
  }
}

/** Anything that reveals a secret: a SecretValue, an env config's among them. */
export interface Revealable<T> {
  reveal(): T;
}

/** The value itself, or what a SecretValue (of any copy of the class) reveals. */
export function revealed<T>(value: T | Revealable<T>): T {
  if (typeof value === 'object' && value !== null && typeof (value as Revealable<T>).reveal === 'function') {
    return (value as Revealable<T>).reveal();
  }
  return value as T;
}
