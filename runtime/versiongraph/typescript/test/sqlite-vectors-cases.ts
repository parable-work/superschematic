// The shared SQLite vectors' checks (runtime/versiongraph/testdata/sqlite),
// each run through a binding: test/sqlite-vectors.test.ts runs them through
// bun:sqlite and node:sqlite under bun, and test/node.mjs through
// node:sqlite under Node. Under Node this file loads with Node's type
// stripping, so it uses no syntax that needs compiling.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { errorCode, isJsonObject, parseJson, SyncEngine, uuidHyphenated, type JsonValue } from "../dist/engine.js";
import { initSync } from "../dist/index.js";
import { defaultTableName, SqliteAdapter, sqliteLayout, type SqliteClient, type SqliteValue } from "../dist/sqlite.js";
import type { Binding } from "./sqlite.ts";
import {
  descriptor,
  dumpDatabase,
  engineOptions,
  gained,
  layoutFile,
  loadDatabase,
  readsFile,
  readVectors,
  sqlFile,
  writeDatabase,
} from "./sqlite-vectors.ts";

/** One check of the vectors, run through a binding. */
export interface VectorCase {
  name: string;
  run(binding: Binding): void;
}

const core = initSync();

/** The descriptor's kinds: each one's name, its role columns and its columns' value classes. */
const kinds = (
  JSON.parse(descriptor) as {
    kinds: { kind: string; key: string; id: string; ref: string; root: string; tombstone: string; version: string; columns: Record<string, string> }[];
  }
).kinds;

/** Opens an in-memory database through binding, runs fn on it, and closes it however fn ends. */
function inMemory<T>(binding: Binding, fn: (client: SqliteClient) => T): T {
  const db = binding.open(":memory:");
  try {
    return fn(db.client);
  } finally {
    db.close();
  }
}

/** The error code fn throws; it fails when fn does not throw. */
function codeOf(fn: () => unknown): string {
  try {
    fn();
  } catch (err) {
    return errorCode(err);
  }
  assert.fail("expected an error");
}

/** What the isolation check reads of typescript.json. */
interface Vectors {
  graphs: {
    graph: string;
    refs: { id: string }[];
    commits: { id: string }[];
    images: { kind: string; id: string; version: number }[];
  }[];
}

/** A JSON object's member, from a row the file holds. */
function memberOf(text: string, name: string): JsonValue | undefined {
  const parsed = parseJson(text);
  assert.ok(isJsonObject(parsed));
  return parsed.get(name);
}

