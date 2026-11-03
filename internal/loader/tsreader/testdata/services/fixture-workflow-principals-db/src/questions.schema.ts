import { Identity } from "superscalar";
import { AutoGenerate, Workflow, key } from "@superschematic/db";

// A question one person asks another (D104). Only the person it is asked of
// answers it, only its asker withdraws it, and closing an answered one needs
// questions.close beside being one of the two; a caller who holds
// questions.moderate makes any of these moves for them, the move's own
// permission still needed. Answering again changes nothing, so a caller
// whose answer was lost may retry.
export abstract class Question {
  @key
  id: AutoGenerate<Identity.UUID>;
  prompt: string;
  askedOf: Identity.UUID;
  createdBy: Identity.UUID;
  status: Workflow<{
    states: ["open", "answered", "withdrawn", "closed"];
    transitions: [
      { from: "open"; to: "answered"; by: ["askedOf"] },
      { from: "open"; to: "withdrawn"; by: ["createdBy"] },
      { from: "answered"; to: "closed"; by: ["askedOf", "createdBy"]; permission: "questions.close" },
      { from: "answered"; to: "open" }
    ];
    overridePermission: "questions.moderate";
    repeat: "unchanged";
  }>;
  // Whether the question was reviewed, apart from its answer, with no
  // repeat: a move to the state it is in stays the veto already_in_state.
  review: Workflow<{
    states: ["pending", "reviewed"];
    transitions: [{ from: "pending"; to: "reviewed" }];
  }>;
}
