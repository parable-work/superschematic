// Approvals' class (D103): the build derives approvals.behavior.json from
// it (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { computed, declaresBehavior, subTable, type Integer, type JsonSchema, type Names } from "@superschematic/db";

/** A state's name, as a config writes it. */
type StateName = JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]*$"; maxLength: 64 }>;

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

/** A kind of caller a requirement counts. */
type CallerKind = JsonSchema<"person" | "acting-for-person" | "agent", { type: "string" }>;

interface ApprovalsConfig {
  /** The gates, by the state of the type's Workflow each gates. */
  gates: JsonSchema<Record<StateName, ApprovalsGate>, { minProperties: 1 }>;
  /**
   * Whether a change of the instance's own fields, an update or an
   * operation's, consumes every gate's sign-offs; true when absent.
   */
  resetOnChange?: boolean;
  /**
   * The field of the type whose Workflow the gates are of. Implied when the
   * type has one Workflow; needed when it has several. A Workflow anchored on
   * the type is named Workflow. A table's alone: an engine type refuses it.
   */
  status?: Names<Named, "workflow">;
}

/**
 * One gate: who signs off for its state, how many, and what consumes it. A
 * gate that reads its requirements from rows names none of a requirement's
 * members; any other needs count and permission.
 */
type ApprovalsGate = JsonSchema<ApprovalsGateMembers, { if: { required: ["requirementsFrom"] }; else: { required: ["count", "permission"] } }>;

/** The members of one gate. */
interface ApprovalsGateMembers {
  /**
   * How many distinct signers must sign off before a transition into the
   * state. Needed unless the gate reads its requirements from rows.
   */
  count?: JsonSchema<Integer, { minimum: 1; maximum: 100 }>;
  /**
   * The permission a caller needs to sign off. Needed unless the gate reads
   * its requirements from rows.
   */
  permission?: Named;
  /**
   * The states of the type's Workflow a transition from is gated; every
   * state when absent.
   */
  from?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * Whether the principal who created the instance is refused a sign-off;
   * true when absent.
   */
  excludeCreator?: boolean;
  /**
   * The own fields whose change, by an update, consumes the gate's
   * sign-offs; [] for none. Absent, the gate follows resetOnChange. A
   * table's alone: an engine type refuses it.
   */
  resetOn?: JsonSchema<Named[], { maxItems: 64; uniqueItems: true }>;
  /**
   * Whether a sign-off names the row's version its signer read: signOff
   * then needs version, and one that is not the row's version is vetoed
   * stale. A table's alone: an engine type refuses it.
   */
  pinned?: boolean;
  /**
   * Own fields a sign-off is pinned to: signOff then names in read the value
   * of each as its signer read it, and one that is not the field's value is
   * vetoed stale. Unlike pinned, a write to another field does not make a
   * signer stale. A table's alone: an engine type refuses it.
   */
  pinOn?: JsonSchema<Named[], { minItems: 1; maxItems: 16; uniqueItems: true }>;
  /**
   * Which callers count, and as whom: person, a person calling as
   * themselves; acting-for-person, an agent run acting for a person, counted
   * as that person; agent, an agent run, counted as its Agent. Every caller
   * counts, as its subject, when absent. A table's alone: an engine type
   * refuses it.
   */
  callers?: JsonSchema<CallerKind[], { minItems: 1; uniqueItems: true }>;
  /**
   * What each sign-off shows, as the host reports the caller's assurance:
   * none; recent-sign-in, a sign-in at most proofWithinMs before it;
   * user-verification, a passkey with user verification on the sign-off
   * itself. none when absent. A table's alone: an engine type refuses it.
   */
  proof?: JsonSchema<"none" | "recent-sign-in" | "user-verification", { type: "string" }>;
  /**
   * For recent-sign-in, the most milliseconds a sign-in may come before the
   * sign-off. A table's alone: an engine type refuses it.
   */
  proofWithinMs?: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * Whether the person the creator acted for, the subject the type's own
   * field createdFor holds, is refused a sign-off too; false when absent. A
   * table's alone: an engine type refuses it.
   */
  excludeCreatorPerson?: boolean;
  /**
   * Own fields that list subjects, persons and Agents who may not sign: a
   * caller is excluded when its subject, the person it acts for or its
   * run's Agent is listed. A table's alone: an engine type refuses it.
   */
  excludeSubjectsIn?: JsonSchema<Named[], { maxItems: 64; uniqueItems: true }>;
  /**
   * Whether each transition the gate guards reads each signer's permission
   * again, so a signer who has lost it stops counting; false when absent,
   * which reads it at the sign-off. A table's alone: an engine type refuses
   * it.
   */
  recheckPermission?: boolean;
  /**
   * Whether a sign-off that counts as its run's Agent (callers lists agent)
   * counts through the permissions its Agent's agent role grants now, read
   * at each transition the gate guards (#625); false when absent, which
   * counts it for nothing when the requirement rechecks permissions and
   * counts what the run held when it signed otherwise. A table's alone: an
   * engine type refuses it.
   */
  agentRole?: boolean;
  /**
   * The conditions on the instance's own fields under which the requirement
   * applies, all of them; it always applies when absent. A requirement that
   * does not apply asks for nothing, and a condition on a field with no
   * value makes it apply. A table's alone: an engine type refuses it.
   */
  when?: JsonSchema<ApprovalsCondition[], { maxItems: 32 }>;
  /**
   * Reads the gate's requirements from the rows of a type, so each instance
   * writes its own: the rows of schema whose to-one relation link points at
   * the instance, or, with in, at one of the keys the instance's own list
   * field in holds. A row holds a requirement's members as its own fields
   * (count, permission, callers, proof, proofWithinMs, excludeCreator,
   * excludeCreatorPerson, excludeSubjectsIn, recheckPermission, agentRole,
   * when) and may hold name, resetOn and archivedAt. An archived row asks for nothing.
   * A row's resetOn spends a sign-off for that requirement alone: a sign-off
   * records the value of each field the rows reset on, and counts for a
   * requirement only while its fields still hold them. The gate then names
   * none of the requirement's members, and names its own resetOn. A table's
   * alone: an engine type refuses it.
   */
  requirementsFrom?: ApprovalsRequirementsFrom;
}

