import { Validate, behavior } from "@superschematic/schema";

@behavior("Links", { links: { project: { schema: "projects", required: true } } })
@behavior("Numbering", { format: "INV-{yyyy}-{seq:4}", scope: { link: "project" }, reset: "yearly" })
export abstract class Invoice {
  title: Validate<string, { maxLength: 200 }>;
  amount?: number;
}
