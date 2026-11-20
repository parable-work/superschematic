import { Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, Workflow, key, queue } from "@superschematic/db";

// A note: a title, what it says, and whether it is pinned.
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: Validate<string, { minLength: 1; maxLength: 200 }>;

  body: Nullable<string>;

  pinned: Default<boolean, false>;

  createdAt: Temporal.DateTime;
}

// A task a person keeps beside their notes, open until it is done. Its
// status is a Workflow, a behavior the table composes (D103): the
// repositories write a task through the behaviors' core, which starts it
// open and holds each move to the transitions the Workflow lists. The third
// release added it.
export abstract class Task {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: Validate<string, { minLength: 1; maxLength: 200 }>;

  status: Workflow<{
    states: ["open", "done"];
    initial: "open";
    transitions: [{ from: "open"; to: "done" }, { from: "done"; to: "open" }];
  }>;
}

// Asked when someone wants a note pinned: the worker PinNotes of notes-api
// pins it. The binary claims the message on the same file the note is in.
@queue()
export abstract class PinRequested {
  noteId: Identity.UUID;
}
