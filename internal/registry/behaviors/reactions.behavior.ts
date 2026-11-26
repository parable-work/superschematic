// Reactions's class (D103): the build derives reactions.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { declaresBehavior, type Hears, type JsonSchema, type Names } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface ReactionsConfig {
  /** The rules, each one when and one then, in the order they run. */
  rules: JsonSchema<{
    /**
     * What sets the rule off: one of enters, changes, allTerminal,
     * anyTerminal, holds and revised.
     */
    when: JsonSchema<{
      /**
       * A state of the type's Workflow: the rule fires when the instance's
       * status becomes it, by a create or a transition.
       */
      enters?: Named;
      /**
       * The rule fires when a create, an update or an operation of the
       * instance moves the value of field: to to when it is given, from from
       * when it is given. A create moves a field from absent; a delete sets
       * nothing off.
       */
      changes?: {
        /**
         * An own field of the type, by its JSON key, or a field of a behavior
         * the type lists, by qualified name (Workflow.status), a member path
         * after it allowed (Lease.ended.reason). A volatile field, which
         * moves at a read with no event, and Workflow.enteredAt, which moves
         * with the status, are refused.
         */
        field: JsonSchema<string, { pattern: "^[^.]+(\\.[^.]+)*$"; maxLength: 512 }>;
        /** The value the field moves to; any value when absent. */
        to?: unknown;
        /**
         * The value the field moves from; any value when absent. A create
         * moves a field from absent, which from never names.
         */
        from?: unknown;
      };
      /**
       * The rule fires on an instance when an instance of schema that links
       * to it through link changes or goes, and every instance that links to
       * it there is in a terminal state of its own schema's Workflow, with an
       * outcome outcomes lists when it is given, at least one.
       */
      allTerminal?: Hears<{
        /**
         * The schema of the linking instances, which composes Links and
         * Workflow.
         */
        schema: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
        /**
         * The link of that schema's Links config that points at this type's
         * schema.
         */
        link: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
        /**
         * The outcomes a linking instance's terminal state must have; every
         * outcome when absent.
         */
        outcomes?: JsonSchema<("success" | "failure" | "neutral")[], { minItems: 1; uniqueItems: true }>;
      }>;
      /**
       * The rule fires on an instance when an instance of schema that links
       * to it through link enters a terminal state of its own schema's
       * Workflow whose outcome outcomes lists, by a create or a transition,
       * or is linked to it while in one.
       */
      anyTerminal?: Hears<{
        /**
         * The schema of the linking instances, which composes Links and
         * Workflow.
         */
        schema: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
        /**
         * The link of that schema's Links config that points at this type's
         * schema.
         */
        link: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
        /** The outcomes of the terminal states that set the rule off. */
        outcomes: JsonSchema<("success" | "failure" | "neutral")[], { minItems: 1; uniqueItems: true }>;
      }>;
      /**
       * An all or any rollup of the type's Rollups config: the rule fires on
       * an instance when an instance of the rollup's schema changes, links,
       * unlinks or goes, and that change makes the rollup hold: it holds
       * after it, and would not with that instance as it was before it, the
       * other linked instances as they are when the rule runs.
       */
      holds?: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
      /**
       * The rule fires on an instance when the instance its link points to
       * gains a new revision of Revisions, or, when its schema composes
       * Branches, a new release (releaseCommit). For a link pinned to a
       * revision, only an instance a new revision leaves stale, and for one
       * pinned to a release, only an instance a new release leaves stale.
       */
      revised?: {
        /** A link of the type's Links config. */
        link: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
      };
    }, { minProperties: 1; maxProperties: 1 }>;
    /**
     * What the rule does: move the instance, or the instance its link points
     * to, to a state through Workflow's transition.
     */
    then: {
      /** The state to move the target to. */
      transition: Named;
      /**
       * A link of the type's Links config: the target is the instance it
       * points to. The instance itself when absent.
       */
      link?: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
    };
  }[], { minItems: 1; maxItems: 64 }>;
  /**
   * The field of the type whose Workflow the rules read and move: an enters
   * rule's, a changes rule's on the status, and a then without a link.
   * Implied when the type has one Workflow; needed when it has several. A
   * type anchored Workflow is named Workflow.
   */
  status?: Names<Named, "workflow">;
}

/**
 * Rules that move Workflow statuses after a change commits: when the
 * instance's status enters a state, when a field of the instance, its own or
 * a behavior's, changes, when every instance that links to it through a link
 * is in a terminal state, optionally one with a listed outcome, when one of
 * them enters a terminal state with a listed outcome, when an all or any
 * rollup of the instance comes to hold, or when the instance one of its links
 * points to gains a revision or a release, move the instance, or the instance
 * one of its links points to, to a state. An engine runs them after the
 * commit, as the principal it names for its runner, so a rule never refuses
 * the change that set it off. A target already in the state, with no
 * transition to it, or whose guards refuse is left as it is.
 */
@declaresBehavior({ requires: ["Workflow"], hearsChanges: true })
export abstract class Reactions<C extends ReactionsConfig> {
}
