import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Comments, Encrypted, Relation, attachments, audience, key, queue, searchable } from "@superschematic/db";

// One message per row whose indexed text changed; the build fills its
// fields, sourceTable and sourceKey. Its worker calls indexSearch.
@queue()
export abstract class SearchIndexing {}

// One message per object a file named, once the file is gone.
@queue()
export abstract class FileReleased {}

// A project: the scope a search may keep to.
@searchable<Project>({ title: "name", body: "summary", scope: "id", queue: SearchIndexing })
export abstract class Project {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  summary: Nullable<string>;
}

// A task in a project, with a conversation and files whose text the
// index holds with it. Its access code is sealed at rest, and never
// indexed.
@attachments<Task>({ released: FileReleased })
@searchable<Task>({ title: "title", body: "details", scope: "project", queue: SearchIndexing })
export abstract class Task implements Comments<Task, {}> {
  @key
  id: AutoGenerate<Identity.UUID>;

  project: Relation<Project, { onDelete: "CASCADE" }>;

  title: string;

  details: Nullable<string>;

  accessCode: Nullable<Encrypted<string>>;
}

// A page a person may keep to themselves: a search shows it to its
// audience alone.
@audience<Page>({ owner: "owner" })
@searchable<Page>({ title: "title", body: "text", scope: "project", queue: SearchIndexing })
export abstract class Page {
  @key
  id: AutoGenerate<Identity.UUID>;

  project: Relation<Project, { onDelete: "CASCADE" }>;

  title: string;

  text: Nullable<string>;

  owner: Identity.UUID;
}
