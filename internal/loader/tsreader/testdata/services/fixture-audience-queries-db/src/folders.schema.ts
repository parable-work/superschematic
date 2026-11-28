import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, audience, key, projection } from "@superschematic/db";

// A folder of documents. Its owner sees it whatever its audience.
@audience<Folder>({ owner: "owner" })
export abstract class Folder {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  owner: Identity.UUID;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// A document, which sees what its folder sees unless it names its own
// audience.
@audience<Document>({ inheritFrom: "folder", agents: "readOnly" })
export abstract class Document {
  @key
  id: AutoGenerate<Identity.UUID>;

  folder: Nullable<Relation<Folder, { onDelete: "SET NULL" }>>;

  title: string;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// The documents as a list shows them: a view keyed by the document's key,
// whose rows keep to the document's audience.
@projection<Document>({ name: "document_list", migration: "20261128000000" })
export abstract class DocumentListEntry {
  id: Identity.UUID;
  title: string;
}
