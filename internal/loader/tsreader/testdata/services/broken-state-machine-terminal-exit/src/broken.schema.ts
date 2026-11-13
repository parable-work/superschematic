import { Identity } from "superscalar";
import { Default, stateMachine } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";

export enum JobState {
  Queued = "QUEUED",
  Done = "DONE"
}

// The compiler takes this graph. The build refuses it: no update leaves a
// terminal state, and Done has a transition.
@stateMachine<Job>({
  field: "state",
  transitions: {
    [JobState.Queued]: [JobState.Done],
    [JobState.Done]: [JobState.Queued]
  },
  terminal: [JobState.Done]
})
export abstract class Job {
  @key
  id: AutoGenerate<Identity.UUID>;

  state: Default<JobState, JobState.Queued>;
}
