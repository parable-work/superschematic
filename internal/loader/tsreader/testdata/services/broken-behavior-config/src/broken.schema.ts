import { behavior } from "@superschematic/schema";

// Type-checks, since a Workflow state is a string; the core registry
// refuses it, since a state name is a letter, then letters, digits, _ and -.
@behavior("Workflow", { states: ["open", "in review"], transitions: [{ from: "open", to: "in review" }] })
export abstract class Memo {
  title: string;
}
