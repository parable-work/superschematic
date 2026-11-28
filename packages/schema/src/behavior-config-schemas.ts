// @generated; do not edit
// The config schema of each behavior an engine runs, as TypeScript: the
// configSchema of its declaration (internal/registry/behaviors in the
// superschematic repository), every member it accepts, a table's alone
// among them. behavior.ts holds the types an engine type's author writes
// against, and behavior-config.typecheck.ts holds them to these.
// Regenerate with:
//   go run ./internal/tools/behaviorconfigtypes

/** A JSON object whose members the schema does not name. */
export type JSONObject = { readonly [key: string]: unknown };

/** Approvals' configSchema. */
export type ApprovalsConfigSchema = {
  /** The gates, by the state of the type's Workflow each gates. */
  readonly gates: { readonly [key: string]: {
    /** Whether a sign-off that counts as its run's Agent (callers lists agent) counts through the permissions its Agent's agent role grants now, read at each transition the gate guards (#625); false when absent, which counts it for nothing when the requirement rechecks permissions and counts what the run held when it signed otherwise. A table's alone: an engine type refuses it. */
    readonly agentRole?: boolean;
    /** Which callers count, and as whom: person, a person calling as themselves; acting-for-person, an agent run acting for a person, counted as that person; agent, an agent run, counted as its Agent. Every caller counts, as its subject, when absent. A table's alone: an engine type refuses it. */
    readonly callers?: ReadonlyArray<'person' | 'acting-for-person' | 'agent'>;
    /** How many distinct signers must sign off before a transition into the state. Needed unless the gate reads its requirements from rows. */
    readonly count?: number;
    /** Whether the principal who created the instance is refused a sign-off; true when absent. */
    readonly excludeCreator?: boolean;
    /** Whether the person the creator acted for, the subject the type's own field createdFor holds, is refused a sign-off too; false when absent. A table's alone: an engine type refuses it. */
    readonly excludeCreatorPerson?: boolean;
    /** Own fields that list subjects, persons and Agents who may not sign: a caller is excluded when its subject, the person it acts for or its run's Agent is listed. A table's alone: an engine type refuses it. */
    readonly excludeSubjectsIn?: readonly string[];
    /** The states of the type's Workflow a transition from is gated; every state when absent. */
    readonly from?: readonly string[];
    /** The permission a caller needs to sign off. Needed unless the gate reads its requirements from rows. */
    readonly permission?: string;
    /** Own fields a sign-off is pinned to: signOff then names in read the value of each as its signer read it, and one that is not the field's value is vetoed stale. Unlike pinned, a write to another field does not make a signer stale. A table's alone: an engine type refuses it. */
    readonly pinOn?: readonly string[];
    /** Whether a sign-off names the row's version its signer read: signOff then needs version, and one that is not the row's version is vetoed stale. A table's alone: an engine type refuses it. */
    readonly pinned?: boolean;
    /** What each sign-off shows, as the host reports the caller's assurance: none; recent-sign-in, a sign-in at most proofWithinMs before it; user-verification, a passkey with user verification on the sign-off itself. none when absent. A table's alone: an engine type refuses it. */
    readonly proof?: 'none' | 'recent-sign-in' | 'user-verification';
    /** For recent-sign-in, the most milliseconds a sign-in may come before the sign-off. A table's alone: an engine type refuses it. */
    readonly proofWithinMs?: number;
    /** Whether each transition the gate guards reads each signer's permission again, so a signer who has lost it stops counting; false when absent, which reads it at the sign-off. A table's alone: an engine type refuses it. */
    readonly recheckPermission?: boolean;
    /** Reads the gate's requirements from the rows of a type, so each instance writes its own: the rows of schema whose to-one relation link points at the instance, or, with in, at one of the keys the instance's own list field in holds. A row holds a requirement's members as its own fields (count, permission, callers, proof, proofWithinMs, excludeCreator, excludeCreatorPerson, excludeSubjectsIn, recheckPermission, agentRole, when) and may hold name, resetOn and archivedAt. An archived row asks for nothing. A row's resetOn spends a sign-off for that requirement alone: a sign-off records the value of each field the rows reset on, and counts for a requirement only while its fields still hold them. The gate then names none of the requirement's members, and names its own resetOn. A table's alone: an engine type refuses it. */
    readonly requirementsFrom?: {
      /** An own list field of the instance that holds the keys link points at; absent, link points at the instance itself. */
      readonly in?: string;
      /** The to-one relation of schema's rows the rows are found by. */
      readonly link: string;
      /** The type whose rows are the requirements. */
      readonly schema: string;
    };
    /** The own fields whose change, by an update, consumes the gate's sign-offs; [] for none. Absent, the gate follows resetOnChange. A table's alone: an engine type refuses it. */
    readonly resetOn?: readonly string[];
    /** The conditions on the instance's own fields under which the requirement applies, all of them; it always applies when absent. A requirement that does not apply asks for nothing, and a condition on a field with no value makes it apply. A table's alone: an engine type refuses it. */
    readonly when?: ReadonlyArray<{
      /** An own field of the instance. */
      readonly field: string;
      /** Whether the condition holds when its test fails; false when absent. */
      readonly negate?: boolean;
      /** every-matches: every value of the field matches one of the glob patterns; any-matches: one value does; contains: the field holds one of the values; at-most: the field's number is at most the one value. */
      readonly test: 'every-matches' | 'any-matches' | 'contains' | 'at-most';
      /** The glob patterns (* within a path segment, ** across segments, ? one character), the values, or the one number. */
      readonly values: readonly string[];
    }>;
  } };
  /** Whether a change of the instance's own fields, an update or an operation's, consumes every gate's sign-offs; true when absent. */
  readonly resetOnChange?: boolean;
  /** The field of the type whose Workflow the gates are of. Implied when the type has one Workflow; needed when it has several. A Workflow anchored on the type is named Workflow. A table's alone: an engine type refuses it. */
  readonly status?: string;
};

/** Assignment's configSchema. */
export type AssignmentConfigSchema = {
  /** The permission a principal needs to assign the instance to another principal, reassign it, or unassign another principal. Absent, only the first two self-service moves are possible. */
  readonly permission?: string;
};

