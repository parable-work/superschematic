// Tools whose SDK methods invokeTool must call as they are declared: body
// arguments without an input type (two or more required, the first an
// object value, next to a path or query parameter, on an encrypted
// operation), an input type whose fields are snake_case, and a file upload.
// The core scalar package has no file-upload scalar, so the SDK generator
// tests mark Network.Url one.
import { Generic, Identity, Network, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { HttpMethod, QueryParam, encrypted, rest } from "@superschematic/api";

export abstract class NoteInput {
  title: string;
  created_by: string;
  due_at: Nullable<Temporal.DateTime>;
}

export abstract class AttachmentUpload {
  file: Network.Url;
  caption: string;
}

export abstract class NoteView {
  id: Identity.UUID;
}

export class NoteMutations {
  // An input type with snake_case fields.
  @rest(HttpMethod.POST, "notes")
  createNote(input: NoteInput): NoteView {
    throw new Error("schema declaration only");
  }

  // Two required body arguments and an optional one, after a path parameter.
  @rest(HttpMethod.PUT, "notes/{id}/tag")
  tagNote(id: Identity.UUID, label: string, weight: number, comment: Nullable<string>): NoteView {
    throw new Error("schema declaration only");
  }

  // Two required body arguments, the first an object value.
  @rest(HttpMethod.POST, "notes/configure")
  configureNotes(settings: Generic.JSON, reason: string): NoteView {
    throw new Error("schema declaration only");
  }

  // Two required body arguments and a query parameter.
  @rest(HttpMethod.POST, "notes/move")
  moveNote(title: string, folder: string, dryRun: QueryParam<Nullable<boolean>>): NoteView {
    throw new Error("schema declaration only");
  }

  // Two required body arguments and a query parameter, encrypted.
  @encrypted
  @rest(HttpMethod.POST, "notes/seal")
  sealNote(title: string, body: string, dryRun: QueryParam<Nullable<boolean>>): NoteView {
    throw new Error("schema declaration only");
  }

  // A file upload.
  @rest(HttpMethod.POST, "notes/attachments")
  attach(input: AttachmentUpload): NoteView {
    throw new Error("schema declaration only");
  }
}
