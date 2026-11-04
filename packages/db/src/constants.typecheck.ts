// Type-level checks of Constants<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Constants, ConstantsOn, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. Its owner
// stays what its create gave it, and urgent and attempts only go up.
export abstract class Task implements Constants<Task, {
  fields: ["owner"];
  oneWay: { urgent: "up"; attempts: "up" };
  permission: "tasks.admin";
}> {
  id: string;
  title: string;
  owner: string;
  urgent: boolean;
  attempts: number;
  closesAt?: Date;
  status: Workflow<{ states: ["planned", "done"]; transitions: [{ from: "planned"; to: "done" }] }>;
}

// A config read with typeof from a constant declared as const, a date-time
// moving down.
const windowConstants = { fields: ["title"], oneWay: { closesAt: "down" } } as const;
export abstract class Window implements Constants<Task, typeof windowConstants> {}
const kept: ConstantsOn<Task> = { fields: ["owner", "title"] };

// Each field is one of the table's, and a field moves up or down.
// @ts-expect-error a field of fields is the table's
type NoSuchField = Constants<Task, { fields: ["assignee"] }>;
// @ts-expect-error a oneWay field is the table's
type NoSuchOneWay = Constants<Task, { fields: ["owner"]; oneWay: { retries: "up" } }>;
// @ts-expect-error a field moves up or down
type Sideways = Constants<Task, { fields: ["owner"]; oneWay: { urgent: "sideways" } }>;
// @ts-expect-error Constants keeps at least its fields
type NoFields = Constants<Task, { oneWay: { urgent: "up" } }>;

// An engine type's @behavior("Constants", ...) takes @superschematic/schema's
// config, which has no oneWay.
// @ts-expect-error oneWay is a table's Constants'
@behavior("Constants", { fields: ["title"], oneWay: { count: "up" } })
export abstract class EngineStep {}
@behavior("Constants", { fields: ["title"], permission: "steps.admin" })
export abstract class EngineNote {}

export const checked: readonly unknown[] = [kept];
export type Refused = NoSuchField | NoSuchOneWay | Sideways | NoFields;