/** A condition of a requirement's when. */
interface ApprovalsCondition {
  /** An own field of the instance. */
  field: Named;
  /**
   * every-matches: every value of the field matches one of the glob
   * patterns; any-matches: one value does; contains: the field holds one of
   * the values; at-most: the field's number is at most the one value.
   */
  test: JsonSchema<"every-matches" | "any-matches" | "contains" | "at-most", { type: "string" }>;
  /**
   * The glob patterns (* within a path segment, ** across segments, ? one
   * character), the values, or the one number.
   */
  values: JsonSchema<string[], { minItems: 1; maxItems: 256 }>;
  /** Whether the condition holds when its test fails; false when absent. */
  negate?: boolean;
}

/** Where a gate reads its requirements from. */
interface ApprovalsRequirementsFrom {
  /** The type whose rows are the requirements. */
  schema: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
  /** The to-one relation of schema's rows the rows are found by. */
  link: Named;
  /**
   * An own list field of the instance that holds the keys link points at;
   * absent, link points at the instance itself.
   */
  in?: Named;
}

interface SignOffParams {
  /** The gated state to sign off for. */
  state: Named;
  /** Why, for the others. */
  note?: JsonSchema<string, { maxLength: 10000 }>;
  /**
   * The row's version the signer read, the version a write through the core
   * is held to. Needed on a pinned gate; on any gate, one that is not the
   * row's version is vetoed stale before anything else is checked. A table's
   * alone: an engine type refuses it.
   */
  version?: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * The value of each field the gate pins (pinOn) as the signer read it, by
   * field. Needed on a gate that pins fields; one that is not the field's
   * value is vetoed stale before anything else is checked. A table's alone:
   * an engine type refuses it.
   */
  read?: JsonSchema<object, { maxProperties: 16 }>;
}

interface SignOffResult {
  /** The gated state. */
  state: string;
  /** The subject of the caller who signed. */
  subject: string;
  note?: string;
  /** When, in epoch milliseconds. */
  at: Integer;
  /**
   * Every subject whose sign-off for the state is not yet consumed, in the
   * order they signed.
   */
  signedBy: string[];
  /**
   * How many the gate needs: its count, or for a gate that reads its
   * requirements from rows, the count of the first requirement the sign-off
   * counts toward.
   */
  count: Integer;
  /**
   * For a gate that reads its requirements from rows, the name of each
   * requirement the sign-off counts toward now.
   */
  countsToward?: string[];
}

interface WithdrawSignOffParams {
  /** The gated state whose sign-off to withdraw. */
  state: Named;
}

interface WithdrawSignOffResult {
  /** The gated state. */
  state: string;
  /** The subject of the caller who signed. */
  subject: string;
  /** The subject the caller acted for. */
  actingFor?: string;
  /** The Agent whose run the caller was. */
  agent?: string;
  note?: string;
  /** When it was signed, in epoch milliseconds. */
  at: Integer;
}

