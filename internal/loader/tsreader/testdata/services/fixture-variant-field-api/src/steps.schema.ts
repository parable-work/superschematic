import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { Authenticated, HttpMethod, rest, source } from "@superschematic/api";
import { Step, StepKind, StepResult } from "@schemas/fixture-variant-field-db";

// A step as the API shows it: its result in the shape its kind gives it.
@source(Step)
export abstract class StepView {
  id: Identity.UUID;
  title: string;
  kind: StepKind;
  result: Nullable<StepResult>;
}

// What a caller sets to record a step: a result its kind names no shape
// for is refused with the rule variant.
@source(Step)
export abstract class CreateStepInput {
  title: string;
  kind: StepKind;
  result: Nullable<StepResult>;
}

export class Steps extends Authenticated {
  // One step.
  @rest(HttpMethod.GET, "steps/{id}")
  getStep(id: Identity.UUID): StepView {
    throw new Error("schema declaration only");
  }

  // Records a step.
  @rest(HttpMethod.POST, "steps")
  createStep(input: CreateStepInput): StepView {
    throw new Error("schema declaration only");
  }
}
