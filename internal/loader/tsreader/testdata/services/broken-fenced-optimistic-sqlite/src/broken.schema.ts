import { Generic, Identity } from "superscalar";
import { Default, fenced } from "@superschematic/schema";
import { AutoGenerate, key, optimistic } from "@superschematic/db";

// The service lists sqlite, where an @optimistic table's bump of _version
// is an update of its own that sets _version alone. A fence with no
// covers counts every update, that one among them, and would refuse it.
@optimistic
@fenced<Shelf>({ field: "revision" })
export abstract class Shelf {
  @key
  id: AutoGenerate<Identity.UUID>;

  label: string;

  revision: Default<Generic.Int64, 0>;
}
