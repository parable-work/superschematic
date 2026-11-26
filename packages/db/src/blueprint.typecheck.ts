// Type-level checks of Blueprint<T, C> (D149). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import type { Blueprint, BlueprintOn, BlueprintStepOn, Constants, Dependencies, HasMany, Relation } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and the first type argument is the table itself. A batch
// stamps a job per step, each after the one before it.
export abstract class Batch implements
  Blueprint<Batch, {
    schema: "Job";
    parentLink: "batch";
    keyField: "step";
    steps: {
      fetch: { data: { title: "Fetch the sources" } };
      index: { after: ["fetch"]; when: { field: "topic"; equals: "search" } };
      report: { after: ["index"]; when: { field: "flags"; includes: "report" } };
    };
    copyFields: ["topic"];
  }>
{
  id: string;
  topic: string;
  flags: string[];
  jobs: HasMany<Job>;
}

export abstract class Job implements
  Constants<Job, { fields: ["step", "topic"] }>,
  Dependencies<Job, { gatedStates: ["running"] }>
{
  id: string;
  step: string;
  topic: string;
  batch: Relation<Batch>;
}

const step: BlueprintStepOn<Batch> = { after: ["fetch"], data: { title: "Index" } };
const config: BlueprintOn<Batch> = { schema: "Job", parentLink: "batch", keyField: "step", steps: { fetch: {}, index: step } };

// A config names its child table, its relation and its key field, and
// its steps; a when names a field of the table and one of equals and
// includes.
// @ts-expect-error a config names its steps
const noSteps: BlueprintOn<Batch> = { schema: "Job", parentLink: "batch", keyField: "step" };
// @ts-expect-error a when names a field of the table
const otherField: BlueprintStepOn<Batch> = { when: { field: "colour", equals: "red" } };
// @ts-expect-error copyFields names fields of the table
const copied: BlueprintOn<Batch> = { schema: "Job", parentLink: "batch", keyField: "step", steps: { a: {} }, copyFields: ["colour"] };

export const checked: readonly unknown[] = [step, config, noSteps, otherField, copied];
