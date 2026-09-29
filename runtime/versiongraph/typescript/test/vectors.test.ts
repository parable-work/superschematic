// Runs every vector in runtime/versiongraph/testdata/vectors through the
// package API, loaded from the built dist/ with its default wasm file.
//
// A vector whose expect is an output decodes its input through the contract
// types (src/contract.ts), and the decoded input must serialize to the
// vector's input; the operation runs on that decoded input, and its output,
// decoded the same way, must serialize to expect. The comparison is of JSON
// text, so member order counts. A vector whose expect is an error sends its
// input as written, since many are malformed on purpose, and the thrown
// VersionGraphError, its code decoded through ErrorCode, must serialize to
// expect.
//
// Each decoder has one field per member of its type, marked required or
// optional as the type marks it, and the compiler requires the fields to
// cover the type exactly. A decoded value missing a required member, or
// carrying a member the type lacks, fails. Each literal union is checked
// against a record the compiler requires to list the union exactly, so a
// value outside it fails. The last test fails when a member or a literal of
// a type never appears in any vector. Together they fail when a type in
// src/contract.ts has a member or literal missing, extra or misnamed.
import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import {
  init,
  VersionGraphError,
  type Change,
  type ChangeOperation,
  type ComposeInput,
  type ComposeOutput,
  type ContentHashOutput,
  type Conflict,
  type Descriptor,
  type DiffInput,
  type DiffOutput,
  type EntityOutcome,
  type ErrorCode,
  type ErrorDocument,
  type Finding,
  type FindingCode,
  type KindDescriptor,
  type MergeInput,
  type MergeOutput,
  type OperationName,
  type ParentEdge,
  type Resolution,
  type RootTable,
  type Side,
  type Take,
  type TakeResolution,
  type Tree,
  type TreeInput,
  type Unit,
  type ValidateOutput,
  type ValueClass,
  type ValueResolution,
  type VersionGraph,
} from "../dist/index.js";

declare global {
  interface JSON {
    rawJSON(text: string): unknown;
  }
}

// Parse JSON keeping every number as written, so an integer wider than a
// double reaches the core, and is compared, exactly.
function parseExact(text: string): unknown {
  return JSON.parse(text, (_key, value, context?: { source?: string }) =>
    typeof value === "number" && context?.source !== undefined ? JSON.rawJSON(context.source) : value,
  );
}

// What each decoder declares and what the vectors showed it, for the
// coverage test at the end: members for an object type, values for a
// literal union.
const declared = new Map<string, string[]>();
const seen = new Map<string, Set<string>>();

function track(name: string, members: string[]): Set<string> {
  const found = new Set<string>();
  declared.set(name, members);
  seen.set(name, found);
  return found;
}

// Contract members no vector can carry: a vector is JSON, so the core never
// answers one with invalid_json (the test below sends it text that is not),
// and internal is the core's own failure.
const unreachable = new Map<string, string[]>([["ErrorCode", ["internal"]]]);

interface Member<Optional extends boolean, V> {
  optional: Optional;
  decode: (value: V) => unknown;
}
const req = <V>(decode: (value: V) => unknown): Member<false, V> => ({ optional: false, decode });
const opt = <V>(decode: (value: V) => unknown): Member<true, V> => ({ optional: true, decode });

type OptionalKeys<T> = { [K in keyof T]-?: {} extends Pick<T, K> ? K : never }[keyof T];

// One field per member of T: opt for an optional member, req for a required
// one, and no other names.
type Fields<T> = {
  [K in keyof T]-?: Member<K extends OptionalKeys<T> ? true : false, NonNullable<T[K]>>;
};

// Decodes a value of T member by member, in the value's order. null passes
// through, since a row column may hold it.
function decoder<T extends object>(name: string, fields: Fields<T>): (value: T) => T {
  const members = Object.keys(fields) as (keyof T & string)[];
  const found = track(name, members);
  return (value) => {
    const out: Record<string, unknown> = {};
    for (const [member, item] of Object.entries(value)) {
      if (!Object.hasOwn(fields, member)) {
        throw new Error(`${name} has no member ${member}`);
      }
      const field = fields[member as keyof T & string] as Member<boolean, unknown>;
      out[member] = item === null ? null : field.decode(item);
      found.add(member);
    }
    for (const member of members) {
      if (!fields[member].optional && !(member in value)) {
        throw new Error(`${name} is missing its required member ${member}`);
      }
    }
    return out as T;
  };
}

// Checks a value against a literal union. The record must name every member
// of T and nothing else, which the compiler enforces.
function literal<T extends string>(name: string, members: Record<T, true>): (value: T) => T {
  const found = track(name, Object.keys(members));
  return (value) => {
    if (typeof value !== "string" || !Object.hasOwn(members, value)) {
      throw new Error(`${name} has no member ${JSON.stringify(value)}`);
    }
    found.add(value);
    return value;
  };
}

