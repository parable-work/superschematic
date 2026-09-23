import { Validate } from "@superschematic/schema";

// A fractional byte count is not an upload bound.
export abstract class Attachment {
  label: Validate<string, { uploadMaxBytes: 1.5 }>;
}
