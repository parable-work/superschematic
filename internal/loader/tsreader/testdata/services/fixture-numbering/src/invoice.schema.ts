import { Validate, behavior } from "@superschematic/schema";

@behavior("Numbering", { format: "INV-{yyyy}-{seq:4}", scope: { link: "project" }, reset: "yearly" })
export abstract class Invoice {
  title: Validate<string, { maxLength: 200 }>;
  amount?: number;
}
