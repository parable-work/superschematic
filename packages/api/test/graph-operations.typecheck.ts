// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: @graphOperations takes a class, the graph's root, and its options,
// and goes on a class with no members, whose @auth, @requirePermission,
// @publicRoute, service clause and rate limit apply to every route the set
// serves (D138).
import { auth, graphOperations, publicRoute, rateLimit, requirePermission, requireService } from "@superschematic/api";

declare abstract class Recipe {
  id: string;
}

@graphOperations(Recipe)
@requirePermission(["recipes.write"])
export abstract class RecipeOperations {}

@graphOperations(Recipe, {
  operations: ["branch", "save", "commit", "compose", "release"],
  permissions: { release: ["recipes.release"] },
  by: ["createdBy"],
  overridePermission: "recipes.admin"
})
@auth
@requireService({ from: [] })
@rateLimit({ requestsPerMinute: 60 })
export abstract class NarrowedRecipeOperations {}

@graphOperations(Recipe, { operations: ["released", "materialize"] })
@publicRoute
export abstract class PublicRecipeOperations {}

// @ts-expect-error the root is a class, not its name
@graphOperations("Recipe")
export abstract class NamedByString {}

// @ts-expect-error publish is no operation of a graph
@graphOperations(Recipe, { operations: ["publish"] })
export abstract class UnknownOperation {}

// @ts-expect-error a permission list is per operation of a graph
@graphOperations(Recipe, { permissions: { publish: ["recipes.release"] } })
export abstract class UnknownPermission {}

// @ts-expect-error the row rule names createdBy alone
@graphOperations(Recipe, { by: ["updatedBy"] })
export abstract class UnknownMember {}

export class Methods {
  // @ts-expect-error a class decorator, not a method's
  @graphOperations(Recipe)
  branch(): void {}
}
