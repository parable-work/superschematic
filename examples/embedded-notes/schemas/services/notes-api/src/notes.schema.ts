import { Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  HttpMethod,
  SortBy,
  behaviorOperations,
  paginated,
  publicRoute,
  requireService,
  rest,
  source,
  tableOperation,
  worker
} from "@superschematic/api";
import { Note, PinRequested, Task } from "@notes/notes-db";

// A note as the API shows it.
@source(Note)
export abstract class NoteView {
  id: Identity.UUID;
  title: string;
  body: Nullable<string>;
  pinned: boolean;
  createdAt: Temporal.DateTime;
}

// What a caller sets to write a note.
@source(Note)
export abstract class CreateNoteInput {
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
  body: Nullable<string>;
}

// What a caller may change of a note: each member left out leaves its
// column as it is.
@source(Note)
export abstract class UpdateNoteInput {
  title: Nullable<Validate<string, { minLength: 1; maxLength: 200 }>>;
  body: Nullable<string>;
  pinned: Nullable<boolean>;
}

// The notes. The binary listens on the machine it is installed on, and
// whoever reaches it there is its one user, so every operation is open.
// Each table operation, and each of a task's behavior operations, is an
// MCP tool too, by the name its declaration gives it (list_notes,
// task_status_transition), which the binary serves at notes-api's
// endpoint, /api/notes-api/api/mcp (D81, D111, D116).
export class Notes {
  // The notes, newest first, a page at a time.
  @rest(HttpMethod.GET, "notes")
  @publicRoute
  @paginated({ defaultSort: "-createdAt" })
  @tableOperation()
  listNotes(sort: SortBy<NoteView, ["createdAt"]>): NoteView[] {
    throw new Error("schema declaration only");
  }

  // One note.
  @rest(HttpMethod.GET, "notes/{id}")
  @publicRoute
  @tableOperation()
  getNote(id: Identity.UUID): NoteView {
    throw new Error("schema declaration only");
  }

  // A new note.
  @rest(HttpMethod.POST, "notes")
  @publicRoute
  @tableOperation()
  createNote(input: CreateNoteInput): NoteView {
    throw new Error("schema declaration only");
  }

  // A note, changed.
  @rest(HttpMethod.PATCH, "notes/{id}")
  @publicRoute
  @tableOperation()
  updateNote(id: Identity.UUID, input: UpdateNoteInput): NoteView {
    throw new Error("schema declaration only");
  }

  // A note, removed: the note it was.
  @rest(HttpMethod.DELETE, "notes/{id}")
  @publicRoute
  @tableOperation()
  deleteNote(id: Identity.UUID): NoteView {
    throw new Error("schema declaration only");
  }
}

// A task as the API shows it: its title, and where its status Workflow
// stands.
@source(Task)
export abstract class TaskView {
  id: Identity.UUID;
  title: string;
  status: string;
}

// What a caller sets to write a task, which starts open.
@source(Task)
export abstract class CreateTaskInput {
  title: Validate<string, { minLength: 1; maxLength: 200 }>;
}

// The tasks, served from notes-db's Task table, which composes a
// Workflow: a create runs through the behaviors' core, in the request's
// transaction (D103, D116).
export class Tasks {
  // One task.
  @rest(HttpMethod.GET, "tasks/{id}")
  @publicRoute
  @tableOperation()
  getTask(id: Identity.UUID): TaskView {
    throw new Error("schema declaration only");
  }

  // A new task, open.
  @rest(HttpMethod.POST, "tasks")
  @publicRoute
  @tableOperation()
  createTask(input: CreateTaskInput): TaskView {
    throw new Error("schema declaration only");
  }
}

// A task's status operations: the transitions its Workflow lists, each at
// POST tasks/{id}/status/transition and run through the behaviors' core on
// the file's writing connection (D103, D116).
@behaviorOperations(Task)
@publicRoute
export abstract class TaskActions {}

// What another API of the binary reads. Only a calling service is
// admitted (D37): no browser reaches it, and notes-digest, which the
// binary holds too, calls it in the process with its server's loopback
// credential.
export class NotesForServices {
  // Every note, newest first, a page at a time.
  @rest(HttpMethod.GET, "internal/notes")
  @requireService()
  @paginated({ defaultSort: "-createdAt" })
  @tableOperation()
  listNotesForServices(sort: SortBy<NoteView, ["createdAt"]>): NoteView[] {
    throw new Error("schema declaration only");
  }
}

// A request to pin a note, as the API answers it: the note it names.
export abstract class PinRequest {
  noteId: Identity.UUID;
}

// What the implementation writes itself: the one operation the tables do
// not serve.
export class Pins {
  // Asks for a note to be pinned. The request is a message on notes-db's
  // queue PinRequested, written in the request's transaction, which the
  // worker PinNotes handles.
  @rest(HttpMethod.POST, "notes/{id}/pin-requests")
  @publicRoute
  requestPin(id: Identity.UUID): PinRequest {
    throw new Error("schema declaration only");
  }
}

// Pins each note a PinRequested names. It runs inside the binary, beside
// the server that enqueues its messages (D116).
@worker({ queue: PinRequested })
export abstract class PinNotes {}
