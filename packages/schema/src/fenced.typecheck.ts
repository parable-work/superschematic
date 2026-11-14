// Type-level checks of @fenced (D119). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { fenced } from "./decorators";
import type { FenceFieldOf, FencedConfig } from "./decorators";
import type { Default, Nullable, Validate } from "./wrappers";

// An integer scalar is a branded number, as superscalar declares one.
type Int64 = number & { readonly __brand: "Generic.Int64" };

abstract class Owner {
  id: string;
}

abstract class Job {
  id: string;
  label: string;
  priority: Default<Int64, 0>;
  owner: Owner;
  version: Default<Int64, 0>;
  claimedBy: Nullable<string>;
  progress: Int64;
  executionVersion: Validate<Int64, { min: 0 }>;
  attempts: number;
  estimate: Nullable<Int64>;
  retries?: Int64;
}

// A fence is a number that is never null: plain, branded, and through
// each wrapper.
const plain: FenceFieldOf<Job> = "attempts";
const branded: FenceFieldOf<Job> = "progress";
const defaulted: FenceFieldOf<Job> = "version";
const validated: FenceFieldOf<Job> = "executionVersion";
// @ts-expect-error a text field counts nothing
const text: FenceFieldOf<Job> = "label";
// @ts-expect-error a nullable number has no next value when it is null
const nullable: FenceFieldOf<Job> = "estimate";
// @ts-expect-error an optional number may be absent
const optional: FenceFieldOf<Job> = "retries";
// @ts-expect-error a relation holds a key
const relation: FenceFieldOf<Job> = "owner";

// One fence with no covers counts every update of the row.
@fenced<Job>({ field: "version" })
export abstract class Whole extends Job {}

// Two fences, each over its own fields, a relation and a nullable field
// among them.
@fenced<Job>({ field: "version", covers: ["label", "priority", "owner"] })
@fenced<Job>({ field: "executionVersion", covers: ["claimedBy", "progress"] })
export abstract class Split extends Job {}

// The options are a type of their own.
const planned: FencedConfig<Job> = { field: "version", covers: ["label"] };

export function refused(): void {
  // @ts-expect-error a text field is no fence
  fenced<Job>({ field: "label" });
  // @ts-expect-error a nullable number is no fence
  fenced<Job>({ field: "estimate" });
  // @ts-expect-error an optional number is no fence
  fenced<Job>({ field: "retries" });
  // @ts-expect-error a field the class does not declare
  fenced<Job>({ field: "revision" });
  // @ts-expect-error a fence names its field
  fenced<Job>({ covers: ["label"] });
  fenced<Job>({
    field: "version",
    // @ts-expect-error a covered field the class does not declare
    covers: ["label", "title"],
  });
  fenced<Job>({
    field: "version",
    // @ts-expect-error covers is a list of fields, not one field
    covers: "label",
  });
  fenced<Job>({
    field: "version",
    // @ts-expect-error an empty covers counts no write: leave it out
    covers: [],
  });
  fenced<Job>({
    field: "version",
    // @ts-expect-error a fence has no other option
    step: 2,
  });
}
