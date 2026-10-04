import { Generic } from "superscalar";
import { behavior } from "@superschematic/schema";

export enum StepKind {
  Verify = "verify",
  Review = "review",
  Note = "note"
}

export abstract class Check {
  name: string;
  ok: boolean;
}

export abstract class VerifyResult {
  passed: boolean;
  checks?: Check[];
}

export abstract class ReviewResult {
  approved: boolean;
  notes?: string;
}

@behavior("Constants", { fields: ["kind"] })
@behavior("Variants", { field: "result", by: "kind", types: { verify: "VerifyResult", review: "ReviewResult" } })
export abstract class Step {
  title: string;
  kind: StepKind;
  result?: Generic.JSON;
}
