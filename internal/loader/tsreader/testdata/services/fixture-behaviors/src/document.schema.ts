import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["draft", "review", "published", "archived"],
  transitions: [
    { from: "draft", to: "review" },
    { from: "review", to: "draft" },
    { from: "review", to: "published", permission: "documents.publish" },
    { from: "published", to: "archived" },
  ],
})
@behavior("Comments")
@behavior("Revisions", { review: { permission: "documents.review" } })
export abstract class Document {
  title: Validate<string, { maxLength: 200 }>;

  body?: string;
}