/** Blueprint's configSchema. */
export type BlueprintConfigSchema = {
  /** Fields of this type copied to every child, a field of the child's type of the same name. */
  readonly copyFields?: readonly string[];
  /** Links of this type copied to every child, a link of the child's Links config of the same name to the same schema, keeping the revision or the release this instance's link records where the child's pins the same: the links the instance holds when it is stamped, which for inline steps are the ones its create gives. */
  readonly copyLinks?: readonly string[];
  /** Where the steps are kept as an instance: a link of this type's Links config pinned to a revision, and the field of the linked instance that holds a map of steps of the same form as steps. The steps are read from the revision the link pins and stamped when the link is first set, by the create that gives it or a later link. */
  readonly from?: {
    /** The field of the linked instance's type that holds the steps. */
    readonly field: string;
    /** A link of this type's Links config pinned to a revision. */
    readonly link: string;
  };
  /** A string field of the child schema's type that each child gets its step's key in. */
  readonly keyField: string;
  /** The link of the child schema's Links config that points at this schema; each child is linked to its parent through it. */
  readonly parentLink: string;
  /** The child schema. It composes Links with parentLink, Constants over keyField and every copied field, Dependencies when a step comes after another, and not Blueprint. */
  readonly schema: string;
  /** The steps, by key, stamped when the instance is created. */
  readonly steps?: { readonly [key: string]: {
    /** The keys of the steps whose children block this step's child. */
    readonly after?: readonly string[];
    /** Fields of the child, beside its key and the copied fields. */
    readonly data?: JSONObject;
    /** Stamps the step only when the instance's field equals a value, or is a list that includes one; absent, always. */
    readonly when?: {
      /** The value the field must equal, as JSON. */
      readonly equals?: unknown;
      /** A field of this schema's type. */
      readonly field: string;
      /** A value the field, a list, must include, as JSON. */
      readonly includes?: unknown;
    } & ({ readonly equals: unknown } | { readonly includes: unknown });
  } };
} & ({
  readonly steps: { readonly [key: string]: {
    /** The keys of the steps whose children block this step's child. */
    readonly after?: readonly string[];
    /** Fields of the child, beside its key and the copied fields. */
    readonly data?: JSONObject;
    /** Stamps the step only when the instance's field equals a value, or is a list that includes one; absent, always. */
    readonly when?: {
      /** The value the field must equal, as JSON. */
      readonly equals?: unknown;
      /** A field of this schema's type. */
      readonly field: string;
      /** A value the field, a list, must include, as JSON. */
      readonly includes?: unknown;
    } & ({ readonly equals: unknown } | { readonly includes: unknown });
  } };
} | {
  readonly from: {
    /** The field of the linked instance's type that holds the steps. */
    readonly field: string;
    /** A link of this type's Links config pinned to a revision. */
    readonly link: string;
  };
});

/** Budget's configSchema. */
export type BudgetConfigSchema = {
  /** Moves the instance's Workflow status when usage leaves it over the limit of a meter, while the status is one of from, in the transaction of that usage: an instance's own usage, or one an instance inside it passes up, so a scope's config moves the scope. A move a guard vetoes or the caller may not make leaves the status, and the usage counts all the same. Needs Workflow on the type. */
  readonly escalate?: {
    /** The states the status moves from; in any other state it stays. */
    readonly from: readonly string[];
    /** The state to move to, through Workflow's transition. */
    readonly transition: string;
  };
  /** The permission a principal needs to change a meter's limit, with setLimit or through limitField. Absent, no limit changes once the instance exists. */
  readonly limitPermission?: string;
  /** The meters, by name: camelCase, at most 64 characters. */
  readonly meters: { readonly [key: string]: {
    /** The most the instance may have used and reserved together; with limitField, the limit while the field holds no value of at least 0. No limit of its own when neither gives one. */
    readonly limit?: number;
    /** An integer field of the type that holds the instance's limit; while it holds no value of at least 0, limit is the limit, and without limit there is none. Only a principal with limitPermission changes it once the instance exists. */
    readonly limitField?: string;
    /** The amount reserve takes for the meter when it is given no meter: what a claim reserves. With reserveField, the amount when the field holds no positive integer. */
    readonly reserve?: number;
    /** An integer field of the type whose positive value is the amount reserve takes for the meter when it is given no meter, in place of reserve. */
    readonly reserveField?: string;
    /** When the used amount starts again at 0, in UTC; reservations are kept. daily: at each UTC day. hourly, weekly (each Monday), monthly (each first of the month) and { everyMs } (at each multiple of everyMs from the epoch, a whole number of minutes) are a table's alone: an engine type refuses them. */
    readonly reset?: 'hourly' | 'daily' | 'weekly' | 'monthly' | {
      /** The period's length, in milliseconds. */
      readonly everyMs: number;
    };
    /** A link of the type's Links config whose target is the enclosing scope: its schema composes Budget with the same meter, and every reservation and usage of the meter here is taken there too, and on up its own scope. */
    readonly scope?: string;
    /** rolling: the meter counts the usage of its last period rather than of the current one, from buckets of a sixtieth of the period each (a month is thirty days), so no boundary lets twice the limit through. Needs reset. A table's alone: an engine type refuses it. */
    readonly window?: 'rolling';
  } };
  /** The permission a caller needs to record the usage a provider reports with observe; absent, observe is refused (not_configured). A table's alone: an engine type refuses it. */
  readonly observePermission?: string;
  /** A directive sent to the holder of the instance's active lease, through Lease's direct, when usage takes the instance or an enclosing scope over its limit: once per meter per lease. Needs Lease on the type. */
  readonly onExceeded?: {
    /** The directive's name. Its data is { meter, used, limit, scope }, scope being the instance over its limit as { schema, id }. */
    readonly direct: string;
  };
  /** The field of the type whose Workflow escalate moves. Implied when the type has one Workflow; needed when it has several. A Workflow anchored on the type is named Workflow. A table's alone: an engine type refuses it. */
  readonly status?: string;
};