const same = <V>(value: V): V => value;
const list =
  <V>(item: (value: V) => V) =>
  (values: V[]): V[] =>
    values.map(item);
const record =
  <V>(item: (value: V) => V) =>
  (value: Record<string, V>): Record<string, V> =>
    Object.fromEntries(Object.entries(value).map(([key, member]) => [key, item(member)]));
// Rows are opaque to the contract: a JSON object per row, keyed by column.
const row = (value: Record<string, unknown>) => ({ ...value });
const tree = (value: Tree): Tree =>
  Object.fromEntries(Object.entries(value).map(([kind, rows]) => [kind, rows.map(row)]));

const unit = literal<Unit>("Unit", { atomic: true, keyed: true, jsonSchema: true });
const findingCode = literal<FindingCode>("FindingCode", {
  absent_parent: true,
  duplicate_entity_key: true,
  singleton: true,
  parent_cycle: true,
  order_out_of_range: true,
});
const take = literal<Take>("Take", { base: true, ours: true, theirs: true });
const side = literal<Side>("Side", { ours: true, theirs: true, merged: true, conflict: true });
const changeOperation = literal<ChangeOperation>("ChangeOperation", { ADD: true, UPDATE: true, DELETE: true });
const errorCode = literal<ErrorCode>("ErrorCode", {
  invalid_json: true,
  invalid_request: true,
  invalid_descriptor: true,
  unknown_kind: true,
  invalid_row: true,
  duplicate_entity_key: true,
  order_out_of_range: true,
  invalid_resolution: true,
  unmatched_resolution: true,
  internal: true,
});

const valueClass = literal<ValueClass>("ValueClass", {
  string: true,
  "string[]": true,
  "string[][]": true,
  integer: true,
  "integer[]": true,
  "integer[][]": true,
  number: true,
  "number[]": true,
  "number[][]": true,
  boolean: true,
  "boolean[]": true,
  "boolean[][]": true,
  uuid: true,
  "uuid[]": true,
  "uuid[][]": true,
  dateTime: true,
  "dateTime[]": true,
  "dateTime[][]": true,
  date: true,
  "date[]": true,
  "date[][]": true,
  time: true,
  "time[]": true,
  "time[][]": true,
  duration: true,
  "duration[]": true,
  "duration[][]": true,
  enum: true,
  "enum[]": true,
  "enum[][]": true,
  json: true,
  "json[]": true,
  "json[][]": true,
});

const parentEdge = decoder<ParentEdge>("ParentEdge", { key: req(same), kind: req(same) });
const kindDescriptor = decoder<KindDescriptor>("KindDescriptor", {
  kind: req(same),
  table: req(same),
  historyTable: req(same),
  key: req(same),
  id: req(same),
  ref: req(same),
  tombstone: req(same),
  version: req(same),
  author: opt(same),
  parent: opt(parentEdge),
  order: opt(same),
  singleton: opt(same),
  units: opt(record(unit)),
  excluded: opt(same),
  columns: req(record(valueClass)),
});
const rootTable = decoder<RootTable>("RootTable", { table: req(same), key: req(same) });
const descriptor = decoder<Descriptor>("Descriptor", {
  version: req(same),
  graph: opt(same),
  root: req(rootTable),
  refTable: req(same),
  commitTable: req(same),
  patchTable: req(same),
  kinds: req(list(kindDescriptor)),
});
const finding = decoder<Finding>("Finding", {
  code: req(findingCode),
  kind: req(same),
  entityKey: opt(same),
  message: req(same),
});

const takeResolution = decoder<TakeResolution>("TakeResolution", {
  kind: req(same),
  entityKey: req(same),
  path: req(same),
  take: req(take),
});
const valueResolution = decoder<ValueResolution>("ValueResolution", {
  kind: req(same),
  entityKey: req(same),
  path: req(same),
  value: req(same),
});
const resolution = (value: Resolution): Resolution =>
  "take" in value ? takeResolution(value) : valueResolution(value);

const composeInput = decoder<ComposeInput>("ComposeInput", {
  descriptor: req(descriptor),
  base: req(tree),
  overlay: req(tree),
});
const composeOutput = decoder<ComposeOutput>("ComposeOutput", { tree: req(tree), findings: req(list(finding)) });

