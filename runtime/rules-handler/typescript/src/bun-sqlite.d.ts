// The part of bun:sqlite that ./sqlite calls, declared here since the
// package compiles without Bun's types (runtime/http/typescript's
// src/bun-sqlite.d.ts is the precedent). tsc emits no copy of this file,
// and no public declaration of the package names these types.
declare module 'bun:sqlite' {
  export interface Statement {
    all(...params: unknown[]): unknown[];
    get(...params: unknown[]): unknown;
    run(...params: unknown[]): { changes: number; lastInsertRowid: number | bigint };
  }

  export class Database {
    constructor(filename?: string, options?: { create?: boolean; readonly?: boolean; strict?: boolean });
    prepare(sql: string): Statement;
    exec(sql: string): unknown;
    close(): void;
  }
}
