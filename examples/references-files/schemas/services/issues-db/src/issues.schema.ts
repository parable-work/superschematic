import { Identity, Temporal } from "superscalar";
import { AutoGenerate, Comments, attachments, audience, key, queue, references } from "@superschematic/db";

// A row's reference cleared because its target was deleted: the row that
// held it, the row deleted and the role. The build fills its fields.
@queue()
export abstract class ReferenceCleared {}

// An object a file named, released once its file row is gone or its upload
// expired. The build fills its fields; the worker deletes the object once
// no file names it.
@queue()
export abstract class FileReleased {}

// An issue: a conversation whose messages carry files and point at other
// rows, references to rows of any table, and files of at most 25 MiB.
@references<Issue>({ cleared: ReferenceCleared })
@attachments<Issue>({ released: FileReleased, maxFileBytes: 26214400 })
export abstract class Issue implements Comments<Issue, { attachments: { maxBytes: 26214400; schemes: ["attachment"] } }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}

// A note, which its owner may keep to themselves: it points at issues and
// other notes, and holds documents and images.
@audience<Note>({ owner: "owner" })
@references<Note>({ tables: [Issue, Note], cleared: ReferenceCleared })
@attachments<Note>({ released: FileReleased, mediaTypes: ["text/*", "image/*", "application/pdf"] })
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  owner: Identity.UUID;
  createdAt: Temporal.DateTime;
}
