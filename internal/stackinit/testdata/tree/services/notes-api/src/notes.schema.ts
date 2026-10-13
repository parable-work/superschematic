import { HttpMethod, rest } from "@superschematic/api";

export abstract class NoteView {
  id: string;
  body: string;
}

// Open to any caller: an end user's operation, so stack init exposes
// notes-api.
export class NoteQueries {
  @rest(HttpMethod.GET, "notes/{id}")
  getNote(id: string): NoteView {
    throw new Error("schema declaration only");
  }
}
