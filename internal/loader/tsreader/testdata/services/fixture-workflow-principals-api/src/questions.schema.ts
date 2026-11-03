import { Identity } from "superscalar";
import { HttpMethod, auth, behaviorOperations, requirePermission, rest, source } from "@superschematic/api";
import { Question } from "@schemas/fixture-workflow-principals-db";

// A question's status and review operations (D104), for any signed-in
// caller: the core decides who may make each move from the row itself.
@behaviorOperations(Question)
@auth
export abstract class QuestionActions {}

// Who a question is asked of, as reassignQuestion takes it: a view of the
// table that carries askedOf, which status's Workflow names in by, so the
// build warns that a caller who may reassign a question may then answer it.
@source(Question)
export abstract class QuestionAssignment {
  askedOf: Identity.UUID;
}

// Moderation of questions, for callers who hold questions.moderate.
export class QuestionAdmin {
  @requirePermission(["questions.moderate"])
  @rest(HttpMethod.PATCH, "questions/{id}/asked-of")
  reassignQuestion(id: Identity.UUID, input: QuestionAssignment): Question {
    throw new Error("schema declaration only");
  }
}
