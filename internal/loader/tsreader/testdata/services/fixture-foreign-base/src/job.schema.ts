import { Stamped } from "@schemas/fixture-base-lib";

// Extends a class from another service. Its flattened fields reference that
// service's StampStage and StampOrigin, which this schema's generated code
// imports.
export abstract class Job extends Stamped {
  name: string;
}
