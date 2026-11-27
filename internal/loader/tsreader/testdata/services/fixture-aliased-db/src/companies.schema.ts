import { Identity } from "superscalar";
import { Aliased, AutoGenerate, Mergeable, Relation, key } from "@superschematic/db";

// A company a person may know by more than one name (D147): Acme Group,
// which was Acme. No two companies hold one name, as a name or an alias,
// with its case and its runs of spaces ignored; a rename keeps the old
// name as an alias, and resolve finds the company a name or an alias
// names. Two companies that turn out to be one are merged: the contacts
// of the one merged away point at the one that stays, which keeps its
// name and aliases as aliases of its own.
export abstract class Company implements Mergeable {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: Aliased<string>;
  city: string;
}

// A person at a company.
export abstract class Contact {
  @key
  id: AutoGenerate<Identity.UUID>;
  company: Relation<Company>;
  email: string;
}

// A tag, known by its letters and digits alone: "Q3 Plan" and "q3-plan"
// are one tag.
export abstract class Tag {
  @key
  id: AutoGenerate<Identity.UUID>;
  label: Aliased<string, { normalize: "slug" }>;
}