/** Calendar's configSchema. */
export type CalendarConfigSchema = {
  /** Time kept free around each hold, which counts against capacity as the hold does. */
  readonly bufferMs?: {
    /** Milliseconds after a hold that no other may overlap; 0 when absent. */
    readonly after?: number;
    /** Milliseconds before a hold that no other may overlap; 0 when absent. */
    readonly before?: number;
  };
  /** How many holds may overlap at any instant: a count, or an own integer field of the type ({ field }) whose value of at least 1 is the instance's; 1 when absent. */
  readonly capacity?: number | {
    /** An own integer field of the type, by JSON key. */
    readonly field: string;
  };
  /** How far ahead a hold may start, by the engine's clock; no limit when absent. */
  readonly horizonMs?: number;
  /** The weekly opening hours an instance has until it sets its own: each day, mon to sun, a list of [open, close] wall-time ranges, each closing after it opens, none overlapping. A day absent or empty is closed. */
  readonly hours: {
    readonly fri?: ReadonlyArray<readonly unknown[]>;
    readonly mon?: ReadonlyArray<readonly unknown[]>;
    readonly sat?: ReadonlyArray<readonly unknown[]>;
    readonly sun?: ReadonlyArray<readonly unknown[]>;
    readonly thu?: ReadonlyArray<readonly unknown[]>;
    readonly tue?: ReadonlyArray<readonly unknown[]>;
    readonly wed?: ReadonlyArray<readonly unknown[]>;
  };
  /** How long ahead of its start a hold must be made, by the engine's clock; 0 when absent. */
  readonly leadMs?: number;
  /** The longest hold, in milliseconds; a day when absent. */
  readonly maxMs?: number;
  /** The shortest hold, in milliseconds; slotMs when absent. */
  readonly minMs?: number;
  /** The permission a caller needs to set an instance's hours and to add or remove its closures; a closure over held time needs its .override. Nobody may when absent. */
  readonly permission?: string;
  /** The start grid, in milliseconds: a hold starts a whole number of slots after an opening time. Any whole minute when absent. */
  readonly slotMs?: number;
  /** The IANA time zone the hours are wall times in, such as Europe/London; an instance may set its own. */
  readonly timeZone: string;
};

/** Comments' configSchema. */
export type CommentsConfigSchema = {
  /** The permission that lets a caller move the anchor of a code block on another's message with moveAnchor, as a git extension does after a push. Absent, only the message's author and a caller holding moderatePermission may. */
  readonly anchorPermission?: string;
  /** What an attachment block may describe. The engine keeps a file's description, never its bytes: the deployment stores and serves the file, through an upload route of its own. */
  readonly attachments?: {
    /** The largest file an attachment block may describe, in bytes; no limit when absent. */
    readonly maxBytes?: number;
    /** The media types an attachment may have, each type/subtype or type/* in lower case; any when absent. */
    readonly mediaTypes?: readonly string[];
    /** The URI schemes an attachment's uri may use, in lower case: https and bucket, say, for files a deployment serves from a bucket by a scheme of its own. https alone when absent. Schemes that run code or carry the bytes themselves are refused. */
    readonly schemes?: readonly string[];
  };
  /** The block kinds a post, an edit or a finish may give: ref, action, code and attachment; every kind when absent, none when empty. */
  readonly blocks?: ReadonlyArray<'ref' | 'action' | 'code' | 'attachment'>;
  /** How long a composing message's author may append nothing before the engine's runner interrupts it, in milliseconds; 60000 when absent. The runner looks for such messages every quarter of it, or every 15 seconds when that is sooner. */
  readonly composeTimeoutMs?: number;
  /** The permission a notifier or an agent dispatcher needs to claim and settle the deliveries of this type's messages, beside the policy's Comments.claimDeliveries and Comments.settleDeliveries. Absent, nobody claims them, and they serve only the inbox. */
  readonly deliverPermission?: string;
  /** How long a delivery is kept from when it was made, in milliseconds, whatever its state but a live claim; the inbox's mentions reach back as far. 2592000000, 30 days, when absent. */
  readonly deliveryRetentionMs?: number;
  /** How long after a message's postedAt its author may edit it, in milliseconds; no limit when absent. A caller holding moderatePermission is not held to it. */
  readonly editWindowMs?: number;
  /** How many times a delivery is claimed before a failure, or a claim that lapses, drops it; 5 when absent. */
  readonly maxAttempts?: number;
  /** The longest body a post, an edit or a finish may give, and the longest draft appendDraft builds, in code points; 40000 when absent. */
  readonly maxBodyLength?: number;
  /** The most composing messages one subject may have on an instance at once; begin refuses another. 5 when absent. */
  readonly maxDrafts?: number;
  /** The permission that lets a caller edit, delete and interrupt another's message, edit past editWindowMs, move the anchor of a code block on another's message, and purge. Absent, only a message's author edits, deletes or interrupts it before composeTimeoutMs passes, and nobody purges. */
  readonly moderatePermission?: string;
  /** The permission a caller needs to pin and unpin. Absent, any caller the policy lets write may. */
  readonly pinPermission?: string;
  /** The permission that lets a caller resolve and reopen another's thread. Absent, only a thread's author does. */
  readonly resolvePermission?: string;
  /** How long after its first failure a delivery is due again, in milliseconds, doubled after each later failure up to an hour; 30000 when absent. */
  readonly retryBackoffMs?: number;
};

/** Constants' configSchema. */
export type ConstantsConfigSchema = {
  /** The type's own top-level fields, by JSON key, that keep the value their create gives them. A table's Constants may leave it out beside oneWay, its one rule a field that moves one way; a config keeps or moves one field at least. */
  readonly fields?: readonly string[];
  /** Own fields, by JSON key, that move one way after the create: up, a boolean that only turns true or a number or a date-time that only grows; down, one that only turns false or shrinks. No value is below every value, so clearing a field is a move down and giving a field with none a value a move up. A move the other way is vetoed (one_way) unless the caller holds the config's permission; a create gives any value. A field is in fields or here, not both. A table's alone: an engine type refuses it. */
  readonly oneWay?: { readonly [key: string]: 'up' | 'down' };
  /** The permission a caller needs to change them after the create; nobody may when absent. */
  readonly permission?: string;
};

/** Dependencies' configSchema. */
export type DependenciesConfigSchema = {
  /** The states of the type's Workflow that a transition into waits for every blocker to finish; every terminal state when absent. */
  readonly gatedStates?: readonly string[];
  /** The outcomes of a blocker's terminal state that finish it; success when absent. A blocker in a terminal state whose outcome the list lacks blocks until it is removed. */
  readonly satisfiedBy?: ReadonlyArray<'success' | 'failure' | 'neutral'>;
  /** The schemas whose instances may block this type's, each composing Workflow; the type's own schema when absent. */
  readonly schemas?: readonly string[];
};