const mergeInput = decoder<MergeInput>("MergeInput", {
  descriptor: req(descriptor),
  base: req(tree),
  ours: req(tree),
  theirs: req(tree),
  resolutions: opt(list(resolution)),
});
const conflict = decoder<Conflict>("Conflict", {
  kind: req(same),
  entityKey: req(same),
  path: req(same),
  base: opt(same),
  ours: opt(same),
  theirs: opt(same),
  oursAuthor: opt(same),
  theirsAuthor: opt(same),
});
const entityOutcome = decoder<EntityOutcome>("EntityOutcome", {
  kind: req(same),
  entityKey: req(same),
  side: req(side),
  deleted: opt(same),
});
const mergeOutput = decoder<MergeOutput>("MergeOutput", {
  merged: req(tree),
  conflicts: req(list(conflict)),
  entities: req(list(entityOutcome)),
});

const diffInput = decoder<DiffInput>("DiffInput", { descriptor: req(descriptor), from: req(tree), to: req(tree) });
const change = decoder<Change>("Change", {
  kind: req(same),
  entityKey: req(same),
  operation: req(changeOperation),
  row: opt(row),
});
const diffOutput = decoder<DiffOutput>("DiffOutput", { changes: req(list(change)) });

const treeInput = decoder<TreeInput>("TreeInput", { descriptor: req(descriptor), tree: req(tree) });
const contentHashOutput = decoder<ContentHashOutput>("ContentHashOutput", { contentHash: req(same) });
const validateOutput = decoder<ValidateOutput>("ValidateOutput", { findings: req(list(finding)) });

const errorBody = decoder<ErrorDocument["error"]>("ErrorDocument.error", {
  code: req(errorCode),
  message: req(same),
});
const errorDocumentOf = decoder<ErrorDocument>("ErrorDocument", { error: req(errorBody) });

// The error document a thrown VersionGraphError stands for.
function errorDocument(thrown: unknown): ErrorDocument {
  expect(thrown).toBeInstanceOf(VersionGraphError);
  const error = thrown as VersionGraphError;
  return errorDocumentOf({ error: { code: error.code, message: error.message } });
}

function thrownBy(run: () => unknown): unknown {
  try {
    run();
  } catch (error) {
    return error;
  }
  return undefined;
}

interface Operation {
  decodeInput(input: unknown): unknown;
  // Runs the operation on a decoded input and decodes its output.
  run(graph: VersionGraph, input: unknown): unknown;
}

function operation<I, O>(
  decodeInput: (input: I) => I,
  call: (graph: VersionGraph, input: I) => O,
  decodeOutput: (output: O) => O,
): Operation {
  return {
    decodeInput: (input) => decodeInput(input as I),
    run: (graph, input) => decodeOutput(call(graph, input as I)),
  };
}

const operations: Record<OperationName, Operation> = {
  compose: operation(composeInput, (g, i) => g.compose(i), composeOutput),
  merge: operation(mergeInput, (g, i) => g.merge(i), mergeOutput),
  diff: operation(diffInput, (g, i) => g.diff(i), diffOutput),
  content_hash: operation(treeInput, (g, i) => g.contentHash(i), contentHashOutput),
  validate: operation(treeInput, (g, i) => g.validate(i), validateOutput),
};

const vectorsDir = join(import.meta.dir, "../../testdata/vectors");
const files = readdirSync(vectorsDir)
  .filter((name) => name.endsWith(".json"))
  .sort();

const graph = await init(undefined, { parse: parseExact });

test("vectors exist", () => {
  expect(files.length).toBeGreaterThan(0);
});

for (const file of files) {
  const vector = parseExact(readFileSync(join(vectorsDir, file), "utf8")) as {
    name: string;
    op: OperationName;
    input: unknown;
    expect: Record<string, unknown>;
  };
  test(vector.name, () => {
    expect(`${vector.name}.json`).toBe(file);
    const op = operations[vector.op];
    expect(op).toBeDefined();
    if ("error" in vector.expect) {
      const thrown = thrownBy(() => op.run(graph, vector.input));
      expect(JSON.stringify(errorDocument(thrown))).toBe(JSON.stringify(vector.expect));
      return;
    }
    const input = op.decodeInput(vector.input);
    expect(JSON.stringify(input)).toBe(JSON.stringify(vector.input));
    expect(JSON.stringify(op.run(graph, input))).toBe(JSON.stringify(vector.expect));
  });
}

test("a document that is not JSON is invalid_json", () => {
  const thrown = thrownBy(() => graph.run("validate", "{not json"));
  expect(errorDocument(thrown).error.code).toBe("invalid_json");
});

// Runs last: every member and literal of every contract type appeared in
// some vector (or above), so none is extra or misnamed without failing.
test("the vectors use every member of the contract types", () => {
  const unused: string[] = [];
  for (const [name, members] of declared) {
    const exempt = unreachable.get(name) ?? [];
    for (const member of members) {
      if (!seen.get(name)?.has(member) && !exempt.includes(member)) {
        unused.push(`${name}.${member}`);
      }
    }
  }
  expect(unused).toEqual([]);
});
