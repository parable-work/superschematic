import { Validate, behavior } from "@superschematic/schema";

@behavior("Search", {
  fields: ["title", "body"],
  weights: { title: 3 },
  vectors: { dimensions: 384, model: "minilm-l6", permission: "notes.embed" }
})
export abstract class Note {
  title: Validate<string, { maxLength: 200 }>;
  body?: string;
}
