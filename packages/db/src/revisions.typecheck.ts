// Type-level checks of Revisions<T, C> (D16, D103's step 4). Nothing
// imports this file: `bun run typecheck`, which `make ts` runs, checks it,
// and fails on an expected error (@ts-expect-error) that no longer errors.
import type { Revisions, RevisionsOn } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. A
// policy's history, whose changes a caller holding policies.review
// approves.
export abstract class Policy implements Revisions<Policy, { review: { permission: "policies.review" } }> {
  id: string;
  title: string;
  body: string;
}

// The history alone: no review step.
export abstract class Note implements Revisions<Note, {}> {
  id: string;
  text: string;
}

const reviewed: RevisionsOn = { review: { permission: "policies.review" } };
const plain: RevisionsOn = {};

// review names its permission, and a config names no member but review.
// @ts-expect-error review names the permission approve and reject need
type NoPermission = Revisions<Policy, { review: {} }>;
// @ts-expect-error a config names no member but review
const unknownMember: RevisionsOn = { review: { permission: "p" }, keep: 10 };
