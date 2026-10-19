// The part of bun:sqlite that ./sqlite calls, declared here since the
// package compiles without Bun's types. tsc emits no copy of this file, and
// no public declaration of the package names these types.
declare module 'bun:sqlite' {
  export interface Statement {
    readonly columnNames: string[];
    all(...params: unknown[]): unknown[];
    get(...params: unknown[]): unknown;
    run(...params: unknown[]): { changes: number; lastInsertRowid: number | bigint };
    values(...params: unknown[]): unknown[][];
  }

  export class Database {
    constructor(filename?: string, options?: { create?: boolean; readonly?: boolean });
    prepare(sql: string): Statement;
    query(sql: string): Statement;
    run(sql: string): unknown;
    transaction<A extends unknown[], R>(fn: (...args: A) => R): (...args: A) => R;
    close(): void;
  }
}
