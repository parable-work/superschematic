// Runs every canonical vector in runtime/versiongraph/testdata/canonical
// through the package's canonical rules (canonicalValue and canonicalRow),
// and, when SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names a Postgres,
// checks each vector's postgres member against what Postgres renders and
// runs the rules on that rendering. The TypeScript counterpart of the Go
// module's canonical_test.go and canonical_postgres_test.go.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import pg from "pg";
import {
  canonicalRow,
  canonicalValue,
  CanonicalError,
  ElementClasses,
  UnknownClassError,
} from "../dist/engine.js";
import { dsn, rawTypes } from "./postgres.js";

interface ValueCase {
  name: string;
  class: string;
  sql: string;
  timeZone?: string;
  postgres: string;
  canonical?: string;
  error?: string;
}

interface RowCase {
  name: string;
  columns: Record<string, string>;
  sql: string;
  timeZone?: string;
  postgres: string;
  canonical?: string;
  error?: string;
}

const vectorDir = new URL("../../testdata/canonical/", import.meta.url);
const values: ValueCase[] = [];
const rows: RowCase[] = [];
for (const file of readdirSync(vectorDir).filter((name) => name.endsWith(".json")).sort()) {
  const doc = JSON.parse(readFileSync(new URL(file, vectorDir), "utf8")) as { cases?: ValueCase[]; rows?: RowCase[] };
  const stem = file.replace(/\.json$/, "");
  for (const c of doc.cases ?? []) {
    if (c.class.replace(/(\[\])+$/, "") !== stem) {
      throw new Error(`${file}: case ${c.name} has class ${c.class}, which belongs in another file`);
    }
  }
  values.push(...(doc.cases ?? []));
  rows.push(...(doc.rows ?? []));
}

test("every element class has a value, a list, a null and a refused vector", () => {
  const seen = new Set(values.map((c) => c.class));
  for (const element of ElementClasses) {
    expect(seen.has(element) && seen.has(element + "[]")).toBe(true);
    const own = values.filter((c) => c.class.replace(/(\[\])+$/, "") === element);
    expect(own.some((c) => c.postgres === "null")).toBe(true);
    expect(own.some((c) => c.error !== undefined)).toBe(true);
  }
  expect(rows.length).toBeGreaterThan(0);
});

describe("value vectors", () => {
  for (const c of values) {
    test(`${c.class}/${c.name}`, () => {
      if (c.error !== undefined) {
        expect(() => canonicalValue(c.class, c.postgres)).toThrow(CanonicalError);
        return;
      }
      const got = canonicalValue(c.class, c.postgres);
      expect(got).toBe(c.canonical!);
      // The canonical form is a fixed point.
      expect(canonicalValue(c.class, got)).toBe(c.canonical!);
    });
  }
});

describe("row vectors", () => {
  for (const c of rows) {
    test(c.name, () => {
      if (c.error !== undefined) {
        expect(() => canonicalRow(c.columns, c.postgres)).toThrow(CanonicalError);
        return;
      }
      expect(canonicalRow(c.columns, c.postgres)).toBe(c.canonical!);
    });
  }
});

test("an unknown class is refused", () => {
  for (const valueClass of ["decimal", "uuid[][][]", "", "[]"]) {
    expect(() => canonicalValue(valueClass, '"x"')).toThrow(UnknownClassError);
  }
  expect(() => canonicalRow({ id: "decimal" }, '{"id":1}')).toThrow(UnknownClassError);
});

test("input that is not one JSON value is refused", () => {
  for (const input of ["", "1 2", '{"a":']) {
    expect(() => canonicalValue("integer", input)).toThrow();
  }
  expect(() => canonicalRow({}, "[1]")).toThrow();
});

// -0, which no Postgres rendering carries (to_jsonb writes 0) but a stored
// JSON value can, is 0 in both numeric classes.
test("negative zero is zero", () => {
  expect(canonicalValue("integer", "-0")).toBe("0");
  expect(canonicalValue("number", "-0")).toBe("0");
});

describe.skipIf(dsn === "")("renderings against Postgres", () => {
  const client = new pg.Client({ connectionString: dsn });

  beforeAll(async () => {
    await client.connect();
  });

  afterAll(async () => {
    await client.end();
  });

  // Renders a query's one text column under a session time zone (UTC when
  // the vector names none).
  async function render(timeZone: string | undefined, query: string): Promise<string> {
    await client.query("SELECT set_config('TimeZone', $1, false)", [timeZone ?? "UTC"]);
    const result = await client.query({ text: query, rowMode: "array", types: rawTypes });
    return (result.rows[0] as string[])[0]!;
  }

  for (const c of values) {
    test(`${c.class}/${c.name}`, async () => {
      const got = await render(c.timeZone, `SELECT to_jsonb(t)::text FROM (SELECT ${c.sql} AS v) AS t`);
      // The member's JSON text as Postgres wrote it: the rendering is
      // {"v": <value>}.
      expect(got.startsWith('{"v": ') && got.endsWith("}")).toBe(true);
      expect(got.slice('{"v": '.length, -1)).toBe(c.postgres);
      // The rules run on what Postgres returned, not only on text written into a file.
      if (c.error !== undefined) {
        expect(() => canonicalRow({ v: c.class }, got)).toThrow();
        return;
      }
      expect(canonicalRow({ v: c.class }, got)).toBe(`{"v":${c.canonical!}}`);
    });
  }

  for (const c of rows) {
    test(`row/${c.name}`, async () => {
      expect(await render(c.timeZone, `SELECT to_jsonb(t)::text FROM (SELECT ${c.sql}) AS t`)).toBe(c.postgres);
    });
  }
});
