import { Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";

// A note: a title, what it says, and whether it is pinned.
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: Validate<string, { minLength: 1; maxLength: 200 }>;

  body: Nullable<string>;

  pinned: Default<boolean, false>;

  createdAt: Temporal.DateTime;
}