export const vectorCases: VectorCase[] = [
  {
    name: "sqliteLayout() is layout.json",
    run() {
      const file = JSON.parse(readFileSync(layoutFile, "utf8")) as { statements: string[] };
      assert.deepEqual(sqliteLayout(), file.statements);
    },
  },
  {
    name: "typescript.sql begins with layout.json's statements",
    run() {
      const file = JSON.parse(readFileSync(layoutFile, "utf8")) as { statements: string[] };
      const lines = readFileSync(sqlFile, "utf8").split("\n");
      assert.deepEqual(
        lines.slice(0, file.statements.length),
        file.statements.map((statement) => statement + ";"),
      );
      assert.ok(
        lines.slice(file.statements.length).every((line) => line === "" || line.startsWith("INSERT INTO ")),
        "every other statement is an INSERT",
      );
    },
  },
  {
    name: "typescript.sql reads as typescript.json, through an adapter opened with each graph's name",
    run(binding) {
      inMemory(binding, (client) => {
        loadDatabase(client, readFileSync(sqlFile, "utf8"));
        assert.equal(readVectors(client), readFileSync(readsFile, "utf8"));
      });
    },
  },
  {
    name: "through each graph's adapter, another graph's refs, commits and images read as none",
    run(binding) {
      const file = JSON.parse(readFileSync(readsFile, "utf8")) as Vectors;
      assert.ok(file.graphs.length >= 2, "the file holds two graphs");
      inMemory(binding, (client) => {
        loadDatabase(client, readFileSync(sqlFile, "utf8"));
        for (const g of file.graphs) {
          const storage = new SqliteAdapter(descriptor, { graph: g.graph }).storage(client);
          const engine = new SyncEngine(core, descriptor, storage, engineOptions);
          for (const other of file.graphs.filter((o) => o.graph !== g.graph)) {
            storage.transact((tx) => {
              for (const ref of other.refs) {
                assert.equal(codeOf(() => tx.readRef(ref.id)), "not_found", `${g.graph} reads ${other.graph}'s ref ${ref.id}`);
                for (const k of kinds) {
                  assert.deepEqual(tx.rows(k.kind, ref.id), [], `${g.graph} reads ${other.graph}'s ${k.kind} rows`);
                }
              }
              for (const commit of other.commits) {
                assert.equal(codeOf(() => tx.readCommit(commit.id)), "not_found", `${g.graph} reads ${other.graph}'s commit ${commit.id}`);
                assert.deepEqual(tx.patches([commit.id]), [], `${g.graph} reads ${other.graph}'s patches`);
                assert.deepEqual(tx.snapshot(commit.id), [], `${g.graph} reads ${other.graph}'s snapshot`);
              }
              for (const pin of other.images) {
                assert.deepEqual(tx.images(pin.kind, [{ id: pin.id, version: pin.version }]), [], `${g.graph} reads ${other.graph}'s image`);
              }
            });
            for (const ref of other.refs) {
              assert.equal(codeOf(() => engine.compose(ref.id)), "not_found");
            }
            for (const commit of other.commits) {
              assert.equal(codeOf(() => engine.materialize(commit.id)), "not_found");
            }
          }
        }
      });
    },
  },
  {
    name: "typescript.sql holds every stored form the script is to write",
    run(binding) {
      inMemory(binding, (client) => {
        loadDatabase(client, readFileSync(sqlFile, "utf8"));
        const t = (local: string) => `"${defaultTableName(local)}"`;
        const holds = (what: string, sql: string) =>
          assert.ok(client.get(`SELECT EXISTS (${sql}) AS found`)!["found"] === 1, `typescript.sql holds ${what}`);
        holds("two graphs", `SELECT 1 FROM ${t("ref")} AS a JOIN ${t("ref")} AS b ON a.graph <> b.graph`);
        holds("a primary line", `SELECT 1 FROM ${t("ref")} WHERE parent_ref_id IS NULL`);
        holds("a live change set", `SELECT 1 FROM ${t("ref")} WHERE parent_ref_id IS NOT NULL AND deleted_at IS NULL AND sealed_at IS NULL`);
        holds("a sealed change set", `SELECT 1 FROM ${t("ref")} WHERE sealed_at IS NOT NULL`);
        holds("a discarded draft", `SELECT 1 FROM ${t("ref")} WHERE deleted_at IS NOT NULL AND deleted_by IS NOT NULL`);
        holds("ref history", `SELECT 1 FROM ${t("ref_history")} WHERE operation = 'INSERT'`);
        holds("a tagged commit", `SELECT 1 FROM ${t("commit")} WHERE sequence IS NOT NULL`);
        holds("an untagged commit", `SELECT 1 FROM ${t("commit")} WHERE sequence IS NULL`);
        holds("a commit without a message", `SELECT 1 FROM ${t("commit")} WHERE message IS NULL`);
        for (const op of ["ADD", "UPDATE", "DELETE"]) {
          holds(`a ${op} patch`, `SELECT 1 FROM ${t("patch")} WHERE operation = '${op}'`);
        }
        holds("a snapshot", `SELECT 1 FROM ${t("snapshot_entry")}`);
        holds(
          "a release moved to a second commit",
          `SELECT 1 FROM ${t("release_history")} AS a JOIN ${t("release_history")} AS b ON a.id = b.id AND a._version = 1 AND b._version = 2 ` +
            `AND json_extract(a.data, '$.commit_id') <> json_extract(b.data, '$.commit_id')`,
        );
        for (const k of kinds) {
          holds(`a ${k.kind} row`, `SELECT 1 FROM ${t("member")} WHERE kind = '${k.kind}'`);
        }
        holds("a tombstone", `SELECT 1 FROM ${t("member")} WHERE tombstone = 1`);
        for (const op of ["INSERT", "UPDATE", "DELETE"]) {
          holds(`a member ${op} image`, `SELECT 1 FROM ${t("member_history")} WHERE operation = '${op}'`);
        }
        holds(
          "a DELETE image that names its actor",
          `SELECT 1 FROM ${t("member_history")} AS d JOIN ${t("member_history")} AS before ON before.id = d.id AND before._version = d._version - 1 ` +
            `WHERE d.operation = 'DELETE' AND d.kind = 'step' AND json_extract(d.data, '$.updated_by') <> json_extract(before.data, '$.updated_by')`,
        );
        holds(
          "a step image without the column its history excludes",
          `SELECT 1 FROM ${t("member_history")} AS h JOIN ${t("member")} AS m ON m.id = h.id ` +
            `WHERE h.kind = 'step' AND json_type(m.data, '$.scratch') = 'text' AND json_type(h.data, '$.scratch') IS NULL`,
        );
        holds(
          "a partial insert's omitted columns stored null",
          `SELECT 1 FROM ${t("member_history")} WHERE kind = 'tasting' AND operation = 'INSERT' ` +
            `AND json_type(data, '$.taster') = 'null' AND json_type(data, '$.score') IN ('integer', 'real')`,
        );
        holds("an integer wider than a double, digit for digit", `SELECT 1 FROM ${t("member")} WHERE instr(data, '"servings":9007199254740993') > 0`);
        holds("a number past 1e21, as 1e+21", `SELECT 1 FROM ${t("member")} WHERE instr(data, '"score":1e+21') > 0`);
        holds("text with an apostrophe", `SELECT 1 FROM ${t("member")} WHERE instr(data, '''') > 0`);
        const data = client.all(`SELECT data FROM ${t("member")}`).map((r) => r["data"] as string);
        assert.ok(data.some((d) => /[^\u0000-\u007f]/.test(d)), "typescript.sql holds text outside ASCII");
        assert.ok(data.some((d) => /[\u{10000}-\u{10ffff}]/u.test(d)), "typescript.sql holds text outside the Basic Multilingual Plane");
        // The script gives entity keys as short names, never a version-4 UUID.
        assert.ok(
          client
            .all(`SELECT entity_key FROM ${t("member")} WHERE kind = 'utensil'`)
            .some((r) => /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(uuidHyphenated(r["entity_key"] as string))),
          "typescript.sql holds a utensil whose entity key the adapter generated",
        );
        const path = `'$.${gained.column}'`;
        holds(
          `a live ${gained.kind} row stored before its kind gained ${gained.column}`,
          `SELECT 1 FROM ${t("member")} WHERE kind = '${gained.kind}' AND json_type(data, ${path}) IS NULL`,
        );
        holds(
          `a ${gained.kind} image stored before the gain, without ${gained.column}`,
          `SELECT 1 FROM ${t("member_history")} WHERE kind = '${gained.kind}' AND json_type(data, ${path}) IS NULL`,
        );
        holds(
          `a ${gained.kind} image written after the gain, with ${gained.column}`,
          `SELECT 1 FROM ${t("member_history")} WHERE kind = '${gained.kind}' AND json_type(data, ${path}) = 'text'`,
        );
        const reads = JSON.parse(readFileSync(readsFile, "utf8")) as {
          graphs: { commits: { readCommit: { contentHash: string }; materialize: { contentHash?: string } }[] }[];
        };
        assert.ok(
          reads.graphs.some((g) => g.commits.some((c) => c.materialize.contentHash !== undefined && c.materialize.contentHash !== c.readCommit.contentHash)),
          "typescript.json holds a commit recorded before the gain, which materializes to another hash",
        );
        const tastings = client.all(`SELECT data FROM ${t("member")} WHERE kind = 'tasting'`).map((r) => r["data"] as string);
        const tasting = kinds.find((k) => k.kind === "tasting")!;
        const roles = new Set([tasting.key, tasting.id, tasting.ref, tasting.root, tasting.tombstone, tasting.version]);
        for (const column of Object.keys(tasting.columns).filter((c) => !roles.has(c))) {
          assert.ok(
            tastings.some((data) => {
              const value = memberOf(data, column);
              return value !== undefined && value !== null && !(Array.isArray(value) && value.length === 0);
            }),
            `a tasting holds a ${tasting.columns[column]} value in ${column}`,
          );
        }
      });
    },
  },
  {
    name: "the script writes typescript.sql, and its database reads as typescript.json",
    run(binding) {
      const crypto = globalThis.crypto;
      const method = crypto.randomUUID;
      const own = Object.getOwnPropertyDescriptor(crypto, "randomUUID");
      const restored = (after: string) => {
        assert.equal(crypto.randomUUID, method, `crypto.randomUUID is the system's again after ${after}`);
        assert.deepEqual(Object.getOwnPropertyDescriptor(crypto, "randomUUID"), own, `crypto's own randomUUID is as it was after ${after}`);
      };
      inMemory(binding, (client) => {
        writeDatabase(client);
        restored("the script");
        assert.equal(dumpDatabase(client), readFileSync(sqlFile, "utf8"));
        assert.equal(readVectors(client), readFileSync(readsFile, "utf8"));
      });
      // A run that fails partway, at the client's 40th statement.
      inMemory(binding, (client) => {
        let statements = 0;
        const failing: SqliteClient = {
          exec: (sql: string) => client.exec!(sql),
          run: (sql: string, params?: readonly SqliteValue[]) => {
            if (++statements === 40) {
              throw new Error("the client failed partway");
            }
            return client.run(sql, params);
          },
          get: (sql: string, params?: readonly SqliteValue[]) => client.get(sql, params),
          all: (sql: string, params?: readonly SqliteValue[]) => client.all(sql, params),
        };
        assert.throws(() => writeDatabase(failing), /the client failed partway/);
        restored("a script that failed partway");
      });
    },
  },
];
