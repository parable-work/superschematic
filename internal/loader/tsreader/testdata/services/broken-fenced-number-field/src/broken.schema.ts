import { Identity } from "superscalar";
import { fenced } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";

// The compiler takes any number as a fence. The build refuses this one: a
// plain number's column is DOUBLE PRECISION, which holds no whole number
// exactly, so a write could not add one to it for ever.
@fenced<Job>({ field: "version" })
export abstract class Job {
  @key
  id: AutoGenerate<Identity.UUID>;

  label: string;

  version: number;
}
