import { Validate } from "@superschematic/schema";

// uploadMaxBytes bounds a multipart upload; a string field is not one.
export abstract class Attachment {
  label: Validate<string, { uploadMaxBytes: 1024 }>;
}