interface ListSignOffsParams {
  /** Only sign-offs for this state. */
  state?: Named;
  /** At most this many sign-offs; 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListSignOffsResult {
  items: ListedSignOff[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

/** A sign-off as listSignOffs lists it. */
interface ListedSignOff {
  /** The gated state. */
  state: string;
  /** The subject of the caller who signed. */
  subject: string;
  /** The subject the caller acted for. */
  actingFor?: string;
  /** The Agent whose run the caller was. */
  agent?: string;
  note?: string;
  /** When, in epoch milliseconds. */
  at: Integer;
  /**
   * When a change of the own fields, entering the state or a new version
   * without its gate consumed it; absent while it counts.
   */
  consumedAt?: Integer;
}

/** The codes Approvals' refusals carry. */
enum ApprovalsVeto {
  /**
   * A transition into a gated state, from a state its gate allows, while a
   * requirement that applies has fewer sign-offs than its count; details
   * give the state, the count required and the sign-offs it has, and for a
   * gate that reads its requirements from rows each requirement it misses.
   */
  approvals_missing,
  /** signOff by a caller whose sign-off for the state is not yet consumed. */
  already_signed,
  /**
   * signOff by the principal who created the instance, when every
   * requirement whose permission the caller holds excludes the creator.
   */
  creator_cannot_sign,
  /** signOff while the instance is in the gated state. */
  in_state,
  /** signOff or withdrawSignOff for a state the config does not gate. */
  no_gate,
  /**
   * signOff by a caller whose sign-off would count toward none of the gate's
   * requirements whose permission it holds: none counts its kind of caller,
   * it excludes the caller, or its proof falls short; details give each
   * requirement's reason.
   */
  not_counted,
  /**
   * withdrawSignOff by a caller with no sign-off for the state that is not
   * yet consumed.
   */
  not_signed,
  /**
   * signOff naming a version that is not the row's version, or a value of a
   * pinned field that is not the field's value: what the signer read has
   * moved since; details give what it named and what is current. A table's
   * alone: an engine type refuses version and read.
   */
  stale,
}

/**
 * Sign-offs that gate states of the type's Workflow. A transition into a
 * gated state, from a state its gate allows, waits until each of the gate's
 * requirements that applies has as many distinct signers holding its
 * permission as its count; the instance's creator cannot sign unless the
 * requirement allows it. A gate is one requirement written in the config, or
 * reads its requirements from the rows of a type that each instance writes
 * (requirementsFrom). A requirement may also name which callers it counts,
 * the proof each sign-off shows, whom it excludes and when it applies. A
 * change of the
 * instance's own fields consumes every gate's sign-offs, unless the config
 * keeps them, and entering a gated state consumes that gate's, so a return to
 * it signs again. Consumed sign-offs stay listed. Deleting the instance
 * deletes its sign-offs.
 */
@declaresBehavior({ requires: ["Workflow"], vetoes: ApprovalsVeto })
export abstract class Approvals<C extends ApprovalsConfig> {
  /**
   * The sign-offs not yet consumed, by gated state: { <state>: { signedBy }
   * }, each subject that signed, in the order they signed; a gate with none
   * is absent.
   */
  @computed gates: unknown;

  /**
   * Signs off on the instance for a gated state, as the caller. Needs the
   * permission of one of the gate's requirements, and is refused unless it
   * counts toward one of them now: for the instance's creator unless the
   * requirement allows it, for a caller the requirement does not count,
   * excludes or whose proof falls short, while the instance is in the state,
   * and for a caller whose sign-off for the state is not yet consumed.
   * @writes
   * @checks gates.*.permission
   * @returns The sign-off, with the gate's sign-offs after it and the count
   * it needs.
   */
  abstract signOff(params: SignOffParams): SignOffResult;

  /**
   * Withdraws the caller's sign-off for a gated state, one not yet consumed.
   * @writes
   * @returns The sign-off withdrawn.
   */
  abstract withdrawSignOff(params: WithdrawSignOffParams): WithdrawSignOffResult;

  /**
   * Lists the instance's sign-offs, oldest first, one page at a time, the
   * consumed ones included; with state, only those for it.
   */
  abstract listSignOffs(params: ListSignOffsParams): ListSignOffsResult;

  /**
   * One row per sign-off on the row, keyed by its number, the order it was
   * given in: the gated state, the caller who signed (its subject and kind,
   * whom it acted for and its run's Agent), the assurance its host reported,
   * the permissions it held and the values of the fields the gate's
   * requirements reset on, why, when, and when it was consumed, absent while
   * it counts. A withdrawal deletes its row; consumed ones stay for
   * listSignOffs.
   */
  @subTable({ key: "number" })
  static signOffs = class {
    /**
     * The sign-off's number on the row, from 1 in the order they were given,
     * in ten digits zero-padded so the key's order is theirs.
     */
    number: string;
    /** The gated state it signs off for. */
    state: string;
    /** The subject of the caller who signed. */
    subject: string;
    /**
     * The caller's kind (D156): user, party, agent or extension; absent when
     * the host named none.
     */
    kind: string;
    /** The subject the caller acted for (D77); absent when it acted for no one. */
    actingFor: string;
    /** The kind of the subject the caller acted for; absent when the host named none. */
    actingForKind: string;
    /**
     * The Agent whose run the caller was (D158); absent for a caller that was
     * no agent run.
     */
    agent: string;
    /** When the caller's session last signed in, as its host reported it. */
    signedInAt: Temporal.DateTime;
    /** Whether the caller proved user verification on the sign-off itself. */
    userVerified: boolean;
    /**
     * The permissions the caller held when it signed, a list, which a
     * requirement read from rows counts unless it reads them again.
     */
    held: Generic.JSON;
    /**
     * The value each field the gate's requirements reset on held when it
     * signed, by field, null for none.
     */
    values: Generic.JSON;
    /** Why, for the others. */
    note: string;
    /** When it was given, in epoch milliseconds. */
    at: Temporal.DateTime;
    /**
     * When a change of the fields its gate names, an entry into the gated
     * state or a build without its gate consumed it; absent while it counts.
     */
    consumedAt: Temporal.DateTime;
  };
}