/** Derived's configSchema. */
export type DerivedConfigSchema = {
  /** The derived fields, by own top-level field. */
  readonly fields: { readonly [key: string]: {
    /** The own top-level fields the value is derived from, never the field itself or another derived field. */
    readonly from: readonly string[];
    /** What the model is asked, given the inputs. */
    readonly instruction: string;
    /** A label the deployment maps to a provider's model, which the engine only compares. */
    readonly model: string;
    /** The states of the type's Workflow in which the field is derived; every state when absent. */
    readonly states?: readonly string[];
  } };
  /** How many failed attempts a field takes before it waits for its inputs to move; 3 when absent. */
  readonly maxAttempts?: number;
  /** The permission a deriver needs to pull, settle and apply values. */
  readonly permission: string;
};

/** Holds' configSchema. */
export type HoldsConfigSchema = {
  /** The states whose entry commits what is held; a move into one while an item is not held is refused. */
  readonly commitOn?: readonly string[];
  /** The holds, by name: camelCase, at most 64 characters. */
  readonly holds: { readonly [key: string]: {
    /** An own list field of the type whose elements each name a supply of schema: one item per element. */
    readonly each?: {
      /** An own list field of the type, by JSON key: one item per element. */
      readonly field: string;
      /** The field of each element, by JSON key, that holds the supply's id. */
      readonly target: string;
    };
    /** Timed supply: the time each item asks, from start to end, or to durationMs after start. start and end are date-time fields of the list's element for an each hold, own fields of the type for a link hold. */
    readonly interval?: {
      /** How long it lasts, in milliseconds. */
      readonly durationMs?: number;
      /** The date-time field it ends at, by JSON key. */
      readonly end?: string;
      /** The date-time field the interval starts at, by JSON key. */
      readonly start: string;
    } & ({ readonly end: string } | { readonly durationMs: number });
    /** A single-valued link of the type's Links config whose target is the supply: one item. */
    readonly link?: string;
    /** Counted supply: the units each item asks, a count or a field ({ field }), an integer field of the list's element for an each hold, an own field of the type for a link hold. */
    readonly quantity?: number | {
      /** The field, by JSON key. */
      readonly field: string;
    };
    /** The supply's schema, for an each hold: it composes a supply, such as Stock or Calendar. */
    readonly schema?: string;
  } };
  /** The most items the claim's holds may ask together; 100 when absent. */
  readonly maxItems?: number;
  /** The states whose entry releases what is held, such as a hold's expiry or a cancellation. */
  readonly releaseOn?: readonly string[];
  /** The states whose entry returns the units of counted supply the claim committed. */
  readonly returnOn?: readonly string[];
  /** On a table, the Workflow whose status takes, commits, releases and returns the holds: the field a Workflow is anchored on, or Workflow for one the type implements. Implied when the type has one Workflow. */
  readonly status?: string;
  /** The states of the type's Workflow whose entry takes the holds, and the create into one of them; the create alone when absent. */
  readonly takeOn?: readonly string[];
};

/** Lease's configSchema. */
export type LeaseConfigSchema = {
  /** The permission a principal needs to acquire a lease. Absent, any principal who may write the instance can. */
  readonly acquirePermission?: string;
  /** Directives the engine's runner sends the holder of an instance's active lease when the target of one of the instance's links moves on: a new revision of Revisions, or a release of Branches; for a pinned link, a revision tells only the instances the target moved past. The runner sends each with direct, as its principal, which needs directPermission, or overridePermission when that is absent, once per lease and move (dedupeKey). An instance with no lease held hears nothing, and its next holder nothing either. Needs Links on the type; one entry per link. */
  readonly directOn?: ReadonlyArray<{
    /** Its data, beside revised, which the runner sets: the link, the target's schema and id, and its revision or the release's commit. */
    readonly data?: JSONObject;
    /** The directive's name, as direct takes it. */
    readonly name: string;
    /** The link whose target's move sends the directive. */
    readonly revised: {
      /** A link of the type's Links config. */
      readonly link: string;
    };
  }>;
  /** The permission a principal needs to send the holder a directive; overridePermission when absent. With neither, direct is refused. */
  readonly directPermission?: string;
  /** Used instead of onExpiry at the expiry or abandon that reaches maxExpiries, which it needs. Needs Workflow on the type. */
  readonly escalate?: {
    /** The states the status moves from; in any other state it stays. */
    readonly from: readonly string[];
    /** The state to move to, through Workflow's transition. */
    readonly transition: string;
  };
  /** Writing operations of the type's other behaviors, each as <Behavior>.<operation>, that a principal other than the holder may run while the lease is active, and that requireToken does not hold to the token. */
  readonly exempt?: readonly string[];
  /** How often the holder should send a heartbeat, in milliseconds, at most half of ttlMs, so one late heartbeat does not lose the lease; a third of ttlMs when absent. acquire returns it. */
  readonly heartbeatMs?: number;
  /** How many expiries an instance may have, abandons included. Once it has had this many, its lease cannot be acquired again until resetExpiries. */
  readonly maxExpiries?: number;
  /** An integer field of the type that gives the instance's own longest hold, in milliseconds. When the instance holds a positive integer there, it overrides maxHoldMs. */
  readonly maxHoldField?: string;
  /** The longest a principal may hold a lease after acquiring it, in milliseconds, renewed or not; no limit when absent. */
  readonly maxHoldMs?: number;
  /** Moves the instance's Workflow status when a lease ends with the work unfinished: at an expiry, an abandon and a release, while the status is one of from. Needs Workflow on the type. */
  readonly onExpiry?: {
    /** The states the status moves from; in any other state it stays. */
    readonly from: readonly string[];
    /** The state to move to, through Workflow's transition. */
    readonly transition: string;
  };
  /** The permission that lets a principal release another principal's lease, change the instance while another principal holds its lease, reset its expiries, and send directives when directPermission is absent. */
  readonly overridePermission?: string;
  /** When true, a write to an instance with an active lease that does not present the current token as Lease's precondition is refused, the holder's own included, so every process of a principal fences its writes. Exempt and read-only operations, Lease's own operations and a principal with overridePermission are not held to it. */
  readonly requireToken?: boolean;
  /** The field of the type whose Workflow onExpiry and escalate move, and in whose terminal states an expiry does not count. Implied when the type has one Workflow; needed when it has several. A Workflow anchored on the type is named Workflow. A table's alone: an engine type refuses it. */
  readonly status?: string;
  /** How often the engine's runner expires the leases past their expiry time or longest hold, in milliseconds; 5000 when absent. */
  readonly sweepMs?: number;
  /** How long a lease lasts after its acquire or its last heartbeat, in milliseconds; 60000 when absent. */
  readonly ttlMs?: number;
};

