import { HttpMethod, docs, mcp, rest } from "@superschematic/api";

export abstract class Note {
  body: string;
}

export class NoteQueries {
  // The handle is not lowercase snake_case.
  @docs({
    title: "Get a note",
    description: "Returns the note.",
    capability: "notes.get",
    lifecycle: "active",
    visibility: "internal"
  })
  @mcp({ handle: "getNote" })
  @rest(HttpMethod.GET, "note")
  getNote(): Note {
    throw new Error("schema declaration only");
  }
}
