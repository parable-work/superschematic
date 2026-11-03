/** Every route of an operation set that extends it needs a caller. */
export abstract class Authenticated {}

/** Every route of an operation set that extends it takes its request body as an encrypted envelope. */
export abstract class Encrypted {}