/** Numbering's configSchema. */
export type NumberingConfigSchema = {
  /** How a number prints: text with {seq}, or {seq:N} zero-padded to N digits (1 to 10), exactly once, and {yyyy} or {yy}, the UTC year the instance was created in, as often as wanted. No other {...} is allowed. */
  readonly format: string;
  /** yearly starts the count again at 1 each UTC year, the year the instance was created in; the format must then print the year, {yyyy} or {yy}. */
  readonly reset?: 'yearly';
  /** Counts per target of a link of the type's Links config, which is required and holds one target: each target's instances are numbered 1, 2, 3, ... of their own. */
  readonly scope?: {
    /** The link's name in the type's Links config. */
    readonly link: string;
  };
};

/** Payments' configSchema. */
export type PaymentsConfigSchema = {
  /** What becomes of money captured while the instance is in a voidOn state, such as a payment reported after a hold expired: refund records a refund of it; keep leaves it for an operator. keep when absent. */
  readonly afterRelease?: 'refund' | 'keep';
  /** What the instance owes: an own integer field of the type, in the currency's minor units ({ field }), or the claim's Holds.total ({ holds: true }), which carries its currency. */
  readonly amount: {
    /** An own integer field of the type, by JSON key, such as a Finance.Money field. */
    readonly field: string;
  } | {
    /** The type's Holds.total. */
    readonly holds: true;
  };
  /** automatic: a provider captures what it authorizes. manual: captureOn states capture it. automatic when absent. */
  readonly capture?: 'automatic' | 'manual';
  /** With capture manual, the states whose entry captures what is authorized. */
  readonly captureOn?: readonly string[];
  /** The currency of an amount from a field: an ISO 4217 code, or an own Finance.Currency field of the type ({ field }) that holds one. Holds.total carries its own. */
  readonly currency?: string | {
    /** An own Finance.Currency field of the type, by JSON key. */
    readonly field: string;
  };
  /** With refundOf, the states whose entry asks the linked instance to refund the amount. */
  readonly issueOn?: readonly string[];
  /** Who reports what providers did: the adapter's principal. */
  readonly record: {
    /** The permission recordPaymentEvent needs: the adapter's service holds it. */
    readonly permission: string;
  };
  /** Makes the instance a refund of the instance a link names: its amount is refunded from that instance's payments. */
  readonly refundOf?: {
    /** A required, single-valued link of the type's Links config to the instance refunded, which composes Payments. */
    readonly link: string;
  };
  /** Who refunds directly; nobody, but a refund instance, when absent. */
  readonly refunds?: {
    /** The permission refundPayment needs. */
    readonly permission: string;
  };
  /** Per state of the type's Workflow, what a move into it waits for: authorized, every amount due authorized or paid; paid, every amount due captured. */
  readonly require?: { readonly [key: string]: 'authorized' | 'paid' };
  /** The states whose entry cancels the open attempts and voids the authorized ones. */
  readonly voidOn?: readonly string[];
};

/** Presence's configSchema. */
export type PresenceConfigSchema = {
  /** Moves the instance's Workflow status at a beat, while the status is one of from: the way back from the state onMissed moves it to. Needs Workflow on the type. */
  readonly onBeat?: {
    /** The states the status moves from; in any other state it stays. */
    readonly from: readonly string[];
    /** The state to move to, through Workflow's transition. */
    readonly transition: string;
  };
  /** Moves the instance's Workflow status when it is missed, while the status is one of from. Needs Workflow on the type. */
  readonly onMissed?: {
    /** The states the status moves from; in any other state it stays. */
    readonly from: readonly string[];
    /** The state to move to, through Workflow's transition. */
    readonly transition: string;
  };
  /** A string field of the type that holds the subject of the principal the instance stands for, the only principal that may beat it. Once set, it cannot change. */
  readonly principalField: string;
  /** Schemas that compose Lease, whose leases the principal holds are expired when the instance is missed, through Lease's schema-level expireHolder, but for a lease renewed after the principal's last beat, whose own heartbeats show it alive. The principal that applies the miss, the runner's in the sweep, needs Lease's overridePermission on each. */
  readonly releaseLeases?: readonly string[];
  /** The field of the type whose Workflow onMissed, onBeat and ttlByState read and move. Implied when the type has one Workflow; needed when it has several. A Workflow anchored on the type is named Workflow. A table's alone: an engine type refuses it. */
  readonly status?: string;
  /** How often the engine's runner misses the instances past their deadline, in milliseconds; 5000 when absent. */
  readonly sweepMs?: number;
  /** How long the row stays present in a state of its Workflow, in milliseconds, by state: a beat arms the deadline its row's state gives, and a transition arms it again, counted from the later of the last beat and the entry into the state; ttlMs for a state it does not list. Needs Workflow on the type. A table's alone: an engine type refuses it. */
  readonly ttlByState?: { readonly [key: string]: number };
  /** How long the instance stays present after its create or its last beat, in milliseconds. */
  readonly ttlMs: number;
};

/** Provenance's configSchema. */
export type ProvenanceConfigSchema = {
  /** The own top-level fields it records; every one when absent. */
  readonly fields?: readonly string[];
  /** By state of the type's Workflow, the fields a move into it requires out of review. */
  readonly gates?: { readonly [key: string]: {
    /** The recorded fields that must not be proposed, or all. */
    readonly reviewed: readonly string[] | 'all';
  } };
  /** Which writes enter review, and who reviews them. */
  readonly review?: {
    /** A confidence at or above which a write is accepted with no reviewer; every write from a source is proposed when absent. */
    readonly below?: number;
    /** The permission a reviewer needs to accept or reject. */
    readonly permission: string;
    /** The behaviors whose writes enter review, each one the type composes. */
    readonly sources: readonly string[];
  };
};

