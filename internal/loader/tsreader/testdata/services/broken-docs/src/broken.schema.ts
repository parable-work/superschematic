import { HttpMethod, docs, rest } from "@superschematic/api";

export abstract class Note {
  body: string;
}

export class NoteQueries {
  // The capability is not a dotted lowercase identifier.
  @docs({
    title: "Get a note",
    description: "Returns the note.",
    capability: "GetNote",
    lifecycle: "active",
    visibility: "internal"
  })
  @rest(HttpMethod.GET, "note")
  getNote(): Note {
    throw new Error("schema declaration only");
  }
}
