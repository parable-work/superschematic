// Type-level checks of Derived<T, C> and Provenance<T, C> (D73, D103's
// step 4). Nothing imports this file: `bun run typecheck`, which `make ts`
// runs, checks it, and fails on an expected error (@ts-expect-error) that
// no longer errors.
import type { Derived, DerivedOn, Provenance, ProvenanceOn } from "./index";

// The carriers impose no members: a table implements them with its own
// fields alone, and their first type argument is the table itself. A
// ticket whose category a model derives from its subject and body, and
// whose writes are recorded, a model's proposed until a reviewer accepts it.
export abstract class Ticket
  implements
    Derived<Ticket, {
      fields: { category: { from: ["subject", "body"]; instruction: "Classify the request."; model: "standard" } };
      permission: "tickets.derive";
      maxAttempts: 2;
    }>,
    Provenance<Ticket, {
      review: { sources: ["Derived"]; below: 0.8; permission: "tickets.review" };
      gates: { assigned: { reviewed: ["category"] } };
    }>
{
  id: string;
  subject: string;
  body: string;
  category: string;
}

const derived: DerivedOn<Ticket> = {
  fields: { category: { from: ["subject"], instruction: "Classify it.", model: "standard", states: ["new"] } },
  permission: "tickets.derive",
};
const recorded: ProvenanceOn<Ticket> = { fields: ["category"], gates: { assigned: { reviewed: "all" } } };

// A derived field and its inputs are the table's own fields.
// @ts-expect-error title is no field of Ticket
const unknownField: DerivedOn<Ticket> = { fields: { title: { from: ["body"], instruction: "i", model: "m" } }, permission: "p" };
// @ts-expect-error notes is no field of Ticket
const unknownInput: DerivedOn<Ticket> = { fields: { category: { from: ["notes"], instruction: "i", model: "m" } }, permission: "p" };
// @ts-expect-error a deriver needs a permission
const noPermission: DerivedOn<Ticket> = { fields: {} };
// @ts-expect-error a recorded field is the table's own
const unrecorded: ProvenanceOn<Ticket> = { fields: ["title"] };
// @ts-expect-error a review names its reviewers' permission
const noReviewer: ProvenanceOn<Ticket> = { review: { sources: ["Derived"] } };