/** Reactions' configSchema. */
export type ReactionsConfigSchema = {
  /** The rules, each one when and one then, in the order they run. */
  readonly rules: ReadonlyArray<{
    /** What the rule does: move the instance, or the instance its link points to, to a state through Workflow's transition. */
    readonly then: {
      /** A link of the type's Links config: the target is the instance it points to. The instance itself when absent. */
      readonly link?: string;
      /** The state to move the target to. */
      readonly transition: string;
    };
    /** What sets the rule off: one of enters, changes, allTerminal, anyTerminal, holds and revised. */
    readonly when: {
      /** The rule fires on an instance when an instance of schema that links to it through link changes or goes, and every instance that links to it there is in a terminal state of its own schema's Workflow, with an outcome outcomes lists when it is given, at least one. */
      readonly allTerminal?: {
        /** The link of that schema's Links config that points at this type's schema. */
        readonly link: string;
        /** The outcomes a linking instance's terminal state must have; every outcome when absent. */
        readonly outcomes?: ReadonlyArray<'success' | 'failure' | 'neutral'>;
        /** The schema of the linking instances, which composes Links and Workflow. */
        readonly schema: string;
      };
      /** The rule fires on an instance when an instance of schema that links to it through link enters a terminal state of its own schema's Workflow whose outcome outcomes lists, by a create or a transition, or is linked to it while in one. */
      readonly anyTerminal?: {
        /** The link of that schema's Links config that points at this type's schema. */
        readonly link: string;
        /** The outcomes of the terminal states that set the rule off. */
        readonly outcomes: ReadonlyArray<'success' | 'failure' | 'neutral'>;
        /** The schema of the linking instances, which composes Links and Workflow. */
        readonly schema: string;
      };
      /** The rule fires when a create, an update or an operation of the instance moves the value of field: to to when it is given, from from when it is given. A create moves a field from absent; a delete sets nothing off. */
      readonly changes?: {
        /** An own field of the type, by its JSON key, or a field of a behavior the type lists, by qualified name (Workflow.status), a member path after it allowed (Lease.ended.reason). A volatile field, which moves at a read with no event, and Workflow.enteredAt, which moves with the status, are refused. */
        readonly field: string;
        /** The value the field moves from; any value when absent. A create moves a field from absent, which from never names. */
        readonly from?: unknown;
        /** The value the field moves to; any value when absent. */
        readonly to?: unknown;
      };
      /** A state of the type's Workflow: the rule fires when the instance's status becomes it, by a create or a transition. */
      readonly enters?: string;
      /** An all or any rollup of the type's Rollups config: the rule fires on an instance when an instance of the rollup's schema changes, links, unlinks or goes, and that change makes the rollup hold: it holds after it, and would not with that instance as it was before it, the other linked instances as they are when the rule runs. */
      readonly holds?: string;
      /** The rule fires on an instance when the instance its link points to gains a new revision of Revisions, or, when its schema composes Branches, a new release (releaseCommit). For a link pinned to a revision, only an instance a new revision leaves stale, and for one pinned to a release, only an instance a new release leaves stale. */
      readonly revised?: {
        /** A link of the type's Links config. */
        readonly link: string;
      };
    };
  }>;
  /** The field of the type whose Workflow the rules read and move: an enters rule's, a changes rule's on the status, and a then without a link. Implied when the type has one Workflow; needed when it has several. A type anchored Workflow is named Workflow. */
  readonly status?: string;
};

/** Retries' configSchema. */
export type RetriesConfigSchema = {
  /** The failure classes, by name: each its own cap of attempts, with an optional hint for the next attempt, or terminal, which exhausts the instance at its first failure. */
  readonly classes: { readonly [key: string]: {
    /** How many failures of the class an instance may have. */
    readonly attempts: number;
    /** How long the row waits after a failure of the class before it runs again: firstMs after its first failure of the class since its last success, factor times longer after each next one, never longer than maxMs. The failure sets the row's retryAt; until then a transition into a state of retryInto, and Lease's acquire, are vetoed backing_off. A table's alone: an engine type refuses it. */
    readonly backoff?: {
      /** How many times longer each next wait is; 2 when absent. */
      readonly factor?: number;
      /** The wait after the first failure, in milliseconds. */
      readonly firstMs: number;
      /** The longest wait, in milliseconds, at least firstMs. */
      readonly maxMs: number;
    };
    /** What recordAttempt returns for a failure of the class, to steer the worker's next attempt. */
    readonly hint?: string;
  } | 'terminal' };
  /** The state of the type's Workflow an exhausted instance moves to. */
  readonly exhaustedState: string;
  /** The states exhaustion moves the status from; every state but the terminal ones and exhaustedState when absent. A transition from each to exhaustedState must exist. */
  readonly from?: readonly string[];
  /** Exhausts the row at its first failed attempt this many milliseconds or more after the first failure since its last success (failingSince), however much room its caps have left. A table's alone: an engine type refuses it. */
  readonly giveUpAfterMs?: number;
  /** Keeps the best scoring result rather than only a success's: a failed attempt's result is kept when its score beats the best kept one by at least minDelta and no neverRegress predicate that held for the kept one fails. */
  readonly keepBest?: {
    /** How much a score must beat the best kept one by; any amount above it when absent. */
    readonly minDelta?: number;
    /** Predicates an attempt reports that a kept result never loses: one true for the kept result must be true for the next one kept. */
    readonly neverRegress?: readonly string[];
  };
  /** An own field whose value keys every class's count: an attempt under another value than the last starts the counts, the total, exhaustion and the wait again, so { attempts: 1 } with keyField: "version" is one failure per version. A table's alone: an engine type refuses it. */
  readonly keyField?: string;
  /** An object field of the type with the instance's own caps: a class's name to a cap of at least 0, and totalAttempts to a total of at least 1. An unknown or terminal class, and a value that is not such an integer, is ignored. Once the instance exists, only a principal with limitsPermission changes it, and the holder of its active lease does not. */
  readonly limitsField?: string;
  /** The permission a principal needs to change limitsField once the instance exists. Absent, the caps an instance is created with do not change. */
  readonly limitsPermission?: string;
  /** The permission a principal needs to record an attempt. Absent, any principal who may write the instance can. */
  readonly permission?: string;
  /** A field of the type a kept result is written to, with the checks of an update. */
  readonly resultField?: string;
  /** The states of the Workflow an attempt starts in: a transition into one waits until the row's retryAt has passed (backing_off) and for room in window (throttled), as Lease's acquire does. Needs a class with a backoff, or window. A table's alone: an engine type refuses it. */
  readonly retryInto?: readonly string[];
  /** The field of the type whose Workflow exhaustedState, from, retryInto and then name states of. Implied when the type has one Workflow; needed when it has several. A Workflow anchored on the type is named Workflow. A table's alone: an engine type refuses it. */
  readonly status?: string;
  /** How many failures in a row, none of them kept, with the same signature exhaust the instance as stuck. */
  readonly stuckAfter?: number;
  /** Moves the row's Workflow status when its backoff ends: a deadline armed at retryAt moves it to transition, through Workflow's transition, while the status is one of from; a move a guard vetoes, or that the deadline's caller may not make, leaves the status as it is. Needs a class with a backoff. A table's alone: an engine type refuses it. */
  readonly then?: {
    /** The states the status moves from; in any other state it stays. */
    readonly from: readonly string[];
    /** The state to move to, through Workflow's transition. */
    readonly transition: string;
  };
  /** How many failures of every class together an instance may have. */
  readonly totalAttempts: number;
  /** Caps the attempts the row records in any ms milliseconds: while attempts of them fall in the last ms, a transition into a state of retryInto, and Lease's acquire, are vetoed throttled until the oldest of them leaves the window. An attempt that ran is recorded whatever the window holds. Each attempt's time is a row of the attempts sub-table. Needs retryInto, or Lease on the type. A table's alone: an engine type refuses it. */
  readonly window?: {
    /** How many attempts the window holds. */
    readonly attempts: number;
    /** How long the window is, in milliseconds. */
    readonly ms: number;
  };
};

