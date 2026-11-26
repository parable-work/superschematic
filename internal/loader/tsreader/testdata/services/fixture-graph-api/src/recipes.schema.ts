import { Identity } from "superscalar";
import { HttpMethod, auth, graphOperations, requirePermission, rest } from "@superschematic/api";
// The database's index exports its sentinel alone, so the root is imported
// from the file that declares it; a project imports it from the package,
// "@schemas/fixture-version-graph-db". The reader records the package
// either way.
import { Recipe } from "../../fixture-version-graph-db/src/recipe.schema";

// A recipe's version graph as routes (D138): its change sets, commits,
// merges and releases, each run through the graph's facade. A route needs
// recipes.write, and a release recipes.release in its place. A change set
// is written by the caller who branched it, or by a holder of
// recipes.admin.
@graphOperations(Recipe, {
  permissions: { release: ["recipes.release"] },
  by: ["createdBy"],
  overridePermission: "recipes.admin"
})
@requirePermission(["recipes.write"])
export abstract class RecipeOperations {}

// A recipe as its table holds it.
export class RecipeQueries {
  @auth
  @rest(HttpMethod.GET, "recipes/{id}")
  getRecipe(id: Identity.UUID): Recipe {
    throw new Error("schema declaration only");
  }
}
