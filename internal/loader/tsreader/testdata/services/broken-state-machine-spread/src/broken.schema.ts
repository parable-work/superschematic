import { Identity } from "superscalar";
import { stateMachine } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";

export enum JobState {
  Queued = "QUEUED",
  Done = "DONE"
}

const shared = { [JobState.Queued]: [JobState.Done] };

// The compiler takes a spread here. The build refuses it: it reads each
// state's moves where they are written, and a spread's are elsewhere.
@stateMachine<Job>({
  field: "state",
  transitions: { ...shared },
  terminal: [JobState.Done]
})
export abstract class Job {
  @key
  id: AutoGenerate<Identity.UUID>;

  state: JobState;
}