/** Revisions' configSchema. */
export type RevisionsConfigSchema = {
  /** Turns on the review step: propose, approve, reject and listProposals. */
  readonly review?: {
    /** The permission a caller needs to approve or reject a proposal. */
    readonly permission: string;
  };
};

/** Rollups' configSchema. */
export type RollupsConfigSchema = {
  /** The rollups, by name: camelCase, at most 64 characters. */
  readonly rollups: { readonly [key: string]: {
    /** For a rollup that gates states, the moves into them its gate lets through though it does not hold: a caller's who holds permission, and the move each deadline of the table's Timers that deadlines names makes, its then, as it falls due. A table's alone: an engine type refuses it. */
    readonly exemptions?: {
      /** Deadlines of the table's Timers whose then moves into a state the rollup gates, each passing the gate as it falls due. */
      readonly deadlines?: readonly string[];
      /** The permission whose holder moves into a gated state whether the rollup holds or not. */
      readonly permission?: string;
    };
    /** For countBy, a string, enum or boolean field of the linked instances, or status when their schema composes Workflow; for sum, min and max, a numeric field; for latest, any field of theirs, or status when their schema composes Workflow. */
    readonly field?: string;
    /** count: how many instances point here. countBy: how many hold each value of field. sum, min, max: of the numeric field. latest: field's value on the instance created last, the greater id on a tie. all, any: whether every one, or some one, is in a terminal state of its schema's Workflow, with an outcome outcomes lists when it is given. */
    readonly function: 'count' | 'countBy' | 'sum' | 'min' | 'max' | 'latest' | 'all' | 'any';
    /** For all and any, the states of the type's Workflow that a transition into waits for the rollup to hold. */
    readonly gatedStates?: readonly string[];
    /** The link of that schema's Links config that points at this schema. */
    readonly link: string;
    /** For all and any, the outcomes a linked instance's terminal state must have to count; every outcome when absent. */
    readonly outcomes?: ReadonlyArray<'success' | 'failure' | 'neutral'>;
    /** The schema whose instances point at this one; it composes Links. */
    readonly schema: string;
  } };
};

/** StateRules' configSchema. */
export type StateRulesConfigSchema = {
  /** The permission a caller needs to change a field a state freezes; nobody may when absent. What a state requires holds for every caller. */
  readonly permission?: string;
  /** The rules, by a state of the type's Workflow. */
  readonly states: { readonly [key: string]: {
    /** Who may change a field the state freezes: own fields that hold a subject, createdBy among them, and seats of the table's Seats, each as seat:<name>. A caller who is the subject one of the fields holds, or who is seated in one of the seats, before the update, changes a frozen field as a caller with the config's permission does; a field with no value names no one, and what the state requires holds for every caller. Refused in a state that freezes nothing. A table's alone: an engine type refuses it. */
    readonly by?: readonly string[];
    /** The type's own top-level fields, by JSON key, that do not change while the instance is in the state, or all, every one of them. */
    readonly frozen?: readonly string[] | 'all';
    /** The type's own top-level fields, by JSON key, an instance must hold to enter the state, and keeps while it is in it. */
    readonly required?: readonly string[];
  } };
  /** The field of the type whose Workflow the states are of. Implied when the type has one Workflow; needed when it has several. A Workflow anchored on the type is named Workflow. A table's alone: an engine type refuses it. */
  readonly status?: string;
};

/** Stock's configSchema. */
export type StockConfigSchema = {
  /** Whether a hold may take more than is available, leaving available below 0; false when absent. */
  readonly backorder?: boolean;
  /** Stock.low is true while available is at or below this: a count, or an own integer field of the type ({ field }) that holds one, while it holds one. Stock.low is false when absent. */
  readonly lowAt?: number | {
    /** An own integer field of the type, by JSON key. */
    readonly field: string;
  };
  /** The most units one item of a claim may hold; no limit when absent. */
  readonly maxPerHold?: number;
  /** The permission a caller needs to receive, adjust and count stock; a count below what claims hold needs its .override. */
  readonly permission: string;
  /** The unit price holdFor returns and Holds keeps on each item, so a line keeps the price it was held at: an own integer field of the type and its currency. No price when absent. */
  readonly price?: {
    /** The price's currency: an ISO 4217 code, or an own Finance.Currency field of the type ({ field }) that holds one. */
    readonly currency: string | {
      /** An own Finance.Currency field of the type, by JSON key. */
      readonly field: string;
    };
    /** An own integer field of the type, by JSON key, holding the unit price in the currency's minor units, such as a Finance.Money field. */
    readonly field: string;
  };
};

