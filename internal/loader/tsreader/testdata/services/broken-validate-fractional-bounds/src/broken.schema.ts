import { Validate } from "@superschematic/schema";

// A length or an item count is a whole number: each fractional bound is
// refused by name, not truncated.
export abstract class Draft {
  title: Validate<string, { minLength: 0.5 }>;
  summary: Validate<string, { maxLength: 2.5 }>;
  tags: Validate<string[], { listMin: 1.5 }>;
  notes: Validate<string[], { listMax: 3.25 }>;
}
