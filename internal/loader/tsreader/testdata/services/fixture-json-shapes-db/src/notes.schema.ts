import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { Approvals, AutoGenerate, Workflow, jsonField, key } from "@superschematic/db";

// A task whose sign-off a change of its title resets: a behavior config
// that names a field of its table, which its checks read from the
// database's table model.
export abstract class Task implements Approvals<Task, { gates: { done: { count: 1; permission: "tasks.review"; excludeCreator: false; resetOn: ["title"] } } }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  status: Workflow<{ states: ["open", "done"]; transitions: [{ from: "open"; to: "done" }] }>;
}

// A line of what a note shows: a JSON shape, since only Shown holds it.
export abstract class Line {
  text: string;
}

// What a note shows: a JSON shape, since only a @jsonField field holds it,
// and it holds a list of another.
export abstract class Shown {
  lines: Line[];
}

// A shape marked as one, which a note holds beside the shape it infers.
@jsonField
export abstract class Margin {
  text: string;
}

// A person, a table with a key, which a note keeps a copy of as JSON.
export abstract class Person {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
}

// A note: what it shows, its margin and a copy of its author, each a JSON
// column.
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;
  @jsonField
  shown: Nullable<Shown>;
  margin: Nullable<Margin>;
  @jsonField
  author: Nullable<Person>;
}
