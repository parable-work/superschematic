// Type-level checks of Reactions<T, C> (D103). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import type { Reactions, Relation, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. An
// allTerminal or anyTerminal rule names the linking table and its relation
// field, which points at this table.
export abstract class Project implements Reactions<Project, {
  rules: [
    { when: { allTerminal: { schema: "Task"; link: "project"; outcomes: ["success"] } }; then: { transition: "done" } },
    { when: { anyTerminal: { schema: "Task"; link: "project"; outcomes: ["failure"] } }; then: { transition: "failed" } }
  ];
}> {
  id: string;
  name: string;
  status: Workflow<{ states: ["active", "done", "failed"]; transitions: [{ from: "active"; to: "done" }, { from: "active"; to: "failed" }] }>;
}

// A changes rule names a field of the table, and a then's link one of its
// relations.
export abstract class Task implements Reactions<Task, {
  rules: [
    { when: { enters: "doing" }; then: { link: "project"; transition: "active" } },
    { when: { changes: { field: "priority"; to: "urgent" } }; then: { transition: "doing" } }
  ];
}> {
  id: string;
  priority?: string;
  project: Relation<Project>;
  status: Workflow<{ states: ["todo", "doing"]; transitions: [{ from: "todo"; to: "doing" }] }>;
}

// A config read with typeof from a constant declared as const, which names
// the Workflow it reads by its field.
const shipmentReactions = {
  status: "delivery",
  rules: [{ when: { enters: "lost" }, then: { transition: "lost" } }],
} as const;
export abstract class Shipment implements Reactions<Shipment, typeof shipmentReactions> {
  id: string;
  delivery: Workflow<{ states: ["moving", "lost"]; transitions: [{ from: "moving"; to: "lost" }] }>;
}

// A changes rule's field, a then's link and the status name a field of the
// table.
// @ts-expect-error a changes rule names a field of the table
type NoSuchField = Reactions<Task, { rules: [{ when: { changes: { field: "colour" } }; then: { transition: "doing" } }] }>;
// @ts-expect-error a then's link names a field of the table
type NoSuchLink = Reactions<Task, { rules: [{ when: { enters: "doing" }; then: { link: "parent"; transition: "active" } }] }>;
// @ts-expect-error status names a field of the table
type NoSuchStatus = Reactions<Task, { rules: [{ when: { enters: "doing" }; then: { transition: "doing" } }]; status: "phase" }>;
// @ts-expect-error an anyTerminal rule lists its outcomes
type NoOutcomes = Reactions<Project, { rules: [{ when: { anyTerminal: { schema: "Task"; link: "project" } }; then: { transition: "failed" } }] }>;
// @ts-expect-error a then names its transition
type NoTransition = Reactions<Task, { rules: [{ when: { enters: "doing" }; then: { link: "project" } }] }>;

export type Refused = NoSuchField | NoSuchLink | NoSuchStatus | NoOutcomes | NoTransition;