/** Timers' configSchema. */
export type TimersConfigSchema = {
  /** The deadlines, by name. */
  readonly deadlines: { readonly [key: string]: {
    /** How long after its start the deadline is due, in milliseconds; 0 when absent. Negative, before its start, for a field start alone. */
    readonly afterMs?: number;
    /** What the deadline starts from: the instance's create ({ create: true }), each entry of the status into a state of the type's Workflow ({ enters }), or the time an own date-time field holds ({ field }), which moves the deadline when it changes and cancels it when it is cleared. */
    readonly start: {
      /** Starts at the instance's create. */
      readonly create: true;
    } | {
      /** A state of the type's Workflow; the deadline starts again at each entry into it. */
      readonly enters: string;
    } | {
      /** An own field of the type, by JSON key, whose values are date-times. */
      readonly field: string;
    };
    /** Moves the status of the type's Workflow, through its transition, when the deadline fires while the status is one of from. */
    readonly then?: {
      /** The states the status moves from; in any other it stays. When absent, the state the deadline starts in: its enters state, or the Workflow's initial state for a create start. A field start names them. */
      readonly from?: readonly string[];
      /** The state to move to. */
      readonly transition: string;
    };
    /** Cancels the deadline while it is pending once a change leaves the status out of the state it starts in ({ leaves }) or in one of the states given ({ enters }). Needs Workflow on the type. */
    readonly until?: {
      /** The state the deadline starts in: its enters state, or the Workflow's initial state for a create start. */
      readonly leaves: string;
    } | {
      /** States of the type's Workflow, none of them the state the deadline starts in. */
      readonly enters: readonly string[];
    };
  } };
  /** The permission a caller needs to reset or snooze a deadline; nobody may when absent. */
  readonly permission?: string;
  /** The field of the type whose Workflow the deadlines read and move: their enters starts, then and until. Implied when the type has one Workflow; needed when it has several. A type anchored Workflow is named Workflow. */
  readonly status?: string;
  /** How often the engine's runner fires the due deadlines, in milliseconds; 5000 when absent. */
  readonly sweepMs?: number;
};

/** Variants' configSchema. */
export type VariantsConfigSchema = {
  /** The type's own string or enum field, by JSON key, whose value picks the type. */
  readonly by: string;
  /** The type's own field the variants type, by JSON key: a Generic.JSON field, or one of a scalar whose values are JSON objects. */
  readonly field: string;
  /** By a value of by, the type of the schema document, besides the instance type, that field holds while by holds the value. */
  readonly types: { readonly [key: string]: string };
};

/** Workflow's configSchema. */
export type WorkflowConfigSchema = {
  /** The outcome of each state it names that a transition leaves: success, failure or neutral. Such a state counts as finished for the rules that read an outcome, as a terminal state does, though a row may leave it: a task that is done and may be reopened. Each key is a state of states that a transition leaves; a terminal state's outcome is outcomes'. A table's alone: an engine type refuses it. */
  readonly finished?: { readonly [key: string]: 'success' | 'failure' | 'neutral' };
  /** The state a new instance starts in; the first of states when absent. */
  readonly initial?: string;
  /** The outcome of each terminal state it names: success, failure or neutral. A terminal state it does not name is a success. Each key is a terminal state of states. */
  readonly outcomes?: { readonly [key: string]: 'success' | 'failure' | 'neutral' };
  /** The permission that lets a caller make a transition its by does not name them for; the transition's own permission is still needed. A table's alone: an engine type refuses it. */
  readonly overridePermission?: string;
  /** unchanged: a transition to the state the instance is in answers { from, to, unchanged: true } and writes nothing, before any other behavior's guard is asked. Absent, it is the veto already_in_state. A table's alone: an engine type refuses it. */
  readonly repeat?: 'unchanged';
  /** Every state an instance can be in. */
  readonly states: readonly string[];
  /** The moves the status may make. A state that no transition leaves is terminal. */
  readonly transitions: ReadonlyArray<{
    /** Who may make this transition: fields of the instance that hold a subject, createdBy among them, and seats of the table's Seats, each as seat:<name>. Only a caller who is the subject one of the fields holds, or who is seated in one of the seats, may, and it needs the transition's permission too when it names one; a field with no value names no one. A caller holding the config's overridePermission may whatever the fields and the seats hold. A table's alone: an engine type refuses it. */
    readonly by?: readonly string[];
    /** The state the instance is in. */
    readonly from: string;
    /** The permission a caller needs to make this transition. Absent, any caller who may write the instance can. */
    readonly permission?: string;
    /** The state it moves to. */
    readonly to: string;
  }>;
};

/** Each behavior's config schema, by the behavior's name. */
export interface BehaviorConfigSchemas {
  Approvals: ApprovalsConfigSchema;
  Assignment: AssignmentConfigSchema;
  Blueprint: BlueprintConfigSchema;
  Budget: BudgetConfigSchema;
  Calendar: CalendarConfigSchema;
  Comments: CommentsConfigSchema;
  Constants: ConstantsConfigSchema;
  Dependencies: DependenciesConfigSchema;
  Derived: DerivedConfigSchema;
  Holds: HoldsConfigSchema;
  Lease: LeaseConfigSchema;
  Numbering: NumberingConfigSchema;
  Payments: PaymentsConfigSchema;
  Presence: PresenceConfigSchema;
  Provenance: ProvenanceConfigSchema;
  Reactions: ReactionsConfigSchema;
  Retries: RetriesConfigSchema;
  Revisions: RevisionsConfigSchema;
  Rollups: RollupsConfigSchema;
  StateRules: StateRulesConfigSchema;
  Stock: StockConfigSchema;
  Timers: TimersConfigSchema;
  Variants: VariantsConfigSchema;
  Workflow: WorkflowConfigSchema;
}
