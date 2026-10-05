import { Identity } from "superscalar";
import { AutoGenerate, key, versioned } from "@superschematic/db";

// History is kept in whole days: a day and a half is refused, not kept as
// one day.
@versioned({ retentionDays: 1.5 })
export abstract class Recipe {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}
