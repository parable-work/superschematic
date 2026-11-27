// Type-level checks of FieldOf<T>: a behavior's config that reads a value
// of the row names a to-one relation by the field that holds its key, as
// the loader asks ("authority is a relation: name the field that holds its
// key, authorityId"). Nothing imports this file: `bun run typecheck`, which
// `make ts` runs, checks it, and fails on an expected error
// (@ts-expect-error) that no longer errors.
import type { Nullable } from "@superschematic/schema";
import type { ApprovalsGateOn, AudienceOptions, Constants, ConstantsOn, FieldOf, PresenceOn, Relation, StateRules, StateRulesOn, Workflow } from "./index";

export abstract class Authority {
  id: string;
}

// A request keeps the authority its create names, needs one to be filed,
// and stands for the principal it acts for, each named by its key.
export abstract class AccessRequest
  implements
    Constants<AccessRequest, { fields: ["authorityId", "title"] }>,
    StateRules<AccessRequest, { states: { filed: { required: ["authorityId"]; frozen: ["actingForId"] } } }>
{
  id: string;
  title: string;
  authority: Relation<Authority>;
  actingFor: Nullable<Relation<Authority>>;
  status: Workflow<{ states: ["draft", "filed"]; transitions: [{ from: "draft"; to: "filed" }] }>;
}

export const keys: readonly FieldOf<AccessRequest>[] = ["authorityId", "actingForId", "title", "status"];
export const presence: PresenceOn<AccessRequest> = { ttlMs: 60_000, principalField: "actingForId" };
export const oneWay: ConstantsOn<AccessRequest> = { fields: ["title"], oneWay: { authorityId: "up" } };
// @audience's owner takes a relation by its name or by its key.
export const ownedByKey: AudienceOptions<AccessRequest> = { owner: "actingForId" };
export const ownedByName: AudienceOptions<AccessRequest> = { owner: "actingFor" };
// Approvals' gates read values of the row too (D200): each field a gate
// names, its resetOn, pinOn, excludeSubjectsIn, conditions and the list
// its rows' keys are in, names a relation by its key.
export const gate: ApprovalsGateOn<AccessRequest> = {
  count: 1,
  permission: "requests.approve",
  resetOn: ["authorityId"],
  pinOn: ["authorityId"],
  excludeSubjectsIn: ["actingForId"],
  when: [{ field: "authorityId", test: "contains", values: ["a"] }],
};
export const rowsGate: ApprovalsGateOn<AccessRequest> = { resetOn: ["authorityId"], requirementsFrom: { schema: "Rule", link: "request" } };
// @ts-expect-error authority is a relation: name authorityId.
export const pinByName: ApprovalsGateOn<AccessRequest> = { count: 1, permission: "requests.approve", pinOn: ["authority"] };
// @ts-expect-error authority is a relation: name authorityId.
export const rowsResetByName: ApprovalsGateOn<AccessRequest> = { resetOn: ["authority"], requirementsFrom: { schema: "Rule", link: "request" } };
// An untyped config names any field.
export const untyped: ConstantsOn<any> = { fields: ["anything"] };

// A relation by its own name is refused where a config reads a value, as
// the loader refuses it.
// @ts-expect-error authority is a relation: name authorityId.
export const byName: ConstantsOn<AccessRequest> = { fields: ["authority"] };

// A field that is no relation has no key field.
// @ts-expect-error titleId holds no relation's key.
export const notARelation: ConstantsOn<AccessRequest> = { fields: ["titleId"] };

// A key field of no field of the table is refused.
// @ts-expect-error ownerId is no field of AccessRequest.
export const noSuchKey: StateRulesOn<AccessRequest> = { states: { filed: { required: ["ownerId"] } } };
