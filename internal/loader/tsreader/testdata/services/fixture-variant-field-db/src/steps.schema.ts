import { Identity } from "superscalar";
import { Validate, Variant, jsonField } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";

// What a step does, which picks the shape of its result.
export enum StepKind {
  Verify = "verify",
  Review = "review",
  Note = "note"
}

// How a review ended.
export enum Verdict {
  Approved = "approved",
  ChangesRequested = "changes_requested"
}

// One check a verify step ran.
@jsonField
export abstract class Check {
  name: string;
  ok: boolean;
}

// A verify step's result: whether it passed, and the checks it ran.
@jsonField
export abstract class VerifyResult {
  passed: boolean;
  checks?: Check[];
}

// A review step's result: its verdict, and the reviewer's notes.
@jsonField
export abstract class ReviewResult {
  verdict: Verdict;
  notes?: Validate<string, { maxLength: 200 }>;
}

// A step's result in the shape its kind gives it (D149): a VerifyResult
// while the step's kind is verify, a ReviewResult while it is review, and
// none while it is note.
export type StepResult = Variant<"kind", { verify: VerifyResult; review: ReviewResult }>;

// One step of a run.
export abstract class Step {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  kind: StepKind;
  result?: StepResult;
}
