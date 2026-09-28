// Runs every vector in runtime/versiongraph/testdata/vectors through the
// package API, loaded from the built dist/ with its default wasm file.
//
// A vector whose expect is an output decodes its input through the contract
// types (src/contract.ts) and must come back byte for byte; the operation
// runs on that decoded input, and its output, decoded the same way, must
// equal expect byte for byte. A vector whose expect is an error sends its
// input as written, since many are malformed on purpose, and the thrown
// VersionGraphError must rebuild expect. The decoders below have one field
// per member of each type, and the compiler requires them to cover it
// exactly, so a member the types miss, add or misname fails here.
import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import {
  init,
  VersionGraphError,
  type Change,
  type ComposeInput,
  type ComposeOutput,
  type ContentHashOutput,
  type Conflict,
  type Descriptor,
  type DiffInput,
  type DiffOutput,
  type EntityOutcome,
  type ErrorDocument,
  type Finding,
  type KindDescriptor,
  type MergeInput,
  type MergeOutput,
  type OperationName,
  type ParentEdge,
  type Resolution,
  type TakeResolution,
  type Tree,
  type TreeInput,
  type ValidateOutput,
  type ValueResolution,
  type VersionGraph,
} from "../dist/index.js";

declare global {
  interface JSON {
    rawJSON(text: string): unknown;
    isRawJSON(value: unknown): boolean;
  }
}

// Parse JSON keeping every number as written, so an integer wider than a
// double reaches the core, and is compared, exactly.
function parseExact(text: string): unknown {
  return JSON.parse(text, (_key, value, context?: { source?: string }) =>
    typeof value === "number" && context?.source !== undefined ? JSON.rawJSON(context.source) : value,
  );
}

// JSON with object keys sorted, so the comparison is about members and
// values, not the order a decoder wrote them in.
function canonical(value: unknown): string {
  return JSON.stringify(sortKeys(value));
}

function sortKeys(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(sortKeys);
  }
  if (value !== null && typeof value === "object" && !JSON.isRawJSON(value)) {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, sortKeys((value as Record<string, unknown>)[key])]),
    );
  }
  return value;
}

// One decoder per member of T, required for every member (optional ones
// included) and refused for any other name. A member absent from the value
// stays absent; null passes through.
type Fields<T> = { [K in keyof T]-?: (value: NonNullable<T[K]>) => unknown };

function decoder<T extends object>(fields: Fields<T>): (value: T) => T {
  return (value) => {
    const out: Record<string, unknown> = {};
    for (const name of Object.keys(fields) as (keyof T & string)[]) {
      if (name in value) {
        const member = value[name];
        out[name] = member === null ? null : fields[name](member as NonNullable<T[typeof name]>);
      }
    }
    return out as T;
  };
}

const same = <V>(value: V): V => value;
const list =
  <V>(item: (value: V) => V) =>
  (values: V[]): V[] =>
    values.map(item);
// Rows are opaque to the contract: a JSON object per row, keyed by column.
const tree = (value: Tree): Tree =>
  Object.fromEntries(Object.entries(value).map(([kind, rows]) => [kind, rows.map((row) => ({ ...row }))]));
const row = (value: Record<string, unknown>) => ({ ...value });

const parentEdge = decoder<ParentEdge>({ key: same, kind: same });
const kindDescriptor = decoder<KindDescriptor>({
  kind: same,
  key: same,
  id: same,
  ref: same,
  tombstone: same,
  version: same,
  author: same,
  parent: parentEdge,
  order: same,
  singleton: same,
  units: same,
  excluded: same,
});
const descriptor = decoder<Descriptor>({ graph: same, kinds: list(kindDescriptor) });
const finding = decoder<Finding>({ code: same, kind: same, entityKey: same, message: same });

const takeResolution = decoder<TakeResolution>({ kind: same, entityKey: same, path: same, take: same });
const valueResolution = decoder<ValueResolution>({ kind: same, entityKey: same, path: same, value: same });
const resolution = (value: Resolution): Resolution =>
  "take" in value ? takeResolution(value) : valueResolution(value);

const composeInput = decoder<ComposeInput>({ descriptor, base: tree, overlay: tree });
const composeOutput = decoder<ComposeOutput>({ tree, findings: list(finding) });

const mergeInput = decoder<MergeInput>({
  descriptor,
  base: tree,
  ours: tree,
  theirs: tree,
  resolutions: list(resolution),
});
const conflict = decoder<Conflict>({
  kind: same,
  entityKey: same,
  path: same,
  base: same,
  ours: same,
  theirs: same,
  oursAuthor: same,
  theirsAuthor: same,
});
const entityOutcome = decoder<EntityOutcome>({ kind: same, entityKey: same, side: same, deleted: same });
const mergeOutput = decoder<MergeOutput>({
  merged: tree,
  conflicts: list(conflict),
  entities: list(entityOutcome),
});

const diffInput = decoder<DiffInput>({ descriptor, from: tree, to: tree });
const change = decoder<Change>({ kind: same, entityKey: same, operation: same, row });
const diffOutput = decoder<DiffOutput>({ changes: list(change) });

const treeInput = decoder<TreeInput>({ descriptor, tree });
const contentHashOutput = decoder<ContentHashOutput>({ contentHash: same });
const validateOutput = decoder<ValidateOutput>({ findings: list(finding) });

function errorDocument(error: VersionGraphError): ErrorDocument {
  return { error: { code: error.code, message: error.message } };
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
      let thrown: unknown;
      try {
        op.run(graph, vector.input);
      } catch (error) {
        thrown = error;
      }
      expect(thrown).toBeInstanceOf(VersionGraphError);
      expect(canonical(errorDocument(thrown as VersionGraphError))).toBe(canonical(vector.expect));
      return;
    }
    const input = op.decodeInput(vector.input);
    expect(canonical(input)).toBe(canonical(vector.input));
    expect(canonical(op.run(graph, input))).toBe(canonical(vector.expect));
  });
}
