// Blueprint's class (D103): the build derives blueprint.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { computed, declaresBehavior, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

type BlueprintConfig = JsonSchema<{
  /**
   * The child schema. It composes Links with parentLink, Constants over
   * keyField and every copied field, Dependencies when a step comes after
   * another, and not Blueprint.
   */
  schema: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
  /**
   * The link of the child schema's Links config that points at this schema;
   * each child is linked to its parent through it.
   */
  parentLink: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
  /**
   * A string field of the child schema's type that each child gets its step's
   * key in.
   */
  keyField: Named;
  /** The steps, by key, stamped when the instance is created. */
  steps?: JsonSchema<Record<JsonSchema<string, { pattern: "^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$" }>, {
    /** The keys of the steps whose children block this step's child. */
    after?: JsonSchema<Named[], { uniqueItems: true }>;
    /**
     * Stamps the step only when the instance's field equals a value, or is a
     * list that includes one; absent, always.
     */
    when?: JsonSchema<{
      /** A field of this schema's type. */
      field: Named;
      /** The value the field must equal, as JSON. */
      equals?: unknown;
      /** A value the field, a list, must include, as JSON. */
      includes?: unknown;
    }, { oneOf: [{ required: ["equals"] }, { required: ["includes"] }] }>;
    /** Fields of the child, beside its key and the copied fields. */
    data?: object;
  }>, { minProperties: 1; maxProperties: 500 }>;
  /**
   * Where the steps are kept as an instance: a link of this type's Links
   * config pinned to a revision, and the field of the linked instance that
   * holds a map of steps of the same form as steps. The steps are read from
   * the revision the link pins and stamped when the link is first set, by the
   * create that gives it or a later link.
   */
  from?: {
    /** A link of this type's Links config pinned to a revision. */
    link: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
    /** The field of the linked instance's type that holds the steps. */
    field: Named;
  };
  /**
   * Fields of this type copied to every child, a field of the child's type of
   * the same name.
   */
  copyFields?: JsonSchema<Named[], { uniqueItems: true }>;
  /**
   * Links of this type copied to every child, a link of the child's Links
   * config of the same name to the same schema, keeping the revision or the
   * release this instance's link records where the child's pins the same: the
   * links the instance holds when it is stamped, which for inline steps are
   * the ones its create gives.
   */
  copyLinks?: JsonSchema<JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>[], { uniqueItems: true }>;
}, { oneOf: [{ required: ["steps"] }, { required: ["from"] }] }>;

/** The codes Blueprint's refusals carry. */
enum BlueprintVeto {
  /**
   * Links' link moving the from link once the instance's children were
   * stamped from the revision it pins.
   */
  stamped,
  /**
   * A stamp whose steps come after others, while the child schema does not
   * compose Dependencies.
   */
  no_dependencies,
  /**
   * A stamp while the child schema's Constants does not keep keyField and
   * every copied field, which a later version of it dropped; details.fields
   * lists the ones it does not keep.
   */
  not_constant,
  /**
   * A stamp from a from link that records no revision to read the steps from.
   */
  no_revision,
  /**
   * A stamp from a pinned revision of the definition the principal cannot
   * read.
   */
  unreadable,
  /** A stamp from a map of steps that is not valid for this type. */
  invalid_steps,
}

/**
 * Creates the instance's children from a map of steps: one instance of the
 * child schema per step, created with its link to the instance and blocked by
 * the children of the steps it comes after, all in one transaction that a
 * failure refuses whole. The steps are inline in the config, stamped when the
 * instance is created, or held by a definition the instance links to, pinned
 * to a revision, stamped when that link is first set, by the create that
 * gives it or a later link. A step can depend on the instance's fields
 * (when); a step left out passes its dependencies on to the steps that come
 * after it.
 */
@declaresBehavior({ vetoes: BlueprintVeto })
export abstract class Blueprint<C extends BlueprintConfig> {
  /**
   * What was stamped: each step's key and its child's id, in the order they
   * were created; absent until the instance is stamped.
   */
  @computed children: unknown;
}
