import { Validate, behavior } from "@superschematic/schema";

@behavior("Search", { fields: ["title", "body"], weights: { title: 3 } })
export abstract class Note {
  title: Validate<string, { maxLength: 200 }>;
  body?: string;
}
